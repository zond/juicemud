package storage

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"log"
	"math/rand"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bxcodec/faker/v4"
	"github.com/bxcodec/faker/v4/pkg/options"
	"github.com/estraier/tkrzw-go"
	"github.com/pkg/errors"
	"github.com/sugawarayuuta/sonnet"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/structs"
	"rogchap.com/v8go"

	crand "crypto/rand"

	goccy "github.com/goccy/go-json"
)

var (
	fakeObjectJSON []byte
)

func withHash(t *testing.T, f func(db *tkrzw.DBM)) {
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
	o := &opener{Dir: tmpFile.Name()}
	dbm := o.OpenHash("test")
	if o.Err != nil {
		t.Fatal(err)
	}
	f(dbm)
}

func withTree(t *testing.T, f func(db *tkrzw.DBM)) {
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
	o := &opener{Dir: tmpFile.Name()}
	dbm := o.OpenTree("test")
	if o.Err != nil {
		t.Fatal(err)
	}
	f(dbm)
}

func init() {
	if err := faker.AddProvider("ByteStringMap", func(v reflect.Value) (any, error) {
		log.Printf("calling ByteStringMap provider")
		t := v.Type()
		mapType := reflect.MapOf(reflect.TypeOf(structs.ByteString("")), t.Elem())
		result := reflect.MakeMap(mapType)
		size := rand.Intn(10)
		for i := 0; i < size; i++ {
			key := make([]byte, 64)
			if _, err := crand.Read(key); err != nil {
				return nil, juicemud.WithStack(err)
			}
			val := reflect.New(t.Elem())
			if err := faker.FakeData(val.Interface()); err != nil {
				return nil, juicemud.WithStack(err)
			}
			result.SetMapIndex(reflect.ValueOf(structs.ByteString(key)), val.Elem())
		}
		return result.Interface(), nil
	}); err != nil {
		log.Panic(err)
	}
	fakeObject := &structs.Object{}
	err := faker.FakeData(fakeObject, options.WithRandomMapAndSliceMaxSize(10))
	if err != nil {
		log.Panic(err)
	}
	if fakeObjectJSON, err = json.Marshal(fakeObject); err != nil {
		log.Panic(err)
	}
}

func WithHash(t *testing.T, f func(db *tkrzw.DBM)) {
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
	o := &opener{Dir: tmpFile.Name()}
	dbm := o.OpenHash("test")
	if o.Err != nil {
		t.Fatal(err)
	}
	f(dbm)
}

func WithTree(t *testing.T, f func(db *tkrzw.DBM)) {
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
	o := &opener{Dir: tmpFile.Name()}
	dbm := o.OpenTree("test")
	if o.Err != nil {
		t.Fatal(err)
	}
	f(dbm)
}

func TestFirst(t *testing.T) {
	WithTree(t, func(db *tkrzw.DBM) {
		for _, vInt := range rand.Perm(100) {
			v := uint32(vInt)
			key := make([]byte, binary.Size(v))
			binary.BigEndian.PutUint32(key, v)
			if stat := db.Set(key, key, true); !stat.IsOK() {
				t.Fatal(stat)
			}
		}
		for want := 0; want < 100; want++ {
			v := uint32(want)
			wantKey := make([]byte, binary.Size(v))
			binary.BigEndian.PutUint32(wantKey, v)
			func() {
				iter := db.MakeIterator()
				defer iter.Destruct()
				if stat := iter.First(); !stat.IsOK() {
					t.Fatal(stat)
				}
				gotKey, stat := iter.GetKey()
				if !stat.IsOK() {
					t.Fatal(stat)
				}
				if !bytes.Equal(gotKey, wantKey) {
					t.Errorf("got %+v, want %+v", gotKey, wantKey)
				}
			}()
			if stat := db.Remove(wantKey); !stat.IsOK() {
				t.Fatal(stat)
			}
		}
	})
}

func TestProcessMulti(t *testing.T) {
	WithHash(t, func(db *tkrzw.DBM) {
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

func BenchmarkV8JSON(b *testing.B) {
	b.StopTimer()
	iso := v8go.NewIsolate()
	ctx := v8go.NewContext(iso)
	b.StartTimer()
	js := string(fakeObjectJSON)
	for i := 0; i < b.N; i++ {
		o, err := v8go.JSONParse(ctx, js)
		if err != nil {
			b.Fatal(err)
		}
		ser, err := v8go.JSONStringify(ctx, o)
		if err != nil {
			b.Fatal(err)
		}
		js = ser
	}
}

func BenchmarkEncodingJSON(b *testing.B) {
	b.StopTimer()
	o := &structs.Object{}
	js := fakeObjectJSON
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		if err := json.Unmarshal(js, o); err != nil {
			b.Fatal(err)
		}
		by, err := json.Marshal(o)
		if err != nil {
			b.Fatal(err)
		}
		js = by
	}
}

func BenchmarkSonnet(b *testing.B) {
	b.StopTimer()
	o := &structs.Object{}
	js := fakeObjectJSON
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		if err := sonnet.Unmarshal(js, o); err != nil {
			b.Fatal(err)
		}
		by, err := sonnet.Marshal(o)
		if err != nil {
			b.Fatal(err)
		}
		js = by
	}
}

func BenchmarkGoccy(b *testing.B) {
	b.StopTimer()
	o := &structs.Object{}
	js := fakeObjectJSON
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		if err := goccy.Unmarshal(js, o); err != nil {
			b.Fatal(err)
		}
		by, err := goccy.Marshal(o)
		if err != nil {
			b.Fatal(err)
		}
		js = by
	}
}

func TestQueue(t *testing.T) {
	ctx := context.Background()
	withTree(t, func(db *tkrzw.DBM) {
		got := []string{}
		mut := &sync.Mutex{}
		taskWG := &sync.WaitGroup{}
		q := NewQueue(ctx, db, func(b []byte) {
			mut.Lock()
			defer mut.Unlock()
			got = append(got, string(b))
		})
		runWG := &sync.WaitGroup{}
		runWG.Add(1)
		go func() {
			if err := q.Start(ctx); err != nil {
				log.Fatal(err)
			}
			runWG.Done()
		}()
		if err := q.Push(ctx, q.After(100*time.Millisecond), []byte("a")); err != nil {
			t.Fatal(err)
		}
		if err := q.Push(ctx, q.After(10*time.Millisecond), []byte("b")); err != nil {
			t.Fatal(err)
		}
		if err := q.Push(ctx, q.After(200*time.Millisecond), []byte("c")); err != nil {
			t.Fatal(err)
		}
		q.Close()
		runWG.Wait()
		taskWG.Wait()
		want := []string{"b", "a", "c"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}
