package storage

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"testing"

	"github.com/estraier/tkrzw-go"
	"github.com/go-test/deep"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/glue"
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

const (
	objectJSON = `{
  "Id": "deadbeef",
  "Location": "beefdead",
  "Content": ["dead", "beef"],
  "Callbacks": ["cb1", "cb2"],
  "Skills": [
    {
      "Name": "skill1",
	  "Theoretical": 2.0,
	  "Practical": 1.5
    },
    {
      "Name": "skill2",
	  "Theoretical": 3.0,
	  "Practical": 2.5
    }
  ],
  "Descriptions": [
    {
      "Short": "short1",
	  "Long": "long1",
	  "Tags": ["tag11", "tag12"],
	  "Challenges": [
	    {
	  	  "Skill": "skill1",
		  "Level": 2.0,
		  "FailMessage": "mess11"
	    },
		{
	  	  "Skill": "skill2",
		  "Level": 3.0,
		  "FailMessage": "mess12"
		}
	  ]
    },
	{
      "Short": "short2",
	  "Long": "long2",
	  "Tags": ["tag21", "tag22"],
	  "Challenges": [
	    {
	  	  "Skill": "skill1",
		  "Level": 3.0,
		  "FailMessage": "mess21"
	    },
		{
	  	  "Skill": "skill2",
		  "Level": 4.0,
		  "FailMessage": "mess22"
		}
	  ]
	}
  ],
  "State": "",
  "Exits": [],
  "Source": ""
}`
)

func TestObjectCreateAndCopy(t *testing.T) {
	iso := v8go.NewIsolate()
	ctx := v8go.NewContext(iso)
	wantV8, err := v8go.JSONParse(ctx, objectJSON)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := MakeObject(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := glue.Copy(*obj, wantV8); err != nil {
		t.Fatal(err)
	}
	gotV8, err := glue.ToV8(ctx, *obj)
	if err != nil {
		log.Print(juicemud.StackTrace(err))
		t.Fatal(err)
	}
	wantJSON, err := v8go.JSONStringify(ctx, wantV8)
	if err != nil {
		t.Fatal(err)
	}
	wantDec := map[string]any{}
	if err := json.Unmarshal([]byte(wantJSON), &wantDec); err != nil {
		t.Fatal(err)
	}
	wantIndentJSON, err := json.MarshalIndent(wantDec, "  ", "  ")
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := v8go.JSONStringify(ctx, gotV8)
	if err != nil {
		t.Fatal(err)
	}
	gotDec := map[string]any{}
	if err := json.Unmarshal([]byte(gotJSON), &gotDec); err != nil {
		t.Fatal(err)
	}
	gotIndentJSON, err := json.MarshalIndent(gotDec, "  ", "  ")
	if err != nil {
		t.Fatal(err)
	}
	diff := deep.Equal(wantDec, gotDec)
	if len(diff) > 0 {
		t.Logf("--- Wanted ---\n%s\n--- but got ---\n%s", string(wantIndentJSON), string(gotIndentJSON))
		for _, d := range diff {
			t.Error(d)
		}
	}
}
