package game

import (
	"sync"

	"golang.org/x/term"
)

const (
	// consoleBufferSize is the number of log messages to buffer per object.
	// When /debug connects, it first receives these buffered messages.
	consoleBufferSize = 64
)

// consoleBuffer is a ring buffer for console log messages.
type consoleBuffer struct {
	messages [][]byte
	start    int // Index of oldest message
	count    int // Number of messages in buffer
}

// push adds a message to the ring buffer, evicting the oldest if full.
func (b *consoleBuffer) push(msg []byte) {
	if b.messages == nil {
		b.messages = make([][]byte, consoleBufferSize)
	}
	// Make a copy of the message to avoid aliasing issues
	msgCopy := make([]byte, len(msg))
	copy(msgCopy, msg)

	idx := (b.start + b.count) % consoleBufferSize
	if b.count < consoleBufferSize {
		b.messages[idx] = msgCopy
		b.count++
	} else {
		// Buffer full, overwrite oldest
		b.messages[b.start] = msgCopy
		b.start = (b.start + 1) % consoleBufferSize
	}
}

// getAll returns all buffered messages in chronological order.
func (b *consoleBuffer) getAll() [][]byte {
	if b.count == 0 {
		return nil
	}
	result := make([][]byte, b.count)
	for i := 0; i < b.count; i++ {
		result[i] = b.messages[(b.start+i)%consoleBufferSize]
	}
	return result
}

// Switchboard manages debug console connections from wizards to objects.
// It provides a Writer for each object that broadcasts to all attached terminals.
// Also maintains a ring buffer of recent messages per object.
type Switchboard struct {
	mu       sync.RWMutex
	consoles map[string]map[*term.Terminal]struct{} // objectID -> set of terminals
	buffers  map[string]*consoleBuffer              // objectID -> ring buffer
}

// NewSwitchboard creates a new console switchboard.
func NewSwitchboard() *Switchboard {
	return &Switchboard{
		consoles: make(map[string]map[*term.Terminal]struct{}),
		buffers:  make(map[string]*consoleBuffer),
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

// GetBuffered returns all buffered messages for an object in chronological order.
// Returns nil if no messages are buffered.
func (s *Switchboard) GetBuffered(objectID string) [][]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if buf := s.buffers[objectID]; buf != nil {
		return buf.getAll()
	}
	return nil
}
