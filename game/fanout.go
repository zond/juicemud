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
	onEmpty   func() // called when fanout becomes empty due to write failures
}

// NewFanout creates a new Fanout with the given terminal and optional empty callback.
func NewFanout(t *term.Terminal, onEmpty func()) *Fanout {
	return &Fanout{
		terminals: map[*term.Terminal]bool{t: true},
		onEmpty:   onEmpty,
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
		empty := len(f.terminals) == 0
		f.mu.Unlock()
		if empty && f.onEmpty != nil {
			f.onEmpty()
		}
	}

	if len(writeErrs) > 0 {
		return len(b), juicemud.WithStack(writeErrs)
	}
	return len(b), nil
}
