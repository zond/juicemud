package js

import (
	"context"
	"fmt"
	"io"
	"log"
	"runtime"
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
	// MEMORY LIMITATION: The v8go library does not expose V8's ResourceConstraints API,
	// which means we CANNOT limit memory usage of JavaScript execution. V8 internally
	// supports heap size limits via Isolate::CreateParams::constraints, but v8go's
	// NewIsolate() doesn't accept these parameters.
	//
	// Mitigations in place:
	// - Execution timeout (200ms) limits how long scripts can allocate memory
	// - State size limit (maxStateSize = 1MB) bounds persistent state
	// - Isolate pool (NumCPU isolates) limits concurrent memory pressure
	//
	// Risks accepted:
	// - A malicious script could allocate large amounts of memory within the timeout
	// - Memory is only reclaimed when the isolate is garbage collected
	// - We rely on the OS/container limits as the ultimate backstop
	//
	// Potential future fixes:
	// - Fork v8go to expose ResourceConstraints
	// - Switch to a different JS engine with memory limits (goja, otto)
	// - Use cgroups/containers to enforce memory limits externally
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

// Callbacks maps function names to Go functions that can be called from JavaScript.
type Callbacks map[string]func(rc *RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value

// Target holds all inputs needed to run JavaScript for an object.
type Target struct {
	Source    string    // JavaScript source code
	Origin    string    // Source file path for stack traces
	State     string    // JSON state persisted between runs
	Callbacks Callbacks // Go functions exposed to JavaScript
	Console   io.Writer // Where console.log output goes
}

// Result contains the outputs from a JavaScript execution.
type Result struct {
	State     string                     // JSON state to persist for next run
	Callbacks map[string]map[string]bool // event type -> set of tags the script wants to handle
	Value     *string                    // JSON return value from callback invocation, nil if no callback was invoked
}

// RunContext provides the execution environment for a single JavaScript run.
// It is passed to Go callbacks so they can interact with the V8 context.
type RunContext struct {
	m         *machine
	r         *Result
	t         *Target
	callbacks map[string]*v8go.Function // JS callbacks registered via addCallback()
	remaining time.Duration             // remaining JS execution budget
}

// JSFromGo converts a Go value to a V8 value by JSON marshaling and parsing.
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

// Copy converts a V8 value to a Go value by JSON stringifying and unmarshaling into dst.
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

// Context returns the V8 context for this run.
func (rc *RunContext) Context() *v8go.Context {
	return rc.m.vctx
}

// String creates a V8 string value from a Go string.
func (rc *RunContext) String(s string) *v8go.Value {
	if res, err := v8go.NewValue(rc.m.iso, s); err == nil {
		return res
	}
	return rc.m.unableToGenerateString
}

// Throw throws a JavaScript exception with the given formatted message.
func (rc *RunContext) Throw(format string, args ...any) *v8go.Value {
	return rc.Context().Isolate().ThrowException(rc.String(fmt.Sprintf(format, args...)))
}

// Null returns a JavaScript null value.
func (rc *RunContext) Null() *v8go.Value {
	return v8go.Null(rc.m.iso)
}

// addJSCallback is the Go implementation of the JavaScript addCallback(eventType, tags, fn) function.
// It registers a JS function to be called when an event with matching type and tag occurs.
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

// removeJSCallback is the Go implementation of the JavaScript removeCallback(eventType) function.
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

// logFunc returns a callback that implements the JavaScript log() function.
// Objects are JSON-stringified for readability.
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

// addCallback registers a Go function as a global JavaScript function.
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

// prepareV8Context sets up the V8 context with all callbacks and initial state.
func (rc *RunContext) prepareV8Context() error {
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
	stateValue, err := v8go.JSONParse(rc.m.vctx, stateJSON)
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

type timedResult struct {
	value   *v8go.Value
	err     error
	elapsed time.Duration
}

// withTimeout runs f with the remaining JS execution budget, respecting context cancellation.
func (rc *RunContext) withTimeout(ctx context.Context, f func() (*v8go.Value, error)) (*v8go.Value, error) {
	if rc.remaining <= 0 {
		return nil, juicemud.WithStack(ErrTimeout)
	}

	results := make(chan timedResult, 1)
	go func() {
		start := time.Now()
		val, err := f()
		results <- timedResult{value: val, err: err, elapsed: time.Since(start)}
	}()

	select {
	case res := <-results:
		rc.remaining -= res.elapsed
		return res.value, juicemud.WithStack(res.err)
	case <-ctx.Done():
		rc.m.iso.TerminateExecution()
		return nil, juicemud.WithStack(ctx.Err())
	case <-time.After(rc.remaining):
		rc.m.iso.TerminateExecution()
		return nil, juicemud.WithStack(ErrTimeout)
	}
}

// Run executes the JavaScript source, optionally invoking a callback if caller is provided.
// Uses a pool of V8 isolates and enforces the given timeout for JS execution time only.
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
		remaining: timeout,
	}

	if err := rc.prepareV8Context(); err != nil {
		return nil, juicemud.WithStack(err)
	}

	if _, err := rc.withTimeout(ctx, func() (*v8go.Value, error) {
		return rc.m.vctx.RunScript(t.Source, t.Origin)
	}); err != nil {
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
		if val, err = v8go.JSONParse(rc.m.vctx, call.Message); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}

	if val, err := rc.withTimeout(ctx, func() (*v8go.Value, error) {
		if val != nil {
			return jsCB.Call(rc.m.vctx.Global(), val)
		} else {
			return jsCB.Call(rc.m.vctx.Global())
		}
	}); err != nil {
		return nil, juicemud.WithStack(err)
	} else {
		return rc.collectResult(val)
	}
}

var (
	ErrStateTooLarge = fmt.Errorf("state exceeds maximum size of %d bytes", maxStateSize)
)

// collectResult extracts the final state and return value from the V8 context.
// A non-nil value parameter indicates a callback was actually invoked (as opposed
// to just running the source code for initialization/refresh).
func (rc *RunContext) collectResult(value *v8go.Value) (*Result, error) {
	if value != nil {
		str, err := v8go.JSONStringify(rc.m.vctx, value)
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
		rc.r.Value = &str
	}
	stateValue, err := rc.m.vctx.Global().Get(stateName)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	if rc.r.State, err = v8go.JSONStringify(rc.m.vctx, stateValue); err != nil {
		return nil, juicemud.WithStack(err)
	}
	if len(rc.r.State) > maxStateSize {
		return nil, juicemud.WithStack(ErrStateTooLarge)
	}
	return rc.r, nil
}
