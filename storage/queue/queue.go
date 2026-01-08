package queue

import (
	"context"
	"os"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/storage/dbm"
	"github.com/zond/juicemud/structs"
)

// TimePersister is called periodically to persist the current game time.
// This allows resuming from the saved time on restart, preventing skills
// from decaying/recharging during server downtime.
type TimePersister func(structs.Timestamp) error

// Queue is a persistent priority queue for scheduled events, backed by a B-tree.
// Events are processed in timestamp order. The offset field handles time jumps
// on restart by adjusting all timestamps relative to persisted game time.
//
// Shutdown is context-based: cancel the context passed to Start() to stop processing.
// Events pushed after context cancellation may be persisted but not processed in
// the current session; they will be processed on next startup.
//
// Timestamp overflow analysis: Timestamps are uint64 nanoseconds. Current Unix
// time is ~1.7e18 ns. JavaScript's MAX_SAFE_INTEGER (2^53-1 ≈ 9e15) as milliseconds
// yields ~9e18 ns, so the maximum practical setTimeout() value produces
// 1.7e18 + 9e18 ≈ 1.1e19, well within uint64's 1.8e19 limit. The system would
// need to run until year 2554 for current time alone to overflow int64.
type Queue struct {
	tree          *dbm.TypeTree[structs.Event, *structs.Event]
	offset        atomic.Int64   // Nanosecond offset for time adjustment on restart
	wake          chan struct{}  // Buffered(1), signals new event pushed
	timePersister TimePersister  // Called every second to persist game time
}

// New creates a new Queue. If lastGameTime is non-zero, the queue will resume
// from that time (preventing skill decay during downtime). The timePersister
// callback is called every second to save current game time.
func New(_ context.Context, t *dbm.TypeTree[structs.Event, *structs.Event], lastGameTime structs.Timestamp, timePersister TimePersister) *Queue {
	q := &Queue{
		tree:          t,
		wake:          make(chan struct{}, 1),
		timePersister: timePersister,
	}
	// If we have a saved game time, compute offset so Now() returns that time
	if lastGameTime != 0 {
		q.offset.Store(int64(lastGameTime) - time.Now().UnixNano())
	}
	return q
}

func (q *Queue) After(dur time.Duration) structs.Timestamp {
	return structs.Timestamp(time.Now().Add(dur).UnixNano() + q.offset.Load())
}

func (q *Queue) At(t time.Time) structs.Timestamp {
	return structs.Timestamp(t.UnixNano() + q.offset.Load())
}

func (q *Queue) Now() structs.Timestamp {
	return structs.Timestamp(time.Now().UnixNano() + q.offset.Load())
}

// NowTime returns the current game time as time.Time.
// This is used by juicemud.Context for skill operations.
func (q *Queue) NowTime() time.Time {
	return q.Now().Time()
}

func (q *Queue) until(at structs.Timestamp) time.Duration {
	return time.Nanosecond * time.Duration(uint64(at)-uint64(q.Now()))
}

func (q *Queue) peekFirst() (*structs.Event, error) {
	_, res, err := q.tree.First()
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

// Push adds an event to the queue. The event will be processed when its
// timestamp arrives. Safe to call concurrently and while Start() is running.
func (q *Queue) Push(_ context.Context, eventer structs.Eventer) error {
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

// EventHandler processes an event. Returns nil if the event was successfully
// handed off for processing (and should be deleted from the queue), or an error
// if the handoff failed (event should remain in queue for next startup).
type EventHandler func(context.Context, *structs.Event) error

// Start runs the event loop, calling handler for each event when its time arrives.
// Blocks until context is cancelled. Due events are processed before returning;
// future events remain in the queue for next startup.
//
// If handler returns an error, the event is NOT deleted from the queue and Start
// returns immediately with that error. This allows handlers to reject events
// (e.g., during shutdown) while preserving them for the next startup.
func (q *Queue) Start(ctx context.Context, handler EventHandler) error {
	if ctx.Err() != nil {
		return juicemud.WithStack(ctx.Err())
	}

	// Only set offset from first event if we don't already have a persisted time
	if q.offset.Load() == 0 {
		next, err := q.peekFirst()
		if err != nil {
			return juicemud.WithStack(err)
		}
		if next != nil {
			q.offset.Store(int64(next.At))
		}
	}

	// Start goroutine to persist game time every second
	if q.timePersister != nil {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					// Best-effort persistence - ignore errors
					_ = q.timePersister(q.Now())
				case <-ctx.Done():
					// Final persistence on shutdown
					_ = q.timePersister(q.Now())
					return
				}
			}
		}()
	}

	next, err := q.peekFirst()
	if err != nil {
		return juicemud.WithStack(err)
	}

	// Create a stopped, drained timer for reuse. We create an already-expired timer
	// and drain it to ensure a clean initial state (stopped and channel empty).
	timer := time.NewTimer(0)
	<-timer.C
	defer timer.Stop()

	for {
		// Process all due events.
		for next != nil && structs.Timestamp(next.At) <= q.Now() {
			if err := handler(ctx, next); err != nil {
				// Handler rejected event (e.g., context cancelled during handoff).
				// Don't delete - event stays in queue for next startup.
				return juicemud.WithStack(err)
			}
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
				// Event became due, restart loop.
				continue
			}
		}
		// If next == nil, timerC is nil, so select blocks on wake/ctx only.

		select {
		case <-timerC:
			// Timer fired, loop to process.
		case <-q.wake:
			// New event pushed. Stop timer, drain if fired.
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
