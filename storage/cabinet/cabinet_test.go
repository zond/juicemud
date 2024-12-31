package cabinet

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"capnproto.org/go/capnp/v3"
	"github.com/estraier/tkrzw-go"
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
	Len() (int, error)
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
	if len, err := h.Len(); len != 0 || err != nil {
		t.Errorf("got %v, %v, want 0, nil", len, err)
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
	if len, err := h.Len(); len != 1 || err != nil {
		t.Errorf("got %v, %v, want 1, nil", len, err)
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
	if len, err := h.Len(); len != 0 || err != nil {
		t.Errorf("got %v, %v, want 0, nil", len, err)
	}
	if has, err := h.Has("a"); has || err != nil {
		t.Errorf("got %v, %, want false, nil", has, err)
	}
	h.Set("b", newTester(t, "f", "g").Message(), false)
	h.Set("c", newTester(t, "h", "i").Message(), false)
	if len, err := h.Len(); len != 2 || err != nil {
		t.Errorf("got %v, %v, want 2, nil", len, err)
	}
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
		h, err := NewHash(dir, "test")
		if err != nil {
			t.Fatal(err)
		}
		testHashLike(t, h)
	})
}

func findAll(t *testing.T, tr *Tree, firstChild string, exitBeforeChild string) (names map[string]string, comments map[string]string) {
	t.Helper()
	foundNames := map[string]string{}
	foundComments := map[string]string{}
	if err := tr.Each(firstChild, func(key []byte, msg *capnp.Message) (bool, error) {
		if bytes.Compare(tkrzw.ToByteArray(exitBeforeChild), tkrzw.ToByteArray(key)) < 1 {
			return false, nil
		}
		tester, err := ReadRootTester(msg)
		if err != nil {
			t.Fatal(err)
		}
		name, err := tester.Name()
		if err != nil {
			t.Fatal(err)
		}
		comment, err := tester.Comment()
		if err != nil {
			t.Fatal(err)
		}
		foundNames[string(key)] = name
		foundComments[string(key)] = comment
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	return foundNames, foundComments
}

func verifyTreeContent(t *testing.T, tr *Tree, firstChild string, exitBeforeChild string, keys []string, names []string, comments []string) {
	t.Helper()
	wantNames := map[string]string{}
	wantComments := map[string]string{}
	for index, key := range keys {
		wantNames[key] = names[index]
		wantComments[key] = comments[index]
	}
	if gotNames, gotComments := findAll(t, tr, firstChild, exitBeforeChild); !reflect.DeepEqual(gotNames, wantNames) || !reflect.DeepEqual(gotComments, wantComments) {
		t.Errorf("got %+v, %+v, want %+v, %+v", gotNames, gotComments, wantNames, wantComments)
	}
}

func testTree(t *testing.T, tr *Tree, prefix string) {
	tr.Set(prefix+"1", newTester(t, prefix+"n1", prefix+"c1").Message(), false)
	tr.Set(prefix+"2", newTester(t, prefix+"n2", prefix+"c2").Message(), false)
	tr.Set(prefix+"3", newTester(t, prefix+"n3", prefix+"c3").Message(), false)
	verifyTreeContent(t, tr, prefix+"0", prefix+"1", nil, nil, nil)
	verifyTreeContent(t, tr, prefix+"0", prefix+"2", []string{prefix + "1"}, []string{prefix + "n1"}, []string{prefix + "c1"})
	verifyTreeContent(t, tr, prefix+"0", prefix+"3", []string{prefix + "1", prefix + "2"}, []string{prefix + "n1", prefix + "n2"}, []string{prefix + "c1", prefix + "c2"})
	verifyTreeContent(t, tr, prefix+"0", prefix+"z", []string{prefix + "1", prefix + "2", prefix + "3"}, []string{prefix + "n1", prefix + "n2", prefix + "n3"}, []string{prefix + "c1", prefix + "c2", prefix + "c3"})

	verifyTreeContent(t, tr, prefix+"0", prefix+"z", []string{prefix + "1", prefix + "2", prefix + "3"}, []string{prefix + "n1", prefix + "n2", prefix + "n3"}, []string{prefix + "c1", prefix + "c2", prefix + "c3"})
	verifyTreeContent(t, tr, prefix+"1", prefix+"z", []string{prefix + "1", prefix + "2", prefix + "3"}, []string{prefix + "n1", prefix + "n2", prefix + "n3"}, []string{prefix + "c1", prefix + "c2", prefix + "c3"})
	verifyTreeContent(t, tr, prefix+"2", prefix+"z", []string{prefix + "2", prefix + "3"}, []string{prefix + "n2", prefix + "n3"}, []string{prefix + "c2", prefix + "c3"})
	verifyTreeContent(t, tr, prefix+"3", prefix+"z", []string{prefix + "3"}, []string{prefix + "n3"}, []string{prefix + "c3"})
	verifyTreeContent(t, tr, prefix+"z", prefix+"z", nil, nil, nil)
}

func TestTree(t *testing.T) {
	withTempDirectory(t, func(t *testing.T, dir string) {
		tr1, err := NewTree(dir, "test")
		if err != nil {
			t.Fatal(err)
		}
		testHashLike(t, tr1)
		testTree(t, tr1, "")
		tr2 := tr1.Subtree("s")
		testHashLike(t, tr2)
		testTree(t, tr2, "")
		testTree(t, tr2, "tr2")
		tr3 := tr1.Subtree("t")
		testHashLike(t, tr3)
		testTree(t, tr3, "")
		testTree(t, tr3, "tr3")
		tr4 := tr3.Subtree("t")
		testHashLike(t, tr4)
		testTree(t, tr4, "")
		testTree(t, tr4, "tr4")
	})
}
