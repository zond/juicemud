package juicemud

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"iter"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"

	goccy "github.com/goccy/go-json"
)

var lastUniqueIDCounter uint64 = 0

const (
	uniqueIDLen = 16
)

// Encoding is the base64 encoding used for unique IDs.
// Uses URL-safe encoding to avoid problematic characters in URLs and file paths.
var Encoding = base64.RawURLEncoding

// NextUniqueID generates a unique ID using a monotonic timestamp prefix
// followed by random bytes, then base64-encodes the result.
// This is used for object IDs, session IDs, and other unique identifiers.
func NextUniqueID() string {
	counter := Increment(&lastUniqueIDCounter)
	timeSize := binary.Size(counter)
	result := make([]byte, uniqueIDLen)
	binary.BigEndian.PutUint64(result, counter)
	if _, err := rand.Read(result[timeSize:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return Encoding.EncodeToString(result)
}

type contextKey int

var (
	mainContect contextKey = 0
)

type Set[K comparable] map[K]struct{}

func (s Set[K]) Set(k K) {
	s[k] = struct{}{}
}

func (s Set[K]) Del(k K) {
	delete(s, k)
}

func (s Set[K]) DelAll(o Set[K]) {
	for k := range o {
		s.Del(k)
	}
}

func (s Set[K]) SetAll(o Set[K]) {
	for k := range o {
		s.Set(k)
	}
}

func (s Set[K]) Union(o Set[K]) Set[K] {
	result := Set[K]{}
	result.SetAll(s)
	result.SetAll(o)
	return result
}

func (s Set[K]) Intersection(o Set[K]) Set[K] {
	result := Set[K]{}
	if len(s) > len(o) {
		for k := range o {
			if _, found := s[k]; found {
				result[k] = struct{}{}
			}
		}
	} else {
		for k := range s {
			if _, found := o[k]; found {
				result[k] = struct{}{}
			}
		}
	}
	return result
}

func IsMainContext(ctx context.Context) bool {
	val := ctx.Value(mainContect)
	if val == nil {
		return false
	}
	if b, ok := val.(bool); ok {
		return b
	}
	return false
}

func MakeMainContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, mainContect, true)
}

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

func (s *SyncMap[K, V]) Clone() map[K]V {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	result := map[K]V{}
	for k, v := range s.m {
		result[k] = v
	}
	return result
}

func (s *SyncMap[K, V]) Replace(m map[K]V) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.m = maps.Clone(m)
}

func (s *SyncMap[K, V]) MarshalJSON() ([]byte, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return goccy.Marshal(s.m)
}

func (s *SyncMap[K, V]) UnmarshalJSON(b []byte) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.m = map[K]V{}
	return goccy.Unmarshal(b, &s.m)
}

func (s *SyncMap[K, V]) Keys() iter.Seq[K] {
	return func(yield func(k K) bool) {
		s.mutex.RLock()
		defer s.mutex.RUnlock()
		for k := range s.m {
			if !yield(k) {
				return
			}
		}
	}
}

func (s *SyncMap[K, V]) Values() iter.Seq[V] {
	return func(yield func(v V) bool) {
		s.mutex.RLock()
		defer s.mutex.RUnlock()
		for _, v := range s.m {
			if !yield(v) {
				return
			}
		}
	}
}

func (s *SyncMap[K, V]) Each() iter.Seq2[K, V] {
	return func(yield func(k K, v V) bool) {
		s.mutex.RLock()
		defer s.mutex.RUnlock()
		for k, v := range s.m {
			if !yield(k, v) {
				return
			}
		}
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

// Len returns the number of entries in the map.
func (s *SyncMap[K, V]) Len() int {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return len(s.m)
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
