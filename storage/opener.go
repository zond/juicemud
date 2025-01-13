package storage

import (
	"fmt"
	"path/filepath"

	"github.com/estraier/tkrzw-go"
	"github.com/zond/juicemud"
)

type opener struct {
	Dir string
	Err error
}

func (o *opener) OpenHash(name string) *tkrzw.DBM {
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
		o.Err = juicemud.WithStack(stat)
	}
	return dbm
}

func (o *opener) OpenTree(name string) *tkrzw.DBM {
	if o.Err != nil {
		return nil
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
	return dbm
}
