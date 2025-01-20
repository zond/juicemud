package queue

import (
	"context"
	"log"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/zond/juicemud/storage/dbm"
)

func TestQueue(t *testing.T) {
	ctx := context.Background()
	dbm.WithTree(t, func(tr dbm.Tree) {
		got := []string{}
		mut := &sync.Mutex{}
		taskWG := &sync.WaitGroup{}
		q := New(ctx, tr)
		runWG := &sync.WaitGroup{}
		runWG.Add(1)
		go func() {
			if err := q.Start(ctx, func(_ context.Context, ev *Event) {
				mut.Lock()
				defer mut.Unlock()
				got = append(got, ev.Object)
			}); err != nil {
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
