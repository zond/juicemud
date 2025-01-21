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

type Queue struct {
	tree      dbm.StructTree[structs.Event, *structs.Event]
	cond      *sync.Cond
	err       chan error
	closed    bool
	nextEvent *structs.Event
	offset    Timestamp
}

type Timestamp uint64

func New(ctx context.Context, t dbm.Tree) *Queue {
	mut := &sync.Mutex{}
	return &Queue{
		cond: sync.NewCond(mut),
		tree: dbm.StructTree[structs.Event, *structs.Event]{StructHash: dbm.StructHash[structs.Event, *structs.Event](t)},
		err:  make(chan error, 1),
	}
}

func (q *Queue) After(dur time.Duration) Timestamp {
	return Timestamp(time.Now().Add(dur).UnixNano()) + q.offset
}

func (q *Queue) At(t time.Time) Timestamp {
	return Timestamp(t.UnixNano()) + q.offset
}

func (q *Queue) until(at Timestamp) time.Duration {
	return time.Nanosecond * time.Duration(uint64(at)-uint64(q.now()))
}

func (q *Queue) now() Timestamp {
	return Timestamp(time.Now().UnixNano()) + q.offset
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

func (q *Queue) Close() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

func (q *Queue) Push(ctx context.Context, ev *structs.Event) error {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	if q.closed {
		return errors.Errorf("queue is closed")
	}

	ev.CreateKey()

	if err := q.tree.Set(ev.Key, ev, false); err != nil {
		return juicemud.WithStack(err)
	}

	if q.nextEvent == nil || ev.At < q.nextEvent.At {
		q.nextEvent = ev
		if Timestamp(ev.At) >= q.now() {
			q.cond.Broadcast()
		}
	}

	return nil
}

func (q *Queue) Start(ctx context.Context, handler func(context.Context, *structs.Event)) error {
	var err error
	if q.nextEvent, err = q.peekFirst(ctx); err != nil {
		return juicemud.WithStack(err)
	}
	if q.nextEvent != nil {
		q.offset = Timestamp(q.nextEvent.At)
	}
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	for !q.closed || q.nextEvent != nil {
		for q.nextEvent != nil && Timestamp(q.nextEvent.At) <= q.now() {
			handler(ctx, q.nextEvent)
			if err := q.tree.Del(q.nextEvent.Key); err != nil {
				return juicemud.WithStack(err)
			}
			if q.nextEvent, err = q.peekFirst(ctx); err != nil {
				return juicemud.WithStack(err)
			}
		}
		if q.nextEvent != nil {
			if toSleep := q.until(Timestamp(q.nextEvent.At)); toSleep > 0 {
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
	return nil
}
