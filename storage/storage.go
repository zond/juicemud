//go:generate sh -c "capnp compile -I `go list -m -f '{{.Dir}}' capnproto.org/go/capnp/v3`/std -ogo user.capnp group.capnp group_member.capnp file.capnp"
package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/estraier/tkrzw-go"
	"golang.org/x/net/webdav"

	_ "capnproto.org/go/capnp/v3"
)

func serror(status *tkrzw.Status) error {
	if status.IsOK() {
		return nil
	}
	return status
}

type tree struct {
	*tkrzw.DBM
}

type hash struct {
	*tkrzw.DBM
}

type opener struct {
	dir string
	err error
}

func (o *opener) openHash(name string) hash {
	if o.err != nil {
		return nil
	}
	dbm := tkrzw.NewDBM()
	o.err = serror(dbm.Open(filepath.Join(o.dir, fmt.Sprintf("%s.tkh", name)), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"restore_mode":     "RESTORE_SYNC|RESTORE_NO_SHORTCUTS|RESTORE_WITH_HARDSYNC",
	}))
	return hash{dbm}
}

func (o *opener) openTree(name string) tree {
	if o.err != nil {
		return nil
	}
	dbm := tkrzw.NewDBM()
	o.err = serror(dbm.Open(filepath.Join(o.dir, fmt.Sprintf("%s.tkt", name)), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"key_comparator":   "SignedBigEndianKeyComparator",
	}))
	return tree{dbm}
}

func New(dir string) (*Storage, error) {
	o := &opener{dir: dir}
	s := &Storage{
		dir:          dir,
		users:        o.openHash("users"),
		groups:       o.openHash("groups"),
		groupMembers: o.openTree("groupMembers"),
		files:        o.openTree("files"),
		source:       o.openHash("source"),
		state:        o.openHash("state"),
		queue:        o.openTree("queue"),
	}
	return s, o.err
}

type Storage struct {
	dir          string
	err          error
	users        hash
	groups       hash
	groupMembers tree
	files        tree
	source       hash
	state        hash
	queue        tree
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
