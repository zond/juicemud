package game

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/term"
)

// testTerminal creates a terminal backed by a buffer for testing.
// Returns the terminal and the underlying buffer to check output.
func testTerminal(t *testing.T) (*term.Terminal, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	rw := &testReadWriter{Reader: &bytes.Buffer{}, Writer: buf}
	terminal := term.NewTerminal(rw, "")
	return terminal, buf
}

// failingTerminal creates a terminal whose writes always fail.
func failingTerminal(t *testing.T) *term.Terminal {
	t.Helper()
	rw := &testReadWriter{Reader: &bytes.Buffer{}, Writer: &failingWriter{}}
	terminal := term.NewTerminal(rw, "")
	return terminal
}

// testReadWriter combines a Reader and Writer into an io.ReadWriter.
type testReadWriter struct {
	Reader io.Reader
	Writer io.Writer
}

func (rw *testReadWriter) Read(p []byte) (int, error) {
	return rw.Reader.Read(p)
}

func (rw *testReadWriter) Write(p []byte) (int, error) {
	return rw.Writer.Write(p)
}

// failingWriter always returns an error on Write.
type failingWriter struct{}

func (w *failingWriter) Write(p []byte) (int, error) {
	return 0, io.ErrClosedPipe
}

// countingWriter counts the number of writes.
type countingWriter struct {
	count atomic.Int32
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.count.Add(1)
	return len(p), nil
}

func TestSwitchboardAttachDetach(t *testing.T) {
	s := NewSwitchboard()
	terminal1, _ := testTerminal(t)
	terminal2, _ := testTerminal(t)

	// Initially not attached
	if s.IsAttached("obj1", terminal1) {
		t.Error("terminal1 should not be attached initially")
	}

	// Attach terminal1
	s.Attach("obj1", terminal1)
	if !s.IsAttached("obj1", terminal1) {
		t.Error("terminal1 should be attached after Attach")
	}
	if s.IsAttached("obj1", terminal2) {
		t.Error("terminal2 should not be attached")
	}
	if s.IsAttached("obj2", terminal1) {
		t.Error("terminal1 should not be attached to obj2")
	}

	// Attach terminal2 to same object
	s.Attach("obj1", terminal2)
	if !s.IsAttached("obj1", terminal1) {
		t.Error("terminal1 should still be attached")
	}
	if !s.IsAttached("obj1", terminal2) {
		t.Error("terminal2 should be attached")
	}

	// Detach terminal1
	s.Detach("obj1", terminal1)
	if s.IsAttached("obj1", terminal1) {
		t.Error("terminal1 should not be attached after Detach")
	}
	if !s.IsAttached("obj1", terminal2) {
		t.Error("terminal2 should still be attached")
	}

	// Detach terminal2 - should clean up the object map
	s.Detach("obj1", terminal2)
	if s.IsAttached("obj1", terminal2) {
		t.Error("terminal2 should not be attached after Detach")
	}
}

func TestSwitchboardAttachNil(t *testing.T) {
	s := NewSwitchboard()

	// Attaching nil should be a no-op and not panic
	s.Attach("obj1", nil)

	// Verify no entry was created
	if s.IsAttached("obj1", nil) {
		t.Error("nil terminal should never be considered attached")
	}
}

func TestSwitchboardDetachNonexistent(t *testing.T) {
	s := NewSwitchboard()
	terminal, _ := testTerminal(t)

	// Detaching from non-existent object should not panic
	s.Detach("nonexistent", terminal)

	// Detaching unattached terminal should not panic
	s.Attach("obj1", terminal)
	s.Detach("obj1", terminal)
	s.Detach("obj1", terminal) // Already detached
}

func TestSwitchboardWriterNoTerminals(t *testing.T) {
	s := NewSwitchboard()
	w := s.Writer("obj1")

	// Writing with no terminals should succeed
	n, err := w.Write([]byte("hello"))
	if err != nil {
		t.Errorf("Write() error = %v", err)
	}
	if n != 5 {
		t.Errorf("Write() returned %d, want 5", n)
	}
}

func TestSwitchboardWriterNilSwitchboard(t *testing.T) {
	// SwitchboardWriter with nil switchboard should handle gracefully
	w := &SwitchboardWriter{s: nil, objectID: "obj1"}

	n, err := w.Write([]byte("hello"))
	if err != nil {
		t.Errorf("Write() with nil switchboard error = %v", err)
	}
	if n != 5 {
		t.Errorf("Write() returned %d, want 5", n)
	}
}

func TestSwitchboardWriterBroadcast(t *testing.T) {
	s := NewSwitchboard()
	terminal1, buf1 := testTerminal(t)
	terminal2, buf2 := testTerminal(t)

	s.Attach("obj1", terminal1)
	s.Attach("obj1", terminal2)

	w := s.Writer("obj1")
	message := []byte("broadcast message")

	// Write should succeed
	n, err := w.Write(message)
	if err != nil {
		t.Errorf("Write() error = %v", err)
	}
	if n != len(message) {
		t.Errorf("Write() returned %d, want %d", n, len(message))
	}

	// Both terminals should receive the message
	if !bytes.Contains(buf1.Bytes(), message) {
		t.Errorf("terminal1 did not receive message, got: %q", buf1.Bytes())
	}
	if !bytes.Contains(buf2.Bytes(), message) {
		t.Errorf("terminal2 did not receive message, got: %q", buf2.Bytes())
	}
}

func TestSwitchboardWriterAutoDetachOnFailure(t *testing.T) {
	s := NewSwitchboard()
	goodTerminal, _ := testTerminal(t)
	badTerminal := failingTerminal(t)

	s.Attach("obj1", goodTerminal)
	s.Attach("obj1", badTerminal)

	if !s.IsAttached("obj1", badTerminal) {
		t.Error("badTerminal should be attached initially")
	}

	w := s.Writer("obj1")
	// Write should still succeed (returns len(b), nil per docs)
	n, err := w.Write([]byte("test"))
	if err != nil {
		t.Errorf("Write() error = %v", err)
	}
	if n != 4 {
		t.Errorf("Write() returned %d, want 4", n)
	}

	// Failed terminal should be auto-detached
	if s.IsAttached("obj1", badTerminal) {
		t.Error("badTerminal should be detached after write failure")
	}
	// Good terminal should still be attached
	if !s.IsAttached("obj1", goodTerminal) {
		t.Error("goodTerminal should still be attached")
	}
}

func TestSwitchboardConcurrentAccess(t *testing.T) {
	s := NewSwitchboard()
	terminal, _ := testTerminal(t)
	w := s.Writer("obj1")

	var wg sync.WaitGroup
	const goroutines = 10
	const iterations = 100

	// Concurrent attach/detach
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				s.Attach("obj1", terminal)
				s.IsAttached("obj1", terminal)
				s.Detach("obj1", terminal)
			}
		}()
	}

	// Concurrent writes
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				w.Write([]byte("concurrent write"))
			}
		}()
	}

	wg.Wait()
	// Test passes if no race conditions or panics occurred
}

func TestSwitchboardMultipleObjects(t *testing.T) {
	s := NewSwitchboard()
	terminal1, _ := testTerminal(t)
	terminal2, _ := testTerminal(t)

	// Attach same terminal to different objects
	s.Attach("obj1", terminal1)
	s.Attach("obj2", terminal1)
	s.Attach("obj2", terminal2)

	if !s.IsAttached("obj1", terminal1) {
		t.Error("terminal1 should be attached to obj1")
	}
	if !s.IsAttached("obj2", terminal1) {
		t.Error("terminal1 should be attached to obj2")
	}
	if !s.IsAttached("obj2", terminal2) {
		t.Error("terminal2 should be attached to obj2")
	}

	// Detach from obj1 should not affect obj2
	s.Detach("obj1", terminal1)
	if s.IsAttached("obj1", terminal1) {
		t.Error("terminal1 should not be attached to obj1")
	}
	if !s.IsAttached("obj2", terminal1) {
		t.Error("terminal1 should still be attached to obj2")
	}
}
