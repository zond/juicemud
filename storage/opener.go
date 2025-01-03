package storage

import (
	"fmt"
	"path/filepath"

	"github.com/estraier/tkrzw-go"
	"github.com/pkg/errors"
)

type opener struct {
	dir string
	err error
}

func (o *opener) openHash(name string) *tkrzw.DBM {
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
		o.err = errors.WithStack(stat)
	}
	return dbm
}

func (o *opener) openTree(name string) *tkrzw.DBM {
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
		o.err = errors.WithStack(stat)
	}
	return dbm
}
