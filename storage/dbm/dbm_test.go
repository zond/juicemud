//go:generate bencgen --in schema.benc --out ./ --file schema --lang go
package dbm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestGetStruct(t *testing.T) {
	WithStructHash[TestObj](t, func(sh StructHash[TestObj, *TestObj]) {
		want := &TestObj{I: 1, S: "s"}
		if err := sh.Set("a", want, true); err != nil {
			t.Fatal(err)
		}
		got, err := sh.Get("a")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

func TestGetStructMulti(t *testing.T) {
	WithStructHash[TestObj](t, func(sh StructHash[TestObj, *TestObj]) {
		want := map[string]*TestObj{"s": &TestObj{I: 1, S: "s"}, "s2": &TestObj{I: 2, S: "s2"}}
		for _, obj := range want {
			if err := sh.Set(obj.S, obj, true); err != nil {
				t.Fatal(err)
			}
		}
		got, err := sh.GetMulti(map[string]bool{"s": true, "s2": true})
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(got, want); diff != "" {
			t.Errorf("got %+v, want %+v: %v", got, want, diff)
		}
	})
}

func TestProc(t *testing.T) {
	WithStructHash(t, func(sh StructHash[TestObj, *TestObj]) {
		want := map[string]*TestObj{"s": &TestObj{I: 1, S: "s"}, "s2": &TestObj{I: 2, S: "s2"}}
		for _, obj := range want {
			if err := sh.Set(obj.S, obj, true); err != nil {
				t.Fatal(err)
			}
		}
		wantErr := fmt.Errorf("wantErr")
		if err := sh.Proc([]Proc{
			sh.SProc("s", func(s string, to *TestObj) (*TestObj, error) {
				to.I = 14
				return to, nil
			}),
			sh.SProc("s2", func(s string, to *TestObj) (*TestObj, error) {
				return nil, wantErr
			}),
		}, true); !errors.Is(err, wantErr) {
			t.Errorf("got %v, want %v", err, wantErr)
		}
		got, err := sh.GetMulti(map[string]bool{"s": true, "s2": true})
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(got, want); diff != "" {
			t.Errorf("got %+v, want %+v: %v", got, want, diff)
		}
		if err := sh.Proc([]Proc{
			sh.SProc("s", func(s string, to *TestObj) (*TestObj, error) {
				to.I = 14
				return to, nil
			}),
			sh.SProc("s2", func(s string, to *TestObj) (*TestObj, error) {
				to.I = 44
				return to, nil
			}),
		}, true); err != nil {
			t.Fatal(err)
		}
		got, err = sh.GetMulti(map[string]bool{"s": true, "s2": true})
		if err != nil {
			t.Fatal(err)
		}
		want["s"].I = 14
		want["s2"].I = 44
		if diff := cmp.Diff(got, want); diff != "" {
			t.Errorf("got %+v, want %+v: %v", got, want, diff)
		}
	})
}

func TestFirst(t *testing.T) {
	WithStructTree(t, func(st StructTree[TestObj, *TestObj]) {
		for _, vInt := range rand.Perm(100) {
			v := uint32(vInt)
			key := make([]byte, binary.Size(v))
			binary.BigEndian.PutUint32(key, v)
			if err := st.Set(string(key), &TestObj{I: vInt}, true); err != nil {
				t.Fatal(err)
			}
		}
		for want := 0; want < 100; want++ {
			v := uint32(want)
			wantKey := make([]byte, binary.Size(v))
			binary.BigEndian.PutUint32(wantKey, v)
			obj, err := st.First()
			if err != nil {
				t.Fatal(err)
			}
			if obj.I != want {
				t.Errorf("got %v, want %v", obj.I, want)
			}
			if err := st.Del(string(wantKey)); err != nil {
				t.Fatal(err)
			}
		}
	})
}
