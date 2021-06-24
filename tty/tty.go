package tty

import (
	"fmt"
	"io"
	"sync"

	"github.com/gliderlabs/ssh"
)

type SSHTTY struct {
	Sess           ssh.Session
	ResizeCallback func()

	resizeCallback func()
	stop           chan bool
	drain          chan bool
	incoming       chan byte
	mutex          sync.Mutex
	width          int
	height         int
}

func (s *SSHTTY) Read(b []byte) (int, error) {
	select {
	case v, ok := <-s.incoming:
		if ok {
			b[0] = v
			return 1, nil
		} else {
			return 0, io.EOF
		}
	case <-s.drain:
		return 0, nil
	}
}

func (s *SSHTTY) Write(b []byte) (int, error) {
	return s.Sess.Write(b)
}

func (s *SSHTTY) Close() error {
	return nil
}

func (s *SSHTTY) Start() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	pty, winCh, isPTY := s.Sess.Pty()
	if !isPTY {
		return fmt.Errorf("session is not interactive")
	}

	s.width, s.height = pty.Window.Width, pty.Window.Height

	s.stop = make(chan bool)
	go func() {
		for {
			select {
			case ev := <-winCh:
				cb1, cb2 := func() (func(), func()) {
					s.mutex.Lock()
					defer s.mutex.Unlock()
					s.width = ev.Width
					s.height = ev.Height
					return s.ResizeCallback, s.resizeCallback
				}()
				if cb2 != nil {
					cb2()
				}
				if cb1 != nil {
					cb1()
				}
			case <-s.stop:
				return
			}
		}
	}()

	s.incoming = make(chan byte)
	go func() {
		defer close(s.incoming)
		buf := []byte{0}
		for nRead, err := s.Sess.Read(buf); err == nil; nRead, err = s.Sess.Read(buf) {
			if nRead == 1 {
				s.incoming <- buf[0]
			}
			select {
			case <-s.stop:
				return
			default:
			}
		}
	}()

	s.drain = make(chan bool)

	return nil
}

func (s *SSHTTY) Drain() error {
	close(s.drain)
	return nil
}

func (s *SSHTTY) Stop() error {
	close(s.stop)
	return nil
}

func (s *SSHTTY) WindowSize() (int, int, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.width, s.height, nil
}

func (s *SSHTTY) NotifyResize(cb func()) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.resizeCallback = cb
}
