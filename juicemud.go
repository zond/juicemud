package juicemud

import (
	"bytes"
	"fmt"

	"github.com/pkg/errors"
)

const (
	DAVAuthRealm = "WebDAV"
)

type stackTracer interface {
	StackTrace() errors.StackTrace
}

func WithStack(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(stackTracer); !ok {
		return errors.WithStack(err)
	}
	return err
}

func StackTrace(err error) string {
	buf := &bytes.Buffer{}
	if err, ok := err.(stackTracer); ok {
		for _, f := range err.StackTrace() {
			fmt.Fprintf(buf, "%+v\n", f)
		}
	}
	return buf.String()
}
