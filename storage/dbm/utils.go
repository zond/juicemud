package dbm

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zond/juicemud/structs"
)

func withFile(t testing.TB, suffix string, f func(string)) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	f(filepath.Join(tmpDir, fmt.Sprintf("test%s", suffix)))
}

func WithHash(t testing.TB, f func(Hash)) {
	t.Helper()
	withFile(t, ".tkh", func(path string) {
		dbm, err := OpenHash(path)
		if err != nil {
			t.Fatal(err)
		}
		f(dbm)
	})
}

func WithTypeHash[T any, S structs.Serializable[T]](t testing.TB, f func(TypeHash[T, S])) {
	t.Helper()
	withFile(t, ".tkh", func(path string) {
		dbm, err := OpenTypeHash[T, S](path)
		if err != nil {
			t.Fatal(err)
		}
		f(dbm)
	})
}

func WithTree(t testing.TB, f func(Tree)) {
	t.Helper()
	t.Helper()
	withFile(t, ".tkt", func(path string) {
		dbm, err := OpenTree(path)
		if err != nil {
			t.Fatal(err)
		}
		f(dbm)
	})
}

func WithTypeTree[T any, S structs.Serializable[T]](t testing.TB, f func(TypeTree[T, S])) {
	t.Helper()
	t.Helper()
	withFile(t, ".tkt", func(path string) {
		dbm, err := OpenTypeTree[T, S](path)
		if err != nil {
			t.Fatal(err)
		}
		f(dbm)
	})
}
