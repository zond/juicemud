package js

import (
	"context"
	"reflect"
	"testing"
	"time"

	"rogchap.com/v8go"
)

func TestBasics(t *testing.T) {
	ctx := context.Background()
	result := ""
	target := &Target{
		Source: `
addCallback("test", (arg) => {
  setResult(state.b + 1 + arg.c);
  state.b += 1;
});
`,
		Origin: "TestBasics",
		State:  "{\"b\": 4}",
		Callbacks: map[string]func(*FunContext, *v8go.FunctionCallbackInfo) *v8go.Value{
			"setResult": func(fctx *FunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
				result = info.Args()[0].String()
				return nil
			},
		},
	}
	res, err := target.Call(ctx, "test", "{\"c\": 15}", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result != "20" {
		t.Errorf("got %q, want 20", result)
	}
	if wantState := "{\"b\":5}"; res.State != wantState {
		t.Errorf("got %q, want %q", res.State, wantState)
	}
	if wantCallbacks := []string{"test"}; !reflect.DeepEqual(res.Callbacks, wantCallbacks) {
		t.Errorf("got %+v, want %+v", res.Callbacks, wantCallbacks)
	}
}
