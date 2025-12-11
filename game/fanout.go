package game

import (
	"sync"

	"github.com/zond/juicemud"
	"golang.org/x/term"
)

// Fanout broadcasts writes to multiple terminals concurrently.
type Fanout struct {
	mu        sync.RWMutex
	terminals map[*term.Terminal]bool
}

// NewFanout creates a new Fanout with the given terminal.
func NewFanout(t *term.Terminal) *Fanout {
	return &Fanout{
		terminals: map[*term.Terminal]bool{t: true},
	}
}

func (f *Fanout) Push(t *term.Terminal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminals[t] = true
}

func (f *Fanout) Drop(t *term.Terminal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.terminals, t)
}

// Len returns the number of terminals in the fanout.
func (f *Fanout) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.terminals)
}

func (f *Fanout) Write(b []byte) (int, error) {
	if f == nil {
		return len(b), nil
	}
	f.mu.RLock()
	// Copy terminals slice to avoid holding lock during writes
	terminals := make([]*term.Terminal, 0, len(f.terminals))
	for t := range f.terminals {
		terminals = append(terminals, t)
	}
	f.mu.RUnlock()

	var toRemove []*term.Terminal
	var writeErrs errs
	for _, t := range terminals {
		if _, err := t.Write(b); err != nil {
			toRemove = append(toRemove, t)
			writeErrs = append(writeErrs, err)
		}
	}

	// Remove failed terminals
	if len(toRemove) > 0 {
		f.mu.Lock()
		for _, t := range toRemove {
			delete(f.terminals, t)
		}
		f.mu.Unlock()
	}

	if len(writeErrs) > 0 {
		return len(b), juicemud.WithStack(writeErrs)
	}
	return len(b), nil
}
