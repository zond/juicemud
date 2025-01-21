package heap

import "testing"

func TestBasics(t *testing.T) {
	h := New(func(a, b int) bool {
		return a < b
	})
	h.Push(10)
	h.Push(4)
	h.Push(100)
	h.Push(8)
	h.Push(20)
	for _, i := range []int{4, 8, 10, 20, 100} {
		if top, found := h.Peek(); !found || top != i {
			t.Errorf("got %v, %v, want %v, true", top, found, i)
		}
		if top, found := h.Pop(); !found || top != i {
			t.Errorf("got %v, %v, want %v, true", top, found, i)
		}
	}
	if _, found := h.Peek(); found {
		t.Errorf("got %v, want false", found)
	}
	if _, found := h.Pop(); found {
		t.Errorf("got %v, want false", found)
	}
}
