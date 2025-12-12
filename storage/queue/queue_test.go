package queue

import (
	"context"
	"log"
	"reflect"
	"sync"
	"sync/atomic"
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
		// Wait for all events to fire before closing.
		time.Sleep(250 * time.Millisecond)
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

func TestQueueFutureEventsNotDrained(t *testing.T) {
	ctx := context.Background()
	dbm.WithTypeTree(t, func(tr *dbm.TypeTree[structs.Event, *structs.Event]) {
		var handlerCalls atomic.Int32
		q := New(ctx, tr)

		// Start the queue BEFORE pushing events to avoid offset calculation issues.
		// The offset is set on Start() based on existing events, so starting first
		// ensures offset stays 0 and timestamps work as expected.
		runWG := &sync.WaitGroup{}
		runWG.Add(1)
		started := make(chan struct{})
		go func() {
			close(started)
			if err := q.Start(ctx, func(_ context.Context, ev *structs.Event) {
				handlerCalls.Add(1)
			}); err != nil {
				log.Fatal(err)
			}
			runWG.Done()
		}()
		<-started
		time.Sleep(10 * time.Millisecond) // Ensure Start() is in its loop

		// Push one immediate event and one far-future event.
		if err := q.Push(ctx, &structs.Event{
			At:     uint64(q.After(10 * time.Millisecond)),
			Object: "immediate",
		}); err != nil {
			t.Fatal(err)
		}
		if err := q.Push(ctx, &structs.Event{
			At:     uint64(q.After(time.Hour)),
			Object: "future",
		}); err != nil {
			t.Fatal(err)
		}

		// Wait for immediate event to fire.
		time.Sleep(50 * time.Millisecond)

		// Close should NOT wait for or process the future event.
		if err := q.Close(); err != nil {
			t.Fatal(err)
		}
		runWG.Wait()

		// Only the immediate event should have been processed.
		if got := handlerCalls.Load(); got != 1 {
			t.Errorf("handler called %d times, want 1 (future event should not be drained)", got)
		}

		// Verify future event is still in the tree.
		ev, err := tr.First()
		if err != nil {
			t.Fatal(err)
		}
		if ev == nil {
			t.Error("future event should still be in tree")
		} else if ev.Object != "future" {
			t.Errorf("remaining event is %q, want %q", ev.Object, "future")
		}
	})
}

func TestQueueCloseWithoutStart(t *testing.T) {
	ctx := context.Background()
	dbm.WithTypeTree(t, func(tr *dbm.TypeTree[structs.Event, *structs.Event]) {
		q := New(ctx, tr)

		// Push an event but never call Start().
		if err := q.Push(ctx, &structs.Event{
			At:     uint64(q.After(time.Hour)),
			Object: "orphan",
		}); err != nil {
			t.Fatal(err)
		}

		// Close should not block even though Start() was never called.
		done := make(chan struct{})
		go func() {
			if err := q.Close(); err != nil {
				t.Error(err)
			}
			close(done)
		}()

		select {
		case <-done:
			// Success - Close returned.
		case <-time.After(100 * time.Millisecond):
			t.Error("Close() blocked without Start() being called")
		}
	})
}
