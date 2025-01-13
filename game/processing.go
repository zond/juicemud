package game

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zond/juicemud"
	"github.com/zond/juicemud/js"
	"github.com/zond/juicemud/structs"
	"rogchap.com/v8go"
)

func jsFromGo(rc *js.RunContext, x any) *v8go.Value {
	b, err := json.Marshal(x)
	if err != nil {
		return rc.Throw("trying to marshal %v: %v", x, err)
	}
	res, err := v8go.JSONParse(rc.Context(), string(b))
	if err != nil {
		return rc.Throw("trying to unmarshal %v: %v", string(b), err)
	}
	return res
}

func copyToGo(rc *js.RunContext, dst any, srcInfo *v8go.FunctionCallbackInfo) *v8go.Value {
	args := srcInfo.Args()
	if len(args) != 1 {
		return rc.Throw("function takes 1 argument, not %+v", args)
	}
	s, err := v8go.JSONStringify(rc.Context(), args[0])
	if err != nil {
		return rc.Throw("trying to marshal %v: %v", args[0], err)
	}
	if err := json.Unmarshal([]byte(s), dst); err != nil {
		return rc.Throw("trying to unmarshal %v: %v", s, err)
	}
	return nil
}

func addGetSetPair(name string, source any, callbacks js.Callbacks) {
	callbacks[fmt.Sprintf("get%s", name)] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		return jsFromGo(rc, source)
	}
	callbacks[fmt.Sprintf("set%s", name)] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		return copyToGo(rc, source, info)
	}
}

func objectCallbacks(object *structs.Object) js.Callbacks {
	result := js.Callbacks{}
	addGetSetPair("Location", &object.Location, result)
	addGetSetPair("Content", &object.Content, result)
	addGetSetPair("Skills", &object.Skills, result)
	addGetSetPair("Descriptions", &object.Descriptions, result)
	addGetSetPair("Exits", &object.Exits, result)
	addGetSetPair("SourcePath", &object.SourcePath, result)
	result["setTimeout"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		// TODO: Set single event in the future.
		return nil
	}
	result["setInterval"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		// TODO: Set repeating events in the future.
		return nil
	}
	result["getEnvironment"] = func(rc *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		// TODO: Get access to pre-saved data about parent, siblings, and content.
		return nil
	}
	return result
}

func call(ctx context.Context, object *structs.Object, callbackName string, message string) error {
	sid := string(object.Id)
	game, err := GetGame(ctx)
	if err != nil {
		return juicemud.WithStack(err)
	}
	source, err := game.storage.GetSource(ctx, object.SourcePath)
	if err != nil {
		return juicemud.WithStack(err)
	}

	callbacks := objectCallbacks(object)
	target := js.Target{
		Source:    string(source),
		Origin:    object.SourcePath,
		State:     object.State,
		Callbacks: callbacks,
		Console:   consoleByObjectID.Get(sid),
	}
	res, err := target.Call(ctx, callbackName, message, 200*time.Millisecond)
	if err != nil {
		return juicemud.WithStack(err)
	}
	object.State = res.State
	clear(object.Callbacks)
	for _, cb := range res.Callbacks {
		object.Callbacks[cb] = true
	}
	return nil
}

func loadAndCall(ctx context.Context, id []byte, callbackName string, message string) error {
	sid := string(id)
	jsContextLocks.Lock(sid)
	defer jsContextLocks.Unlock(sid)

	game, err := GetGame(ctx)
	if err != nil {
		return juicemud.WithStack(err)
	}
	object, err := game.storage.GetObject(ctx, id)
	if err != nil {
		return juicemud.WithStack(err)
	}
	oldLocation := object.Location
	if err := call(ctx, object, callbackName, message); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(game.storage.SetObject(ctx, oldLocation, object))
}
