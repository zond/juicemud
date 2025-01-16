package dbm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
)

type testObj struct {
	I int
	S string
}

func TestGetJSON(t *testing.T) {
	WithHash(t, func(h Hash) {
		want := &testObj{I: 1, S: "s"}
		if err := h.SetJSON("a", want, true); err != nil {
			t.Fatal(err)
		}
		got := &testObj{}
		if err := h.GetJSON("a", got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

func TestGetJSONMulti(t *testing.T) {
	WithHash(t, func(h Hash) {
		want := []testObj{{I: 1, S: "s"}, {I: 2, S: "s2"}}
		for _, obj := range want {
			if err := h.SetJSON(obj.S, obj, true); err != nil {
				t.Fatal(err)
			}
		}
		got := []testObj{}
		if err := h.GetJSONMulti([]string{"s", "s2"}, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

func TestProc(t *testing.T) {
	WithHash(t, func(h Hash) {
		want := []testObj{{I: 1, S: "s"}, {I: 2, S: "s2"}}
		for _, obj := range want {
			if err := h.SetJSON(obj.S, obj, true); err != nil {
				t.Fatal(err)
			}
		}
		wantErr := fmt.Errorf("wantErr")
		if err := h.Proc([]Proc{
			JProc[testObj]{
				K: "s",
				F: func(s string, to *testObj) (*testObj, error) {
					to.I = 14
					return to, nil
				},
			},
			JProc[testObj]{
				K: "s2",
				F: func(s string, to *testObj) (*testObj, error) {
					return nil, wantErr
				},
			},
		}, true); !errors.Is(err, wantErr) {
			t.Errorf("got %v, want %v", err, wantErr)
		}
		got := []testObj{}
		if err := h.GetJSONMulti([]string{"s", "s2"}, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
		if err := h.Proc([]Proc{
			JProc[testObj]{
				K: "s",
				F: func(s string, to *testObj) (*testObj, error) {
					to.I = 14
					return to, nil
				},
			},
			JProc[testObj]{
				K: "s2",
				F: func(s string, to *testObj) (*testObj, error) {
					to.I = 44
					return to, nil
				},
			},
		}, true); err != nil {
			t.Fatal(err)
		}
		if err := h.GetJSONMulti([]string{"s", "s2"}, &got); err != nil {
			t.Fatal(err)
		}
		want[0].I = 14
		want[1].I = 44
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

func TestFirst(t *testing.T) {
	WithTree(t, func(tr Tree) {
		for _, vInt := range rand.Perm(100) {
			v := uint32(vInt)
			key := make([]byte, binary.Size(v))
			binary.BigEndian.PutUint32(key, v)
			if err := tr.SetJSON(string(key), testObj{I: vInt}, true); err != nil {
				t.Fatal(err)
			}
		}
		for want := 0; want < 100; want++ {
			v := uint32(want)
			wantKey := make([]byte, binary.Size(v))
			binary.BigEndian.PutUint32(wantKey, v)
			obj := &testObj{}
			k, err := tr.FirstJSON(obj)
			if err != nil {
				t.Fatal(err)
			}
			if k != string(wantKey) {
				t.Errorf("got %q, want %q", k, wantKey)
			}
			if obj.I != want {
				t.Errorf("got %v, want %v", obj.I, want)
			}
			if err := tr.Del(string(wantKey)); err != nil {
				t.Fatal(err)
			}
		}
	})
}
