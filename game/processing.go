package game

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"time"

	"github.com/zond/juicemud"
	"github.com/zond/juicemud/game/skills"
	"github.com/zond/juicemud/js"
	"github.com/zond/juicemud/storage"
	"github.com/zond/juicemud/structs"
	"rogchap.com/v8go"
)

var (
	jsContextLocks = juicemud.NewSyncMap[string, bool]()
)

const (
	defaultReactionDelay = 100 * time.Millisecond
)

func addGetSetPair(name string, source any, callbacks js.Callbacks) {
	callbacks[fmt.Sprintf("get%s", name)] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		res, err := rc.JSFromGo(source)
		if err != nil {
			return rc.Throw("trying to convert %v to *v8go.Value: %v", source, err)
		}
		return res
	}
	callbacks[fmt.Sprintf("set%s", name)] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
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
	Source      string
	Destination string
}

func (g *Game) emitMovement(ctx context.Context, bigM *storage.Movement) error {
	at := g.storage.Queue().After(defaultReactionDelay)

	firstDetections := map[string]*structs.Object{}

	fromNeigh, err := g.loadNeighbourhoodAt(ctx, bigM.Source)
	if err != nil {
		return juicemud.WithStack(err)
	}
	fromDetectors := juicemud.Set[string]{}
	for det, err := range fromNeigh.Detections(bigM.Object) {
		if err != nil {
			return juicemud.WithStack(err)
		}
		if _, found := firstDetections[det.Subject.Id]; !found {
			firstDetections[det.Subject.Id] = det.Object
		}
		fromDetectors.Set(det.Subject.Id)
	}

	toNeigh, err := g.loadNeighbourhoodAt(ctx, bigM.Destination)
	if err != nil {
		return juicemud.WithStack(err)
	}
	toDetectors := juicemud.Set[string]{}
	for det, err := range toNeigh.Detections(bigM.Object) {
		if err != nil {
			return juicemud.WithStack(err)
		}
		if _, found := firstDetections[det.Subject.Id]; !found {
			firstDetections[det.Subject.Id] = det.Object
		}
		toDetectors.Set(det.Subject.Id)
	}

	bothDetectors := fromDetectors.Intersection(toDetectors)
	fromDetectors.DelAll(toDetectors)
	toDetectors.DelAll(fromDetectors)

	pushFunc := func(detectors juicemud.Set[string], source string, destination string) error {
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

	if err := pushFunc(bothDetectors, bigM.Source, bigM.Destination); err != nil {
		return juicemud.WithStack(err)
	}
	if err := pushFunc(fromDetectors, bigM.Source, ""); err != nil {
		return juicemud.WithStack(err)
	}
	if err := pushFunc(toDetectors, "", bigM.Destination); err != nil {
		return juicemud.WithStack(err)
	}

	return nil
}

func (g *Game) rerunSource(ctx context.Context, object *structs.Object) error {
	return g.run(ctx, object, nil)
}

func (g *Game) loadLocationOf(ctx context.Context, id string) (*structs.Object, *structs.Location, error) {
	obj, err := g.storage.LoadObject(ctx, id, g.rerunSource)
	if err != nil {
		return nil, nil, juicemud.WithStack(err)
	}
	loc, err := g.loadLocation(ctx, obj.Location)
	if err != nil {
		return nil, nil, juicemud.WithStack(err)
	}
	return obj, loc, nil
}

func (g *Game) loadLocation(ctx context.Context, locationID string) (*structs.Location, error) {
	result := &structs.Location{}
	var err error
	if result.Container, err = g.storage.LoadObject(ctx, locationID, g.rerunSource); err != nil {
		return nil, juicemud.WithStack(err)
	}
	if result.Content, err = g.storage.LoadObjects(ctx, result.Container.Content, g.rerunSource); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return result, nil
}

// loadNeighbourhoodOf returns the object, and the neighbourhood (location, location neighborus) of it.
func (g *Game) loadNeighbourhoodOf(ctx context.Context, id string) (*structs.Object, *structs.Neighbourhood, error) {
	obj, err := g.storage.LoadObject(ctx, id, g.rerunSource)
	if err != nil {
		return nil, nil, juicemud.WithStack(err)
	}
	neigh, err := g.loadNeighbourhoodAt(ctx, obj.Location)
	if err != nil {
		return nil, nil, juicemud.WithStack(err)
	}
	return obj, neigh, nil
}

// loadNeighbourhoodAt returns the location and location neighbours.
func (g *Game) loadNeighbourhoodAt(ctx context.Context, loc string) (*structs.Neighbourhood, error) {
	neighbourhood := &structs.Neighbourhood{}
	var err error
	if neighbourhood.Location, err = g.loadLocation(ctx, loc); err != nil {
		return nil, juicemud.WithStack(err)
	}
	neighbourhood.Neighbours = map[string]*structs.Location{}
	for _, exit := range neighbourhood.Location.Container.Exits {
		neighbour, err := g.loadLocation(ctx, exit.Destination)
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
		neighbourhood.Neighbours[exit.Destination] = neighbour
	}
	return neighbourhood, nil
}

func (g *Game) addGlobalCallbacks(_ context.Context, callbacks js.Callbacks) {
	callbacks["getSkills"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 0 {
			return rc.Throw("getSkills takes no arguments")
		}
		res, err := rc.JSFromGo(skills.Skills)
		if err != nil {
			return rc.Throw("trying to convert %v to *v8go.Value: %v", skills.Skills, err)
		}
		return res
	}
	callbacks["setSkills"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 2 || !args[0].IsString() || !args[1].IsObject() {
			return rc.Throw("setSkills takes [string, Object] arguments")
		}
		if err := rc.Copy(&skills.Skills, args[1]); err != nil {
			return rc.Throw("trying to convert %v to &skill{}: %v", args[1], err)
		}
		return nil

	}
	callbacks["getSkill"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 1 || !args[0].IsString() {
			return rc.Throw("getSkills takes [string] arguments")
		}
		skill, found := skills.Skills.GetHas(args[0].String())
		if !found {
			return nil
		}
		res, err := rc.JSFromGo(skill)
		if err != nil {
			return rc.Throw("trying to convert %v to *v8go.Value: %v", skills.Skills, err)
		}
		return res
	}
	callbacks["setSkill"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 2 || !args[0].IsString() || !args[1].IsObject() {
			return rc.Throw("setSkill takes [string, Object] arguments")
		}
		skill := skills.Skill{}
		if err := rc.Copy(&skill, args[1]); err != nil {
			return rc.Throw("trying to convert %v to &skill{}: %v", args[1], err)
		}
		skills.Skills.Set(args[0].String(), skill)
		return nil
	}
}

func (g *Game) addObjectCallbacks(ctx context.Context, object *structs.Object, callbacks js.Callbacks) {
	addGetSetPair("Location", &object.Location, callbacks)
	addGetSetPair("Content", &object.Content, callbacks)
	addGetSetPair("Skills", &object.Skills, callbacks)
	addGetSetPair("Descriptions", &object.Descriptions, callbacks)
	addGetSetPair("Exits", &object.Exits, callbacks)
	addGetSetPair("SourcePath", &object.SourcePath, callbacks)
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
		if err := g.emitJSON(ctx, g.storage.Queue().After(delay), object.Id, args[1].String(), message); err != nil {
			return rc.Throw("trying to enqueue %v for %v: %v", message, object.Id, err)
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
		_, neighbourhood, err := g.loadNeighbourhoodOf(ctx, object.Id)
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

/*
Some events we should send to objects:
- moved: Object changed Location.
- received: Object got new Content.
- transmitted: Object lost Content.
*/
func (g *Game) run(ctx context.Context, object *structs.Object, caller structs.Caller) error {
	if caller != nil {
		call, err := caller.Call()
		if err != nil {
			return juicemud.WithStack(err)
		}
		if call != nil {
			t, err := g.storage.SourceModTime(ctx, object.SourcePath)
			if err != nil {
				return juicemud.WithStack(err)
			}
			if object.SourceModTime >= t && !object.HasCallback(call.Name, call.Tag) {
				return nil
			}
		}
	}

	sid := string(object.Id)
	source, modTime, err := g.storage.LoadSource(ctx, object.SourcePath)
	if err != nil {
		return juicemud.WithStack(err)
	}

	callbacks := js.Callbacks{}
	g.addGlobalCallbacks(ctx, callbacks)
	g.addObjectCallbacks(ctx, object, callbacks)
	target := js.Target{
		Source:    string(source),
		Origin:    object.SourcePath,
		State:     object.State,
		Callbacks: callbacks,
		Console:   consoleByObjectID.Get(sid),
	}
	res, err := target.Run(ctx, caller, 200*time.Millisecond)
	if err != nil {
		jserr := &v8go.JSError{}
		if errors.As(err, &jserr) {
			log.New(consoleByObjectID.Get(string(object.Id)), "", 0).Printf("---- error in %s ----\n%s\n%s", jserr.Location, jserr.Message, jserr.StackTrace)
		}
		return juicemud.WithStack(err)
	}
	object.State = res.State
	object.Callbacks = res.Callbacks
	object.SourceModTime = modTime
	return nil
}

func (g *Game) runSave(ctx context.Context, object *structs.Object, caller structs.Caller) error {
	oldLocation := object.Location
	if err := g.run(ctx, object, caller); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(g.storage.StoreObject(ctx, &oldLocation, object))
}

func (g *Game) loadRunSave(ctx context.Context, id string, caller structs.Caller) error {
	sid := string(id)
	jsContextLocks.Lock(sid)
	defer jsContextLocks.Unlock(sid)

	object, err := g.storage.LoadObject(ctx, id, nil)
	if err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(g.runSave(ctx, object, caller))
}
