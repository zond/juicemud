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
//
// Coordination uses channels instead of sync.Cond for clean integration with
// timers and context cancellation in a single select loop.
//
// Timestamp overflow analysis: Timestamps are uint64 nanoseconds. Current Unix
// time is ~1.7e18 ns. JavaScript's MAX_SAFE_INTEGER (2^53-1 ≈ 9e15) as milliseconds
// yields ~9e18 ns, so the maximum practical setTimeout() value produces
// 1.7e18 + 9e18 ≈ 1.1e19, well within uint64's 1.8e19 limit. The system would
// need to run until year 2554 for current time alone to overflow int64.
type Queue struct {
	tree   *dbm.TypeTree[structs.Event, *structs.Event]
	offset structs.Timestamp
	wake   chan struct{} // Buffered(1), signals new event or state change
	done   chan struct{} // Closed when Start() exits
	mu     sync.Mutex    // Protects closed flag
	closed bool
}

func New(ctx context.Context, t *dbm.TypeTree[structs.Event, *structs.Event]) *Queue {
	return &Queue{
		tree: t,
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
}

func (q *Queue) After(dur time.Duration) structs.Timestamp {
	return structs.Timestamp(time.Now().Add(dur).UnixNano()) + q.offset
}

func (q *Queue) At(t time.Time) structs.Timestamp {
	return structs.Timestamp(t.UnixNano()) + q.offset
}

func (q *Queue) Now() structs.Timestamp {
	return structs.Timestamp(time.Now().UnixNano()) + q.offset
}

func (q *Queue) until(at structs.Timestamp) time.Duration {
	return time.Nanosecond * time.Duration(uint64(at)-uint64(q.Now()))
}

func (q *Queue) peekFirst() (*structs.Event, error) {
	res, err := q.tree.First()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return res, nil
}

// signal sends a non-blocking wake signal.
func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Close signals the queue to stop and waits for Start() to exit.
func (q *Queue) Close() error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.closed = true
	q.mu.Unlock()
	q.signal()
	<-q.done
	return nil
}

func (q *Queue) Push(ctx context.Context, eventer structs.Eventer) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return errors.Errorf("queue is closed")
	}
	q.mu.Unlock()

	ev, err := eventer.Event()
	if err != nil {
		return juicemud.WithStack(err)
	}
	ev.CreateKey()

	if err := q.tree.Set(ev.Key, ev, false); err != nil {
		return juicemud.WithStack(err)
	}

	q.signal()
	return nil
}

type EventHandler func(context.Context, *structs.Event)

// Start runs the event loop, calling handler for each event when its time arrives.
// Blocks until the queue is closed or context is cancelled.
// Due events are processed before returning; future events remain in the queue.
func (q *Queue) Start(ctx context.Context, handler EventHandler) error {
	defer close(q.done)

	if ctx.Err() != nil {
		return juicemud.WithStack(ctx.Err())
	}

	next, err := q.peekFirst()
	if err != nil {
		return juicemud.WithStack(err)
	}
	if next != nil {
		q.offset = structs.Timestamp(next.At)
	}

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	for {
		q.mu.Lock()
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return nil
		}

		// Process all due events.
		for next != nil && structs.Timestamp(next.At) <= q.Now() {
			handler(ctx, next)
			if err := q.tree.Del(next.Key); err != nil {
				return juicemud.WithStack(err)
			}
			if next, err = q.peekFirst(); err != nil {
				return juicemud.WithStack(err)
			}
		}

		// Determine what to wait on.
		var timerC <-chan time.Time
		if next != nil {
			if d := q.until(structs.Timestamp(next.At)); d > 0 {
				timer.Reset(d)
				timerC = timer.C
			} else {
				continue
			}
		}

		select {
		case <-timerC:
			// Timer fired, loop to process.
		case <-q.wake:
			// New event or close requested. Stop timer, drain if fired.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if next, err = q.peekFirst(); err != nil {
				return juicemud.WithStack(err)
			}
		case <-ctx.Done():
			return juicemud.WithStack(ctx.Err())
		}
	}
}
