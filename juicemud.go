package juicemud

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

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
	locks map[K]*sync.WaitGroup
	mutex sync.RWMutex
}

func NewSyncMap[K comparable, V comparable]() *SyncMap[K, V] {
	return &SyncMap[K, V]{
		m:     map[K]V{},
		locks: map[K]*sync.WaitGroup{},
	}
}

func (s *SyncMap[K, V]) GetHas(key K) (V, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	v, found := s.m[key]
	return v, found
}

func (s *SyncMap[K, V]) Get(key K) V {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.m[key]
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

func (l *SyncMap[K, V]) WithLock(key K, f func()) {
	l.Lock(key)
	defer l.Unlock(key)
	f()
}

func (l *SyncMap[K, V]) Lock(key K) {
	trylock := func() *sync.WaitGroup {
		l.mutex.Lock()
		defer l.mutex.Unlock()
		if wg, found := l.locks[key]; found {
			return wg
		}
		wg := &sync.WaitGroup{}
		wg.Add(1)
		l.locks[key] = wg
		return nil
	}
	for wg := trylock(); wg != nil; wg = trylock() {
		wg.Wait()
	}
}

func (l *SyncMap[K, V]) Unlock(key K) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if wg, found := l.locks[key]; found {
		delete(l.locks, key)
		wg.Done()
	}
}

func Increment(prevPointer *uint64) uint64 {
	next := uint64(0)
	for {
		next = uint64(time.Now().UnixNano())
		previous := atomic.LoadUint64(prevPointer)
		if next > previous && atomic.CompareAndSwapUint64(prevPointer, previous, next) {
			break
		}
	}
	return next
}
