package queue

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/storage/dbm"
	"github.com/zond/juicemud/structs"
)

// Queue is a persistent priority queue for scheduled events, backed by a B-tree.
// Events are processed in timestamp order. The offset field handles time jumps
// on restart by adjusting all timestamps relative to the earliest queued event.
type Queue struct {
	tree      *dbm.TypeTree[structs.Event, *structs.Event]
	cond      *sync.Cond
	closed    bool
	nextEvent *structs.Event
	offset    structs.Timestamp
}

func New(ctx context.Context, t *dbm.TypeTree[structs.Event, *structs.Event]) *Queue {
	return &Queue{
		cond: sync.NewCond(&sync.Mutex{}),
		tree: t,
	}
}

func (q *Queue) After(dur time.Duration) structs.Timestamp {
	return structs.Timestamp(time.Now().Add(dur).UnixNano()) + q.offset
}

func (q *Queue) At(t time.Time) structs.Timestamp {
	return structs.Timestamp(t.UnixNano()) + q.offset
}

func (q *Queue) until(at structs.Timestamp) time.Duration {
	return time.Nanosecond * time.Duration(uint64(at)-uint64(q.Now()))
}

func (q *Queue) Now() structs.Timestamp {
	return structs.Timestamp(time.Now().UnixNano()) + q.offset
}

func (q *Queue) peekFirst(_ context.Context) (*structs.Event, error) {
	res, err := q.tree.First()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return res, nil
}

func (q *Queue) Close() error {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	q.closed = true
	q.cond.Broadcast()
	q.cond.Wait()
	return nil
}

func (q *Queue) Push(ctx context.Context, eventer structs.Eventer) error {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	if q.closed {
		return errors.Errorf("queue is closed")
	}

	ev, err := eventer.Event()
	if err != nil {
		return juicemud.WithStack(err)
	}

	ev.CreateKey()

	if err := q.tree.Set(ev.Key, ev, false); err != nil {
		return juicemud.WithStack(err)
	}

	if q.nextEvent == nil || ev.At < q.nextEvent.At {
		q.nextEvent = ev
		q.cond.Broadcast()
	}

	return nil
}

type EventHandler func(context.Context, *structs.Event)

// Start runs the event loop, calling handler for each event when its time arrives.
// Blocks until the queue is closed.
func (q *Queue) Start(ctx context.Context, handler EventHandler) error {
	var err error
	if q.nextEvent, err = q.peekFirst(ctx); err != nil {
		return juicemud.WithStack(err)
	}
	if q.nextEvent != nil {
		q.offset = structs.Timestamp(q.nextEvent.At)
	}
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	for !q.closed || q.nextEvent != nil {
		for q.nextEvent != nil && structs.Timestamp(q.nextEvent.At) <= q.Now() {
			handler(ctx, q.nextEvent)
			if err := q.tree.Del(q.nextEvent.Key); err != nil {
				return juicemud.WithStack(err)
			}
			if q.nextEvent, err = q.peekFirst(ctx); err != nil {
				return juicemud.WithStack(err)
			}
		}
		if q.nextEvent != nil {
			if toSleep := q.until(structs.Timestamp(q.nextEvent.At)); toSleep > 0 {
				go func() {
					time.Sleep(toSleep)
					q.cond.Broadcast()
				}()
			}
		}
		if !q.closed {
			q.cond.Wait()
		}
	}
	q.cond.Broadcast()
	return nil
}
