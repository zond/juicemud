package cabinet

import (
	"os"
	"reflect"
	"testing"

	"capnproto.org/go/capnp/v3"
)

func newTester(t *testing.T, name string, comment string) Tester {
	t.Helper()
	arena := capnp.SingleSegment(nil)
	_, seg, err := capnp.NewMessage(arena)
	if err != nil {
		t.Fatal(err)
	}
	tester, err := NewRootTester(seg)
	if err != nil {
		t.Fatal(err)
	}
	if err := tester.SetName(name); err != nil {
		t.Fatal(err)
	}
	if err := tester.SetComment(comment); err != nil {
		t.Fatal(err)
	}
	return tester
}

func verifyTester(t *testing.T, msg *capnp.Message, wantName string, wantComment string) {
	t.Helper()
	gotTester, err := ReadRootTester(msg)
	if err != nil {
		t.Errorf("got %v, want nil", err)
	}
	if gotName, err := gotTester.Name(); gotName != wantName || err != nil {
		t.Errorf("got %v, %v, want %v, nil", gotName, err, wantName)
	}
	if gotComment, err := gotTester.Comment(); gotComment != wantComment || err != nil {
		t.Errorf("got %v, %v, want %v, nil", gotComment, err, wantComment)
	}
}

func withTempDirectory(t *testing.T, f func(t *testing.T, dir string)) {
	t.Helper()
	dir, err := os.MkdirTemp("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	f(t, dir)
}

type hashlike interface {
	Get(any) (*capnp.Message, error)
	Set(any, *capnp.Message, bool) error
	Has(any) (bool, error)
	Del(any) (bool, error)
	DelMulti([]any) error
}

func testHashLike(t *testing.T, h hashlike) {
	if has, err := h.Has("a"); has || err != nil {
		t.Errorf("got %v, %v, want false, nil", has, err)
	}
	if found, err := h.Del("a"); found || err != nil {
		t.Errorf("got %v, %v, want false, nil", found, err)
	}
	if got, err := h.Get("a"); got != nil || err != ErrNotFound {
		t.Errorf("got %v, %v, want nil, ErrNotFound", got, err)
	}
	tester := newTester(t, "b", "c")
	if err := h.Set("a", tester.Message(), true); err != nil {
		t.Errorf("got %v, wanted nil", err)
	}
	if has, err := h.Has("a"); !has || err != nil {
		t.Errorf("got %v, %v, want true, nil", has, err)
	}
	if got, err := h.Get("a"); got == nil || err != nil {
		t.Errorf("got %v, %v, want something, nil", got, err)
	} else {
		verifyTester(t, got, "b", "c")
	}
	tester2 := newTester(t, "d", "e")
	if err := h.Set("a", tester2.Message(), false); err != ErrDuplication {
		t.Errorf("got %v, want ErrDuplication", err)
	}
	if err := h.Set("a", tester2.Message(), true); err != nil {
		t.Errorf("got %v, want nil", err)
	}
	if got, err := h.Get("a"); got == nil || err != nil {
		t.Errorf("got %v, %v, want something, nil", got, err)
	} else {
		verifyTester(t, got, "d", "e")
	}
	if found, err := h.Del("a"); !found || err != nil {
		t.Errorf("got %v, %v, want true, nil", found, err)
	}
	if has, err := h.Has("a"); has || err != nil {
		t.Errorf("got %v, %, want false, nil", has, err)
	}
	h.Set("b", newTester(t, "f", "g").Message(), false)
	h.Set("c", newTester(t, "h", "i").Message(), false)
	if err := h.DelMulti([]any{"b", "c", "d"}); err != nil {
		t.Errorf("got %v, want nil", err)
	}
	if has, err := h.Has("b"); has || err != nil {
		t.Errorf("got %v, %v, want false, nil", has, err)
	}
	if has, err := h.Has("c"); has || err != nil {
		t.Errorf("got %v, %v, want false, nil", has, err)
	}
}

func TestHash(t *testing.T) {
	withTempDirectory(t, func(t *testing.T, dir string) {
		o := &Opener{Dir: dir}
		h := o.Hash("test")
		if h == nil {
			t.Fatalf("got nil, wanted something")
		}
		if o.Err != nil {
			t.Fatalf("got %v, wanted nil", o.Err)
		}
		testHashLike(t, h)
	})
}

func findAll(t *testing.T, tr *Tree) map[string]bool {
	t.Helper()
	foundKeys := map[string]bool{}
	if err := tr.Each("", func(key []byte, msg *capnp.Message) (bool, error) {
		foundKeys[string(key)] = true
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	return foundKeys
}

func TestTree(t *testing.T) {
	withTempDirectory(t, func(t *testing.T, dir string) {
		o := &Opener{Dir: dir}
		tr := o.Tree("test")
		if tr == nil {
			t.Fatalf("got nil, wanted something")
		}
		if o.Err != nil {
			t.Fatalf("got %v, wanted nil", o.Err)
		}
		testHashLike(t, tr)
		t1 := newTester(t, "n1", "c1")
		tr.Set("1", t1.Message(), false)
		t2 := newTester(t, "n2", "c2")
		tr.Set("2", t2.Message(), false)
		t3 := newTester(t, "n3", "c3")
		tr.Set("3", t3.Message(), false)
		wantKeys := map[string]bool{"1": true, "2": true, "3": true}
		if gotKeys := findAll(t, tr); !reflect.DeepEqual(gotKeys, wantKeys) {
			t.Errorf("got %+v, want %+v", gotKeys, wantKeys)
		}
	})
}
