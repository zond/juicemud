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

	goccy "github.com/goccy/go-json"
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

func (g *Game) emitAny(ctx context.Context, at structs.Timestamp, id string, name string, message any) error {
	b, err := goccy.Marshal(message)
	if err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(g.emitJSON(ctx, at, id, name, string(b)))
}

func (g *Game) emitJSONIf(ctx context.Context, at structs.Timestamp, object *structs.Object, name string, json string) error {
	if object.HasCallback(name, emitEventTag) {
		return juicemud.WithStack(g.emitJSON(ctx, at, object.Id, name, json))
	}
	return nil
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

func (g *Game) emitJSONToNeighbourhoodIf(ctx context.Context, at structs.Timestamp, n *structs.Neighbourhood, name string, json string) error {
	for _, obj := range n.All() {
		if err := g.emitJSONIf(ctx, at, obj, name, json); err != nil {
			return juicemud.WithStack(err)
		}
	}
	return nil
}

type movement struct {
	Object      string
	Source      string
	Destination string
}

func (g *Game) emitMovementToNeighbourhood(ctx context.Context, bigM *storage.Movement) error {
	n, err := g.loadNeighbourhood(ctx, bigM.Object)
	if err != nil {
		return juicemud.WithStack(err)
	}
	json, err := goccy.Marshal(&movement{
		Object:      bigM.Object.Id,
		Source:      bigM.Source,
		Destination: bigM.Destination,
	})
	if err != nil {
		return juicemud.WithStack(err)
	}
	at := g.storage.Queue().After(defaultReactionDelay)
	return juicemud.WithStack(g.emitJSONToNeighbourhoodIf(ctx, at, n, movementEventType, string(json)))
}

func (g *Game) rerunSource(ctx context.Context, object *structs.Object) error {
	return g.run(ctx, object, nil)
}

func (g *Game) loadLocation(ctx context.Context, id string) (*structs.Location, error) {
	result := &structs.Location{}
	var err error
	if result.Container, err = g.storage.LoadObject(ctx, id, g.rerunSource); err != nil {
		return nil, juicemud.WithStack(err)
	}
	if result.Content, err = g.storage.LoadObjects(ctx, result.Container.Content, g.rerunSource); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return result, nil
}

func (g *Game) loadNeighbourhood(ctx context.Context, object *structs.Object) (*structs.Neighbourhood, error) {
	result := &structs.Neighbourhood{}
	var err error
	if result.Self, err = g.loadLocation(ctx, object.Id); err != nil {
		return nil, juicemud.WithStack(err)
	}
	if result.Location, err = g.loadLocation(ctx, object.Location); err != nil {
		return nil, juicemud.WithStack(err)
	}
	result.Neighbours = map[string]*structs.Location{}
	for _, exit := range result.Location.Container.Exits {
		neighbour, err := g.loadLocation(ctx, exit.Destination)
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
		result.Neighbours[exit.Destination] = neighbour
	}
	return result, nil
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
		object, err := g.storage.LoadObject(ctx, object.Id, g.rerunSource)
		if err != nil {
			return rc.Throw("trying to load Object: %v", err)
		}
		neighbourhood, err := g.loadNeighbourhood(ctx, object)
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

type Caller interface {
	Call() (*structs.Call, error)
}

type AnyCall struct {
	Name    string
	Tag     string
	Content any
}

type JSCall structs.Call

func (j JSCall) Call() (*structs.Call, error) {
	return (*structs.Call)(&j), nil
}

func (a *AnyCall) Call() (*structs.Call, error) {
	js, err := goccy.Marshal(a.Content)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return &structs.Call{
		Name:    a.Name,
		Tag:     a.Tag,
		Message: string(js),
	}, nil
}

/*
Some events we should send to objects:
- moved: Object changed Location.
- received: Object got new Content.
- transmitted: Object lost Content.
*/
func (g *Game) run(ctx context.Context, object *structs.Object, caller Caller) error {
	var call *structs.Call
	if caller != nil {
		var err error
		if call, err = caller.Call(); err != nil {
			return juicemud.WithStack(err)
		}
		t, err := g.storage.SourceModTime(ctx, object.SourcePath)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if object.SourceModTime >= t && !object.HasCallback(call.Name, call.Tag) {
			return nil
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
	res, err := target.Run(ctx, call, 200*time.Millisecond)
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

func (g *Game) runSave(ctx context.Context, object *structs.Object, caller Caller) error {
	oldLocation := object.Location
	if err := g.run(ctx, object, caller); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(g.storage.StoreObject(ctx, &oldLocation, object))
}

func (g *Game) loadRunSave(ctx context.Context, id string, caller Caller) error {
	sid := string(id)
	jsContextLocks.Lock(sid)
	defer jsContextLocks.Unlock(sid)

	object, err := g.storage.LoadObject(ctx, id, nil)
	if err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(g.runSave(ctx, object, caller))
}
