package queue

import (
	"context"
	"log"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/zond/juicemud/storage/dbm"
	"github.com/zond/juicemud/structs"
)

func TestQueue(t *testing.T) {
	ctx := context.Background()
	dbm.WithTypeTree(t, func(tr *dbm.TypeTree[structs.Event, *structs.Event]) {
		got := []string{}
		mut := &sync.Mutex{}
		q := New(ctx, tr)
		runWG := &sync.WaitGroup{}
		runWG.Add(1)
		go func() {
			if err := q.Start(ctx, func(_ context.Context, ev *structs.Event) {
				mut.Lock()
				defer mut.Unlock()
				got = append(got, ev.Object)
			}); err != nil {
				log.Fatal(err)
			}
			runWG.Done()
		}()
		if err := q.Push(ctx, &structs.Event{
			At:     uint64(q.After(100 * time.Millisecond)),
			Object: "a",
		}); err != nil {
			t.Fatal(err)
		}
		if err := q.Push(ctx, &structs.Event{
			At:     uint64(q.After(10 * time.Millisecond)),
			Object: "b",
		}); err != nil {
			t.Fatal(err)
		}
		if err := q.Push(ctx, &structs.Event{
			At:     uint64(q.After(200 * time.Millisecond)),
			Object: "c",
		}); err != nil {
			t.Fatal(err)
		}
		if err := q.Close(); err != nil {
			t.Fatal(err)
		}
		runWG.Wait()
		want := []string{"b", "a", "c"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}
