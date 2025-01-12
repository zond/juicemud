package storage

import (
	"context"
	"log"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/estraier/tkrzw-go"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/glue"
	"github.com/zond/juicemud/js"
	"rogchap.com/v8go"
)

func withHash(t *testing.T, f func(db *tkrzw.DBM)) {
	t.Helper()
	dbm := tkrzw.NewDBM()
	tmpFile, err := os.CreateTemp("", "*.tkh")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())
	stat := dbm.Open(tmpFile.Name(), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"restore_mode":     "RESTORE_SYNC|RESTORE_NO_SHORTCUTS|RESTORE_WITH_HARDSYNC",
	})
	if !stat.IsOK() {
		t.Fatal(stat)
	}
	f(dbm)
}

func TestTkrzw(t *testing.T) {
	withHash(t, func(db *tkrzw.DBM) {
		if stat := db.Set("a", "b", true); !stat.IsOK() {
			t.Fatal(stat)
		}
		if stat := db.Set("b", "c", true); !stat.IsOK() {
			t.Fatal(stat)
		}
		if err := processMulti(db, []funcPair{
			{
				Key: "a",
				Func: func(k []byte, v []byte) (any, error) {
					if string(v) != "b" {
						return nil, errors.Errorf("not b")
					}
					return nil, nil
				},
			},
			{
				Key: "b",
				Func: func(k []byte, v []byte) (any, error) {
					if string(v) != "b" {
						return nil, errors.Errorf("not b")
					}
					return nil, nil
				},
			},
			{
				Key: "a",
				Func: func(k, v []byte) (any, error) {
					return "a2", nil
				},
			},
			{
				Key: "b",
				Func: func(k, v []byte) (any, error) {
					return "b2", nil
				},
			},
		}, true); err == nil {
			t.Errorf("got nil, wanted an error")
		}
		if val, stat := db.Get("a"); !stat.IsOK() || string(val) != "b" {
			t.Errorf("got %v, %v, wanted OK, b", val, stat)
		}
		if val, stat := db.Get("b"); !stat.IsOK() || string(val) != "c" {
			t.Errorf("got %v, %v, wanted OK, b", val, stat)
		}
	})
}

func TestObjectHelper(t *testing.T) {
	ctx := context.Background()
	o, err := MakeObject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oh := OH(o)
	if has, err := oh.Content().Has([]byte("a")); err != nil || has {
		t.Errorf("got %v, %v, wanted false, nil", has, err)
	}
	if err := oh.Content().Append([]byte("a")); err != nil {
		t.Errorf("got %v, want nil", err)
	}
	if has, err := oh.Content().Has([]byte("a")); err != nil || !has {
		t.Errorf("got %v, %v, want true, nil", has, err)
	}
	if err := oh.Content().Remove([]byte("a")); err != nil {
		t.Errorf("got %v, want nil", err)
	}
	if has, err := oh.Content().Has([]byte("a")); err != nil || has {
		t.Errorf("got %v, %v, want false, nil", has, err)
	}
	if err := oh.Content().Set([][]byte{[]byte("b")}); err != nil {
		t.Errorf("got %v, want nil", err)
	}
	if has, err := oh.Content().Has([]byte("b")); err != nil || !has {
		t.Errorf("got %v, %v, want true, nil", has, err)
	}
}

func TestObjectCopy(t *testing.T) {
	ctx := context.Background()
	obj, err := MakeObject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := js.Target{
		Source: `
set(["a", "b"]);
`,
		Origin: "test",
		Callbacks: map[string]func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value{
			"set": func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
				if err := glue.CreateAndCopy(obj.NewCallbacks, info.Args()[0]); err != nil {
					rc.Throw("trying to copy from %v: %v", info.Args()[0], err)
				}
				return nil
			},
		},
	}
	if _, err := target.Call(ctx, "", "", time.Second); err != nil {
		log.Print(juicemud.StackTrace(err))
		t.Fatal(err)
	}
	wantCB := []string{"a", "b"}
	if gotCB, err := OH(obj).Callbacks().All(); err != nil || !reflect.DeepEqual(gotCB, wantCB) {
		t.Errorf("got %+v, %v, want %+v, nil", gotCB, err, wantCB)
	}

	target = js.Target{
		Source: `
set([{"Name": "a", "Theoretical": 1.5, "Practical": 1.0}, {"Name": "b", "Theoretical": 2.3, "Practical": 2.1}]);
`,
		Origin: "test",
		Callbacks: map[string]func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value{
			"set": func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
				if err := glue.CreateAndCopy(obj.NewSkills, info.Args()[0]); err != nil {
					rc.Throw("trying to copy from %v: %v", info.Args()[0], err)
				}
				return nil
			},
		},
	}
	if _, err := target.Call(ctx, "", "", time.Second); err != nil {
		log.Print(juicemud.StackTrace(err))
		t.Fatal(err)
	}
	skills, err := obj.Skills()
	if err != nil {
		t.Fatal(err)
	}
	if skills.Len() != 2 {
		t.Errorf("wanted 2, got %v", skills.Len())
	}
	skillList, err := obj.Skills()
	if err != nil {
		t.Fatal(err)
	}
	if skillList.Len() != 2 {
		t.Errorf("got %v, want 2", skillList.Len())
	}
	skill0 := skillList.At(0)
	if name, err := skill0.Name(); err != nil || name != "a" {
		t.Errorf("got %v, %v, want 'a', nil", name, err)
	}
	if theo := skill0.Theoretical(); theo != 1.5 {
		t.Errorf("got %v, want 1.5", theo)
	}
	if prac := skill0.Practical(); prac != 1.0 {
		t.Errorf("got %v, want 1.0", prac)
	}
	skill1 := skillList.At(1)
	if name, err := skill1.Name(); err != nil || name != "b" {
		t.Errorf("got %v, %v, want 'b', nil", name, err)
	}
	if theo := skill1.Theoretical(); theo != 2.3 {
		t.Errorf("got %v, want 2.3", theo)
	}
	if prac := skill1.Practical(); prac != 2.1 {
		t.Errorf("got %v, want 2.1", prac)
	}
}
