package game

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"time"

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

type movement struct {
	Object      *structs.Object
	Source      *string
	Destination *string
}

// contentChange represents a change in a container's content.
type contentChange struct {
	Object *structs.Object // the object that was added/removed
}

// movementPerspective describes a location from an observer's perspective.
// Either Here is true (observer's current room) or Exit contains the exit name.
type movementPerspective struct {
	Here bool
	Exit string
}

// renderMovementRequest is sent to a moving object when Movement.Active is false.
// The object should emit movementRenderedResponse back to the observer.
type renderMovementRequest struct {
	Observer    string               // Observer's object ID to emit response to
	Source      *movementPerspective // nil if not visible
	Destination *movementPerspective // nil if not visible
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
	_, err := g.run(ctx, object, nil)
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
func (g *Game) addGlobalCallbacks(_ context.Context, callbacks js.Callbacks) {
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

		swapped := structs.SkillConfigs.CompareAndSwap(name, oldConfig, newConfig)
		res, err := rc.JSFromGo(swapped)
		if err != nil {
			return rc.Throw("trying to convert result: %v", err)
		}
		return res
	}
}

func (g *Game) addObjectCallbacks(ctx context.Context, object *structs.Object, callbacks js.Callbacks) {
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

		// Load the location and its content
		loc, err := g.loadLocation(ctx, locationId)
		if err != nil {
			return rc.Throw("location %q not found: %v", locationId, err)
		}

		at := g.storage.Queue().After(defaultReactionDelay)
		emitterId := object.GetId()

		// Emit to each object in the location
		for recipientId, recipient := range loc.Content {
			// If challenges provided, check recipient's skills
			if len(challenges) > 0 {
				if challenges.Check(recipient, emitterId) <= 0 {
					// Recipient fails challenge - skip
					continue
				}
			}

			if err := g.emitJSON(ctx, at, recipientId, eventName, message); err != nil {
				return rc.Throw("trying to enqueue %v for %v: %v", message, recipientId, err)
			}
		}

		return nil
	}
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
func (g *Game) run(ctx context.Context, object *structs.Object, caller structs.Caller) (bool, error) {
	id := object.GetId()

	if caller != nil {
		call, err := caller.Call()
		if err != nil {
			return false, juicemud.WithStack(err)
		}
		if call != nil {

			if call.Tag == emitEventTag {
				if c, found := connectionByObjectID.GetHas(id); found {
					if err := c.handleEmitEvent(call); err != nil {
						return false, juicemud.WithStack(err)
					}
				}
			}

			t, err := g.storage.ResolvedSourceModTime(ctx, object.GetSourcePath())
			if err != nil {
				return false, juicemud.WithStack(err)
			}
			if object.GetSourceModTime() >= t && !object.HasCallback(call.Name, call.Tag) {
				return false, nil
			}
		}
	}

	source, modTime, err := g.storage.LoadResolvedSource(ctx, object.GetSourcePath())
	if err != nil {
		return false, juicemud.WithStack(err)
	}

	callbacks := js.Callbacks{}
	g.addGlobalCallbacks(ctx, callbacks)
	g.addObjectCallbacks(ctx, object, callbacks)
	target := js.Target{
		Source:    string(source),
		Origin:    object.GetSourcePath(),
		State:     object.GetState(),
		Callbacks: callbacks,
		Console:   consoleSwitchboard.Writer(object.GetId()),
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
	g.jsStats.RecordExecution(object.GetSourcePath(), object.GetId(), duration)

	if err != nil {
		jserr := &v8go.JSError{}
		if errors.As(err, &jserr) {
			log.New(consoleSwitchboard.Writer(object.GetId()), "", 0).Printf("---- error in %s ----\n%s\n%s", jserr.Location, jserr.Message, jserr.StackTrace)
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

func (g *Game) loadRun(ctx context.Context, id string, caller structs.Caller) (*structs.Object, bool, error) {
	object, err := g.storage.AccessObject(ctx, id, nil)
	if err != nil {
		return nil, false, juicemud.WithStack(err)
	}
	found, err := g.run(ctx, object, caller)
	return object, found, juicemud.WithStack(err)
}
