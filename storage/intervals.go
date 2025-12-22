package storage

import (
	"iter"
	"os"

	"github.com/zond/juicemud"
	"github.com/zond/juicemud/storage/dbm"
	"github.com/zond/juicemud/structs"
)

// Intervals manages persistent storage for recurring intervals.
// Uses a TypeTree with objectID as the "set" and intervalID as the "key".
type Intervals struct {
	tree *dbm.TypeTree[structs.Interval, *structs.Interval]
}

// NewIntervals creates an Intervals wrapper around an opened TypeTree.
func NewIntervals(tree *dbm.TypeTree[structs.Interval, *structs.Interval]) *Intervals {
	return &Intervals{tree: tree}
}

// Set stores an interval. Updates if it already exists.
func (i *Intervals) Set(interval *structs.Interval) error {
	return juicemud.WithStack(i.tree.SubSet(interval.ObjectID, interval.IntervalID, interval))
}

// Get retrieves an interval by objectID and intervalID.
// Returns os.ErrNotExist if not found.
func (i *Intervals) Get(objectID, intervalID string) (*structs.Interval, error) {
	return i.tree.SubGet(objectID, intervalID)
}

// Del removes an interval. Returns os.ErrNotExist if not found.
func (i *Intervals) Del(objectID, intervalID string) error {
	return juicemud.WithStack(i.tree.SubDel(objectID, intervalID))
}

// Has checks if an interval exists.
func (i *Intervals) Has(objectID, intervalID string) bool {
	_, err := i.tree.SubGet(objectID, intervalID)
	return err == nil
}

// Update atomically updates an interval if it exists.
// The function f receives the current interval and returns the updated interval (or error).
// If the interval doesn't exist (was cleared), f is not called and nil is returned.
// This avoids TOCTOU races between checking existence and updating.
func (i *Intervals) Update(objectID, intervalID string, f func(*structs.Interval) (*structs.Interval, error)) error {
	proc := i.tree.SubSProc(objectID, intervalID, func(current *structs.Interval) (*structs.Interval, error) {
		if current == nil {
			// Interval was cleared, return nil to keep it deleted
			return nil, nil
		}
		return f(current)
	})
	return juicemud.WithStack(i.tree.Proc([]dbm.Proc{proc}, true))
}

// EachForObject iterates over all intervals for a specific object.
func (i *Intervals) EachForObject(objectID string) iter.Seq2[*structs.Interval, error] {
	return i.tree.SubEach(objectID)
}

// CountForObject returns the number of intervals for a specific object.
func (i *Intervals) CountForObject(objectID string) (int, error) {
	return i.tree.SubCount(objectID)
}

// Each iterates over all intervals in the database.
// Uses EachSet() to get all objectIDs, then SubEach() for each object's intervals.
func (i *Intervals) Each() iter.Seq2[*structs.Interval, error] {
	return func(yield func(*structs.Interval, error) bool) {
		for objectID, err := range i.tree.EachSet() {
			if err != nil {
				if !yield(nil, juicemud.WithStack(err)) {
					return
				}
				continue
			}
			for interval, err := range i.EachForObject(objectID) {
				if err != nil {
					if !yield(nil, err) {
						return
					}
					continue
				}
				if !yield(interval, nil) {
					return
				}
			}
		}
	}
}

// DelAllForObject removes all intervals for a specific object.
// Returns nil if the object has no intervals.
func (i *Intervals) DelAllForObject(objectID string) error {
	// Collect interval IDs first to avoid modifying during iteration
	var intervalIDs []string
	for interval, err := range i.EachForObject(objectID) {
		if err != nil {
			return juicemud.WithStack(err)
		}
		intervalIDs = append(intervalIDs, interval.IntervalID)
	}
	// Delete each interval
	for _, intervalID := range intervalIDs {
		if err := i.Del(objectID, intervalID); err != nil {
			// Ignore ErrNotExist in case of concurrent deletion
			if !os.IsNotExist(err) {
				return juicemud.WithStack(err)
			}
		}
	}
	return nil
}

// Close closes the underlying tree database.
func (i *Intervals) Close() error {
	return juicemud.WithStack(i.tree.Close())
}
