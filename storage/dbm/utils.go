package dbm

import (
	"os"
	"testing"
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
