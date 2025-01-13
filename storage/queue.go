package storage

import (
	"context"
	"encoding/binary"
	"sync"
	"time"

	"github.com/estraier/tkrzw-go"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
)

var (
	lastEventCounter = uint64(0)
)

type event struct {
	at      uint64
	key     []byte
	content []byte
}

type Queue struct {
	tree      *tkrzw.DBM
	cond      *sync.Cond
	err       chan error
	closed    bool
	nextEvent *event
	offset    uint64
	fun       func(context.Context, []byte)
}

func NewQueue(ctx context.Context, dbm *tkrzw.DBM, fun func(context.Context, []byte)) *Queue {
	mut := &sync.Mutex{}
	return &Queue{
		cond: sync.NewCond(mut),
		tree: dbm,
		err:  make(chan error, 1),
		fun:  fun,
	}
}

func (q *Queue) After(dur time.Duration) uint64 {
	return uint64(time.Now().Add(dur).UnixNano()) + q.offset
}

func (q *Queue) At(t time.Time) uint64 {
	return uint64(t.UnixNano()) + q.offset
}

func (q *Queue) until(at uint64) time.Duration {
	return time.Nanosecond * time.Duration(int64(at)-int64(q.now()))
}

func (q *Queue) now() uint64 {
	return uint64(time.Now().UnixNano()) + q.offset
}

func (q *Queue) peekFirst(_ context.Context) (*event, error) {
	iter := q.tree.MakeIterator()
	defer iter.Destruct()
	if stat := iter.First(); !stat.IsOK() {
		return nil, juicemud.WithStack(stat)
	}
	k, v, stat := iter.Get()
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return nil, nil
	} else if !stat.IsOK() {
		return nil, juicemud.WithStack(stat)
	}
	return &event{at: binary.BigEndian.Uint64(k[:binary.Size(uint64(0))]), key: k, content: v}, nil
}

func (q *Queue) Close() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

func (q *Queue) Push(ctx context.Context, at uint64, content []byte) error {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	if q.closed {
		return errors.Errorf("queue is closed")
	}

	eventCounter := juicemud.Increment(&lastEventCounter)
	atSize := binary.Size(at)
	key := make([]byte, atSize+binary.Size(eventCounter))
	binary.BigEndian.PutUint64(key, at)
	binary.BigEndian.PutUint64(key[atSize:], eventCounter)

	if stat := q.tree.Set(key, content, false); !stat.IsOK() {
		return juicemud.WithStack(stat)
	}

	if q.nextEvent == nil || at < q.nextEvent.at {
		q.nextEvent = &event{
			at:      at,
			key:     key,
			content: content,
		}
		if q.nextEvent.at >= q.now() {
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
		q.offset = q.nextEvent.at
	}
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	for !q.closed || q.nextEvent != nil {
		for q.nextEvent != nil && q.nextEvent.at <= q.now() {
			q.fun(ctx, q.nextEvent.content)
			if stat := q.tree.Remove(q.nextEvent.key); !stat.IsOK() {
				return juicemud.WithStack(err)
			}
			if q.nextEvent, err = q.peekFirst(ctx); err != nil {
				return juicemud.WithStack(err)
			}
		}
		if q.nextEvent != nil {
			if toSleep := q.until(q.nextEvent.at); toSleep > 0 {
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
