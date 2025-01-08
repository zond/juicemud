package game

import (
	"github.com/zond/juicemud"
	"golang.org/x/term"
)

type Fanout map[*term.Terminal]bool

func (f Fanout) Push(t *term.Terminal) {
	f[t] = true
}

func (f Fanout) Drop(t *term.Terminal) {
	delete(f, t)
}

func (f *Fanout) Write(b []byte) (int, error) {
	if f == nil {
		return len(b), nil
	}
	errs := errs{}
	max := 0
	for t := range *f {
		if written, err := t.Write(b); err != nil {
			delete(*f, t)
			errs = append(errs, err)
		} else {
			if written > max {
				max = written
			}
		}
	}
	if len(errs) > 0 {
		return max, juicemud.WithStack(errs)
	}
	return max, nil
}
