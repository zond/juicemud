//go:generate sh -c "capnp compile -I `go list -m -f '{{.Dir}}' capnproto.org/go/capnp/v3`/std -ogo tester.capnp"
package cabinet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"

	"capnproto.org/go/capnp/v3"
	"github.com/estraier/tkrzw-go"
)

type Opener struct {
	Dir string
	Err error
}

func (o *Opener) Hash(name string) *Hash {
	if o.Err != nil {
		return nil
	}
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(filepath.Join(o.Dir, fmt.Sprintf("%s.tkh", name)), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"restore_mode":     "RESTORE_SYNC|RESTORE_NO_SHORTCUTS|RESTORE_WITH_HARDSYNC",
	})
	if !stat.IsOK() {
		o.Err = stat
	}
	return &Hash{dbm}
}

func (o *Opener) Tree(name string) *Tree {
	if o.Err != nil {
		return nil
	}
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(filepath.Join(o.Dir, fmt.Sprintf("%s.tkt", name)), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"key_comparator":   "LexicalKeyComparator",
	})
	if !stat.IsOK() {
		o.Err = stat
	}
	return &Tree{hash: Hash{dbm}}
}

type Hash struct {
	dbm *tkrzw.DBM
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

func (t *Tree) fullPrefix() []byte {
	if t == nil {
		return nil
	}
	parentPrefix := t.parent.fullPrefix()
	parentPrefixLen := len(parentPrefix)
	prefixLen := int32(len(t.prefix))
	lenSize := binary.Size(prefixLen)
	fullPrefix := make([]byte, parentPrefixLen+lenSize+int(prefixLen))
	copy(fullPrefix, parentPrefix)
	encodeDestination := fullPrefix[parentPrefixLen:]
	if _, err := binary.Encode(encodeDestination, binary.BigEndian, prefixLen); err != nil {
		panic(fmt.Errorf("this should never happen, but unable to write a %v to a %v of length %v: %v",
			reflect.TypeOf(prefixLen),
			reflect.TypeOf(encodeDestination),
			len(encodeDestination),
			err))
	}
	copy(fullPrefix[parentPrefixLen+lenSize:], t.prefix)
	return fullPrefix
}

func concat(prefix []byte, key []byte) []byte {
	result := make([]byte, len(prefix)+len(key))
	copy(result, prefix)
	copy(result[len(prefix):], key)
	return result
}

func (t *Tree) fullKey(key any) []byte {
	return concat(t.fullPrefix(), tkrzw.ToByteArray(key))
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

func (t *Tree) Subtree(prefix []byte) *Tree {
	return &Tree{
		hash:   t.hash,
		prefix: prefix,
		parent: t,
	}
}

func (t *Tree) each(firstChild any, f func(fullKey []byte, msg *capnp.Message) (bool, error)) error {
	treePrefix := t.fullPrefix()
	firstChildKey := concat(treePrefix, tkrzw.ToByteArray(firstChild))
	iter := t.hash.dbm.MakeIterator()
	defer iter.Destruct()
	stat := iter.Jump(firstChildKey)
	if !stat.IsOK() {
		return stat
	}
	var fullKey, value []byte
	for ; stat.IsOK(); stat = iter.Next() {
		fullKey, value, stat = iter.Get()
		if !stat.IsOK() {
			break
		}
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
	treePrefix := t.fullPrefix()
	return t.each(firstChildKey, func(fullKey []byte, msg *capnp.Message) (bool, error) {
		return f(fullKey[len(treePrefix):], msg)
	})
}

func (t *Tree) DelManyToMany(key []byte, keyToOther *Tree, otherToKey *Tree) (bool, error) {
	fullKey := t.fullKey(key)
	if exists, err := t.Has(fullKey); err != nil {
		return false, err
	} else if !exists {
		return false, nil
	}
	toRemove := []any{fullKey}
	keyToOtherPrefix := keyToOther.fullPrefix()
	keyToOther.Subtree(key).Each(nil, func(otherChildKey []byte, _ *capnp.Message) (bool, error) {
		toRemove = append(toRemove, concat(keyToOtherPrefix, otherChildKey))
		toRemove = append(toRemove, otherToKey.Subtree(otherChildKey).fullKey(key))
		return true, nil
	})
	return true, t.hash.DelMulti(toRemove)
}
