package game

import (
	"context"

	"github.com/zond/juicemud"
	"github.com/zond/juicemud/storage"
	"rogchap.com/v8go"
)

type Object struct {
	storage.Object
}

func (o *Object) Emit(ctx context.Context, eventType string, message string) error {
	return js.WithIsolate(ctx, func(iso *v8go.Isolate) error {
		if err != nil {
			return juicemud.WithStack(err)
		}
	})

}
