package js

import (
	"context"
	"fmt"
	"io"
	"log"
	"runtime"
	"time"

	"github.com/zond/juicemud"
	"rogchap.com/v8go"
)

const (
	stateName = "state"
)

var (
	machines chan *machine
)

func init() {
	machines = make(chan *machine, runtime.NumCPU())
	for i := 0; i < runtime.NumCPU(); i++ {
		m, err := newMachine()
		if err != nil {
			log.Panic(err)
		}
		machines <- m
	}
}

type machine struct {
	iso                    *v8go.Isolate
	vctx                   *v8go.Context
	unableToGenerateString *v8go.Value
}

func newMachine() (*machine, error) {
	m := &machine{
		iso: v8go.NewIsolate(),
	}
	var err error
	if m.vctx = v8go.NewContext(m.iso); err != nil {
		return nil, juicemud.WithStack(err)
	}
	if m.unableToGenerateString, err = v8go.NewValue(m.iso, "unable to generate exception"); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return m, nil
}

type Callbacks map[string]func(rc *RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value

type Target struct {
	Source    string
	Origin    string
	State     string
	Callbacks Callbacks
	Console   io.Writer
}

type Result struct {
	State     string
	Callbacks []string
	Value     string
}

type RunContext struct {
	m         *machine
	t         *Target
	callbacks map[string]*v8go.Function
}

func (rc *RunContext) Context() *v8go.Context {
	return rc.m.vctx
}

func (rc *RunContext) log(format string, args ...any) {
	if rc.t.Console != nil {
		log.New(rc.t.Console, "", 0).Printf(format, args...)
	}
}

func (rc *RunContext) String(s string) *v8go.Value {
	if res, err := v8go.NewValue(rc.m.iso, s); err == nil {
		return res
	}
	return rc.m.unableToGenerateString
}

func (rc *RunContext) Throw(format string, args ...any) *v8go.Value {
	return rc.Context().Isolate().ThrowException(rc.String(fmt.Sprintf(format, args...)))
}

func addJSCallback(rc *RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
	args := info.Args()
	if len(args) == 2 && args[0].IsString() && args[1].IsFunction() {
		eventType := args[0].String()
		fun, err := args[1].AsFunction()
		if err != nil {
			return rc.Throw("trying to cast %v to *v8go.Function: %v", args[1], err)
		}
		rc.callbacks[eventType] = fun
		return nil
	}
	return rc.Throw("addEventListener takes [string, function] arguments")
}

func removeJSCallback(rc *RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
	args := info.Args()
	if len(args) == 1 && args[0].IsString() {
		delete(rc.callbacks, args[0].String())
		return nil
	}
	return rc.Throw("removeEventListener takes [string] arguments")
}

func logFunc(w io.Writer) func(*RunContext, *v8go.FunctionCallbackInfo) *v8go.Value {
	return func(ctx *RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
		anyArgs := []any{}
		for _, arg := range info.Args() {
			stringArg := arg.String()
			if stringArg == "[object Object]" {
				jsonArg, err := v8go.JSONStringify(ctx.Context(), arg)
				if err == nil {
					stringArg = jsonArg
				}
			}
			anyArgs = append(anyArgs, stringArg)
		}
		log.New(w, "", 0).Println(anyArgs...)
		return nil
	}
}

func (rc *RunContext) addCallback(
	name string,
	f func(*RunContext, *v8go.FunctionCallbackInfo) *v8go.Value,
) error {
	return juicemud.WithStack(
		rc.m.vctx.Global().Set(
			name,
			v8go.NewFunctionTemplate(
				rc.m.iso,
				func(info *v8go.FunctionCallbackInfo) *v8go.Value {
					return f(rc, info)
				},
			).GetFunction(rc.m.vctx),
		),
	)
}

func (rc *RunContext) prepareV8Context(timeout *time.Duration) error {
	for name, fun := range rc.t.Callbacks {
		if err := rc.addCallback(
			name,
			fun,
		); err != nil {
			return juicemud.WithStack(err)
		}
	}
	for _, cb := range []struct {
		name string
		fun  func(*RunContext, *v8go.FunctionCallbackInfo) *v8go.Value
	}{
		{
			name: "addCallback",
			fun:  addJSCallback,
		},
		{
			name: "removeCallback",
			fun:  removeJSCallback,
		},
	} {
		if err := rc.addCallback(cb.name, cb.fun); err != nil {
			return juicemud.WithStack(err)
		}
	}
	if rc.t.Console != nil {
		if err := rc.addCallback("log", logFunc(rc.t.Console)); err != nil {
			return juicemud.WithStack(err)
		}
	}

	stateJSON := rc.t.State
	if stateJSON == "" {
		stateJSON = "{}"
	}
	startTime := time.Now()
	stateValue, err := v8go.JSONParse(rc.m.vctx, stateJSON)
	*timeout -= time.Since(startTime)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if err := rc.m.vctx.Global().Set(stateName, stateValue); err != nil {
		return juicemud.WithStack(err)
	}

	return nil
}

var (
	ErrTimeout = fmt.Errorf("Timeout")
)

type result struct {
	value *v8go.Value
	err   error
}

func (rc *RunContext) withTimeout(_ context.Context, f func() (*v8go.Value, error), timeout *time.Duration) (*v8go.Value, error) {
	results := make(chan result, 1)
	go func() {
		t := time.Now()
		val, err := f()
		*timeout -= time.Since(t)
		results <- result{value: val, err: err}
	}()

	select {
	case res := <-results:
		if res.err != nil {
			rc.log("-- error in %q --\n%v\n", rc.t.Origin, res.err)
		}
		return res.value, juicemud.WithStack(res.err)
	case <-time.After(*timeout):
		rc.m.iso.TerminateExecution()
		return nil, juicemud.WithStack(ErrTimeout)
	}
}

func (t Target) Call(ctx context.Context, callbackName string, message string, timeout time.Duration) (*Result, error) {
	m := <-machines
	defer func() { machines <- m }()

	rc := &RunContext{
		m:         m,
		t:         &t,
		callbacks: map[string]*v8go.Function{},
	}

	if err := rc.prepareV8Context(&timeout); err != nil {
		return nil, juicemud.WithStack(err)
	}

	if _, err := rc.withTimeout(ctx, func() (*v8go.Value, error) {
		return rc.m.vctx.RunScript(t.Source, t.Origin)
	}, &timeout); err != nil {
		return nil, juicemud.WithStack(err)
	}

	jsCB, found := rc.callbacks[callbackName]
	if !found {
		return collectResult(rc, nil)
	}

	var val *v8go.Value
	if message != "" {
		var err error
		start := time.Now()
		if val, err = v8go.JSONParse(rc.m.vctx, message); err != nil {
			return nil, juicemud.WithStack(err)
		}
		timeout -= time.Since(start)
	}

	if val, err := rc.withTimeout(ctx, func() (*v8go.Value, error) {
		if val != nil {
			return jsCB.Call(rc.m.vctx.Global(), val)
		} else {
			return jsCB.Call(rc.m.vctx.Global())
		}
	}, &timeout); err != nil {
		return nil, juicemud.WithStack(err)
	} else {
		return collectResult(rc, val)
	}
}

func collectResult(rc *RunContext, value *v8go.Value) (*Result, error) {
	valueJSON := "{}"
	if value != nil && !value.IsNull() {
		var err error
		valueJSON, err = v8go.JSONStringify(rc.m.vctx, value)
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
	result := &Result{
		Value: valueJSON,
	}
	stateValue, err := rc.m.vctx.Global().Get(stateName)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	if result.State, err = v8go.JSONStringify(rc.m.vctx, stateValue); err != nil {
		return nil, juicemud.WithStack(err)
	}
	for name := range rc.callbacks {
		result.Callbacks = append(result.Callbacks, name)
	}
	return result, nil
}
