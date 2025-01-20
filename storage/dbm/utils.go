package dbm

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/estraier/tkrzw-go"
	"github.com/zond/juicemud"
)

func WithHash(t *testing.T, f func(Hash)) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "*.tkh")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	if err := os.Remove(tmpFile.Name()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tmpFile.Name(), 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpFile.Name())
	o := Opener{Dir: tmpFile.Name()}
	dbm := o.Hash("test")
	if o.Err != nil {
		t.Fatal(err)
	}
	f(dbm)
}

func WithTree(t *testing.T, f func(Tree)) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "*.tkh")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	if err := os.Remove(tmpFile.Name()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tmpFile.Name(), 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpFile.Name())
	o := Opener{Dir: tmpFile.Name()}
	dbm := o.Tree("test")
	if o.Err != nil {
		t.Fatal(err)
	}
	f(dbm)
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
