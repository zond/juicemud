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
	unableToGenerateString *v8go.Value
}

func newMachine() (*machine, error) {
	m := &machine{
		iso: v8go.NewIsolate(),
	}
	var err error
	if m.unableToGenerateString, err = v8go.NewValue(m.iso, "unable to generate exception"); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return m, nil
}

type Target struct {
	Source    string
	Origin    string
	State     string
	Callbacks map[string]func(rc *RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value
}

type Result struct {
	State     string
	Callbacks []string
}

type RunContext struct {
	m         *machine
	vctx      *v8go.Context
	t         *Target
	callbacks map[string]*v8go.Function
}

func (rc *RunContext) Context() *v8go.Context {
	return rc.vctx
}

func (rc *RunContext) String(s string) *v8go.Value {
	if res, err := v8go.NewValue(rc.m.iso, s); err == nil {
		return res
	}
	return rc.m.unableToGenerateString
}

func addJSCallback(rc *RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
	args := info.Args()
	if len(args) == 2 && args[0].IsString() && args[1].IsFunction() {
		eventType := args[0].String()
		fun, err := args[1].AsFunction()
		if err != nil {
			return rc.Context().Isolate().ThrowException(rc.String("unable to cast callback to *v8go.Function"))
		}
		rc.callbacks[eventType] = fun
		return nil
	}
	return rc.Context().Isolate().ThrowException(rc.String("addEventListener takes [string, function] arguments"))
}

func removeJSCallback(rc *RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value {
	args := info.Args()
	if len(args) == 1 && args[0].IsString() {
		delete(rc.callbacks, args[0].String())
		return nil
	}
	return rc.Context().Isolate().ThrowException(rc.String("removeEventListener takes [string] arguments"))
}

func (rc *RunContext) addCallback(
	tmpl *v8go.ObjectTemplate,
	name string,
	f func(*RunContext, *v8go.FunctionCallbackInfo) *v8go.Value,
) error {
	return juicemud.WithStack(
		tmpl.Set(
			name,
			v8go.NewFunctionTemplate(
				rc.m.iso,
				func(info *v8go.FunctionCallbackInfo) *v8go.Value {
					return f(rc, info)
				},
			),
			v8go.ReadOnly,
		),
	)
}

func (rc *RunContext) createV8Context(iso *v8go.Isolate, timeout *time.Duration) (*v8go.Context, error) {
	globalTemplate := v8go.NewObjectTemplate(iso)

	for name, fun := range rc.t.Callbacks {
		if err := rc.addCallback(
			globalTemplate,
			name,
			fun,
		); err != nil {
			return nil, juicemud.WithStack(err)
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
		if err := rc.addCallback(globalTemplate, cb.name, cb.fun); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}

	vctx := v8go.NewContext(rc.m.iso, globalTemplate)

	stateJSON := rc.t.State
	if stateJSON == "" {
		stateJSON = "{}"
	}
	startTime := time.Now()
	stateValue, err := v8go.JSONParse(vctx, stateJSON)
	*timeout -= time.Since(startTime)
	if err != nil {
		vctx.Close()
		return nil, juicemud.WithStack(err)
	}
	if err := vctx.Global().Set(stateName, stateValue); err != nil {
		vctx.Close()
		return nil, juicemud.WithStack(err)
	}

	return vctx, nil
}

var (
	ErrTimeout = fmt.Errorf("Timeout")
)

func (rc *RunContext) withTimeout(_ context.Context, f func() error, timeout *time.Duration) error {
	errs := make(chan error, 1)
	go func() {
		t := time.Now()
		errs <- f()
		*timeout -= time.Since(t)
	}()

	select {
	case err := <-errs:
		return juicemud.WithStack(err)
	case <-time.After(*timeout):
		rc.m.iso.TerminateExecution()
		return juicemud.WithStack(ErrTimeout)
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

	vctx, err := rc.createV8Context(m.iso, &timeout)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	defer vctx.Close()

	rc.vctx = vctx

	if err := rc.withTimeout(ctx, func() error {
		_, err := vctx.RunScript(t.Source, t.Origin)
		return err
	}, &timeout); err != nil {
		return nil, juicemud.WithStack(err)
	}

	jsCB, found := rc.callbacks[callbackName]
	if !found {
		return collectResult(rc)
	}

	var val *v8go.Value
	if message != "" {
		var err error
		start := time.Now()
		if val, err = v8go.JSONParse(rc.vctx, message); err != nil {
			return nil, juicemud.WithStack(err)
		}
		timeout -= time.Since(start)
	}

	if err := rc.withTimeout(ctx, func() error {
		var err error
		if val != nil {
			_, err = jsCB.Call(rc.vctx.Global(), val)
		} else {
			_, err = jsCB.Call(rc.vctx.Global())
		}
		return juicemud.WithStack(err)
	}, &timeout); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return collectResult(rc)
}

func collectResult(rc *RunContext) (*Result, error) {
	result := &Result{}
	stateValue, err := rc.vctx.Global().Get(stateName)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	if result.State, err = v8go.JSONStringify(rc.vctx, stateValue); err != nil {
		return nil, juicemud.WithStack(err)
	}
	for name := range rc.callbacks {
		result.Callbacks = append(result.Callbacks, name)
	}
	return result, nil
}

func Log(w io.Writer) func(*RunContext, *v8go.FunctionCallbackInfo) *v8go.Value {
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
