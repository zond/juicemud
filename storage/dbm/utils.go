package dbm

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
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

func WithHash(t *testing.T, f func(Hash)) {
	t.Helper()
	withFile(t, ".tkh", func(path string) {
		dbm, err := OpenHash(path)
		if err != nil {
			t.Fatal(err)
		}
		f(dbm)
	})
}

func WithStructHash[T any, S Serializable[T]](t *testing.T, f func(StructHash[T, S])) {
	t.Helper()
	withFile(t, ".tkh", func(path string) {
		dbm, err := OpenStructHash[T, S](path)
		if err != nil {
			t.Fatal(err)
		}
		f(dbm)
	})
}

func WithTree(t *testing.T, f func(Tree)) {
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

func WithStructTree[T any, S Serializable[T]](t *testing.T, f func(StructTree[T, S])) {
	t.Helper()
	t.Helper()
	withFile(t, ".tkt", func(path string) {
		dbm, err := OpenStructTree[T, S](path)
		if err != nil {
			t.Fatal(err)
		}
		f(dbm)
	})
}
