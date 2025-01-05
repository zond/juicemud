package juicemud

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/pkg/errors"
)

const (
	DAVAuthRealm = "WebDAV"
)

type stackTracer interface {
	StackTrace() errors.StackTrace
}

func WithStack(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(stackTracer); !ok {
		return errors.WithStack(err)
	}
	return err
}

func StackTrace(err error) string {
	buf := &bytes.Buffer{}
	if err, ok := err.(stackTracer); ok {
		for _, f := range err.StackTrace() {
			fmt.Fprintf(buf, "%+v\n", f)
		}
	}
	return buf.String()
}

type SyncMap[K comparable, V comparable] struct {
	m     map[K]V
	mutex sync.RWMutex
}

func NewSyncMap[K comparable, V comparable]() *SyncMap[K, V] {
	return &SyncMap[K, V]{
		m: map[K]V{},
	}
}

func (s *SyncMap[K, V]) Get(key K) (V, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	v, found := s.m[key]
	return v, found
}

func (s *SyncMap[K, V]) Set(key K, value V) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.m[key] = value
}

func (s *SyncMap[K, V]) Del(key K) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	delete(s.m, key)
}

func (s *SyncMap[K, V]) Has(key K) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	_, found := s.m[key]
	return found
}

func (s *SyncMap[K, V]) Swap(key K, oldValue V, newValue V) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.m[key] == oldValue {
		s.m[key] = newValue
		return true
	}
	return false
}

type LockMap[K comparable] struct {
	sm *SyncMap[K, *sync.WaitGroup]
}

func NewLockMap[K comparable]() *LockMap[K] {
	sm := NewSyncMap[K, *sync.WaitGroup]()
	return &LockMap[K]{sm: sm}
}

func (l *LockMap[K]) Lock(key K) {
	for {
		wg := &sync.WaitGroup{}
		wg.Add(1)
		if l.sm.Swap(key, nil, wg) {
			break
		}
		otherWg, found := l.sm.Get(key)
		if found {
			otherWg.Wait()
		}
	}
}

func (l *LockMap[K]) Unlock(key K) {
	if wg, found := l.sm.Get(key); found {
		l.sm.Del(key)
		wg.Done()
	}
}
