package storage

import (
	"context"
	"log"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bxcodec/faker/v4"
	"github.com/bxcodec/faker/v4/pkg/options"
	"github.com/sugawarayuuta/sonnet"
	"github.com/zond/juicemud/storage/dbm"
	"github.com/zond/juicemud/structs"
	"rogchap.com/v8go"

	goccy "github.com/goccy/go-json"
)

var (
	fakeObjectJSON []byte
)

func init() {
	fakeObject := &structs.Object{}
	err := faker.FakeData(fakeObject, options.WithRandomMapAndSliceMaxSize(10))
	if err != nil {
		log.Panic(err)
	}
	if fakeObjectJSON, err = goccy.Marshal(fakeObject); err != nil {
		log.Panic(err)
	}
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
	dbm.WithTree(t, func(tr dbm.Tree) {
		got := []string{}
		mut := &sync.Mutex{}
		taskWG := &sync.WaitGroup{}
		q := NewQueue(ctx, tr, func(_ context.Context, ev *Event) {
			mut.Lock()
			defer mut.Unlock()
			got = append(got, ev.Object)
		})
		runWG := &sync.WaitGroup{}
		runWG.Add(1)
		go func() {
			if err := q.Start(ctx); err != nil {
				log.Fatal(err)
			}
			runWG.Done()
		}()
		if err := q.Push(ctx, &Event{
			At:     q.After(100 * time.Millisecond),
			Object: "a",
		}); err != nil {
			t.Fatal(err)
		}
		if err := q.Push(ctx, &Event{
			At:     q.After(10 * time.Millisecond),
			Object: "b",
		}); err != nil {
			t.Fatal(err)
		}
		if err := q.Push(ctx, &Event{
			At:     q.After(200 * time.Millisecond),
			Object: "c",
		}); err != nil {
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
