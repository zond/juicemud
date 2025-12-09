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

	goccy "github.com/goccy/go-json"
)

const (
	defaultReactionDelay = 100 * time.Millisecond
)

type RWMutex interface {
	Lock()
	Unlock()
	RLock()
	RUnlock()
}

// addGetSetPair registers JavaScript getter/setter functions for an object property.
func addGetSetPair(name string, source any, mutex RWMutex, callbacks js.Callbacks) {
	callbacks[fmt.Sprintf("get%s", name)] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		mutex.RLock()
		defer mutex.RUnlock()
		res, err := rc.JSFromGo(source)
		if err != nil {
			return rc.Throw("trying to convert %v to *v8go.Value: %v", source, err)
		}
		return res
	}
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

// emitMovement notifies all objects that can perceive the moving object about the movement.
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

var (
	ErrCircularContainer = errors.New("Objects can't contain themselves.")
)

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

func (g *Game) addGlobalCallbacks(_ context.Context, callbacks js.Callbacks) {
	callbacks["getSkillConfigs"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 0 {
			return rc.Throw("getSkillConfigs takes no arguments")
		}
		res, err := rc.JSFromGo(structs.SkillConfigs)
		if err != nil {
			return rc.Throw("trying to convert %v to *v8go.Value: %v", structs.SkillConfigs, err)
		}
		return res
	}
	callbacks["setSkillConfigs"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 1 || !args[0].IsObject() {
			return rc.Throw("setSkillConfigss takes [Object] arguments")
		}
		if err := rc.Copy(&structs.SkillConfigs, args[0]); err != nil {
			return rc.Throw("trying to convert %v to structs.SkillConfigs: %v", args[0], err)
		}
		return nil

	}
	callbacks["getSkillConfig"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 1 || !args[0].IsString() {
			return rc.Throw("getSkillConfig takes [string] arguments")
		}
		skill, found := structs.SkillConfigs.GetHas(args[0].String())
		if !found {
			return nil
		}
		res, err := rc.JSFromGo(skill)
		if err != nil {
			return rc.Throw("trying to convert %v to *v8go.Value: %v", structs.SkillConfigs, err)
		}
		return res
	}
	callbacks["setSkillConfig"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 2 || !args[0].IsString() || !args[1].IsObject() {
			return rc.Throw("setSkillConfig takes [string, Object] arguments")
		}
		skill := structs.SkillConfig{}
		if err := rc.Copy(&skill, args[1]); err != nil {
			return rc.Throw("trying to convert %v to &structs.SkillConfig{}: %v", args[1], err)
		}
		structs.SkillConfigs.Set(args[0].String(), skill)
		return nil
	}
}

func (g *Game) addObjectCallbacks(ctx context.Context, object *structs.Object, callbacks js.Callbacks) {
	addGetSetPair("Location", &object.Unsafe.Location, object, callbacks)
	addGetSetPair("Content", &object.Unsafe.Content, object, callbacks)
	addGetSetPair("Skills", &object.Unsafe.Skills, object, callbacks)
	addGetSetPair("Descriptions", &object.Unsafe.Descriptions, object, callbacks)
	addGetSetPair("Exits", &object.Unsafe.Exits, object, callbacks)
	addGetSetPair("SourcePath", &object.Unsafe.SourcePath, object, callbacks)
	addGetSetPair("Learning", &object.Unsafe.Learning, object, callbacks)
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
		// TODO: Set repeating events in the future - or is that too risky?
		return nil
	}
	callbacks["emit"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 3 || !args[0].IsString() || !args[1].IsString() {
			return rc.Throw("emit takes [string, string, any] arguments")
		}
		message, err := v8go.JSONStringify(rc.Context(), args[2])
		if err != nil {
			return rc.Throw("trying to serialize %v: %v", args[2], err)
		}
		if err := g.emitJSON(ctx, g.storage.Queue().After(defaultReactionDelay), args[0].String(), args[1].String(), message); err != nil {
			return rc.Throw("trying to enqueue %v for %v: %v", message, args[0].String(), err)
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
//
// TODO: Consider adding events for container objects when content changes:
// - "received": notify container when it gains content
// - "transmitted": notify container when it loses content
func (g *Game) run(ctx context.Context, object *structs.Object, caller structs.Caller) (bool, error) {
	id := object.GetId()

	if caller != nil {
		call, err := caller.Call()
		if err != nil {
			return false, juicemud.WithStack(err)
		}
		if call != nil {

			if call.Name == movementEventType && call.Tag == emitEventTag {
				if c, found := connectionByObjectID.GetHas(id); found {
					m := &movement{}
					if err := goccy.Unmarshal([]byte(call.Message), m); err != nil {
						return false, juicemud.WithStack(err)
					}
					if err := c.renderMovement(m); err != nil {
						return false, juicemud.WithStack(err)
					}
				}
			}

			t, err := g.storage.SourceModTime(ctx, object.GetSourcePath())
			if err != nil {
				return false, juicemud.WithStack(err)
			}
			if object.GetSourceModTime() >= t && !object.HasCallback(call.Name, call.Tag) {
				return false, nil
			}
		}
	}

	source, modTime, err := g.storage.LoadSource(ctx, object.GetSourcePath())
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
		Console:   consoleByObjectID.Get(object.GetId()),
	}
	res, err := target.Run(ctx, caller, 200*time.Millisecond)
	if err != nil {
		jserr := &v8go.JSError{}
		if errors.As(err, &jserr) {
			log.New(consoleByObjectID.Get(string(object.GetId())), "", 0).Printf("---- error in %s ----\n%s\n%s", jserr.Location, jserr.Message, jserr.StackTrace)
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
