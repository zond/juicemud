//go:generate sh -c "capnp compile -I `go list -m -f '{{.Dir}}' capnproto.org/go/capnp/v3`/std -ogo tester.capnp"
package cabinet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"reflect"

	"capnproto.org/go/capnp/v3"
	"github.com/estraier/tkrzw-go"
)

type Hash struct {
	dbm *tkrzw.DBM
}

func NewHash(dir string, name string) (*Hash, error) {
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(filepath.Join(dir, fmt.Sprintf("%s.tkh", name)), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"restore_mode":     "RESTORE_SYNC|RESTORE_NO_SHORTCUTS|RESTORE_WITH_HARDSYNC",
	})
	if !stat.IsOK() {
		return nil, stat
	}
	return &Hash{dbm}, nil
}

var (
	ErrNotFound = errors.New("ErrNotFound")
)

func (h *Hash) Get(key any) (*capnp.Message, error) {
	b, stat := h.dbm.Get(key)
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return nil, ErrNotFound
	} else if !stat.IsOK() {
		return nil, stat
	}
	return capnp.Unmarshal(b)
}

func (h *Hash) Len() (int, error) {
	size, stat := h.dbm.Count()
	if !stat.IsOK() {
		return 0, stat
	}
	return int(size), nil
}

func (h *Hash) Has(key any) (bool, error) {
	_, stat := h.dbm.Get(key)
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return false, nil
	} else if !stat.IsOK() {
		return false, stat
	}
	return true, nil
}

var (
	ErrDuplication = fmt.Errorf("ErrDuplication")
)

func (h *Hash) Set(key any, value *capnp.Message, overwrite bool) error {
	log.Printf("setting %+v", key)
	b, err := value.Marshal()
	if err != nil {
		return err
	}
	stat := h.dbm.Set(key, b, overwrite)
	if stat.GetCode() == tkrzw.StatusDuplicationError {
		return ErrDuplication
	} else if !stat.IsOK() {
		return stat
	}
	return nil
}

func (h *Hash) Del(key any) (bool, error) {
	if stat := h.dbm.Remove(key); stat.GetCode() == tkrzw.StatusNotFoundError {
		return false, nil
	} else if !stat.IsOK() {
		return false, stat
	} else {
		return true, nil
	}
}

func (h *Hash) DelMulti(keys []any) error {
	pairs := make([]tkrzw.KeyProcPair, len(keys))
	for index, key := range keys {
		pairs[index] = tkrzw.KeyProcPair{
			Key: key,
			Proc: func(key []byte, value []byte) any {
				return tkrzw.RemoveBytes
			},
		}
	}
	stat := h.dbm.ProcessMulti(pairs, true)
	if !stat.IsOK() {
		return stat
	}
	return nil
}

type Tree struct {
	hash   Hash
	prefix []byte
	parent *Tree
}

func NewTree(dir string, name string) (*Tree, error) {
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(filepath.Join(dir, fmt.Sprintf("%s.tkt", name)), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"key_comparator":   "LexicalKeyComparator",
	})
	if !stat.IsOK() {
		return nil, stat
	}
	return &Tree{hash: Hash{dbm}}, nil
}

func (t *Tree) Len() (int, error) {
	if t.parent == nil {
		return t.hash.Len()
	}
	size := 0
	if err := t.Each(nil, func(_key []byte, _msg *capnp.Message) (bool, error) {
		size++
		return true, nil
	}); err != nil {
		return 0, err
	}
	return size, nil
}

func concat(prefix []byte, key []byte) []byte {
	result := make([]byte, len(prefix)+len(key))
	copy(result, prefix)
	copy(result[len(prefix):], key)
	return result
}

func concatWithSeparator(prefix []byte, separator int, key []byte) []byte {
	prefixLen := len(prefix)
	sep32 := int32(separator)
	sepSize := binary.Size(sep32)
	result := make([]byte, prefixLen+sepSize+len(key))
	copy(result, prefix)
	encodeDestination := result[prefixLen:]
	if _, err := binary.Encode(encodeDestination, binary.BigEndian, sep32); err != nil {
		panic(fmt.Errorf("this should never happen, but unable to write a %v to a %v of length %v: %v",
			reflect.TypeOf(sep32),
			reflect.TypeOf(encodeDestination),
			len(encodeDestination),
			err))
	}
	copy(result[prefixLen+sepSize:], key)
	return result
}

func (t *Tree) fullPrefix() []byte {
	if t == nil || t.parent == nil {
		return nil
	}
	return concatWithSeparator(t.parent.fullPrefix(), len(t.prefix), t.prefix)
}

func (t *Tree) fullKey(key any) []byte {
	return concatWithSeparator(t.fullPrefix(), 0, tkrzw.ToByteArray(key))
}

func (t *Tree) Get(key any) (*capnp.Message, error) {
	return t.hash.Get(t.fullKey(key))
}

func (t *Tree) Has(key any) (bool, error) {
	return t.hash.Has(t.fullKey(key))
}

func (t *Tree) Set(key any, value *capnp.Message, overwrite bool) error {
	return t.hash.Set(t.fullKey(key), value, overwrite)
}

func (t *Tree) Del(key any) (bool, error) {
	return t.hash.Del(t.fullKey(key))
}

func (t *Tree) DelMulti(keys []any) error {
	fullKeys := make([]any, len(keys))
	for index, key := range keys {
		fullKeys[index] = t.fullKey(key)
	}
	return t.hash.DelMulti(fullKeys)
}

func (t *Tree) Subtree(prefix any) *Tree {
	return &Tree{
		hash:   t.hash,
		prefix: tkrzw.ToByteArray(prefix),
		parent: t,
	}
}

func (t *Tree) each(firstChild any, f func(fullKey []byte, msg *capnp.Message) (bool, error)) error {
	treePrefix := concatWithSeparator(t.fullPrefix(), 0, nil)
	firstChildKey := concat(treePrefix, tkrzw.ToByteArray(firstChild))
	iter := t.hash.dbm.MakeIterator()
	defer iter.Destruct()
	stat := iter.Jump(firstChildKey)
	if !stat.IsOK() {
		return stat
	}
	log.Printf("jumped to %+v", firstChildKey)
	var fullKey, value []byte
	for ; stat.IsOK(); stat = iter.Next() {
		fullKey, value, stat = iter.Get()
		if !stat.IsOK() {
			break
		}
		log.Printf("found %+v, checking if it has treePrefix %+v", fullKey, treePrefix)
		if !bytes.HasPrefix(fullKey, treePrefix) {
			break
		}
		msg, err := capnp.Unmarshal(value)
		if err != nil {
			return err
		}
		cont, err := f(fullKey, msg)
		if err != nil {
			return err
		}
		if !cont {
			break
		}
	}
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return nil
	} else if !stat.IsOK() {
		return stat
	}
	return nil
}

func (t *Tree) Each(firstChildKey any, f func(childKey []byte, msg *capnp.Message) (bool, error)) error {
	treePrefix := concatWithSeparator(t.fullPrefix(), 0, nil)
	return t.each(firstChildKey, func(fullKey []byte, msg *capnp.Message) (bool, error) {
		return f(fullKey[len(treePrefix):], msg)
	})
}
