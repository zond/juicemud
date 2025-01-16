package storage

import (
	"context"
	"encoding/binary"
	"os"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/js"
	"github.com/zond/juicemud/storage/dbm"
)

var (
	lastEventCounter = uint64(0)
)

type Timestamp uint64

type Event struct {
	At     Timestamp
	Object string
	Call   *js.Call

	key string
}

func (e *Event) createKey() {
	eventCounter := juicemud.Increment(&lastEventCounter)
	atSize := binary.Size(e.At)
	k := make([]byte, atSize+binary.Size(eventCounter))
	binary.BigEndian.PutUint64(k, uint64(e.At))
	binary.BigEndian.PutUint64(k[atSize:], eventCounter)
	e.key = string(k)
}

type Queue struct {
	tree      dbm.Tree
	cond      *sync.Cond
	err       chan error
	closed    bool
	nextEvent *Event
	offset    Timestamp
	handler   func(context.Context, *Event)
}

func NewQueue(ctx context.Context, t dbm.Tree, handler func(context.Context, *Event)) *Queue {
	mut := &sync.Mutex{}
	return &Queue{
		cond:    sync.NewCond(mut),
		tree:    t,
		err:     make(chan error, 1),
		handler: handler,
	}
}

func (q *Queue) After(dur time.Duration) Timestamp {
	return Timestamp(time.Now().Add(dur).UnixNano()) + q.offset
}

func (q *Queue) At(t time.Time) Timestamp {
	return Timestamp(t.UnixNano()) + q.offset
}

func (q *Queue) until(at Timestamp) time.Duration {
	return time.Nanosecond * time.Duration(int64(at)-int64(q.now()))
}

func (q *Queue) now() Timestamp {
	return Timestamp(time.Now().UnixNano()) + q.offset
}

func (q *Queue) peekFirst(_ context.Context) (*Event, error) {
	res := &Event{}
	key, err := q.tree.FirstJSON(res)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, juicemud.WithStack(err)
	}
	res.key = key
	return res, nil
}

func (q *Queue) Close() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

func (q *Queue) Push(ctx context.Context, ev *Event) error {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	if q.closed {
		return errors.Errorf("queue is closed")
	}

	ev.createKey()

	if err := q.tree.SetJSON(ev.key, ev, false); err != nil {
		return juicemud.WithStack(err)
	}

	if q.nextEvent == nil || ev.At < q.nextEvent.At {
		q.nextEvent = ev
		if ev.At >= q.now() {
			q.cond.Broadcast()
		}
	}

	return nil
}

func (q *Queue) Start(ctx context.Context) error {
	var err error
	if q.nextEvent, err = q.peekFirst(ctx); err != nil {
		return juicemud.WithStack(err)
	}
	if q.nextEvent != nil {
		q.offset = q.nextEvent.At
	}
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	for !q.closed || q.nextEvent != nil {
		for q.nextEvent != nil && q.nextEvent.At <= q.now() {
			q.handler(ctx, q.nextEvent)
			if err := q.tree.Del(q.nextEvent.key); err != nil {
				return juicemud.WithStack(err)
			}
			if q.nextEvent, err = q.peekFirst(ctx); err != nil {
				return juicemud.WithStack(err)
			}
		}
		if q.nextEvent != nil {
			if toSleep := q.until(q.nextEvent.At); toSleep > 0 {
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
