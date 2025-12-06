package js

import (
	"context"
	"fmt"
	"io"
	"log"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/zond/juicemud"
	"github.com/zond/juicemud/structs"
	"rogchap.com/v8go"

	goccy "github.com/goccy/go-json"
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

const (
	maxStateSize = 1024 * 1024 // 1 MB
)

func newMachine() (*machine, error) {
	m := &machine{
		iso: v8go.NewIsolate(),
	}
	m.vctx = v8go.NewContext(m.iso)
	var err error
	if m.unableToGenerateString, err = v8go.NewValue(m.iso, "unable to generate exception"); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return m, nil
}

type Callbacks map[string]func(rc *RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value

// Target holds all inputs needed to run JavaScript for an object.
type Target struct {
	Source    string    // JavaScript source code
	Origin    string    // Source file path for stack traces
	State     string    // JSON state persisted between runs
	Callbacks Callbacks // Go functions exposed to JavaScript
	Console   io.Writer // Where console.log output goes
}

type Result struct {
	State     string
	Callbacks map[string]map[string]bool
	Value     string
}

type RunContext struct {
	m         *machine
	r         *Result
	t         *Target
	callbacks map[string]*v8go.Function
}

func (rc *RunContext) JSFromGo(x any) (*v8go.Value, error) {
	b, err := goccy.Marshal(x)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	res, err := v8go.JSONParse(rc.Context(), string(b))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return res, nil
}

func (rc *RunContext) Copy(dst any, src *v8go.Value) error {
	s, err := v8go.JSONStringify(rc.Context(), src)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if err := goccy.Unmarshal([]byte(s), dst); err != nil {
		return juicemud.WithStack(err)
	}
	return nil
}

func (rc *RunContext) Context() *v8go.Context {
	return rc.m.vctx
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
	if len(args) == 3 && args[0].IsString() && args[1].IsArray() && args[2].IsFunction() {
		eventType := args[0].String()
		tags := []string{}
		if err := rc.Copy(&tags, args[1]); err != nil {
			return rc.Throw("trying to copy %v to a &[]string{}: %v", args[1], err)
		}
		fun, err := args[2].AsFunction()
		if err != nil {
			return rc.Throw("trying to cast %v to *v8go.Function: %v", args[2], err)
		}
		rc.callbacks[eventType] = fun
		rc.r.Callbacks[eventType] = map[string]bool{}
		if len(tags) == 0 {
			rc.r.Callbacks[eventType][""] = true
		} else {
			for _, tag := range tags {
				rc.r.Callbacks[eventType][tag] = true
			}
		}
		return nil
	}
	return rc.Throw("addCallback takes [string, []string, function] arguments")
}

func removeJSCallback(rc *RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
	args := info.Args()
	if len(args) == 1 && args[0].IsString() {
		eventType := args[0].String()
		delete(rc.callbacks, eventType)
		delete(rc.r.Callbacks, eventType)
		return nil
	}
	return rc.Throw("removeCallback takes [string] arguments")
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

func (rc *RunContext) prepareV8Context(timeoutNanos *int64) error {
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
	atomic.AddInt64(timeoutNanos, -int64(time.Since(startTime)))
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

func (rc *RunContext) withTimeout(_ context.Context, f func() (*v8go.Value, error), timeoutNanos *int64) (*v8go.Value, error) {
	results := make(chan result, 1)
	thisTimeout := atomic.LoadInt64(timeoutNanos)
	go func() {
		t := time.Now()
		val, err := f()
		atomic.AddInt64(timeoutNanos, -int64(time.Since(t)))
		results <- result{value: val, err: err}
	}()

	select {
	case res := <-results:
		return res.value, juicemud.WithStack(res.err)
	case <-time.After(time.Duration(thisTimeout)):
		rc.m.iso.TerminateExecution()
		return nil, juicemud.WithStack(ErrTimeout)
	}
}

// Run executes the JavaScript source, optionally invoking a callback if caller is provided.
// Uses a pool of V8 isolates and enforces the given timeout.
func (t Target) Run(ctx context.Context, caller structs.Caller, timeout time.Duration) (*Result, error) {
	m := <-machines
	defer func() { machines <- m }()

	rc := &RunContext{
		m: m,
		r: &Result{
			Callbacks: map[string]map[string]bool{},
		},
		t:         &t,
		callbacks: map[string]*v8go.Function{},
	}

	timeoutNanos := int64(timeout)

	if err := rc.prepareV8Context(&timeoutNanos); err != nil {
		return nil, juicemud.WithStack(err)
	}

	if _, err := rc.withTimeout(ctx, func() (*v8go.Value, error) {
		return rc.m.vctx.RunScript(t.Source, t.Origin)
	}, &timeoutNanos); err != nil {
		return nil, juicemud.WithStack(err)
	}

	if caller == nil {
		return rc.collectResult(nil)
	}

	call, err := caller.Call()
	if err != nil {
		return nil, juicemud.WithStack(err)
	}

	if call == nil {
		return rc.collectResult(nil)
	}

	if tags, found := rc.r.Callbacks[call.Name]; !found {
		return rc.collectResult(nil)
	} else if _, found = tags[call.Tag]; !found {
		return rc.collectResult(nil)
	}

	jsCB, found := rc.callbacks[call.Name]
	if !found {
		return rc.collectResult(nil)
	}

	var val *v8go.Value
	if call.Message != "" {
		var err error
		start := time.Now()
		if val, err = v8go.JSONParse(rc.m.vctx, call.Message); err != nil {
			return nil, juicemud.WithStack(err)
		}
		atomic.AddInt64(&timeoutNanos, -int64(time.Since(start)))
	}

	if val, err := rc.withTimeout(ctx, func() (*v8go.Value, error) {
		if val != nil {
			return jsCB.Call(rc.m.vctx.Global(), val)
		} else {
			return jsCB.Call(rc.m.vctx.Global())
		}
	}, &timeoutNanos); err != nil {
		return nil, juicemud.WithStack(err)
	} else {
		return rc.collectResult(val)
	}
}

func (rc *RunContext) collectResult(value *v8go.Value) (*Result, error) {
	rc.r.Value = "{}"
	if value != nil && !value.IsNull() {
		var err error
		if rc.r.Value, err = v8go.JSONStringify(rc.m.vctx, value); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
	stateValue, err := rc.m.vctx.Global().Get(stateName)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	if rc.r.State, err = v8go.JSONStringify(rc.m.vctx, stateValue); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return rc.r, nil
}
