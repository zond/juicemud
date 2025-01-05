package js

import (
	"context"
	"log"
	"time"

	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"rogchap.com/v8go"
)

const (
	stateName = "state"
)

type Context struct {
	state         string
	subscriptions map[string][]*v8go.Function
	v8Context     *v8go.Context
}

func (c *Context) State() string {
	return c.state
}

func (c *Context) Subscriptions() []string {
	result := make([]string, 0, len(c.subscriptions))
	for eventType := range c.subscriptions {
		result = append(result, eventType)
	}
	return result
}

func (c *Context) Notify(ctx context.Context, eventType string, content string) error {
	var val *v8go.Value
	if content != "" {
		var err error
		if val, err = v8go.JSONParse(c.v8Context, content); err != nil {
			return juicemud.WithStack(err)
		}
	}
	for _, callback := range c.subscriptions[eventType] {
		if err := c.withTimeout(ctx, func() error {
			var err error
			if val != nil {
				_, err = callback.Call(c.v8Context.Global(), val)
			} else {
				_, err = callback.Call(c.v8Context.Global())
			}
			return juicemud.WithStack(err)
		}, 200*time.Millisecond); err != nil {
			return juicemud.WithStack(err)
		}
	}
	return nil
}

func (c *Context) setup() error {
	iso := v8go.NewIsolate()
	globalTemplate := v8go.NewObjectTemplate(iso)

	invalidArguments, err := v8go.NewValue(iso, "invalid arguments")
	if err != nil {
		return juicemud.WithStack(err)
	}

	if err := globalTemplate.Set(
		"addEventListener",
		v8go.NewFunctionTemplate(
			iso,
			func(info *v8go.FunctionCallbackInfo) *v8go.Value {
				args := info.Args()
				if len(args) == 2 && args[0].IsString() && args[1].IsFunction() {
					eventType := args[0].String()
					fun, err := args[1].AsFunction()
					if err != nil {
						log.Panic(err)
					}
					c.subscriptions[eventType] = append(c.subscriptions[eventType], fun)
					return nil
				}
				return iso.ThrowException(invalidArguments)
			}),
		v8go.ReadOnly,
	); err != nil {
		return juicemud.WithStack(err)
	}
	if err := globalTemplate.Set(
		"removeEventListener",
		v8go.NewFunctionTemplate(
			iso,
			func(info *v8go.FunctionCallbackInfo) *v8go.Value {
				args := info.Args()
				if len(args) == 1 && args[0].IsString() {
					delete(c.subscriptions, args[0].String())
					return nil
				} else if len(args) == 2 && args[0].IsString() && args[1].IsFunction() {
					eventType := args[0].String()
					fun, err := args[1].AsFunction()
					if err != nil {
						log.Panic(err)
					}
					newSubs := []*v8go.Function{}
					for _, sub := range c.subscriptions[eventType] {
						if sub != fun {
							newSubs = append(newSubs, sub)
						}
					}
					c.subscriptions[eventType] = newSubs
					return nil
				}
				return iso.ThrowException(invalidArguments)
			},
		),
		v8go.ReadOnly,
	); err != nil {
		return juicemud.WithStack(err)
	}

	c.v8Context = v8go.NewContext(iso, globalTemplate)

	state := c.state
	if state == "" {
		state = "{}"
	}
	stateValue, err := v8go.JSONParse(c.v8Context, state)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if err := c.v8Context.Global().Set(stateName, stateValue); err != nil {
		return juicemud.WithStack(err)
	}

	return nil
}

func NewContext(state string) (*Context, error) {
	result := &Context{
		state:         state,
		subscriptions: map[string][]*v8go.Function{},
	}
	if err := result.setup(); err != nil {
		result.Close()
		return nil, juicemud.WithStack(err)
	}
	return result, nil
}

func (c *Context) Close() {
	if c.v8Context != nil {
		defer c.v8Context.Isolate().Dispose()
		c.v8Context.Close()
	}
}

var (
	TimeoutErr = errors.New("Timeout")
)

func (c *Context) withTimeout(ctx context.Context, f func() error, timeout time.Duration) error {
	errs := make(chan error, 1)
	go func() {
		errs <- f()
	}()

	select {
	case err := <-errs:
		return juicemud.WithStack(err)
	case <-time.After(timeout):
		vm := c.v8Context.Isolate()
		vm.TerminateExecution()
		return juicemud.WithStack(TimeoutErr)
	}
}

func (c *Context) Run(ctx context.Context, source string, origin string, timeout time.Duration) error {
	defer c.collectState()
	return c.withTimeout(ctx, func() error {
		_, err := c.v8Context.RunScript(source, origin)
		return err
	}, timeout)
}

func (c *Context) collectState() error {
	stateValue, err := c.v8Context.Global().Get(stateName)
	if err != nil {
		return juicemud.WithStack(err)
	}
	newState, err := v8go.JSONStringify(c.v8Context, stateValue)
	if err != nil {
		return juicemud.WithStack(err)
	}
	c.state = newState
	return nil
}
