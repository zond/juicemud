package dbm

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func withHash(t *testing.T, f func(h Hash)) {
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

type testObj struct {
	I int
	S string
}

func TestGetJSON(t *testing.T) {
	withHash(t, func(h Hash) {
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
	withHash(t, func(h Hash) {
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
	withHash(t, func(h Hash) {
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
