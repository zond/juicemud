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
		_, ev, err := benchTree.First()
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
	})
}

func TestLiveTypeHashDel(t *testing.T) {
	WithLiveTypeHash(t, func(lh *LiveTypeHash[Live, *Live]) {
		// Create and flush an object
		to := &Live{Unsafe: &LiveDO{}}
		to.Unsafe.Id = "deltest"
		to.Unsafe.S = "original"
		if err := lh.Set(to); err != nil {
			t.Fatal(err)
		}
		if err := lh.Flush(); err != nil {
			t.Fatal(err)
		}

		// Verify it exists
		if !lh.Has("deltest") {
			t.Error("expected Has to return true before Del")
		}

		// Delete it
		if err := lh.Del("deltest"); err != nil {
			t.Fatal(err)
		}

		// Has should return false immediately (before Flush)
		if lh.Has("deltest") {
			t.Error("expected Has to return false after Del")
		}

		// Get should return error immediately (before Flush)
		if _, err := lh.Get("deltest"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("expected os.ErrNotExist after Del, got %v", err)
		}

		// Flush the delete to disk
		if err := lh.Flush(); err != nil {
			t.Fatal(err)
		}

		// Should still not exist
		if lh.Has("deltest") {
			t.Error("expected Has to return false after Flush")
		}
	})
}

func TestLiveTypeHashDelNotFound(t *testing.T) {
	WithLiveTypeHash(t, func(lh *LiveTypeHash[Live, *Live]) {
		// Del on non-existent key should return error
		if err := lh.Del("nonexistent"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("expected os.ErrNotExist for Del on nonexistent key, got %v", err)
		}
	})
}

func TestLiveTypeHashProcDelete(t *testing.T) {
	WithLiveTypeHash(t, func(lh *LiveTypeHash[Live, *Live]) {
		// Create and flush an object
		to := &Live{Unsafe: &LiveDO{}}
		to.Unsafe.Id = "procdeltest"
		if err := lh.Set(to); err != nil {
			t.Fatal(err)
		}
		if err := lh.Flush(); err != nil {
			t.Fatal(err)
		}

		// Delete via Proc (returning nil)
		if err := lh.Proc([]LProc[Live, *Live]{
			lh.LProc("procdeltest", func(k string, v *Live) (*Live, error) {
				return nil, nil // Delete
			}),
		}); err != nil {
			t.Fatal(err)
		}

		// Should not exist after Proc
		if lh.Has("procdeltest") {
			t.Error("expected Has to return false after Proc delete")
		}

		// Flush to disk
		if err := lh.Flush(); err != nil {
			t.Fatal(err)
		}

		// Should still not exist
		if lh.Has("procdeltest") {
			t.Error("expected Has to return false after Flush")
		}
	})
}

func TestLiveTypeHashDeletePreventsRewrite(t *testing.T) {
	WithLiveTypeHash(t, func(lh *LiveTypeHash[Live, *Live]) {
		// Create and flush an object
		to := &Live{Unsafe: &LiveDO{}}
		to.Unsafe.Id = "rewritetest"
		to.Unsafe.S = "original"
		if err := lh.Set(to); err != nil {
			t.Fatal(err)
		}
		if err := lh.Flush(); err != nil {
			t.Fatal(err)
		}

		// Get a reference to the object
		obj, err := lh.Get("rewritetest")
		if err != nil {
			t.Fatal(err)
		}

		// Delete it
		if err := lh.Del("rewritetest"); err != nil {
			t.Fatal(err)
		}

		// Modify the in-memory object (simulates what run() does after JS)
		obj.Lock()
		obj.Unsafe.S = "modified"
		obj.Unlock() // This triggers PostUnlock -> updated()

		// Flush should delete, not rewrite the modified object
		if err := lh.Flush(); err != nil {
			t.Fatal(err)
		}

		// Object should not exist
		if lh.Has("rewritetest") {
			t.Error("expected object to be deleted, not rewritten")
		}
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
			_, obj, err := st.First()
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

func TestEachSet(t *testing.T) {
	WithTree(t, func(tr *Tree) {
		// Create sets with various names to test iteration and jumping
		wantSets := map[string]bool{
			"alpha":   true,
			"beta":    true,
			"gamma":   true,
			"delta":   true,
			"epsilon": true,
		}

		// Add entries to each set
		for setID := range wantSets {
			for i := 0; i < 10; i++ {
				if err := tr.SubSet(setID, fmt.Sprintf("key%d", i), nil); err != nil {
					t.Fatal(err)
				}
			}
		}

		// Verify EachSet returns all unique sets
		gotSets := map[string]bool{}
		for setID, err := range tr.EachSet() {
			if err != nil {
				t.Fatal(err)
			}
			if gotSets[setID] {
				t.Errorf("got duplicate set %q", setID)
			}
			gotSets[setID] = true
		}

		if len(gotSets) != len(wantSets) {
			t.Errorf("got %d sets, want %d", len(gotSets), len(wantSets))
		}
		for setID := range wantSets {
			if !gotSets[setID] {
				t.Errorf("missing set %q", setID)
			}
		}
	})
}

func TestEachSetEmpty(t *testing.T) {
	WithTree(t, func(tr *Tree) {
		// Empty tree should yield nothing
		count := 0
		for _, err := range tr.EachSet() {
			if err != nil {
				t.Fatal(err)
			}
			count++
		}
		if count != 0 {
			t.Errorf("got %d sets from empty tree, want 0", count)
		}
	})
}

func TestEachSetWithRandomData(t *testing.T) {
	WithTree(t, func(tr *Tree) {
		rng := rand.New(&rand.PCG{})
		wantSets := map[string]bool{}
		numSets := 50
		entriesPerSet := 20

		// Create random sets
		for range numSets {
			setID := randBytes(rng)
			wantSets[setID] = true
			for range entriesPerSet {
				valID := randBytes(rng)
				if err := tr.SubSet(setID, valID, nil); err != nil {
					t.Fatal(err)
				}
			}
		}

		// Verify EachSet returns all unique sets
		gotSets := map[string]bool{}
		for setID, err := range tr.EachSet() {
			if err != nil {
				t.Fatal(err)
			}
			gotSets[setID] = true
		}

		if len(gotSets) != len(wantSets) {
			t.Errorf("got %d sets, want %d", len(gotSets), len(wantSets))
		}
		for setID := range wantSets {
			if !gotSets[setID] {
				t.Errorf("missing set %q", setID)
			}
		}
	})
}

func TestTreeFirst(t *testing.T) {
	WithTree(t, func(tr *Tree) {
		// Empty tree should return error
		_, _, err := tr.First()
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("got %v, want os.ErrNotExist", err)
		}

		// Add entries with ordered keys
		for _, v := range rand.Perm(10) {
			key := make([]byte, 4)
			binary.BigEndian.PutUint32(key, uint32(v))
			if err := tr.Set(string(key), key, true); err != nil {
				t.Fatal(err)
			}
		}

		// First should return smallest key and value
		k, v, err := tr.First()
		if err != nil {
			t.Fatal(err)
		}
		gotKey := binary.BigEndian.Uint32(k)
		if gotKey != 0 {
			t.Errorf("key: got %d, want 0", gotKey)
		}
		gotVal := binary.BigEndian.Uint32(v)
		if gotVal != 0 {
			t.Errorf("value: got %d, want 0", gotVal)
		}
	})
}

func TestSubBProc(t *testing.T) {
	WithTree(t, func(tr *Tree) {
		// Set initial values
		if err := tr.SubSet("set1", "key1", []byte("value1")); err != nil {
			t.Fatal(err)
		}
		if err := tr.SubSet("set1", "key2", []byte("value2")); err != nil {
			t.Fatal(err)
		}

		// Use Proc to atomically update
		if err := tr.Proc([]Proc{
			tr.SubBProc("set1", "key1", func(v []byte) ([]byte, error) {
				return []byte("updated1"), nil
			}),
			tr.SubBProc("set1", "key2", func(v []byte) ([]byte, error) {
				return []byte("updated2"), nil
			}),
		}, true); err != nil {
			t.Fatal(err)
		}

		// Verify updates
		v1, err := tr.SubGet("set1", "key1")
		if err != nil {
			t.Fatal(err)
		}
		if string(v1) != "updated1" {
			t.Errorf("got %q, want %q", string(v1), "updated1")
		}

		v2, err := tr.SubGet("set1", "key2")
		if err != nil {
			t.Fatal(err)
		}
		if string(v2) != "updated2" {
			t.Errorf("got %q, want %q", string(v2), "updated2")
		}
	})
}

func TestSubBProcDelete(t *testing.T) {
	WithTree(t, func(tr *Tree) {
		if err := tr.SubSet("set1", "key1", []byte("value1")); err != nil {
			t.Fatal(err)
		}

		// Return nil to delete
		if err := tr.Proc([]Proc{
			tr.SubBProc("set1", "key1", func(v []byte) ([]byte, error) {
				return nil, nil
			}),
		}, true); err != nil {
			t.Fatal(err)
		}

		// Verify deleted
		_, err := tr.SubGet("set1", "key1")
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("got %v, want os.ErrNotExist", err)
		}
	})
}

func TestSubBProcUpdateIfExists(t *testing.T) {
	WithTree(t, func(tr *Tree) {
		// Proc on non-existent key - should not create
		if err := tr.Proc([]Proc{
			tr.SubBProc("set1", "key1", func(v []byte) ([]byte, error) {
				if v == nil {
					return nil, nil // Don't create if doesn't exist
				}
				return []byte("updated"), nil
			}),
		}, true); err != nil {
			t.Fatal(err)
		}

		// Verify not created
		_, err := tr.SubGet("set1", "key1")
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("got %v, want os.ErrNotExist", err)
		}
	})
}

func TestTypeTreeSubOps(t *testing.T) {
	WithTypeTree(t, func(tt *TypeTree[Obj, *Obj]) {
		// SubSet and SubGet
		want := &Obj{I: 42, S: "test"}
		if err := tt.SubSet("set1", "key1", want); err != nil {
			t.Fatal(err)
		}

		got, err := tt.SubGet("set1", "key1")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}

		// SubEach
		if err := tt.SubSet("set1", "key2", &Obj{I: 43, S: "test2"}); err != nil {
			t.Fatal(err)
		}

		count := 0
		for obj, err := range tt.SubEach("set1") {
			if err != nil {
				t.Fatal(err)
			}
			if obj.I != 42 && obj.I != 43 {
				t.Errorf("unexpected I: %d", obj.I)
			}
			count++
		}
		if count != 2 {
			t.Errorf("got %d entries, want 2", count)
		}
	})
}

func TestTypeTreeSubSProc(t *testing.T) {
	WithTypeTree(t, func(tt *TypeTree[Obj, *Obj]) {
		if err := tt.SubSet("set1", "key1", &Obj{I: 10, S: "original"}); err != nil {
			t.Fatal(err)
		}

		// Use SubSProc to update
		if err := tt.Proc([]Proc{
			tt.SubSProc("set1", "key1", func(obj *Obj) (*Obj, error) {
				obj.I = 20
				obj.S = "updated"
				return obj, nil
			}),
		}, true); err != nil {
			t.Fatal(err)
		}

		got, err := tt.SubGet("set1", "key1")
		if err != nil {
			t.Fatal(err)
		}
		if got.I != 20 || got.S != "updated" {
			t.Errorf("got %+v, want I=20 S=updated", got)
		}
	})
}

func TestTypeTreeSubSProcConditionalUpdate(t *testing.T) {
	WithTypeTree(t, func(tt *TypeTree[Obj, *Obj]) {
		// Try to update non-existent key - should not create
		if err := tt.Proc([]Proc{
			tt.SubSProc("set1", "key1", func(obj *Obj) (*Obj, error) {
				if obj == nil {
					return nil, nil // Don't create
				}
				obj.I = 99
				return obj, nil
			}),
		}, true); err != nil {
			t.Fatal(err)
		}

		// Should not exist
		_, err := tt.SubGet("set1", "key1")
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("got %v, want os.ErrNotExist", err)
		}

		// Now create it
		if err := tt.SubSet("set1", "key1", &Obj{I: 1}); err != nil {
			t.Fatal(err)
		}

		// Update should work now
		if err := tt.Proc([]Proc{
			tt.SubSProc("set1", "key1", func(obj *Obj) (*Obj, error) {
				if obj == nil {
					return nil, nil
				}
				obj.I = 99
				return obj, nil
			}),
		}, true); err != nil {
			t.Fatal(err)
		}

		got, err := tt.SubGet("set1", "key1")
		if err != nil {
			t.Fatal(err)
		}
		if got.I != 99 {
			t.Errorf("got I=%d, want 99", got.I)
		}
	})
}

func TestTypeTreeEachAndGetMulti(t *testing.T) {
	WithTypeTree(t, func(tt *TypeTree[Obj, *Obj]) {
		want := map[string]*Obj{
			"a": {I: 1, S: "a"},
			"b": {I: 2, S: "b"},
			"c": {I: 3, S: "c"},
		}
		for k, v := range want {
			if err := tt.Set(k, v, true); err != nil {
				t.Fatal(err)
			}
		}

		// Test Each
		got := map[string]*Obj{}
		for entry, err := range tt.Each() {
			if err != nil {
				t.Fatal(err)
			}
			got[entry.K] = entry.V
		}
		if diff := cmp.Diff(got, want); diff != "" {
			t.Errorf("Each: %v", diff)
		}

		// Test GetMulti
		got, err := tt.GetMulti(map[string]bool{"a": true, "b": true, "c": true})
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(got, want); diff != "" {
			t.Errorf("GetMulti: %v", diff)
		}
	})
}
