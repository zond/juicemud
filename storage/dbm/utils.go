package dbm

import (
	"context"
	"fmt"
	"io"
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

func withDB[T io.Closer](t testing.TB, suffix string, open func(string) (T, error), f func(T)) {
	t.Helper()
	withFile(t, suffix, func(path string) {
		db, err := open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		}()
		f(db)
	})
}

func WithHash(t testing.TB, f func(*Hash)) {
	t.Helper()
	withDB(t, ".tkh", OpenHash, f)
}

func WithTypeHash[T any, S structs.Serializable[T]](t testing.TB, f func(*TypeHash[T, S])) {
	t.Helper()
	withDB(t, ".tkh", OpenTypeHash[T, S], f)
}

func WithLiveTypeHash[T any, S structs.Snapshottable[T]](t testing.TB, f func(*LiveTypeHash[T, S])) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	withFile(t, ".tkh", func(path string) {
		db, err := OpenLiveTypeHash[T, S](ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			cancel()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		}()
		f(db)
	})
}

func WithTree(t testing.TB, f func(*Tree)) {
	t.Helper()
	withDB(t, ".tkt", OpenTree, f)
}

func WithTypeTree[T any, S structs.Serializable[T]](t testing.TB, f func(*TypeTree[T, S])) {
	t.Helper()
	withDB(t, ".tkt", OpenTypeTree[T, S], f)
}
