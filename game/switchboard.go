package game

import (
	"context"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	// consoleBufferSize is the number of log messages to buffer per object.
	// When /debug connects, it first receives these buffered messages.
	consoleBufferSize = 64

	// consoleBufferTTL is how long messages are retained in the buffer.
	// Messages older than this are discarded when retrieved.
	consoleBufferTTL = 10 * time.Minute
)

// bufferedMessage is a console log message with its timestamp.
type bufferedMessage struct {
	data      []byte
	timestamp time.Time
}

// consoleBuffer is a ring buffer for console log messages with TTL.
type consoleBuffer struct {
	messages []bufferedMessage
	start    int // Index of oldest message
	count    int // Number of messages in buffer
}

// push adds a message to the ring buffer, evicting the oldest if full.
func (b *consoleBuffer) push(msg []byte) {
	if b.messages == nil {
		b.messages = make([]bufferedMessage, consoleBufferSize)
	}
	// Make a copy of the message to avoid aliasing issues
	msgCopy := make([]byte, len(msg))
	copy(msgCopy, msg)

	entry := bufferedMessage{data: msgCopy, timestamp: time.Now()}

	idx := (b.start + b.count) % consoleBufferSize
	if b.count < consoleBufferSize {
		b.messages[idx] = entry
		b.count++
	} else {
		// Buffer full, overwrite oldest
		b.messages[b.start] = entry
		b.start = (b.start + 1) % consoleBufferSize
	}
}

// getAll returns all non-expired buffered messages in chronological order.
// Messages older than consoleBufferTTL are excluded.
func (b *consoleBuffer) getAll() [][]byte {
	if b.count == 0 {
		return nil
	}
	cutoff := time.Now().Add(-consoleBufferTTL)
	result := make([][]byte, 0, b.count)
	for i := 0; i < b.count; i++ {
		msg := b.messages[(b.start+i)%consoleBufferSize]
		if msg.timestamp.After(cutoff) {
			result = append(result, msg.data)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// isEmpty returns true if the buffer has no non-expired messages.
func (b *consoleBuffer) isEmpty() bool {
	if b.count == 0 {
		return true
	}
	cutoff := time.Now().Add(-consoleBufferTTL)
	for i := 0; i < b.count; i++ {
		msg := b.messages[(b.start+i)%consoleBufferSize]
		if msg.timestamp.After(cutoff) {
			return false
		}
	}
	return true
}

// Switchboard manages debug console connections from wizards to objects.
// It provides a Writer for each object that broadcasts to all attached terminals.
// Also maintains a ring buffer of recent messages per object.
type Switchboard struct {
	mu       sync.RWMutex
	consoles map[string]map[*term.Terminal]struct{} // objectID -> set of terminals
	buffers  map[string]*consoleBuffer              // objectID -> ring buffer
}

// NewSwitchboard creates a new console switchboard and starts the cleanup goroutine.
// The cleanup goroutine runs until ctx is cancelled.
func NewSwitchboard(ctx context.Context) *Switchboard {
	s := &Switchboard{
		consoles: make(map[string]map[*term.Terminal]struct{}),
		buffers:  make(map[string]*consoleBuffer),
	}
	go s.cleanupLoop(ctx)
	return s
}

// cleanupLoop periodically removes empty (all-expired) console buffers.
func (s *Switchboard) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(consoleBufferTTL)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupExpiredBuffers()
		}
	}
}

// cleanupExpiredBuffers removes all buffers where every message has expired.
func (s *Switchboard) cleanupExpiredBuffers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for objectID, buf := range s.buffers {
		if buf.isEmpty() {
			delete(s.buffers, objectID)
		}
	}
}

// Attach connects a terminal to receive debug output from an object.
// Nil terminals are ignored.
func (s *Switchboard) Attach(objectID string, t *term.Terminal) {
	if t == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consoles[objectID] == nil {
		s.consoles[objectID] = make(map[*term.Terminal]struct{})
	}
	s.consoles[objectID][t] = struct{}{}
}

// Detach disconnects a terminal from an object's debug output.
func (s *Switchboard) Detach(objectID string, t *term.Terminal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if terms := s.consoles[objectID]; terms != nil {
		delete(terms, t)
		if len(terms) == 0 {
			delete(s.consoles, objectID)
		}
	}
}

// IsAttached returns true if the terminal is attached to the object.
func (s *Switchboard) IsAttached(objectID string, t *term.Terminal) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if terms := s.consoles[objectID]; terms != nil {
		_, attached := terms[t]
		return attached
	}
	return false
}

// Writer returns an io.Writer that broadcasts to all terminals attached to the object.
// Failed writes automatically detach the terminal.
//
// Note: Due to lock-free I/O (to avoid deadlocks with slow terminals), a terminal
// may receive one additional write after being detached if a Write() was already
// in progress when Detach() was called.
func (s *Switchboard) Writer(objectID string) *SwitchboardWriter {
	return &SwitchboardWriter{s: s, objectID: objectID}
}

// SwitchboardWriter broadcasts writes to all terminals attached to an object.
// Must be created via Switchboard.Writer(), not instantiated directly.
type SwitchboardWriter struct {
	s        *Switchboard
	objectID string
}

// Write implements io.Writer. For broadcast semantics, this always returns
// (len(b), nil) as long as the write is attempted, even if some terminals fail.
// Failed terminals are automatically detached from the switchboard.
// All messages are also stored in the ring buffer for later retrieval.
func (w *SwitchboardWriter) Write(b []byte) (int, error) {
	if w.s == nil {
		return len(b), nil
	}

	// Buffer the message first (under write lock since we modify buffers)
	w.s.mu.Lock()
	if w.s.buffers[w.objectID] == nil {
		w.s.buffers[w.objectID] = &consoleBuffer{}
	}
	w.s.buffers[w.objectID].push(b)
	w.s.mu.Unlock()

	w.s.mu.RLock()
	terms := w.s.consoles[w.objectID]
	// Copy to slice to release lock before writing
	list := make([]*term.Terminal, 0, len(terms))
	for t := range terms {
		list = append(list, t)
	}
	w.s.mu.RUnlock()

	if len(list) == 0 {
		return len(b), nil
	}

	var failed []*term.Terminal
	for _, t := range list {
		if _, err := t.Write(b); err != nil {
			failed = append(failed, t)
		}
	}

	// Remove failed terminals
	if len(failed) > 0 {
		w.s.mu.Lock()
		for _, t := range failed {
			if terms := w.s.consoles[w.objectID]; terms != nil {
				delete(terms, t)
				if len(terms) == 0 {
					delete(w.s.consoles, w.objectID)
				}
			}
		}
		w.s.mu.Unlock()
	}

	return len(b), nil
}

// GetBuffered returns all non-expired buffered messages for an object in chronological order.
// Returns nil if no messages are buffered or all have expired.
// Also cleans up the buffer entry if all messages have expired.
func (s *Switchboard) GetBuffered(objectID string) [][]byte {
	s.mu.RLock()
	buf := s.buffers[objectID]
	if buf == nil {
		s.mu.RUnlock()
		return nil
	}
	messages := buf.getAll()
	isEmpty := buf.isEmpty()
	s.mu.RUnlock()

	// Clean up empty buffer (all messages expired)
	if isEmpty {
		s.mu.Lock()
		// Re-check under write lock
		if buf := s.buffers[objectID]; buf != nil && buf.isEmpty() {
			delete(s.buffers, objectID)
		}
		s.mu.Unlock()
	}

	return messages
}
