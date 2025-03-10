//go:generate bencgen --in schema.benc --out ./ --file schema --lang go
//go:generate go run ../../decorator/decorator.go -in schema.go -out decorated.go -pkg dbm
package dbm

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/bxcodec/faker/v4"
	"github.com/bxcodec/faker/v4/pkg/options"
	"github.com/google/go-cmp/cmp"
	"github.com/zond/juicemud/structs"
)

var (
	fakeObject structs.Object
)

func init() {
	err := faker.FakeData(&fakeObject.Unsafe, options.WithRandomMapAndSliceMaxSize(10))
	if err != nil {
		log.Panic(err)
	}
}

func BenchmarkHash(b *testing.B) {
	b.StopTimer()
	by := make([]byte, fakeObject.Size())
	fakeObject.Marshal(by)
	WithHash(b, func(h *Hash) {
		b.StartTimer()
		for i := 0; i < b.N; i++ {
			if err := h.Set(fakeObject.GetId(), by, true); err != nil {
				b.Fatal(err)
			}
			_, err := h.Get(fakeObject.GetId())
			if err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
	})
}

func BenchmarkStructHash(b *testing.B) {
	b.StopTimer()
	WithTypeHash(b, func(h *TypeHash[structs.Object, *structs.Object]) {
		b.StartTimer()
		for i := 0; i < b.N; i++ {
			if err := h.Set(fakeObject.GetId(), &fakeObject, true); err != nil {
				b.Fatal(err)
			}
			_, err := h.Get(fakeObject.GetId())
			if err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
	})
}

func BenchmarkStructHashSmall(b *testing.B) {
	b.StopTimer()
	sk := structs.Skill{
		Name:        "str",
		Practical:   1.0,
		Theoretical: 1.0,
		LastBase:    1.0,
		LastUsedAt:  100.0,
	}
	WithTypeHash(b, func(h *TypeHash[structs.Skill, *structs.Skill]) {
		b.StartTimer()
		for i := 0; i < b.N; i++ {
			if err := h.Set(sk.Name, &sk, true); err != nil {
				b.Fatal(err)
			}
			_, err := h.Get(sk.Name)
			if err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
	})
}

var (
	benchTree *TypeTree[structs.Event, *structs.Event]
)

func TestMain(m *testing.M) {
	flag.Parse()
	bench := flag.Lookup("test.bench")
	if bench.Value.String() != "" {
		func() {
			tmpDir, err := os.MkdirTemp("", "")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpDir)
			if benchTree, err = OpenTypeTree[structs.Event](filepath.Join(tmpDir, "TestMain")); err != nil {
				log.Fatal(err)
			}
			ev := &structs.Event{
				Object: fmt.Sprint(rand.Int64()),
				Call: structs.Call{
					Name:    fmt.Sprint(rand.Int64()),
					Tag:     fmt.Sprint(rand.Int64()),
					Message: fmt.Sprint(rand.Int64()),
				},
			}
			for i := 0; i < 1000000; i++ {
				ev.At = uint64(rand.Int64())
				ev.CreateKey()
				if err := benchTree.Set(ev.Key, ev, false); err != nil {
					log.Fatal(err)
				}
			}
			m.Run()
		}()
	} else {
		m.Run()
	}
}

func BenchmarkStructTree(b *testing.B) {
	b.StopTimer()
	ev := &structs.Event{
		Object: fmt.Sprint(rand.Int64()),
		Call: structs.Call{
			Name:    fmt.Sprint(rand.Int64()),
			Tag:     fmt.Sprint(rand.Int64()),
			Message: fmt.Sprint(rand.Int64()),
		},
	}
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		ev.At = uint64(rand.Int64())
		ev.CreateKey()
		if err := benchTree.Set(ev.Key, ev, false); err != nil {
			b.Fatal(err)
		}
		ev, err := benchTree.First()
		if err != nil {
			b.Fatal(err)
		}
		if err := benchTree.Del(ev.Key); err != nil {
			b.Fatal(err)
		}
	}
}

func TestGetStruct(t *testing.T) {
	WithTypeHash(t, func(sh *TypeHash[Obj, *Obj]) {
		want := &Obj{I: 1, S: "s"}
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

func TestLiveTypeHash(t *testing.T) {
	WithLiveTypeHash(t, func(lh *LiveTypeHash[Live, *Live]) {
		runWG := &sync.WaitGroup{}
		runWG.Add(1)
		go func() {
			if err := lh.Start(); err != nil {
				log.Fatal(err)
			}
			runWG.Done()
		}()
		to := &Live{Unsafe: &LiveDO{}}
		to.Unsafe.Id = "id"
		if err := lh.Set(to); err != nil {
			t.Fatal(err)
		}
		cpy1, err := lh.Get(to.GetId())
		if err != nil {
			t.Fatal(err)
		}
		cpy1.SetS("aaa")
		cpy2, err := lh.Get(to.GetId())
		if err != nil {
			t.Fatal(err)
		}
		if err := lh.Flush(); err != nil {
			t.Fatal(err)
		}
		if cpy2.GetS() != "aaa" {
			t.Errorf("got %q, want 'aaa'", cpy2.GetS())
		}
		lh.Close()
		runWG.Wait()
	})
}

func TestGetStructMulti(t *testing.T) {
	WithTypeHash(t, func(sh *TypeHash[Obj, *Obj]) {
		want := map[string]*Obj{"s": {I: 1, S: "s"}, "s2": {I: 2, S: "s2"}}
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
	WithTypeHash(t, func(sh *TypeHash[Obj, *Obj]) {
		want := map[string]*Obj{"s": {I: 1, S: "s"}, "s2": {I: 2, S: "s2"}}
		for _, obj := range want {
			if err := sh.Set(obj.S, obj, true); err != nil {
				t.Fatal(err)
			}
		}
		wantErr := fmt.Errorf("wantErr")
		if err := sh.Proc([]Proc{
			sh.SProc("s", func(s string, to *Obj) (*Obj, error) {
				to.I = 14
				return to, nil
			}),
			sh.SProc("s2", func(s string, to *Obj) (*Obj, error) {
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
			sh.SProc("s", func(s string, to *Obj) (*Obj, error) {
				to.I = 14
				return to, nil
			}),
			sh.SProc("s2", func(s string, to *Obj) (*Obj, error) {
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

func TestStructTree(t *testing.T) {
	WithTypeTree(t, func(st *TypeTree[Obj, *Obj]) {
		if err := st.Set(string([]byte{24, 34, 149, 40, 93, 3, 23, 184, 24, 34, 149, 40, 87, 33, 87, 16}), &Obj{I: 10}, false); err != nil {
			t.Fatal(fmt.Errorf("Set 1: %w", err))
		}
		if err := st.Set(string([]byte{24, 34, 149, 40, 93, 3, 23, 184, 24, 34, 149, 40, 87, 34, 77, 40}), &Obj{I: 10}, false); err != nil {
			t.Fatal(fmt.Errorf("Set 2: %w", err))
		}
	})
}

func TestFirst(t *testing.T) {
	WithTypeTree(t, func(st *TypeTree[Obj, *Obj]) {
		for _, vInt := range rand.Perm(100) {
			v := uint32(vInt)
			key := make([]byte, binary.Size(v))
			binary.BigEndian.PutUint32(key, v)
			if err := st.Set(string(key), &Obj{I: vInt}, true); err != nil {
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

func randBytes(rng *rand.Rand) string {
	res := []byte{}
	for range 8 {
		ui32 := uint32(rng.Int64())
		b := make([]byte, binary.Size(ui32))
		binary.BigEndian.PutUint32(b, ui32)
		res = append(res, b...)
	}
	return string(res)
}

func TestSubSets(t *testing.T) {
	WithTree(t, func(tr *Tree) {
		rng := rand.New(&rand.PCG{})
		sets := map[string]map[string]bool{}
		sizes := 200
		for range sizes {
			setID := randBytes(rng)
			sets[setID] = map[string]bool{}
			for range sizes {
				valID := randBytes(rng)
				sets[setID][valID] = true
				if err := tr.SubSet(setID, valID, nil); err != nil {
					t.Fatal(err)
				}
			}
			if c, err := tr.SubCount(setID); err != nil || c != sizes {
				t.Errorf("got %v, %v, want %v, nil", c, err, sizes)
			}
		}
		for setID, wantValIDs := range sets {
			for valID := range wantValIDs {
				if got, err := tr.SubGet(setID, valID); err != nil || len(got) != 0 {
					t.Errorf("want nil, nil, got %v, %v", got, err)
				}
			}
			toDelete := map[string]bool{}
			for entry, err := range tr.SubEach(setID) {
				if err != nil || len(entry.V) != 0 {
					t.Errorf("want nil, nil, got %v, %v", entry.V, err)
				}
				delete(wantValIDs, entry.K)
				toDelete[entry.K] = true
			}
			if len(wantValIDs) > 0 {
				t.Errorf("didn't find %+v in %q", wantValIDs, setID)
			}
			for valID := range toDelete {
				if err := tr.SubDel(setID, valID); err != nil {
					t.Fatal(err)
				}
				if _, err := tr.SubGet(setID, valID); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("got %v, want %v", err, os.ErrNotExist)
				}
				if err := tr.SubDel(setID, valID); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("got %v, want %v", err, os.ErrNotExist)
				}
			}
			found := 0
			for range tr.SubEach(setID) {
				found++
			}
			if found > 0 {
				t.Errorf("found %v post delete", found)
			}
		}
	})
}
