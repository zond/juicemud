//go:generate sh -c "capnp compile -I `go list -m -f '{{.Dir}}' capnproto.org/go/capnp/v3`/std -ogo user.capnp group.capnp group_member.capnp file.capnp object.capnp"
package storage

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"github.com/estraier/tkrzw-go"
	"golang.org/x/net/webdav"

	"capnproto.org/go/capnp/v3"
)

type hash struct {
	dbm *tkrzw.DBM
}

func (h *hash) get(key any) (*capnp.Message, error) {
	b, stat := h.dbm.Get(key)
	if !stat.IsOK() {
		return nil, stat
	}
	return capnp.Unmarshal(b)
}

func (h *hash) has(key any) (bool, error) {
	_, stat := h.dbm.Get(key)
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return false, nil
	} else if !stat.IsOK() {
		return false, stat
	}
	return true, nil
}

func (h *hash) set(key any, value *capnp.Message, overwrite bool) error {
	b, err := value.Marshal()
	if err != nil {
		return err
	}
	stat := h.dbm.Set(key, b, overwrite)
	if !stat.IsOK() {
		return stat
	}
	return nil
}

func (h *hash) del(key any) (bool, error) {
	if stat := h.dbm.Remove(key); stat.GetCode() == tkrzw.StatusNotFoundError {
		return false, nil
	} else if !stat.IsOK() {
		return false, stat
	} else {
		return true, nil
	}
}

func (h *hash) delMulti(keys []any) error {
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

type tree struct {
	*hash
	prefix []byte
	parent *tree
}

func (t *tree) subtree(prefix []byte) *tree {
	return &tree{
		hash:   t.hash,
		prefix: prefix,
		parent: t,
	}
}

func (t *tree) fullPrefix() []byte {
	if t == nil {
		return nil
	}
	parentPrefix := t.parent.fullPrefix()
	parentPrefixLen := len(parentPrefix)
	prefixLen := len(t.prefix)
	lenSize := binary.Size(prefixLen)
	fullPrefix := make([]byte, parentPrefixLen+lenSize+prefixLen)
	copy(fullPrefix, parentPrefix)
	if _, err := binary.Encode(fullPrefix[parentPrefixLen:], binary.BigEndian, prefixLen); err != nil {
		// Should never ever happen.
		panic(err)
	}
	copy(fullPrefix[parentPrefixLen+lenSize:], prefix)
	return fullPrefix
}

func (t *tree) fullPrefixAndKey(key []byte) ([]byte, []byte) {
	prefix := t.fullPrefix()
	fullKey := make([]byte, len(prefix))
	copy(fullKey, prefix)
	copy(fullKey[len(prefix):], key)
	return prefix, fullKey
}

func (t *tree) each(firstChild []byte, f func(completeKey []byte, childKey []byte) (bool, error)) error {
	fullPrefix, firstChildFullKey := t.fullPrefixAndKey(firstChild)
	iter := t.dbm.MakeIterator()
	defer iter.Destruct()
	stat := iter.Jump(firstChildFullKey)
	if !stat.IsOK() {
		return stat
	}
	for ; stat.IsOK(); stat = iter.Next() {
		key, stat := iter.GetKey()
		if !stat.IsOK() {
			return stat
		}
		if !bytes.HasPrefix(key, fullPrefix) {
			break
		}
		cont, err := f(key, key[len(fullPrefix):])
		if err != nil {
			return err
		}
		if !cont {
			break
		}
	}
	if !stat.IsOK() {
		return stat
	}
	return nil
}

func (t *tree) removeManyToMany(key []byte, keyToOther *tree, otherToKey *tree) (bool, error) {
	_, fullKey := t.fullPrefixAndKey(key)
	if exists, err := t.has(fullKey); err != nil {
		return false, err
	} else if !exists {
		return false, nil
	}
	toRemove := []any{fullKey}
	keyToOther.subtree(key).each(nil, func(completeKey []byte, childKey []byte) (bool, error) {
		toRemove = append(toRemove, completeKey)
		_, reverseKey := otherToKey.subtree(childKey).fullPrefixAndKey(childKey)
		toRemove = append(toRemove, reverseKey)
		return true, nil
	})
	return true, t.delMulti(toRemove)
}

type opener struct {
	dir string
	err error
}

func (o *opener) openHash(name string) *hash {
	if o.err != nil {
		return nil
	}
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(filepath.Join(o.dir, fmt.Sprintf("%s.tkh", name)), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"restore_mode":     "RESTORE_SYNC|RESTORE_NO_SHORTCUTS|RESTORE_WITH_HARDSYNC",
	})
	if !stat.IsOK() {
		o.err = stat
	}
	return &hash{dbm}
}

func (o *opener) openTree(name string) *tree {
	if o.err != nil {
		return nil
	}
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(filepath.Join(o.dir, fmt.Sprintf("%s.tkt", name)), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"key_comparator":   "LexicalKeyComparator",
	})
	if !stat.IsOK() {
		o.err = stat
	}
	return &tree{&hash{dbm}}
}

var (
	membersByGroupCollection = []byte("membersByGroup")
	groupsByMemberCollection = []byte("groupsByMember")
	usersCollection          = []byte("users")
	groupsCollection         = []byte("groups")
)

func New(dir string) (*Storage, error) {
	o := &opener{dir: dir}
	s := &Storage{
		users:   o.openTree("users"),
		files:   o.openTree("files"),
		sources: o.openHash("sources"),
		objects: o.openHash("objects"),
		queue:   o.openTree("queue"),
	}
	return s, o.err
}

type Storage struct {
	users   *tree
	files   *tree
	sources *hash
	objects *hash
	queue   *tree
}

func (s *Storage) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return nil
}

func (s *Storage) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	return nil, nil
}

func (s *Storage) RemoveAll(ctx context.Context, name string) error {
	return nil
}

func (s *Storage) Rename(ctx context.Context, oldName, newName string) error {
	return nil
}

func (s *Storage) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	return nil, nil
}

func (s *Storage) LoadUser(name any) (*User, error) {
	key, err := s.users.concatKey(usersCollection, tkrzw.ToByteArray(name))
	if err != nil {
		return nil, err
	}
	msg, err := s.users.get(key)
	if err != nil {
		return nil, err
	}
	user, err := ReadRootUser(msg)
	return &user, err
}

func (s *Storage) StoreUser(u *User, overwrite bool) error {
	name, err := u.Name()
	if err != nil {
		return err
	}
	key, err := s.users.concatKey(usersCollection, tkrzw.ToByteArray(name))
	if err != nil {
		return err
	}
	return s.users.set(key, u.Message(), overwrite)
}

func (s *Storage) RemoveUser(u *User) (bool, error) {
	name, err := u.Name()
	if err != nil {
		return false, err
	}
	nameBytes := tkrzw.ToByteArray(name)
	userKey, err := s.users.concatKey(usersCollection, nameBytes)
	if err != nil {
		return false, err
	}
	if exists, err := s.users.has(userKey); err != nil {
		return false, err
	} else if !exists {
		return false, nil
	}

	toRemove := [][]byte{userKey}
	s.users.eachChildKey(groupsByMemberCollection, nameBytes, nil, func(groupsByMemberKey []byte, groupKey []byte) (bool, error) {
		toRemove = append(toRemove, groupsByMemberKey)
		_, membersByGroupKey, err := s.users.concateChildKey(membersByGroupCollection, groupKey, userKey)
		if err != nil {
			return false, err
		}
		toRemove = append(toRemove, membersByGroupKey)
		return true, nil
	})

}

// func (s *Storage) LoadGroup(name string) (*Group, error) {
// 	msg, err := s.groups.get(name)
// 	if err != nil {
// 		return nil, err
// 	}
// 	group, err := ReadRootGroup(msg)
// 	return &group, err
// }

// func (s *Storage) StoreGroup(g *Group, overwrite bool) error {
// 	name, err := g.Name()
// 	if err != nil {
// 		return err
// 	}
// 	return s.groups.set(name, g.Message(), overwrite)
// }

// func (s *Storage) EachGroupMember(groupName string, startAt string, f func(*User) (bool, error)) error {
// 	return s.users.each(membersByGroup, []byte(groupName), []byte(startAt), func(childKey []byte]) (bool, error) {
// 		user, err := s.LoadUser(childKey)
// 		if err != nil {
// 			return false, err
// 		}
// 		return f(user)
// 	})
// }

// func (s *Storage) EachMemberGroup(memberName string, startAt string, f func(*Group) (bool, error)) error {
// 	return s.groupMembers.each(groupsByMember, []byte(memberName), []byte(startAt), func(msg *capnp.Message) (bool, error) {
// 		groupMember, err := ReadRootGroupMember(msg)
// 		if err != nil {
// 			return false, err
// 		}
// 		groupName, err := groupMember.Group()
// 		if err != nil {
// 			return false, err
// 		}
// 		group, err := s.LoadGroup(groupName)
// 		if err != nil {
// 			return false, err
// 		}
// 		return f(group)
// 	})
// }
