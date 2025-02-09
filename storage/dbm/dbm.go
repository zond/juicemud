package dbm

import (
	"bytes"
	"fmt"
	"os"
	"sync"

	"github.com/estraier/tkrzw-go"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/structs"
)

type Hash struct {
	dbm   *tkrzw.DBM
	mutex *sync.RWMutex
}

func (h Hash) Get(k string) ([]byte, error) {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	b, stat := h.dbm.Get(k)
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return nil, juicemud.WithStack(os.ErrNotExist)
	} else if !stat.IsOK() {
		return nil, juicemud.WithStack(stat)
	}
	return b, nil
}

func (h Hash) Set(k string, v []byte, overwrite bool) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	if stat := h.dbm.Set(k, v, overwrite); !stat.IsOK() {
		return juicemud.WithStack(stat)
	}
	return nil
}

func (h Hash) Del(k string) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	if stat := h.dbm.Remove(k); !stat.IsOK() {
		return juicemud.WithStack(stat)
	}
	return nil
}

type TypeHash[T any, S structs.Serializable[T]] struct {
	Hash
}

func (h TypeHash[T, S]) Get(k string) (*T, error) {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	b, stat := h.dbm.Get(k)
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return nil, juicemud.WithStack(os.ErrNotExist)
	} else if !stat.IsOK() {
		return nil, juicemud.WithStack(stat)
	}
	t := S(new(T))
	if err := t.Unmarshal(b); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return (*T)(t), nil
}

func (h TypeHash[T, S]) GetMulti(keys map[string]bool) (map[string]*T, error) {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	ids := make([]string, 0, len(keys))
	for key := range keys {
		ids = append(ids, key)
	}
	byteResults := h.dbm.GetMulti(ids)
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

func (h TypeHash[T, S]) Set(k string, v *T, overwrite bool) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	s := S(v)
	b := make([]byte, s.Size())
	s.Marshal(b)
	if stat := h.dbm.Set(k, b, overwrite); !stat.IsOK() {
		return juicemud.WithStack(stat)
	}
	return nil
}

type Proc interface {
	Key() string
	Proc(string, []byte) ([]byte, error)
}

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

func (h TypeHash[T, S]) SProc(key string, fun func(string, *T) (*T, error)) SProc[T, S] {
	return SProc[T, S]{
		K: key,
		F: fun,
	}
}

type SProc[T any, S structs.Serializable[T]] struct {
	K string
	F func(string, *T) (*T, error)
}

func (j SProc[T, S]) Key() string {
	return j.K
}

func (j SProc[T, S]) Proc(k string, v []byte) ([]byte, error) {
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

func (h Hash) Proc(pairs []Proc, write bool) error {
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

type Tree struct {
	Hash
}

type TypeTree[T any, S structs.Serializable[T]] struct {
	TypeHash[T, S]
}

func (t TypeTree[T, S]) First() (*T, error) {
	t.mutex.RLock()
	defer t.mutex.RUnlock()
	iter := t.dbm.MakeIterator()
	defer iter.Destruct()
	if stat := iter.First(); !stat.IsOK() {
		return nil, juicemud.WithStack(stat)
	}
	_, b, stat := iter.Get()
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return nil, juicemud.WithStack(os.ErrNotExist)
	} else if !stat.IsOK() {
		return nil, juicemud.WithStack(stat)
	}
	first := S(new(T))
	if err := first.Unmarshal(b); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return (*T)(first), nil
}

func OpenHash(path string) (Hash, error) {
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(fmt.Sprintf("%s.tkh", path), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"restore_mode":     "RESTORE_SYNC|RESTORE_NO_SHORTCUTS|RESTORE_WITH_HARDSYNC",
	})
	if !stat.IsOK() {
		return Hash{}, juicemud.WithStack(stat)
	}
	return Hash{dbm, &sync.RWMutex{}}, nil
}

func OpenTypeHash[T any, S structs.Serializable[T]](path string) (TypeHash[T, S], error) {
	h, err := OpenHash(path)
	if err != nil {
		return TypeHash[T, S]{}, juicemud.WithStack(err)
	}
	return TypeHash[T, S]{h}, nil
}

func OpenTree(path string) (Tree, error) {
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(fmt.Sprintf("%s.tkt", path), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"key_comparator":   "LexicalKeyComparator",
	})
	if !stat.IsOK() {
		return Tree{}, juicemud.WithStack(stat)
	}
	return Tree{Hash{dbm, &sync.RWMutex{}}}, nil
}

func OpenTypeTree[T any, S structs.Serializable[T]](path string) (TypeTree[T, S], error) {
	t, err := OpenTree(path)
	if err != nil {
		return TypeTree[T, S]{}, juicemud.WithStack(err)
	}
	return TypeTree[T, S]{TypeHash[T, S](t)}, nil
}
