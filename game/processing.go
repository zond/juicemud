package game

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"time"

	"github.com/zond/juicemud"
	"github.com/zond/juicemud/js"
	"github.com/zond/juicemud/storage"
	"github.com/zond/juicemud/structs"
	"rogchap.com/v8go"

	goccy "github.com/goccy/go-json"
)

func copyToGo(rc *js.RunContext, dst any, srcInfo *v8go.FunctionCallbackInfo) *v8go.Value {
	args := srcInfo.Args()
	if len(args) != 1 {
		return rc.Throw("function takes 1 argument, not %+v", args)
	}
	if err := rc.Copy(dst, args[0]); err != nil {
		return rc.Throw("trying to copy %v to a %v: %v", args[0], reflect.TypeOf(dst), err)
	}
	return nil
}

func addGetSetPair(name string, source any, callbacks js.Callbacks) {
	callbacks[fmt.Sprintf("get%s", name)] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		res, err := rc.JSFromGo(source)
		if err != nil {
			return rc.Throw("trying to convert %v to *v8go.Value: %v", source, err)
		}
		return res
	}
	callbacks[fmt.Sprintf("set%s", name)] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		return copyToGo(rc, source, info)
	}
}

func (g *Game) objectCallbacks(ctx context.Context, object *structs.Object) js.Callbacks {
	result := js.Callbacks{}
	addGetSetPair("Location", &object.Location, result)
	addGetSetPair("Content", &object.Content, result)
	addGetSetPair("Skills", &object.Skills, result)
	addGetSetPair("Descriptions", &object.Descriptions, result)
	addGetSetPair("Exits", &object.Exits, result)
	addGetSetPair("SourcePath", &object.SourcePath, result)
	result["setTimeout"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 3 || !args[1].IsString() {
			return rc.Throw("setTimeout takes [int, string, any] arguments")
		}
		message, err := v8go.JSONStringify(rc.Context(), args[2])
		if err != nil {
			return rc.Throw("trying to serialize %v: %v", args[2], err)
		}
		if err := g.queue.Push(ctx, &storage.Event{
			At:     g.queue.After(time.Duration(args[0].Integer()) * time.Millisecond),
			Object: object.Id,
			Call: &js.Call{
				Name:    args[1].String(),
				Message: message,
				Tag:     emitEventTag,
			},
		}); err != nil {
			return rc.Throw("trying to enqueue %v for %v: %v", message, object.Id)
		}
		return nil
	}
	result["setInterval"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		// TODO: Set repeating events in the future - or is that too risky?
		return nil
	}
	result["emit"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) != 3 || !args[0].IsString() || !args[1].IsString() {
			return rc.Throw("emit takes [string, string, any] arguments")
		}
		message, err := v8go.JSONStringify(rc.Context(), args[1])
		if err != nil {
			return rc.Throw("trying to serialize %v: %v", args[1], err)
		}
		if err := g.queue.Push(ctx, &storage.Event{
			At:     g.queue.After(100 * time.Millisecond),
			Object: args[0].String(),
			Call: &js.Call{
				Name:    args[1].String(),
				Message: message,
				Tag:     emitEventTag,
			},
		}); err != nil {
			return rc.Throw("trying to enqueue %v for %v: %v", message, args[0].String(), err)
		}
		return nil
	}
	result["getEnvironment"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		location, err := g.storage.GetObject(ctx, object.Location)
		if err != nil {
			return rc.Throw("trying to load Object Location: %v", err)
		}
		keys := make([]string, 0, len(object.Content)+len(location.Content))
		for id := range object.Content {
			keys = append(keys, id)
		}
		for id := range location.Content {
			if id != object.Id {
				keys = append(keys, id)
			}
		}
		loaded, err := g.storage.GetObjects(ctx, keys)
		if err != nil {
			return rc.Throw("trying to load Object and Location Content: %v", err)
		}
		content := loaded[:len(object.Content)]
		siblings := loaded[len(object.Content):]
		js, err := goccy.Marshal(map[string]any{
			"Location": location,
			"Content":  content,
			"Siblings": siblings,
		})
		if err != nil {
			return rc.Throw("trying to serialize Object Location, Content and siblings: %v", err)
		}
		result, err := v8go.JSONParse(rc.Context(), string(js))
		if err != nil {
			return rc.Throw("trying to unserialize Object Location, Content and siblings: %v", err)
		}
		return result
	}
	return result
}

/*
Some events we should send to objects:
- moved: Object changed Location.
- received: Object got new Content.
- transmitted: Object lost Content.
*/
func (g *Game) run(ctx context.Context, object *structs.Object, call *js.Call) error {
	if call != nil {
		if callbacks, found := object.Callbacks[call.Name]; !found {
			return nil
		} else if _, found = callbacks[call.Tag]; !found {
			return nil
		}
	}

	sid := string(object.Id)
	source, err := g.storage.GetSource(ctx, object.SourcePath)
	if err != nil {
		return juicemud.WithStack(err)
	}

	callbacks := g.objectCallbacks(ctx, object)
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
	return nil
}

func (g *Game) loadAndRun(ctx context.Context, id string, call *js.Call) error {
	sid := string(id)
	jsContextLocks.Lock(sid)
	defer jsContextLocks.Unlock(sid)

	object, err := g.storage.GetObject(ctx, id)
	if err != nil {
		return juicemud.WithStack(err)
	}
	oldLocation := object.Location
	if err := g.run(ctx, object, call); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(g.storage.SetObject(ctx, &oldLocation, object))
}
