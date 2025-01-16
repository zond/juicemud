package dbm

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/estraier/tkrzw-go"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"

	goccy "github.com/goccy/go-json"
)

type Hash struct {
	dbm *tkrzw.DBM
}

func (h Hash) GetJSON(k string, v any) error {
	b, stat := h.dbm.Get(k)
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return juicemud.WithStack(os.ErrNotExist)
	} else if !stat.IsOK() {
		return juicemud.WithStack(stat)
	}
	return juicemud.WithStack(goccy.Unmarshal(b, v))
}

func (h Hash) GetJSONMulti(k []string, v any) error {
	vVal := reflect.ValueOf(v)
	if vVal.Kind() != reflect.Ptr {
		return errors.Errorf("%v is no reflect.Ptr", v)
	}
	vVal = vVal.Elem()
	if vVal.Kind() != reflect.Slice {
		return errors.Errorf("%v is no pointer to reflect.Slice", v)
	}
	vVal.Set(reflect.MakeSlice(vVal.Type(), len(k), len(k)))
	results := h.dbm.GetMulti(k)
	for index, key := range k {
		b, found := results[key]
		if !found {
			return errors.Wrapf(os.ErrNotExist, "key %q not found", key)
		}
		if err := goccy.Unmarshal(b, vVal.Index(index).Addr().Interface()); err != nil {
			return juicemud.WithStack(err)
		}
	}
	return nil
}

func (h Hash) Get(k string) ([]byte, error) {
	b, stat := h.dbm.Get(k)
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return nil, juicemud.WithStack(os.ErrNotExist)
	} else if !stat.IsOK() {
		return nil, juicemud.WithStack(stat)
	}
	return b, nil
}

func (h Hash) SetJSON(k string, v any, overwrite bool) error {
	b, err := goccy.Marshal(v)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if stat := h.dbm.Set(k, b, overwrite); !stat.IsOK() {
		return juicemud.WithStack(err)
	}
	return nil
}

func (h Hash) Set(k string, v []byte, overwrite bool) error {
	return juicemud.WithStack(h.dbm.Set(k, v, overwrite))
}

func (h Hash) Del(k string) error {
	if stat := h.dbm.Remove(k); !stat.IsOK() {
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

type JProc[T any] struct {
	K string
	F func(string, *T) (*T, error)
}

func (j JProc[T]) Key() string {
	return j.K
}

func (j JProc[T]) Proc(k string, v []byte) ([]byte, error) {
	var input *T
	if v != nil {
		input = new(T)
		if err := goccy.Unmarshal(v, input); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
	output, err := j.F(k, input)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	if output != nil {
		if v, err = goccy.Marshal(output); err != nil {
			return nil, juicemud.WithStack(err)
		}
	} else {
		v = nil
	}
	return v, nil
}

func (h Hash) Proc(pairs []Proc, write bool) error {
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
				} else {
					return outputs[index]
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

type Opener struct {
	Dir string
	Err error
}

func (o *Opener) Hash(name string) Hash {
	if o.Err != nil {
		return Hash{}
	}
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(filepath.Join(o.Dir, fmt.Sprintf("%s.tkh", name)), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"restore_mode":     "RESTORE_SYNC|RESTORE_NO_SHORTCUTS|RESTORE_WITH_HARDSYNC",
	})
	if !stat.IsOK() {
		o.Err = juicemud.WithStack(stat)
	}
	return Hash{dbm}
}

func (o *Opener) Tree(name string) Tree {
	if o.Err != nil {
		return Tree{}
	}
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(filepath.Join(o.Dir, fmt.Sprintf("%s.tkt", name)), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"key_comparator":   "SignedBigEndianKeyComparator",
	})
	if !stat.IsOK() {
		o.Err = juicemud.WithStack(stat)
	}
	return Tree{Hash{dbm}}
}
