package game

import (
	"sync"

	"golang.org/x/term"
)

// Switchboard manages debug console connections from wizards to objects.
// It provides a Writer for each object that broadcasts to all attached terminals.
type Switchboard struct {
	mu       sync.RWMutex
	consoles map[string]map[*term.Terminal]struct{} // objectID -> set of terminals
}

// NewSwitchboard creates a new console switchboard.
func NewSwitchboard() *Switchboard {
	return &Switchboard{
		consoles: make(map[string]map[*term.Terminal]struct{}),
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
func (w *SwitchboardWriter) Write(b []byte) (int, error) {
	if w.s == nil {
		return len(b), nil
	}

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
