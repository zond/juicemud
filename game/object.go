package game

import (
	"fmt"

	"github.com/zond/gojuice/machine"
)

type Object struct {
	ID uint64 `badgerhold:"key"`

	runtime *machine.Runtime
}

func (o *Object) call(name string, args ...interface{}) (interface{}, error) {
	res, err := o.runtime.Call(name, args...)
	if err != nil {
		return nil, fmt.Errorf("[%s].%v(%+v): %v", o.ID, args, err)
	}
	return res, nil
}

func (o *Object) ShortDescription() (string, error) {
	if o == nil {
		return "The infinite void", nil
	}
	res, err := o.call("shortDescription")
	if err != nil {
		return "", err
	}
	return fmt.Sprint(res), nil
}

func (o *Object) LongDescription() (string, error) {
	if o == nil {
		return "Cosmic desolation disturbed only be the faint crackle of quantum foam.", nil
	}
	res, err := o.call("longDescription")
	if err != nil {
		return "", err
	}
	return fmt.Sprint(res), nil
}
