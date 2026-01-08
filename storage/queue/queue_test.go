package queue

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zond/juicemud/storage/dbm"
	"github.com/zond/juicemud/structs"
)

func TestQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbm.WithTypeTree(t, func(tr *dbm.TypeTree[structs.Event, *structs.Event]) {
		got := []string{}
		mut := &sync.Mutex{}
		q := New(ctx, tr, "")

		// Start the queue first to ensure offset is initialized.
		runWG := &sync.WaitGroup{}
		runWG.Add(1)
		started := make(chan struct{})
		go func() {
			close(started)
			q.Start(ctx, func(_ context.Context, ev *structs.Event) error {
				mut.Lock()
				defer mut.Unlock()
				got = append(got, ev.Object)
				return nil
			})
			runWG.Done()
		}()
		<-started
		time.Sleep(10 * time.Millisecond) // Ensure Start() is in its loop

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
		// Wait for all events to fire before cancelling.
		time.Sleep(250 * time.Millisecond)
		cancel()
		runWG.Wait()
		want := []string{"b", "a", "c"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

func TestQueueFutureEventsNotDrained(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbm.WithTypeTree(t, func(tr *dbm.TypeTree[structs.Event, *structs.Event]) {
		var handlerCalls atomic.Int32
		q := New(ctx, tr, "")

		// Start the queue BEFORE pushing events to avoid offset calculation issues.
		// The offset is set on Start() based on existing events, so starting first
		// ensures offset stays 0 and timestamps work as expected.
		runWG := &sync.WaitGroup{}
		runWG.Add(1)
		started := make(chan struct{})
		go func() {
			close(started)
			q.Start(ctx, func(_ context.Context, ev *structs.Event) error {
				handlerCalls.Add(1)
				return nil
			})
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

		// Cancel context - should NOT wait for or process the future event.
		cancel()
		runWG.Wait()

		// Only the immediate event should have been processed.
		if got := handlerCalls.Load(); got != 1 {
			t.Errorf("handler called %d times, want 1 (future event should not be drained)", got)
		}

		// Verify future event is still in the tree.
		_, ev, err := tr.First()
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

func TestQueueHandlerError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbm.WithTypeTree(t, func(tr *dbm.TypeTree[structs.Event, *structs.Event]) {
		q := New(ctx, tr, "")

		// Start the queue.
		runWG := &sync.WaitGroup{}
		runWG.Add(1)
		started := make(chan struct{})
		go func() {
			close(started)
			// Handler returns error on first event.
			q.Start(ctx, func(_ context.Context, ev *structs.Event) error {
				return context.Canceled // Simulate handoff failure
			})
			runWG.Done()
		}()
		<-started
		time.Sleep(10 * time.Millisecond)

		// Push an immediate event.
		if err := q.Push(ctx, &structs.Event{
			At:     uint64(q.After(10 * time.Millisecond)),
			Object: "test",
		}); err != nil {
			t.Fatal(err)
		}

		// Wait for event to be processed (or rejected).
		runWG.Wait()

		// Event should still be in tree since handler returned error.
		_, ev, err := tr.First()
		if err != nil {
			t.Fatal(err)
		}
		if ev == nil {
			t.Error("event should still be in tree after handler error")
		} else if ev.Object != "test" {
			t.Errorf("remaining event is %q, want %q", ev.Object, "test")
		}
	})
}

func TestQueueConcurrentPush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbm.WithTypeTree(t, func(tr *dbm.TypeTree[structs.Event, *structs.Event]) {
		var processed atomic.Int32
		q := New(ctx, tr, "")

		// Start the queue.
		runWG := &sync.WaitGroup{}
		runWG.Add(1)
		started := make(chan struct{})
		go func() {
			close(started)
			q.Start(ctx, func(_ context.Context, ev *structs.Event) error {
				processed.Add(1)
				return nil
			})
			runWG.Done()
		}()
		<-started
		time.Sleep(10 * time.Millisecond)

		// Push 100 events from 10 goroutines concurrently.
		const numGoroutines = 10
		const eventsPerGoroutine = 10
		var pushWG sync.WaitGroup
		for i := 0; i < numGoroutines; i++ {
			pushWG.Add(1)
			go func(id int) {
				defer pushWG.Done()
				for j := 0; j < eventsPerGoroutine; j++ {
					if err := q.Push(ctx, &structs.Event{
						At:     uint64(q.After(10 * time.Millisecond)),
						Object: "event",
					}); err != nil {
						t.Error(err)
					}
				}
			}(i)
		}
		pushWG.Wait()

		// Wait for all events to be processed.
		time.Sleep(100 * time.Millisecond)
		cancel()
		runWG.Wait()

		// All events should have been processed.
		if got := processed.Load(); got != numGoroutines*eventsPerGoroutine {
			t.Errorf("processed %d events, want %d", got, numGoroutines*eventsPerGoroutine)
		}
	})
}
