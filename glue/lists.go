package glue

import (
	"bytes"
	"iter"

	"capnproto.org/go/capnp/v3"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
)

type toPtrer interface {
	ToPtr() (capnp.Ptr, error)
}

func equal(a, b any) (bool, error) {
	var aPtr *capnp.Ptr
	var aBytes []byte
	switch v := a.(type) {
	case toPtrer:
		p, err := v.ToPtr()
		if err != nil {
			return false, juicemud.WithStack(err)
		}
		aPtr = &p
	case string:
		aBytes = []byte(v)
	case []byte:
		aBytes = v
	}
	var bPtr *capnp.Ptr
	var bBytes []byte
	switch v := b.(type) {
	case toPtrer:
		p, err := v.ToPtr()
		if err != nil {
			return false, juicemud.WithStack(err)
		}
		bPtr = &p
	case string:
		bBytes = []byte(v)
	case []byte:
		bBytes = v
	}
	if aPtr != nil && bPtr != nil {
		return capnp.Equal(*aPtr, *bPtr)
	}
	if aBytes != nil && bBytes != nil {
		return bytes.Equal(aBytes, bBytes), nil
	}
	return false, errors.Errorf("can't compare %v to %v", a, b)
}

type ListType[T any] interface {
	Len() int
	At(int) (T, error)
	Set(int, T) error
}

type ListHelper[V any, T ListType[V]] struct {
	Get func() (T, error)
	New func(int32) (T, error)
}

func (l ListHelper[V, T]) Iter() iter.Seq2[*V, error] {
	return func(yield func(*V, error) bool) {
		list, err := l.Get()
		if err != nil {
			yield(nil, juicemud.WithStack(err))
		} else {
			for i := 0; i < list.Len(); i++ {
				e, err := list.At(i)
				if err != nil {
					yield(nil, juicemud.WithStack(err))
					return
				} else {
					if !yield(&e, nil) {
						break
					}
				}
			}
		}
	}
}

func (l ListHelper[V, T]) All() ([]V, error) {
	list, err := l.Get()
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	result := make([]V, list.Len())
	for i := 0; i < list.Len(); i++ {
		el, err := list.At(i)
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
		result[i] = el
	}
	return result, nil
}

func (l ListHelper[V, T]) Append(v V) error {
	oldList, err := l.Get()
	if err != nil {
		return juicemud.WithStack(err)
	}
	newList, err := l.New(int32(oldList.Len() + 1))
	if err != nil {
		return juicemud.WithStack(err)
	}
	for i := 0; i < oldList.Len(); i++ {
		oldVal, err := oldList.At(i)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if err := newList.Set(i, oldVal); err != nil {
			return juicemud.WithStack(err)
		}
	}
	return juicemud.WithStack(newList.Set(oldList.Len(), v))
}

func (l ListHelper[V, T]) Has(needle V) (bool, error) {
	for v, err := range l.Iter() {
		if err != nil {
			return false, juicemud.WithStack(err)
		}
		if eq, err := equal(*v, needle); err != nil {
			return false, juicemud.WithStack(err)
		} else if eq {
			return true, nil
		}
	}
	return false, nil
}

func (l ListHelper[V, T]) Set(a []V) error {
	newList, err := l.New(int32(len(a)))
	if err != nil {
		return juicemud.WithStack(err)
	}
	for index, v := range a {
		if err := newList.Set(index, v); err != nil {
			return juicemud.WithStack(err)
		}
	}
	return nil
}

func (l ListHelper[V, T]) Remove(v V) error {
	oldList, err := l.Get()
	if err != nil {
		return juicemud.WithStack(err)
	}
	foundAt := -1
	for i := 0; i < oldList.Len(); i++ {
		oldVal, err := oldList.At(i)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if eq, err := equal(v, oldVal); err != nil {
			return juicemud.WithStack(err)
		} else if eq {
			foundAt = i
			break
		}
	}
	if foundAt == -1 {
		return nil
	}
	newList, err := l.New(int32(oldList.Len() - 1))
	if err != nil {
		return juicemud.WithStack(err)
	}
	newListIndex := 0
	for i := 0; i < oldList.Len(); i++ {
		if i != foundAt {
			oldVal, err := oldList.At(i)
			if err != nil {
				return juicemud.WithStack(err)
			}
			if err := newList.Set(newListIndex, oldVal); err != nil {
				return juicemud.WithStack(err)
			}
			newListIndex++
		}
	}
	return nil
}
