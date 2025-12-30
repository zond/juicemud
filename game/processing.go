package game

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"time"

	goccy "github.com/goccy/go-json"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/js"
	"github.com/zond/juicemud/structs"
	"rogchap.com/v8go"
)

const (
	defaultReactionDelay = 100 * time.Millisecond
	// jsExecutionTimeout is the maximum time allowed for a single JavaScript execution.
	jsExecutionTimeout = 200 * time.Millisecond

	// Interval limits
	minIntervalMS         = 5000 // Minimum interval: 5 seconds
	maxIntervalsPerObject = 10   // Maximum intervals per object
)

type RWMutex interface {
	Lock()
	Unlock()
	RLock()
	RUnlock()
}

// addGetter registers a JavaScript getter function for an object property (read-only).
func addGetter(name string, source any, mutex RWMutex, callbacks js.Callbacks) {
	callbacks[fmt.Sprintf("get%s", name)] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		mutex.RLock()
		defer mutex.RUnlock()
		res, err := rc.JSFromGo(source)
		if err != nil {
			return rc.Throw("trying to convert %v to *v8go.Value: %v", source, err)
		}
		return res
	}
}

// addGetSetPair registers JavaScript getter/setter functions for an object property.
func addGetSetPair(name string, source any, mutex RWMutex, callbacks js.Callbacks) {
	addGetter(name, source, mutex, callbacks)
	callbacks[fmt.Sprintf("set%s", name)] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		mutex.Lock()
		defer mutex.Unlock()
		args := info.Args()
		if len(args) != 1 {
			return rc.Throw("function takes 1 argument, not %+v", args)
		}
		if err := rc.Copy(source, args[0]); err != nil {
			return rc.Throw("trying to copy %v to a %v: %v", args[0], reflect.TypeOf(source), err)
		}
		return nil
	}
}

func (g *Game) emitJSON(ctx context.Context, at structs.Timestamp, id string, name string, json string) error {
	return juicemud.WithStack(g.storage.Queue().Push(ctx, &structs.Event{
		At:     uint64(at),
		Object: id,
		Call: structs.Call{
			Name:    name,
			Message: json,
			Tag:     emitEventTag,
		},
	}))
}

// intervalMetadata is embedded in interval event messages.
type intervalMetadata struct {
	ID     string
	Missed int
}

// intervalMessage wraps user data with interval metadata.
type intervalMessage struct {
	Interval intervalMetadata
	Data     goccy.RawMessage
}

// enqueueIntervalEvent creates and enqueues an event for an interval.
// The missedCount indicates how many intervals were missed (e.g., due to server downtime).
func (g *Game) enqueueIntervalEvent(ctx context.Context, interval *structs.Interval, missedCount int) error {
	// Create the message with interval metadata and user data
	msg := intervalMessage{
		Interval: intervalMetadata{
			ID:     interval.IntervalID,
			Missed: missedCount,
		},
		Data: goccy.RawMessage(interval.EventData),
	}

	// Marshal the combined message
	message, err := goccy.Marshal(msg)
	if err != nil {
		return juicemud.WithStack(err)
	}

	// Enqueue at the scheduled time
	return juicemud.WithStack(g.storage.Queue().Push(ctx, &structs.Event{
		At:     uint64(interval.NextFireTime),
		Object: interval.ObjectID,
		Call: structs.Call{
			Name:    interval.EventName,
			Message: string(message),
			Tag:     emitEventTag,
		},
		IntervalID: interval.IntervalID,
	}))
}

// reEnqueueInterval atomically checks if an interval still exists and schedules its next execution.
// If the interval was cleared, it silently returns nil.
// This is called after an interval event handler completes.
// Uses atomic Update to avoid TOCTOU race between checking existence and updating NextFireTime.
func (g *Game) reEnqueueInterval(ctx context.Context, objectID, intervalID string) error {
	now := int64(g.storage.Queue().Now())

	// Atomically update NextFireTime if interval still exists
	var updated *structs.Interval
	err := g.storage.Intervals().Update(objectID, intervalID, func(interval *structs.Interval) (*structs.Interval, error) {
		interval.NextFireTime = now + interval.IntervalMS*1e6
		updated = interval
		return interval, nil
	})
	if err != nil {
		return juicemud.WithStack(err)
	}
	if updated == nil {
		// Interval was cleared, nothing to re-enqueue
		return nil
	}

	// Enqueue the next occurrence
	return g.enqueueIntervalEvent(ctx, updated, 0)
}

// RecoverIntervals loads all intervals from persistent storage and enqueues them.
// Called at startup to restore interval functionality after a server restart.
// Uses NextFireTime to determine scheduling:
// - If in future: enqueue at that time (clean shutdown, timing preserved)
// - If in past: calculate missed intervals and enqueue for now
// Uses atomic Update to avoid race conditions with concurrent operations.
func (g *Game) RecoverIntervals(ctx context.Context) error {
	now := int64(g.storage.Queue().Now())
	recovered := 0

	// Collect all intervals first to avoid holding read lock while calling Update.
	// The Each() iterator holds a read lock, and Update() needs a write lock on
	// the same mutex, which would cause a deadlock.
	var intervals []*structs.Interval
	for interval, err := range g.storage.Intervals().Each() {
		if err != nil {
			return juicemud.WithStack(err)
		}
		intervals = append(intervals, interval)
	}

	for _, interval := range intervals {
		var fireAt int64
		var missedCount int

		if interval.NextFireTime > now {
			// Future: enqueue at scheduled time (clean shutdown case)
			fireAt = interval.NextFireTime
			missedCount = 0
		} else {
			// Past: server was down, calculate missed intervals
			elapsed := now - interval.NextFireTime
			intervalNS := interval.IntervalMS * 1e6
			if intervalNS > 0 {
				missedCount = int(elapsed / intervalNS)
			}
			fireAt = now // Fire immediately
		}

		// Atomically update NextFireTime before enqueueing
		// This avoids race with concurrent clearInterval operations
		objectID := interval.ObjectID
		intervalID := interval.IntervalID
		var updated *structs.Interval
		if err := g.storage.Intervals().Update(objectID, intervalID, func(i *structs.Interval) (*structs.Interval, error) {
			i.NextFireTime = fireAt
			updated = i
			return i, nil
		}); err != nil {
			g.jsStats.RecordRecoveryError(objectID, intervalID, err)
			log.Printf("updating interval %s for recovery: %v", intervalID, err)
			continue
		}
		if updated == nil {
			// Interval was cleared during recovery, skip it
			continue
		}

		if err := g.enqueueIntervalEvent(ctx, updated, missedCount); err != nil {
			g.jsStats.RecordRecoveryError(objectID, intervalID, err)
			log.Printf("recovering interval %s: %v", intervalID, err)
			continue
		}
		recovered++
	}

	if recovered > 0 {
		log.Printf("recovered %d intervals", recovered)
	}
	return nil
}

type movement struct {
	Object      *structs.Object
	Source      *string
	Destination *string
}

// contentChange represents a change in a container's content.
type contentChange struct {
	Object *structs.Object // the object that was added/removed
}

// renderMovementRequest is sent to a moving object when Movement.Active is false.
// The object should emit movementRenderedResponse back to the observer.
type renderMovementRequest struct {
	Observer    string               // Observer's object ID to emit response to
	Source      *structs.Perspective // nil if not visible
	Destination *structs.Perspective // nil if not visible
}

// movementRenderedResponse is sent back to an observer with the message to display.
type movementRenderedResponse struct {
	Message string
}

// emitMovement notifies all objects that can perceive the moving object about the movement.
// Also notifies source and destination containers about content changes:
// - source container receives "transmitted" event (lost content)
// - destination container receives "received" event (gained content)
// Handles observers in both source and destination locations, including through exits.
func (g *Game) emitMovement(ctx context.Context, obj *structs.Object, source *string, destination *string) error {
	mov := &movement{
		Object:      obj,
		Source:      source,
		Destination: destination,
	}

	at := g.storage.Queue().After(defaultReactionDelay)

	firstDetections := map[string]*structs.Object{}

	fromDetectors := juicemud.Set[string]{}
	toDetectors := juicemud.Set[string]{}

	if mov.Source != nil {
		fromNeigh, err := g.loadDeepNeighbourhoodAt(ctx, *mov.Source)
		if err != nil {
			return juicemud.WithStack(err)
		}
		for det, err := range fromNeigh.Detections(mov.Object) {
			if err != nil {
				return juicemud.WithStack(err)
			}
			if _, found := firstDetections[det.Subject.GetId()]; !found {
				firstDetections[det.Subject.GetId()] = det.Object
			}
			fromDetectors.Set(det.Subject.GetId())
		}
	}

	if mov.Destination != nil {
		toNeigh, err := g.loadDeepNeighbourhoodAt(ctx, *mov.Destination)
		if err != nil {
			return juicemud.WithStack(err)
		}
		for det, err := range toNeigh.Detections(mov.Object) {
			if err != nil {
				return juicemud.WithStack(err)
			}
			if _, found := firstDetections[det.Subject.GetId()]; !found {
				firstDetections[det.Subject.GetId()] = det.Object
			}
			toDetectors.Set(det.Subject.GetId())
		}
	}

	bothDetectors := fromDetectors.Intersection(toDetectors)
	fromDetectors.DelAll(toDetectors)
	toDetectors.DelAll(fromDetectors)

	pushFunc := func(detectors juicemud.Set[string], source *string, destination *string) error {
		for detectorID := range detectors {
			if err := g.storage.Queue().Push(ctx, &structs.AnyEvent{
				At:     at,
				Object: detectorID,
				Caller: &structs.AnyCall{
					Name: movementEventType,
					Tag:  emitEventTag,
					Content: &movement{
						Object:      firstDetections[detectorID],
						Source:      source,
						Destination: destination,
					},
				},
			}); err != nil {
				return juicemud.WithStack(err)
			}
		}
		return nil
	}

	if err := pushFunc(bothDetectors, mov.Source, mov.Destination); err != nil {
		return juicemud.WithStack(err)
	}
	if err := pushFunc(fromDetectors, mov.Source, nil); err != nil {
		return juicemud.WithStack(err)
	}
	if err := pushFunc(toDetectors, nil, mov.Destination); err != nil {
		return juicemud.WithStack(err)
	}

	// Notify containers about content changes
	change := &contentChange{Object: obj}

	// Notify source container that it lost content
	if mov.Source != nil && *mov.Source != "" {
		if err := g.storage.Queue().Push(ctx, &structs.AnyEvent{
			At:     at,
			Object: *mov.Source,
			Caller: &structs.AnyCall{
				Name:    transmittedEventType,
				Tag:     emitEventTag,
				Content: change,
			},
		}); err != nil {
			return juicemud.WithStack(err)
		}
	}

	// Notify destination container that it gained content
	if mov.Destination != nil && *mov.Destination != "" {
		if err := g.storage.Queue().Push(ctx, &structs.AnyEvent{
			At:     at,
			Object: *mov.Destination,
			Caller: &structs.AnyCall{
				Name:    receivedEventType,
				Tag:     emitEventTag,
				Content: change,
			},
		}); err != nil {
			return juicemud.WithStack(err)
		}
	}

	return nil
}

func (g *Game) removeObject(ctx context.Context, obj *structs.Object) error {
	loc := obj.GetLocation()
	if err := g.storage.RemoveObject(ctx, obj); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(g.emitMovement(ctx, obj, &loc, nil))
}

func (g *Game) accessObject(ctx context.Context, id string) (*structs.Object, error) {
	res, err := g.storage.AccessObject(ctx, id, g.runSource)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return res, nil
}

func (g *Game) createObject(ctx context.Context, obj *structs.Object) error {
	dest := obj.GetLocation()
	if err := g.storage.CreateObject(ctx, obj); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(g.emitMovement(ctx, obj, nil, &dest))
}

func (g *Game) moveObject(ctx context.Context, obj *structs.Object, destination string) error {
	source := obj.GetLocation()
	if err := g.storage.MoveObject(ctx, obj, destination); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(g.emitMovement(ctx, obj, &source, &destination))
}

func (g *Game) runSource(ctx context.Context, object *structs.Object) error {
	_, err := g.run(ctx, object, nil, nil)
	return juicemud.WithStack(err)
}

func (g *Game) loadLocationOf(ctx context.Context, id string) (*structs.Object, *structs.Location, error) {
	obj, err := g.storage.AccessObject(ctx, id, g.runSource)
	if err != nil {
		return nil, nil, juicemud.WithStack(err)
	}
	loc, err := g.loadLocation(ctx, obj.GetLocation())
	if err != nil {
		return nil, nil, juicemud.WithStack(err)
	}
	return obj, loc, nil
}

func (g *Game) loadLocation(ctx context.Context, locationID string) (*structs.Location, error) {
	result := &structs.Location{}
	var err error
	if result.Container, err = g.storage.AccessObject(ctx, locationID, g.runSource); err != nil {
		return nil, juicemud.WithStack(err)
	}
	if result.Content, err = g.storage.LoadObjects(ctx, result.Container.GetContent(), g.runSource); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return result, nil
}

// loadDeepNeighbourhoodOf returns the object, and the neighbourhood (location, location neighborus, all content) of it.
func (g *Game) loadDeepNeighbourhoodOf(ctx context.Context, id string) (*structs.Object, *structs.DeepNeighbourhood, error) {
	obj, err := g.storage.AccessObject(ctx, id, g.runSource)
	if err != nil {
		return nil, nil, juicemud.WithStack(err)
	}
	neigh, err := g.loadDeepNeighbourhoodAt(ctx, obj.GetLocation())
	if err != nil {
		return nil, nil, juicemud.WithStack(err)
	}
	return obj, neigh, nil
}

// loadDeepNeighbourhoodAt returns the location, its content, and the destinations and content of its exits.
func (g *Game) loadDeepNeighbourhoodAt(ctx context.Context, loc string) (*structs.DeepNeighbourhood, error) {
	neighbourhood := &structs.DeepNeighbourhood{}
	var err error
	if neighbourhood.Location, err = g.loadLocation(ctx, loc); err != nil {
		return nil, juicemud.WithStack(err)
	}
	neighbourhood.Neighbours = map[string]*structs.Location{}
	for _, exit := range neighbourhood.Location.Container.GetExits() {
		neighbour, err := g.loadLocation(ctx, exit.Destination)
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
		neighbourhood.Neighbours[exit.Destination] = neighbour
	}
	return neighbourhood, nil
}

// loadNeighbourhoodOf returns the object, and the neighbourhood (location, location neighbours) of it.
func (g *Game) loadNeighbourhoodOf(ctx context.Context, id string) (*structs.Object, *structs.Neighbourhood, error) {
	obj, err := g.storage.AccessObject(ctx, id, g.runSource)
	if err != nil {
		return nil, nil, juicemud.WithStack(err)
	}
	neigh, err := g.loadNeighbourhoodAt(ctx, obj.GetLocation())
	if err != nil {
		return nil, nil, juicemud.WithStack(err)
	}
	return obj, neigh, nil
}

// loadNeighbourhood returns the location and the destinations of its exits.
func (g *Game) loadNeighbourhoodAt(ctx context.Context, loc string) (*structs.Neighbourhood, error) {
	neighbourhood := &structs.Neighbourhood{}
	var err error
	if neighbourhood.Location, err = g.storage.AccessObject(ctx, loc, g.runSource); err != nil {
		return nil, juicemud.WithStack(err)
	}
	neighbourhood.Neighbours = map[string]*structs.Object{}
	for _, exit := range neighbourhood.Location.GetExits() {
		neighbour, err := g.storage.AccessObject(ctx, exit.Destination, g.runSource)
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
		neighbourhood.Neighbours[exit.Destination] = neighbour
	}
	return neighbourhood, nil
}

// addGlobalCallbacks adds callbacks available to all JavaScript code (boot.js and object scripts).
// These include skill configuration functions that modify global state. This is safe because:
// 1. Only wizards can create/edit source files (sources are stored on the filesystem)
// 2. Wizards are trusted to configure the game
// 3. Skill configs are game balance settings, not security-sensitive
func (g *Game) addGlobalCallbacks(ctx context.Context, callbacks js.Callbacks) {
	// getSkillConfig(name) -> config or null
	callbacks["getSkillConfig"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 1 || !args[0].IsString() {
			return rc.Throw("getSkillConfig takes [string] arguments")
		}
		skill, found := structs.SkillConfigs.Get(args[0].String())
		if !found {
			return rc.Null()
		}
		res, err := rc.JSFromGo(skill)
		if err != nil {
			return rc.Throw("trying to convert %v to *v8go.Value: %v", skill, err)
		}
		return res
	}

	// casSkillConfig(name, oldConfig, newConfig) -> boolean
	// Atomically updates a skill config if current value matches oldConfig.
	// - oldConfig null: succeeds only if key doesn't exist (insert)
	// - newConfig null: deletes the key (if oldConfig matched)
	// Returns true if swap succeeded, false if current value didn't match.
	callbacks["casSkillConfig"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 3 || !args[0].IsString() {
			return rc.Throw("casSkillConfig takes [string, Object|null, Object|null] arguments")
		}

		name := args[0].String()

		var oldConfig *structs.SkillConfig
		if !args[1].IsNull() && !args[1].IsUndefined() {
			if !args[1].IsObject() {
				return rc.Throw("casSkillConfig: oldConfig must be Object or null")
			}
			old := structs.SkillConfig{}
			if err := rc.Copy(&old, args[1]); err != nil {
				return rc.Throw("trying to convert oldConfig: %v", err)
			}
			oldConfig = &old
		}

		var newConfig *structs.SkillConfig
		if !args[2].IsNull() && !args[2].IsUndefined() {
			if !args[2].IsObject() {
				return rc.Throw("casSkillConfig: newConfig must be Object or null")
			}
			new := structs.SkillConfig{}
			if err := rc.Copy(&new, args[2]); err != nil {
				return rc.Throw("trying to convert newConfig: %v", err)
			}
			newConfig = &new
		}

		swapped, persistErr := structs.SkillConfigs.CompareAndSwapThen(name, oldConfig, newConfig, func() error {
			// Persist to root object state while holding the lock
			return g.updateServerConfig(ctx, func(serverConfig *ServerConfig) {
				if serverConfig.SkillConfigs == nil {
					serverConfig.SkillConfigs = make(map[string]structs.SkillConfig)
				}
				if newConfig == nil {
					delete(serverConfig.SkillConfigs, name)
				} else {
					serverConfig.SkillConfigs[name] = *newConfig
				}
			})
		})
		if persistErr != nil {
			return rc.Throw("casSkillConfig: failed to persist config: %v", persistErr)
		}
		res, err := rc.JSFromGo(swapped)
		if err != nil {
			return rc.Throw("trying to convert result: %v", err)
		}
		return res
	}
}

func (g *Game) addObjectCallbacks(ctx context.Context, object *structs.Object, callbacks js.Callbacks) {
	// --- Property getters/setters ---
	// Location and Content are read-only - use moveObject() for safe modifications
	addGetter("Location", &object.Unsafe.Location, object, callbacks)
	addGetter("Content", &object.Unsafe.Content, object, callbacks)
	addGetSetPair("Skills", &object.Unsafe.Skills, object, callbacks)
	addGetSetPair("Descriptions", &object.Unsafe.Descriptions, object, callbacks)
	addGetSetPair("Exits", &object.Unsafe.Exits, object, callbacks)
	// SourcePath can be set to any value, but path traversal is prevented at load time
	// by storage.safePath() which validates all paths stay within the sources directory.
	// This allows wizards to reassign object sources while maintaining security.
	addGetSetPair("SourcePath", &object.Unsafe.SourcePath, object, callbacks)
	addGetSetPair("Learning", &object.Unsafe.Learning, object, callbacks)
	addGetSetPair("Movement", &object.Unsafe.Movement, object, callbacks)

	// --- Object movement ---
	// moveObject(objectId, destinationId) - safely moves an object using storage.MoveObject
	// which validates containment, prevents cycles, and atomically updates all references.
	callbacks["moveObject"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 2 || !args[0].IsString() || !args[1].IsString() {
			return rc.Throw("moveObject takes [string, string] arguments (objectId, destinationId)")
		}
		objectId := args[0].String()
		if objectId == "" {
			return rc.Throw("moveObject: objectId cannot be empty")
		}
		destId := args[1].String()
		if destId == "" {
			return rc.Throw("moveObject: destinationId cannot be empty")
		}
		// Load the object to move
		obj, err := g.storage.AccessObject(ctx, objectId, nil)
		if err != nil {
			return rc.Throw("moveObject: object %q not found: %v", objectId, err)
		}
		// Use storage.MoveObject for safe, validated movement
		if err := g.storage.MoveObject(ctx, obj, destId); err != nil {
			return rc.Throw("moveObject: %v", err)
		}
		return nil
	}

	// --- Timers ---
	callbacks["setTimeout"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 3 || !args[1].IsString() {
			return rc.Throw("setTimeout takes [int, string, any] arguments")
		}
		message, err := v8go.JSONStringify(rc.Context(), args[2])
		if err != nil {
			return rc.Throw("trying to serialize %v: %v", args[2], err)
		}
		delay := time.Duration(args[0].Integer()) * time.Millisecond
		if err := g.emitJSON(ctx, g.storage.Queue().After(delay), object.GetId(), args[1].String(), message); err != nil {
			return rc.Throw("trying to enqueue %v for %v: %v", message, object.GetId(), err)
		}
		return nil
	}

	callbacks["setInterval"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 3 || !args[1].IsString() {
			return rc.Throw("setInterval takes [int, string, any] arguments")
		}

		intervalMS := args[0].Integer()
		if intervalMS < minIntervalMS {
			return rc.Throw("interval must be at least %dms", minIntervalMS)
		}

		eventName := args[1].String()
		eventData, err := v8go.JSONStringify(rc.Context(), args[2])
		if err != nil {
			return rc.Throw("trying to serialize %v: %v", args[2], err)
		}

		objectID := object.GetId()

		// Check per-object interval limit
		count, err := g.storage.Intervals().CountForObject(objectID)
		if err != nil {
			return rc.Throw("checking interval count: %v", err)
		}
		if count >= maxIntervalsPerObject {
			return rc.Throw("object %q has too many intervals (max %d)", objectID, maxIntervalsPerObject)
		}

		// Create interval record
		intervalID := juicemud.NextUniqueID()
		now := g.storage.Queue().Now()
		nextFireTime := int64(now) + intervalMS*int64(time.Millisecond)

		interval := &structs.Interval{
			ObjectID:     objectID,
			IntervalID:   intervalID,
			IntervalMS:   intervalMS,
			EventName:    eventName,
			EventData:    eventData,
			NextFireTime: nextFireTime,
		}

		if err := g.storage.Intervals().Set(interval); err != nil {
			return rc.Throw("storing interval: %v", err)
		}

		// Enqueue the first event
		if err := g.enqueueIntervalEvent(ctx, interval, 0); err != nil {
			// Rollback: delete the interval
			if delErr := g.storage.Intervals().Del(objectID, intervalID); delErr != nil {
				log.Printf("rollback failed deleting interval %s: %v", intervalID, delErr)
			}
			return rc.Throw("enqueueing interval event: %v", err)
		}

		// Return the interval ID
		return rc.String(intervalID)
	}

	callbacks["clearInterval"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 1 || !args[0].IsString() {
			return rc.Throw("clearInterval takes [string] argument (interval ID)")
		}

		intervalID := args[0].String()
		objectID := object.GetId()

		// Delete the interval - next fire check will find it gone
		// Ignore not found - interval may have already been cleared
		if err := g.storage.Intervals().Del(objectID, intervalID); err != nil && !os.IsNotExist(err) {
			return rc.Throw("deleting interval: %v", err)
		}
		return nil
	}

	// --- Events ---
	callbacks["emit"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		// Accept 3 or 4 arguments
		if len(args) < 3 || len(args) > 4 || !args[0].IsString() || !args[1].IsString() {
			return rc.Throw("emit takes [string, string, any, challenges?] arguments")
		}

		targetId := args[0].String()
		if targetId == "" {
			return rc.Throw("emit: targetId cannot be empty")
		}
		eventName := args[1].String()

		message, err := v8go.JSONStringify(rc.Context(), args[2])
		if err != nil {
			return rc.Throw("trying to serialize %v: %v", args[2], err)
		}

		// Parse optional challenges
		var challenges structs.Challenges
		if len(args) == 4 && !args[3].IsNullOrUndefined() {
			if err := rc.Copy(&challenges, args[3]); err != nil {
				return rc.Throw("invalid challenges: %v", err)
			}
		}

		// Always load target to validate it exists (and for challenge checks)
		recipient, err := g.storage.AccessObject(ctx, targetId, nil)
		if err != nil {
			return rc.Throw("target %q not found: %v", targetId, err)
		}

		// If challenges provided, check recipient's skills
		// challenges.Check(recipient, emitterID) - can recipient perceive event from emitter?
		if len(challenges) > 0 {
			if challenges.Check(recipient, object.GetId()) <= 0 {
				// Recipient fails challenge - silently don't emit
				return nil
			}
		}

		if err := g.emitJSON(ctx, g.storage.Queue().After(defaultReactionDelay), targetId, eventName, message); err != nil {
			return rc.Throw("trying to enqueue %v for %v: %v", message, targetId, err)
		}
		return nil
	}
	callbacks["emitToLocation"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		// Accept 3 or 4 arguments
		if len(args) < 3 || len(args) > 4 || !args[0].IsString() || !args[1].IsString() {
			return rc.Throw("emitToLocation takes [string, string, any, challenges?] arguments")
		}

		locationId := args[0].String()
		if locationId == "" {
			return rc.Throw("emitToLocation: locationId cannot be empty")
		}
		eventName := args[1].String()

		// Parse the message data
		var messageData any
		if err := rc.Copy(&messageData, args[2]); err != nil {
			return rc.Throw("invalid message data: %v", err)
		}

		// Parse optional challenges
		var challenges structs.Challenges
		if len(args) == 4 && !args[3].IsNullOrUndefined() {
			if err := rc.Copy(&challenges, args[3]); err != nil {
				return rc.Throw("invalid challenges: %v", err)
			}
		}

		// Load the deep neighbourhood (location + neighbours with content)
		neighbourhood, err := g.loadDeepNeighbourhoodAt(ctx, locationId)
		if err != nil {
			return rc.Throw("location %q not found: %v", locationId, err)
		}

		at := g.storage.Queue().After(defaultReactionDelay)
		emitterId := object.GetId()

		// Iterate over all observers who pass the challenges
		for obs := range neighbourhood.Observers(emitterId, challenges) {
			if err := g.storage.Queue().Push(ctx, &structs.AnyEvent{
				At:     at,
				Object: obs.Subject.GetId(),
				Caller: &structs.AnyCall{
					Name: eventName,
					Tag:  emitEventTag,
					Content: &structs.LocationEmit{
						Data:        messageData,
						Perspective: obs.Perspective,
					},
				},
			}); err != nil {
				return rc.Throw("emitting to %v: %v", obs.Subject.GetId(), err)
			}
		}

		return nil
	}

	// --- Object queries and lifecycle ---
	callbacks["getNeighbourhood"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		_, neighbourhood, err := g.loadDeepNeighbourhoodOf(ctx, object.GetId())
		if err != nil {
			return rc.Throw("trying to load Object neighbourhood: %v", err)
		}
		val, err := rc.JSFromGo(neighbourhood)
		if err != nil {
			return rc.Throw("trying to convert %v to *v8go.Value: %v", neighbourhood, err)
		}
		return val
	}
	callbacks["getId"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		return rc.String(object.GetId())
	}

	// createObject(sourcePath, location) - creates a new object from a source file.
	// Rate limited to prevent abuse (max 10/minute per object).
	// Returns the new object's ID.
	callbacks["createObject"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 2 || !args[0].IsString() || !args[1].IsString() {
			return rc.Throw("createObject takes [string, string] arguments (sourcePath, location)")
		}

		sourcePath := args[0].String()
		location := args[1].String()
		creatorID := object.GetId()

		// Rate limit check and record atomically (consume the slot upfront)
		if !g.createLimiter.checkAndRecord(creatorID) {
			return rc.Throw("createObject: rate limit exceeded (max %d/minute)", maxCreatesPerMinute)
		}

		// Validate and clean source path
		sourcePath = filepath.Clean(sourcePath)
		if sourcePath == "." || sourcePath == "/" {
			return rc.Throw("createObject: invalid source path")
		}
		// Ensure path starts with /
		if sourcePath[0] != '/' {
			sourcePath = "/" + sourcePath
		}

		// Validate source exists
		exists, err := g.storage.SourceExists(ctx, sourcePath)
		if err != nil {
			return rc.Throw("createObject: checking source: %v", err)
		}
		if !exists {
			return rc.Throw("createObject: source %q not found", sourcePath)
		}

		// Validate location exists and is not empty (root container)
		if location == "" {
			return rc.Throw("createObject: location cannot be empty")
		}
		if _, err := g.storage.AccessObject(ctx, location, nil); err != nil {
			return rc.Throw("createObject: location %q not found", location)
		}

		// Create the new object
		newObj, err := structs.MakeObject(ctx)
		if err != nil {
			return rc.Throw("createObject: %v", err)
		}
		newObj.Unsafe.SourcePath = sourcePath
		newObj.Unsafe.Location = location

		if err := g.createObject(ctx, newObj); err != nil {
			return rc.Throw("createObject: %v", err)
		}

		// Fire 'created' event with creator info (async via queue)
		createdData := map[string]any{
			"creatorId": creatorID,
		}
		createdJSON, err := goccy.Marshal(createdData)
		if err != nil {
			log.Printf("createObject: failed to marshal created event: %v", err)
		} else if err := g.emitJSON(ctx, g.storage.Queue().After(0), newObj.GetId(), createdEventType, string(createdJSON)); err != nil {
			// Log but don't fail - object was created successfully
			log.Printf("createObject: failed to emit created event: %v", err)
		}

		return rc.String(newObj.GetId())
	}

	// removeObject(objectId) - removes an object by ID.
	// Cannot remove caller's current location.
	// Cannot remove non-empty objects (must remove contents first).
	callbacks["removeObject"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 1 || !args[0].IsString() {
			return rc.Throw("removeObject takes [string] argument (objectId)")
		}

		targetId := args[0].String()
		callerId := object.GetId()

		// Cannot remove current location (would leave caller in invalid state)
		if targetId == object.GetLocation() {
			return rc.Throw("removeObject: cannot remove current location")
		}

		// Load the target object
		target, err := g.storage.AccessObject(ctx, targetId, nil)
		if err != nil {
			return rc.Throw("removeObject: object %q not found", targetId)
		}

		// Remove the object (storage.RemoveObject validates it's empty)
		if err := g.removeObject(ctx, target); err != nil {
			return rc.Throw("removeObject: %v", err)
		}

		// If removing self, log it for debugging
		if targetId == callerId {
			log.Printf("Object %q removed itself", callerId)
		}

		return nil
	}

	// print(message) - prints a message to the object's connection, if one exists.
	// If the object has no active connection (e.g., it's an NPC), this silently does nothing.
	// Use this for immediate output that doesn't need to go through the event queue.
	callbacks["print"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 1 || !args[0].IsString() {
			return rc.Throw("print takes [string] argument (message)")
		}
		message := args[0].String()
		if conn, found := g.connectionByObjectID.GetHas(object.GetId()); found {
			fmt.Fprintln(conn.term, message)
		}
		return nil
	}
}

// run executes an object's JavaScript source with the given caller event.
// Loads source, sets up callbacks, runs in V8, and saves resulting state.
//
// Returns true if a JavaScript callback was actually invoked for the event,
// false otherwise. This includes cases where:
//   - No caller was provided (source refresh only)
//   - The caller's event type has no registered callback
//   - The caller's tag doesn't match any registered tag for that event
//
// Note: Even if a callback returns null or undefined, the return value is true
// because a callback was still executed. This distinction matters for command
// handling where we need to know if the event was "handled" by JavaScript.
// intervalInfo is optional interval metadata for stats tracking (nil for non-interval events).
func (g *Game) run(ctx context.Context, object *structs.Object, caller structs.Caller, intervalInfo *IntervalExecInfo) (bool, error) {
	// Serialize JS execution for this object to prevent concurrent runs from
	// producing "last writer wins" race conditions on state updates.
	object.JSLock()
	defer object.JSUnlock()

	id := object.GetId()
	sourcePath := object.GetSourcePath()

	if caller != nil {
		call, err := caller.Call()
		if err != nil {
			return false, juicemud.WithStack(err)
		}
		if call != nil {

			if call.Tag == emitEventTag {
				if c, found := g.connectionByObjectID.GetHas(id); found {
					if err := c.handleEmitEvent(call); err != nil {
						return false, juicemud.WithStack(err)
					}
				}
			}

			t, err := g.storage.ResolvedSourceModTime(ctx, sourcePath)
			if err != nil {
				return false, juicemud.WithStack(err)
			}
			if object.GetSourceModTime() >= t && !object.HasCallback(call.Name, call.Tag) {
				return false, nil
			}
		}
	}

	source, modTime, err := g.storage.LoadResolvedSource(ctx, sourcePath)
	if err != nil {
		return false, juicemud.WithStack(err)
	}

	callbacks := js.Callbacks{}
	g.addGlobalCallbacks(ctx, callbacks)
	g.addObjectCallbacks(ctx, object, callbacks)
	target := js.Target{
		Source:    string(source),
		Origin:    sourcePath,
		State:     object.GetState(),
		Callbacks: callbacks,
		Console:   g.consoleSwitchboard.Writer(id),
	}

	// Time JavaScript execution for stats tracking
	startTime := time.Now()
	res, err := target.Run(ctx, caller, jsExecutionTimeout)
	duration := time.Since(startTime)

	// For timed-out executions, use canonical timeout value for consistent stats
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, js.ErrTimeout) {
		duration = jsExecutionTimeout
	}

	// Record execution stats (always, even on error)
	g.jsStats.RecordExecution(sourcePath, id, duration, intervalInfo)

	if err != nil {
		// Record error to unified stats
		g.jsStats.RecordError(sourcePath, id, err, duration, intervalInfo)

		jserr := &v8go.JSError{}
		if errors.As(err, &jserr) {
			log.New(g.consoleSwitchboard.Writer(id), "", 0).Printf("---- error in %s ----\n%s\n%s", jserr.Location, jserr.Message, jserr.StackTrace)
		}
		return false, juicemud.WithStack(err)
	}

	object.Lock()
	defer object.Unlock()

	object.Unsafe.State = res.State
	object.Unsafe.Callbacks = res.Callbacks
	object.Unsafe.SourceModTime = modTime
	return res.Value != nil, nil
}

func (g *Game) loadRun(ctx context.Context, id string, caller structs.Caller, intervalInfo *IntervalExecInfo) (*structs.Object, bool, error) {
	object, err := g.storage.AccessObject(ctx, id, nil)
	if err != nil {
		// Record pre-run loading error (object not found, etc.)
		g.jsStats.RecordLoadError(id, err)
		return nil, false, juicemud.WithStack(err)
	}
	// run() handles its own error recording for JS execution errors
	found, err := g.run(ctx, object, caller, intervalInfo)
	return object, found, juicemud.WithStack(err)
}
