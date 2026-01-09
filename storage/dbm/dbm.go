package dbm

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"log"
	"os"
	"sync"
	"time"

	"github.com/estraier/tkrzw-go"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/structs"
)

// checkStatus converts a tkrzw status to an error. If the status is a NotFoundError,
// it returns an error wrapping os.ErrNotExist with the provided message. If the status
// is OK, it returns nil. Otherwise, it returns the status as an error.
func checkStatus(stat *tkrzw.Status, notFoundMsg string) error {
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return juicemud.WithStack(fmt.Errorf("%s: %w", notFoundMsg, os.ErrNotExist))
	}
	if !stat.IsOK() {
		return juicemud.WithStack(stat)
	}
	return nil
}

// Hash wraps a tkrzw hash database for unordered key-value storage.
// All operations are thread-safe via internal mutex.
type Hash struct {
	dbm   *tkrzw.DBM
	mutex sync.RWMutex
}

// Close closes the underlying database file.
func (h *Hash) Close() error {
	if stat := h.dbm.Close(); !stat.IsOK() {
		return juicemud.WithStack(stat)
	}
	return nil
}

// BEntry is a byte-level key-value pair used for iteration.
type BEntry struct {
	K string
	V []byte
}

// Each iterates over all entries in the hash.
func (h *Hash) Each() iter.Seq2[BEntry, error] {
	return func(yield func(BEntry, error) bool) {
		h.mutex.RLock()
		defer h.mutex.RUnlock()
		iter := h.dbm.MakeIterator()
		defer iter.Destruct()
		iter.First()
		for {
			key, value, status := iter.Get()
			if status.GetCode() == tkrzw.StatusNotFoundError {
				break
			} else if !status.IsOK() {
				// On error, yield empty entry with error and stop iteration
				yield(BEntry{}, juicemud.WithStack(status))
				break
			} else {
				if !yield(BEntry{
					K: string(key),
					V: value,
				}, nil) {
					break
				}
			}
			iter.Next()
		}
	}
}

// Has returns true if the key exists in the hash.
func (h *Hash) Has(k string) bool {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	return h.dbm.Check(k)
}

// Get retrieves a value by key. Returns os.ErrNotExist if key doesn't exist.
func (h *Hash) Get(k string) ([]byte, error) {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	b, stat := h.dbm.Get(k)
	if err := checkStatus(stat, fmt.Sprintf("key %q", k)); err != nil {
		return nil, err
	}
	return b, nil
}

// Set stores a key-value pair. If overwrite is false, fails if key exists.
func (h *Hash) Set(k string, v []byte, overwrite bool) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	if stat := h.dbm.Set(k, v, overwrite); !stat.IsOK() {
		return juicemud.WithStack(stat)
	}
	return nil
}

// Del removes a key. Returns os.ErrNotExist if key doesn't exist.
func (h *Hash) Del(k string) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	return checkStatus(h.dbm.Remove(k), fmt.Sprintf("key %q", k))
}

// GetMulti retrieves multiple values atomically. Missing keys are omitted from result.
func (h *Hash) GetMulti(keys map[string]bool) map[string][]byte {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	ids := make([]string, 0, len(keys))
	for key := range keys {
		ids = append(ids, key)
	}
	return h.dbm.GetMulti(ids)
}

const (
	flushBaseInterval = time.Second
	flushMaxBackoff   = 30 * time.Second
)

// FlushHealth contains information about the flush loop's current state.
type FlushHealth struct {
	LastFlushAt    time.Time     // When the last successful flush occurred
	LastErrorAt    time.Time     // When the last error occurred (zero if none)
	LastError      error         // The most recent error (nil if healthy)
	ConsecErrors   int           // Number of consecutive errors
	CurrentBackoff time.Duration // Current backoff duration
}

// Healthy returns true if the last flush succeeded (no current error).
func (h *FlushHealth) Healthy() bool {
	return h.LastError == nil
}

// recordSuccess updates state after a successful flush and returns the next interval.
func (h *FlushHealth) recordSuccess() time.Duration {
	h.LastFlushAt = time.Now()
	h.LastError = nil
	h.ConsecErrors = 0
	h.CurrentBackoff = flushBaseInterval
	return flushBaseInterval
}

// recordError updates state after a failed flush and returns the next interval (with backoff).
func (h *FlushHealth) recordError(err error) time.Duration {
	h.LastError = err
	h.LastErrorAt = time.Now()
	h.ConsecErrors++
	h.CurrentBackoff = max(min(h.CurrentBackoff*2, flushMaxBackoff), flushBaseInterval)
	return h.CurrentBackoff
}

// LiveTypeHash is an in-memory cache over a TypeHash that automatically flushes
// dirty entries to disk every second. Objects are tracked for changes via PostUnlock.
//
// # Lock Ordering
//
// This type uses two mutexes with a strict ordering requirement:
//
//	stageMutex -> updatesMutex (always acquire stageMutex first)
//
// Methods that need both locks must acquire stageMutex first to avoid deadlock.
// Exception: The updated() callback only acquires updatesMutex since it is called
// from user code via PostUnlock after the object's own mutex is released.
//
// Methods suffixed with NOLOCK (e.g., getNOLOCK, setNOLOCK) expect the caller
// to already hold stageMutex.
type LiveTypeHash[T any, S structs.Snapshottable[T]] struct {
	hash         *TypeHash[T, S] // Underlying persistent storage
	stage        map[string]*T   // In-memory cache of loaded objects
	stageMutex   sync.RWMutex    // Protects stage
	updates      map[string]bool // Keys with dirty objects pending write to disk
	deletes      map[string]bool // Keys pending deletion, flushed to disk by Flush()
	updatesMutex sync.RWMutex    // Protects updates, deletes, and flushHealth
	done         chan struct{}   // Closed when flush goroutine exits
	flushHealth  FlushHealth     // Flush health tracking
}

// FlushHealth returns a snapshot of the current flush health state.
func (l *LiveTypeHash[T, S]) FlushHealth() FlushHealth {
	l.updatesMutex.RLock()
	defer l.updatesMutex.RUnlock()
	return l.flushHealth
}

// Flush atomically writes all pending updates and deletes to disk.
// All operations succeed or fail together using the underlying TypeHash.Proc.
func (l *LiveTypeHash[T, S]) Flush() error {
	// Hold both locks to build proc slice and clear pending maps atomically.
	// Lock order: stageMutex before updatesMutex (consistent with other methods).
	l.stageMutex.RLock()
	l.updatesMutex.Lock()

	procs := make([]Proc, 0, len(l.updates)+len(l.deletes))

	// Build update operations (skip keys pending deletion)
	for key := range l.updates {
		if l.deletes[key] {
			continue
		}
		obj, found := l.stage[key]
		if !found {
			continue
		}
		objCopy := S(obj).UnsafeShallowCopy()
		procs = append(procs, l.hash.SProc(key, func(k string, v *T) (*T, error) {
			return objCopy, nil
		}))
	}

	// Build delete operations
	for key := range l.deletes {
		procs = append(procs, l.hash.SProc(key, func(k string, v *T) (*T, error) {
			return nil, nil
		}))
	}

	// Clear pending maps before releasing locks
	l.updates = map[string]bool{}
	l.deletes = map[string]bool{}

	l.updatesMutex.Unlock()
	l.stageMutex.RUnlock()

	if len(procs) == 0 {
		return nil
	}

	// Execute all operations atomically
	return juicemud.WithStack(l.hash.Proc(procs, true))
}

// Close waits for the flush goroutine to stop, then flushes and closes the file.
func (l *LiveTypeHash[T, S]) Close() error {
	<-l.done // Wait for flush goroutine to stop
	if err := l.Flush(); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(l.hash.Close())
}

// Each flushes pending changes and iterates over all entries in the underlying hash.
func (l *LiveTypeHash[T, S]) Each() iter.Seq2[*T, error] {
	if err := l.Flush(); err != nil {
		return func(yield func(*T, error) bool) {
			yield(nil, juicemud.WithStack(err))
		}
	}
	return func(yield func(*T, error) bool) {
		for entry, err := range l.hash.Each() {
			if !yield(entry.V, err) {
				break
			}
		}
	}
}

// runFlushLoop periodically flushes dirty entries to disk until context is cancelled.
// Uses exponential backoff on errors (1s -> 2s -> 4s -> ... -> 30s max).
func (l *LiveTypeHash[T, S]) runFlushLoop(ctx context.Context) {
	defer close(l.done)

	interval := flushBaseInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			err := l.Flush()

			l.updatesMutex.Lock()
			if err != nil {
				interval = l.flushHealth.recordError(err)
				log.Printf("LiveTypeHash flush error (attempt %d, next retry in %v): %v",
					l.flushHealth.ConsecErrors, interval, err)
			} else {
				interval = l.flushHealth.recordSuccess()
			}
			l.updatesMutex.Unlock()

			timer.Reset(interval)
		}
	}
}

// updated marks an object as dirty. Called via PostUnlock from user code
// after the object's own mutex is released, so stageMutex is never held here.
func (l *LiveTypeHash[T, S]) updated(t *T) {
	l.updatesMutex.Lock()
	defer l.updatesMutex.Unlock()
	id := S(t).GetId()
	l.updates[id] = true
}

// SetIfMissing adds a value only if the key doesn't exist.
func (l *LiveTypeHash[T, S]) SetIfMissing(t *T) error {
	id := S(t).GetId()

	// Fast path: read-only check (safe with RLock, doesn't modify stage)
	l.stageMutex.RLock()
	_, inStage := l.stage[id]
	inHash := !inStage && l.hash.Has(id)
	l.stageMutex.RUnlock()
	if inStage || inHash {
		return nil
	}

	// Slow path: need to add, take write lock
	l.stageMutex.Lock()
	defer l.stageMutex.Unlock()

	// Double-check under write lock (another goroutine may have added it)
	if _, err := l.getNOLOCK(id); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return juicemud.WithStack(err)
	}

	return l.setNOLOCK(t)
}

// setNOLOCK stores a value without acquiring stageMutex.
// IMPORTANT: Caller must already hold stageMutex (write lock).
func (l *LiveTypeHash[T, S]) setNOLOCK(t *T) error {
	id := S(t).GetId()

	S(t).SetPostUnlock(l.updated)
	l.stage[id] = t
	return juicemud.WithStack(l.hash.Set(id, t, true))
}

// Set stores a value in the cache and writes it to disk immediately.
func (l *LiveTypeHash[T, S]) Set(t *T) error {
	l.stageMutex.Lock()
	defer l.stageMutex.Unlock()

	return juicemud.WithStack(l.setNOLOCK(t))
}

// GetMulti retrieves multiple values atomically. Returns an error if any key is missing.
func (l *LiveTypeHash[T, S]) GetMulti(keys map[string]bool) (map[string]*T, error) {
	l.stageMutex.Lock()
	defer l.stageMutex.Unlock()

	res := map[string]*T{}
	var err error
	for key := range keys {
		if res[key], err = l.getNOLOCK(key); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
	return res, nil
}

// LProc is a key-function pair for batch operations on LiveTypeHash.
type LProc[T any, S structs.Snapshottable[T]] struct {
	K string
	F func(string, *T) (*T, error)
}

// LProc creates an LProc for use with Proc.
func (l *LiveTypeHash[T, S]) LProc(key string, fun func(string, *T) (*T, error)) LProc[T, S] {
	return LProc[T, S]{
		K: key,
		F: fun,
	}
}

// Proc atomically applies multiple operations to the in-memory stage.
// Each LProc's function receives the current value and returns the new value
// (or nil to delete). Changes are written to disk on the next Flush.
func (l *LiveTypeHash[T, S]) Proc(procs []LProc[T, S]) error {
	l.stageMutex.Lock()
	defer l.stageMutex.Unlock()

	// Collect all changes before applying any (for atomicity on error)
	type change struct {
		key    string
		newVal *T
		delete bool
	}
	changes := make([]change, len(procs))

	for i, proc := range procs {
		foundV, err := l.getNOLOCK(proc.K)
		if errors.Is(err, os.ErrNotExist) {
			foundV = nil
		} else if err != nil {
			return juicemud.WithStack(err)
		}
		newV, err := proc.F(proc.K, foundV)
		if err != nil {
			return juicemud.WithStack(err)
		}
		changes[i] = change{key: proc.K, newVal: newV, delete: newV == nil}
	}

	// Apply all changes (none applied if any proc.F returned an error above)
	l.updatesMutex.Lock()
	defer l.updatesMutex.Unlock()

	for _, c := range changes {
		if c.delete {
			delete(l.stage, c.key)
			l.deletes[c.key] = true
			delete(l.updates, c.key)
		} else {
			S(c.newVal).SetPostUnlock(l.updated)
			l.stage[c.key] = c.newVal
			l.updates[c.key] = true
			delete(l.deletes, c.key) // In case it was previously marked for deletion
		}
	}

	return nil
}

// getNOLOCK retrieves a value without acquiring stageMutex.
// IMPORTANT: Caller must already hold stageMutex (read or write lock).
// This method does acquire updatesMutex internally to check the delete set.
func (l *LiveTypeHash[T, S]) getNOLOCK(k string) (*T, error) {
	l.updatesMutex.RLock()
	isDeleted := l.deletes[k]
	l.updatesMutex.RUnlock()
	if isDeleted {
		return nil, juicemud.WithStack(fmt.Errorf("key %q: %w", k, os.ErrNotExist))
	}

	if res, found := l.stage[k]; found {
		return res, nil
	}

	res, err := l.hash.Get(k)
	if err != nil {
		return nil, err
	}

	S(res).SetPostUnlock(l.updated)
	l.stage[k] = res

	return res, nil
}

// Has returns true if the key exists and is not pending deletion.
func (l *LiveTypeHash[T, S]) Has(k string) bool {
	l.stageMutex.RLock()
	defer l.stageMutex.RUnlock()

	l.updatesMutex.RLock()
	isDeleted := l.deletes[k]
	l.updatesMutex.RUnlock()
	if isDeleted {
		return false
	}

	if _, found := l.stage[k]; found {
		return true
	}
	return l.hash.Has(k)
}

// Del marks an object for deletion. The delete is written to disk on the next Flush.
// Returns os.ErrNotExist if the key doesn't exist.
func (l *LiveTypeHash[T, S]) Del(k string) error {
	l.stageMutex.Lock()
	defer l.stageMutex.Unlock()

	// Check if key exists (in stage or on disk)
	_, found := l.stage[k]
	if !found && !l.hash.Has(k) {
		return juicemud.WithStack(fmt.Errorf("key %q: %w", k, os.ErrNotExist))
	}

	// Remove from stage
	delete(l.stage, k)

	// Mark for deletion on disk
	l.updatesMutex.Lock()
	l.deletes[k] = true
	// Remove from updates since we're deleting
	delete(l.updates, k)
	l.updatesMutex.Unlock()

	return nil
}

// Get retrieves a typed value by key. Returns os.ErrNotExist if key doesn't exist.
func (l *LiveTypeHash[T, S]) Get(k string) (*T, error) {
	l.stageMutex.Lock()
	defer l.stageMutex.Unlock()
	return l.getNOLOCK(k)
}

// TypeHash wraps a Hash with automatic serialization for typed values.
// T is the value type, S is a pointer type implementing Serializable[T].
type TypeHash[T any, S structs.Serializable[T]] struct {
	*Hash
}

// Get retrieves a typed value by key. Returns os.ErrNotExist if key doesn't exist.
func (h *TypeHash[T, S]) Get(k string) (*T, error) {
	b, err := h.Hash.Get(k)
	if err != nil {
		return nil, err
	}
	t := S(new(T))
	if err := t.Unmarshal(b); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return (*T)(t), nil
}

// SEntry is a typed key-value pair used for iteration.
type SEntry[T any, S structs.Serializable[T]] struct {
	K string
	V *T
}

// Each iterates over all entries, deserializing values to type T.
func (h *TypeHash[T, S]) Each() iter.Seq2[SEntry[T, S], error] {
	return func(yield func(SEntry[T, S], error) bool) {
		for entry, err := range h.Hash.Each() {
			if err != nil {
				if !yield(SEntry[T, S]{
					K: entry.K,
					V: nil,
				}, juicemud.WithStack(err)) {
					break
				}
			} else {
				t := S(new(T))
				if err := t.Unmarshal(entry.V); err != nil {
					if !yield(SEntry[T, S]{
						K: entry.K,
						V: nil,
					}, juicemud.WithStack(err)) {
						break
					}
				} else {
					if !yield(SEntry[T, S]{
						K: entry.K,
						V: S(t),
					}, nil) {
						break
					}
				}
			}
		}
	}
}

// GetMulti retrieves multiple typed values atomically. Missing keys are omitted.
func (h *TypeHash[T, S]) GetMulti(keys map[string]bool) (map[string]*T, error) {
	byteResults := h.Hash.GetMulti(keys)
	results := map[string]*T{}
	for key, byteResult := range byteResults {
		result := S(new(T))
		if err := result.Unmarshal(byteResult); err != nil {
			return nil, juicemud.WithStack(err)
		}
		results[key] = (*T)(result)
	}
	return results, nil
}

// Set stores a typed value. If overwrite is false, fails if key exists.
func (h *TypeHash[T, S]) Set(k string, v *T, overwrite bool) error {
	s := S(v)
	b := make([]byte, s.Size())
	s.Marshal(b)
	return h.Hash.Set(k, b, overwrite)
}

// Proc is an atomic read-modify-write operation. Returns new value or nil to delete.
type Proc interface {
	Key() string
	Proc(string, []byte) ([]byte, error)
}

// BProc is a byte-level Proc implementation.
type BProc struct {
	K string
	F func(string, []byte) ([]byte, error)
}

func (p *BProc) Key() string {
	return p.K
}

func (p *BProc) Proc(k string, v []byte) ([]byte, error) {
	return p.F(k, v)
}

// SProc creates a typed Proc for use with Hash.Proc().
func (h TypeHash[T, S]) SProc(key string, fun func(string, *T) (*T, error)) *SProc[T, S] {
	return &SProc[T, S]{
		K: key,
		F: fun,
	}
}

// SProc is a typed Proc implementation that handles serialization.
type SProc[T any, S structs.Serializable[T]] struct {
	K string
	F func(string, *T) (*T, error)
}

func (j *SProc[T, S]) Key() string {
	return j.K
}

func (j *SProc[T, S]) Proc(k string, v []byte) ([]byte, error) {
	var input *T
	if v != nil {
		input = new(T)
		if err := S(input).Unmarshal(v); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
	output, err := j.F(k, input)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	if output != nil {
		outputS := S(output)
		v = make([]byte, outputS.Size())
		outputS.Marshal(v)
	} else {
		v = nil
	}
	return v, nil
}

// Proc atomically reads values, applies transformations, then writes results.
// Uses tkrzw's ProcessMulti for transactional semantics.
func (h *Hash) Proc(pairs []Proc, write bool) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	outputs := make([][]byte, len(pairs))
	procs := make([]tkrzw.KeyProcPair, len(pairs)*2)
	var abort error
	for index, pair := range pairs {
		procs[index] = tkrzw.KeyProcPair{
			Key: pair.Key(),
			Proc: func(key []byte, value []byte) any {
				if abort != nil {
					return nil
				}
				b, err := pair.Proc(string(key), value)
				if err != nil {
					abort = err
					return nil
				}
				outputs[index] = b
				return nil
			},
		}
	}
	for index, pair := range pairs {
		procs[index+len(pairs)] = tkrzw.KeyProcPair{
			Key: pair.Key(),
			Proc: func(key []byte, value []byte) any {
				if abort != nil {
					return nil
				}
				if outputs[index] == nil {
					return tkrzw.RemoveBytes
				} else if !bytes.Equal(value, outputs[index]) {
					return outputs[index]
				} else {
					return nil
				}
			},
		}
	}
	if stat := h.dbm.ProcessMulti(procs, write); !stat.IsOK() {
		return juicemud.WithStack(stat)
	}
	return juicemud.WithStack(abort)
}

// Tree wraps a tkrzw B-tree for ordered key-value storage.
// Supports hierarchical keys via Sub* methods using length-prefixed encoding.
type Tree struct {
	*Hash
}

// SubBProc is a byte-level proc for Tree's hierarchical keys.
// Implements the Proc interface so it can be used with Tree.Proc (inherited from Hash).
type SubBProc struct {
	compositeKey []byte
	F            func([]byte) ([]byte, error)
}

func (p *SubBProc) Key() string                            { return string(p.compositeKey) }
func (p *SubBProc) Proc(_ string, v []byte) ([]byte, error) { return p.F(v) }

// SubBProc creates a byte-level Proc for hierarchical keys. Use with Hash.Proc().
func (t *Tree) SubBProc(set, key string, f func([]byte) ([]byte, error)) *SubBProc {
	return &SubBProc{
		compositeKey: appendKey(nil, set, key),
		F:            f,
	}
}

// First returns the first (smallest) key-value pair in the tree.
// Returns os.ErrNotExist if tree is empty.
func (t *Tree) First() ([]byte, []byte, error) {
	t.mutex.RLock()
	defer t.mutex.RUnlock()
	iter := t.dbm.MakeIterator()
	defer iter.Destruct()
	if err := checkStatus(iter.First(), "tree is empty"); err != nil {
		return nil, nil, err
	}
	k, v, stat := iter.Get()
	if err := checkStatus(stat, "tree is empty"); err != nil {
		return nil, nil, err
	}
	return k, v, nil
}

// appendKey builds a composite key from parts using length-prefixed encoding.
func appendKey(b []byte, parts ...string) []byte {
	for _, part := range parts {
		partBytes := []byte(part)
		partBytesLen := uint32(len(partBytes))
		sizeBytes := make([]byte, binary.Size(partBytesLen))
		binary.BigEndian.PutUint32(sizeBytes, partBytesLen)
		b = append(b, sizeBytes...)
		b = append(b, partBytes...)
	}
	return b
}

// SubSet stores a value under a hierarchical key (set, key).
func (t *Tree) SubSet(set string, key string, b []byte) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	completeKey := appendKey(nil, set, key)
	if stat := t.Hash.dbm.Set(completeKey, b, true); !stat.IsOK() {
		return juicemud.WithStack(stat)
	}
	return nil
}

// SubDel removes a value under a hierarchical key (set, key).
func (t *Tree) SubDel(set string, key string) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	completeKey := appendKey(nil, set, key)
	return checkStatus(t.Hash.dbm.Remove(completeKey), fmt.Sprintf("set %q key %q", set, key))
}

// SubGet retrieves a value under a hierarchical key (set, key).
func (t *Tree) SubGet(set string, key string) ([]byte, error) {
	t.mutex.RLock()
	defer t.mutex.RUnlock()
	completeKey := appendKey(nil, set, key)
	b, stat := t.Hash.dbm.Get(completeKey)
	if err := checkStatus(stat, fmt.Sprintf("set %q key %q", set, key)); err != nil {
		return nil, err
	}
	return b, nil
}

// SubCount returns the number of keys in a set.
func (t *Tree) SubCount(set string) (int, error) {
	c := 0
	for _, err := range t.SubEach(set) {
		if err != nil {
			return 0, juicemud.WithStack(err)
		}
		c++
	}
	return c, nil
}

// SubEach iterates over all entries within a set.
func (t *Tree) SubEach(set string) iter.Seq2[BEntry, error] {
	keyPrefix := appendKey(nil, set)
	keyOffset := len(keyPrefix) + binary.Size(uint32(0))
	return func(yield func(BEntry, error) bool) {
		t.mutex.RLock()
		defer t.mutex.RUnlock()
		iter := t.dbm.MakeIterator()
		defer iter.Destruct()
		iter.Jump(keyPrefix)
		for {
			key, value, status := iter.Get()
			if status.GetCode() == tkrzw.StatusNotFoundError {
				break
			} else if !status.IsOK() {
				if !yield(BEntry{
					K: "",
					V: nil,
				}, juicemud.WithStack(status)) {
					break
				}
			} else {
				if bytes.HasPrefix(key, keyPrefix) {
					if len(key) < keyOffset {
						// Malformed key - too short to contain the sub-key
						if !yield(BEntry{}, juicemud.WithStack(fmt.Errorf("malformed key: too short"))) {
							break
						}
						iter.Next()
						continue
					}
					if !yield(BEntry{
						K: string(key[keyOffset:]),
						V: value,
					}, nil) {
						break
					}
				} else {
					break
				}
			}
			iter.Next()
		}
	}
}

// EachSet iterates over all unique set names in the tree.
// Uses jump-based iteration to efficiently skip all entries within each set.
func (t *Tree) EachSet() iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		t.mutex.RLock()
		defer t.mutex.RUnlock()

		iter := t.dbm.MakeIterator()
		defer iter.Destruct()

		// Start at the first key
		if stat := iter.First(); !stat.IsOK() {
			if stat.GetCode() != tkrzw.StatusNotFoundError {
				yield("", juicemud.WithStack(stat))
			}
			return
		}

		for {
			key, _, status := iter.Get()
			if status.GetCode() == tkrzw.StatusNotFoundError {
				break
			} else if !status.IsOK() {
				if !yield("", juicemud.WithStack(status)) {
					break
				}
				continue
			}

			// Extract set from key: [4 bytes len][set bytes][...]
			if len(key) < 4 {
				break // Invalid key format
			}
			setLen := binary.BigEndian.Uint32(key[:4])
			// Check for overflow and valid length (setLen must fit in remaining key bytes)
			if setLen > uint32(len(key)-4) {
				break // Invalid key format
			}
			set := string(key[4 : 4+setLen])

			if !yield(set, nil) {
				break
			}

			// Jump to the next set: increment the set prefix
			setPrefix := key[:4+setLen]
			nextSetKey := incrementBytes(setPrefix)
			if nextSetKey == nil {
				break // No more sets possible (all bytes were 0xFF)
			}

			// Jump to the incremented key
			iter.Jump(nextSetKey)
		}
	}
}

// incrementBytes increments a byte slice as a big-endian integer.
// Returns nil if all bytes overflow (were 0xFF).
func incrementBytes(b []byte) []byte {
	result := make([]byte, len(b))
	copy(result, b)

	for i := len(result) - 1; i >= 0; i-- {
		if result[i] < 0xFF {
			result[i]++
			return result
		}
		result[i] = 0
	}

	return nil // All bytes overflowed
}

// TypeTree provides typed operations on a Tree.
// It embeds *Tree to inherit tree operations, and casts to *TypeHash
// for typed get/set since Tree and TypeHash have identical memory layout.
type TypeTree[T any, S structs.Serializable[T]] struct {
	*Tree
}

// asTypeHash returns a TypeHash view of the underlying Hash for typed operations.
func (t *TypeTree[T, S]) asTypeHash() *TypeHash[T, S] {
	return &TypeHash[T, S]{t.Tree.Hash}
}

// Get retrieves a typed value by key.
func (t *TypeTree[T, S]) Get(k string) (*T, error) {
	return t.asTypeHash().Get(k)
}

// Set stores a typed value.
func (t *TypeTree[T, S]) Set(k string, v *T, overwrite bool) error {
	return t.asTypeHash().Set(k, v, overwrite)
}

// Each iterates over all entries in order.
func (t *TypeTree[T, S]) Each() iter.Seq2[SEntry[T, S], error] {
	return t.asTypeHash().Each()
}

// GetMulti retrieves multiple typed values atomically.
func (t *TypeTree[T, S]) GetMulti(keys map[string]bool) (map[string]*T, error) {
	return t.asTypeHash().GetMulti(keys)
}

// SubGet retrieves a typed value under a hierarchical key (set, key).
func (t *TypeTree[T, S]) SubGet(set, key string) (*T, error) {
	b, err := t.Tree.SubGet(set, key)
	if err != nil {
		return nil, err
	}
	v := S(new(T))
	if err := v.Unmarshal(b); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return (*T)(v), nil
}

// SubSet stores a typed value under a hierarchical key (set, key).
func (t *TypeTree[T, S]) SubSet(set, key string, v *T) error {
	s := S(v)
	b := make([]byte, s.Size())
	s.Marshal(b)
	return t.Tree.SubSet(set, key, b)
}

// SubEach iterates over all typed entries within a set.
func (t *TypeTree[T, S]) SubEach(set string) iter.Seq2[*T, error] {
	return func(yield func(*T, error) bool) {
		for entry, err := range t.Tree.SubEach(set) {
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			v := S(new(T))
			if err := v.Unmarshal(entry.V); err != nil {
				if !yield(nil, juicemud.WithStack(err)) {
					return
				}
				continue
			}
			if !yield((*T)(v), nil) {
				return
			}
		}
	}
}

// SubSProc is a typed Proc for hierarchical keys. Implements Proc interface.
type SubSProc[T any, S structs.Serializable[T]] struct {
	compositeKey []byte
	F            func(*T) (*T, error)
}

func (p *SubSProc[T, S]) Key() string { return string(p.compositeKey) }

func (p *SubSProc[T, S]) Proc(_ string, v []byte) ([]byte, error) {
	var input *T
	if v != nil {
		input = new(T)
		if err := S(input).Unmarshal(v); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
	output, err := p.F(input)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	if output != nil {
		s := S(output)
		v = make([]byte, s.Size())
		s.Marshal(v)
	} else {
		v = nil
	}
	return v, nil
}

// SubSProc creates a typed Proc for hierarchical keys. Use with Hash.Proc().
func (t *TypeTree[T, S]) SubSProc(set, key string, f func(*T) (*T, error)) *SubSProc[T, S] {
	return &SubSProc[T, S]{
		compositeKey: appendKey(nil, set, key),
		F:            f,
	}
}

// First returns the first (smallest) key-value pair. Returns os.ErrNotExist if empty.
func (t *TypeTree[T, S]) First() (string, *T, error) {
	k, v, err := t.Tree.First()
	if err != nil {
		return "", nil, err
	}
	first := S(new(T))
	if err := first.Unmarshal(v); err != nil {
		return "", nil, juicemud.WithStack(err)
	}
	return string(k), (*T)(first), nil
}

// OpenHash opens or creates a hash database file (appends .tkh extension).
func OpenHash(path string) (*Hash, error) {
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(fmt.Sprintf("%s.tkh", path), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"restore_mode":     "RESTORE_SYNC|RESTORE_NO_SHORTCUTS|RESTORE_WITH_HARDSYNC",
	})
	if !stat.IsOK() {
		return nil, juicemud.WithStack(stat)
	}
	return &Hash{dbm: dbm}, nil
}

// OpenTypeHash opens a typed hash database file.
func OpenTypeHash[T any, S structs.Serializable[T]](path string) (*TypeHash[T, S], error) {
	h, err := OpenHash(path)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return &TypeHash[T, S]{h}, nil
}

// OpenLiveTypeHash opens a LiveTypeHash and starts the flush goroutine.
// The goroutine stops when the context is cancelled.
func OpenLiveTypeHash[T any, S structs.Snapshottable[T]](ctx context.Context, path string) (*LiveTypeHash[T, S], error) {
	h, err := OpenHash(path)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	l := &LiveTypeHash[T, S]{
		hash:    &TypeHash[T, S]{h},
		stage:   map[string]*T{},
		updates: map[string]bool{},
		deletes: map[string]bool{},
		done:    make(chan struct{}),
	}
	go l.runFlushLoop(ctx)
	return l, nil
}

// OpenTree opens or creates a B-tree database file (appends .tkt extension).
func OpenTree(path string) (*Tree, error) {
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(fmt.Sprintf("%s.tkt", path), true, map[string]string{
		"page_update_mode": "PAGE_UPDATE_WRITE",
		"record_comp_mode": "RECORD_COMP_NONE",
		"key_comparator":   "LexicalKeyComparator",
	})
	if !stat.IsOK() {
		return nil, juicemud.WithStack(stat)
	}
	return &Tree{&Hash{dbm: dbm}}, nil
}

// OpenTypeTree opens a typed B-tree database file.
func OpenTypeTree[T any, S structs.Serializable[T]](path string) (*TypeTree[T, S], error) {
	t, err := OpenTree(path)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return &TypeTree[T, S]{t}, nil
}
