package js

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/zond/juicemud/structs"
	"rogchap.com/v8go"
)

func TestArrays(t *testing.T) {
	iso := v8go.NewIsolate()
	defer iso.Dispose()
	ctx := v8go.NewContext(iso)
	defer ctx.Close()
	if _, err := ctx.RunScript("a = ['a', 'b', 'c', 'd', 'e'];", "test"); err != nil {
		t.Fatal(err)
	}
	a, err := ctx.Global().Get("a")
	if err != nil {
		t.Fatal(err)
	}
	if !a.IsArray() {
		t.Fatalf("%s is no Array", a)
	}
	aObj, err := a.AsObject()
	if err != nil {
		t.Fatal(err)
	}
	idx2, err := aObj.Get("2")
	if err != nil {
		t.Fatal(err)
	}
	if !idx2.IsString() {
		t.Errorf("%v is no string", idx2)
	}
	if v := idx2.String(); v != "c" {
		t.Errorf("wanted 'c', got %v", v)
	}
	f, err := v8go.NewValue(iso, "f")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aObj.MethodCall("push", f); err != nil {
		t.Error(err)
	}
	idx5, err := aObj.Get("5")
	if err != nil {
		t.Fatal(err)
	}
	if !idx5.IsString() {
		t.Errorf("%v is no string", idx5)
	}
	if v := idx5.String(); v != "f" {
		t.Errorf("wanted 'f', got %v", v)
	}
}

func TestBasics(t *testing.T) {
	ctx := context.Background()
	result := ""
	target := Target{
		Source: `
addCallback("test", [], (arg) => {
  setResult(state.b + 1 + arg.c);
  state.b += 1;
});
addCallback("test2", ["x"], (arg) => {
  setResult(state.b + 10 + arg.c);
});
`,
		Origin: "TestBasics",
		State:  "{\"b\": 4}",
		Callbacks: map[string]func(*RunContext, *v8go.FunctionCallbackInfo) *v8go.Value{
			"setResult": func(fctx *RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
				result = info.Args()[0].String()
				return nil
			},
		},
	}
	res, err := target.Run(ctx, &structs.Call{Name: "test", Message: "{\"c\": 15}"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result != "20" {
		t.Errorf("got %q, want 20", result)
	}
	wantState := "{\"b\":5}"
	if res.State != wantState {
		t.Errorf("got %q, want %q", res.State, wantState)
	}
	wantCallbacks := map[string]map[string]bool{
		"test": map[string]bool{
			"": true,
		},
		"test2": map[string]bool{
			"x": true,
		},
	}
	if !reflect.DeepEqual(res.Callbacks, wantCallbacks) {
		t.Errorf("got %+v, want %+v", res.Callbacks, wantCallbacks)
	}
	target.State = res.State

	res, err = target.Run(ctx, &structs.Call{Name: "test2", Message: "{\"c\": 30}"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result != "20" {
		t.Errorf("got %q, want 20 (callback should not have been invoked without matching tag)", result)
	}
	if res.State != wantState {
		t.Errorf("got %q, want %q", res.State, wantState)
	}
	if !reflect.DeepEqual(res.Callbacks, wantCallbacks) {
		t.Errorf("got %+v, want %+v", res.Callbacks, wantCallbacks)
	}

	res, err = target.Run(ctx, &structs.Call{Name: "test2", Message: "{\"c\": 30}", Tag: "x"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result != "45" {
		t.Errorf("got %q, want 45", result)
	}
	if res.State != wantState {
		t.Errorf("got %q, want %q", res.State, wantState)
	}
	if !reflect.DeepEqual(res.Callbacks, wantCallbacks) {
		t.Errorf("got %+v, want %+v", res.Callbacks, wantCallbacks)
	}
}

func TestCallbackInvoked(t *testing.T) {
	ctx := context.Background()
	target := Target{
		Source: `
addCallback("returnsValue", [], (arg) => {
  return {result: "hello"};
});
addCallback("returnsNull", [], (arg) => {
  return null;
});
addCallback("returnsUndefined", [], (arg) => {
  // implicit undefined return
});
`,
		Origin:    "TestCallbackInvoked",
		State:     "{}",
		Callbacks: map[string]func(*RunContext, *v8go.FunctionCallbackInfo) *v8go.Value{},
	}

	// Test 1: No caller - should not invoke any callback
	res, err := target.Run(ctx, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Value != nil {
		t.Errorf("expected Value to be nil when no caller, got %q", *res.Value)
	}

	// Test 2: Caller with non-matching event - should not invoke any callback
	res, err = target.Run(ctx, &structs.Call{Name: "nonexistent", Message: "{}"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Value != nil {
		t.Errorf("expected Value to be nil for non-matching event, got %q", *res.Value)
	}

	// Test 3: Caller with matching event that returns a value
	res, err = target.Run(ctx, &structs.Call{Name: "returnsValue", Message: "{}"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Value == nil {
		t.Error("expected Value to be non-nil when callback returns a value")
	} else if *res.Value != `{"result":"hello"}` {
		t.Errorf("expected Value to be the returned object, got %q", *res.Value)
	}

	// Test 4: Caller with matching event that returns null
	res, err = target.Run(ctx, &structs.Call{Name: "returnsNull", Message: "{}"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Value == nil {
		t.Error("expected Value to be non-nil when callback returns null (callback was still invoked)")
	} else if *res.Value != "null" {
		t.Errorf("expected Value to be \"null\", got %q", *res.Value)
	}

	// Test 5: Caller with matching event that returns undefined
	res, err = target.Run(ctx, &structs.Call{Name: "returnsUndefined", Message: "{}"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Value == nil {
		t.Error("expected Value to be non-nil when callback returns undefined (callback was still invoked)")
	} else if *res.Value != "undefined" {
		t.Errorf("expected Value to be \"undefined\", got %q", *res.Value)
	}
}

func BenchmarkV8(b *testing.B) {
	b.StopTimer()
	iso := v8go.NewIsolate()
	defer iso.Dispose()
	ctx := v8go.NewContext(iso)
	defer ctx.Close()
	result := ""
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		glob := ctx.Global()
		setResult := v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
			result = info.Args()[0].String()
			return nil
		}).GetFunction(ctx)
		if err := glob.Set("setResult", setResult); err != nil {
			b.Fatal(err)
		}
		if _, err := ctx.RunScript(`
var b = 4;
function test(arg) {
  setResult(b + 1 + arg.c);
}
test({"c": 15});
`, "test"); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if result != "20" {
		b.Fatalf("wanted 20, got %q", result)
	}
}

func BenchmarkCall(b *testing.B) {
	b.StopTimer()
	ctx := context.Background()
	result := ""
	target := Target{
		Source: `
addCallback("test", [], (arg) => {
  setResult(state.b + 1 + arg.c);
  state.b += 1;
});
`,
		Origin: "TestBasics",
		State:  "{\"b\": 4}",
		Callbacks: map[string]func(*RunContext, *v8go.FunctionCallbackInfo) *v8go.Value{
			"setResult": func(fctx *RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
				result = info.Args()[0].String()
				return nil
			},
		},
	}
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		_, err := target.Run(ctx, &structs.Call{Name: "test", Message: "{\"c\": 15}"}, time.Second)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if result != "20" {
		b.Fatalf("got %q, want \"20\"", result)
	}
}
