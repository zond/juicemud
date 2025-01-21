package heap

type Heap[T any] struct {
	data []T
	less func(a, b T) bool // Custom comparison function for generic type
}

func New[T any](less func(a, b T) bool) *Heap[T] {
	return &Heap[T]{
		data: []T{},
		less: less,
	}
}

func (h *Heap[T]) Push(value T) {
	h.data = append(h.data, value)
	h.bubbleUp(len(h.data) - 1)
}

func (h *Heap[T]) Pop() (T, bool) {
	if len(h.data) == 0 {
		var zero T
		return zero, false
	}
	top := h.data[0]
	h.data[0] = h.data[len(h.data)-1]
	h.data = h.data[:len(h.data)-1]
	h.bubbleDown(0)
	return top, true
}

func (h *Heap[T]) Peek() (T, bool) {
	if len(h.data) == 0 {
		var zero T
		return zero, false
	}
	return h.data[0], true
}

func (h *Heap[T]) bubbleUp(index int) {
	for index > 0 {
		parent := (index - 1) / 2
		if h.less(h.data[index], h.data[parent]) {
			h.data[index], h.data[parent] = h.data[parent], h.data[index]
			index = parent
		} else {
			break
		}
	}
}

func (h *Heap[T]) bubbleDown(index int) {
	size := len(h.data)
	for {
		left := 2*index + 1
		right := 2*index + 2
		smallest := index

		if left < size && h.less(h.data[left], h.data[smallest]) {
			smallest = left
		}
		if right < size && h.less(h.data[right], h.data[smallest]) {
			smallest = right
		}
		if smallest == index {
			break
		}

		h.data[index], h.data[smallest] = h.data[smallest], h.data[index]
		index = smallest
	}
}

func (h *Heap[T]) Size() int {
	return len(h.data)
}
