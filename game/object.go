package game

import (
	"fmt"

	"github.com/timshannon/badgerhold"
	"github.com/zond/gojuice/machine"
)

type Object struct {
	ID         uint64 `badgerhold:"key"`
	LocationID uint64 `badgerhold:"index"`

	runtime *machine.Runtime
}

type Objects []Object

func (o *Object) call(name string, args ...interface{}) (interface{}, error) {
	if o == nil {
		return nil, fmt.Errorf("void is uncallable")
	}
	if o.runtime == nil {
		return nil, fmt.Errorf("%v missing runtime", o.ID)
	}
	res, err := o.runtime.Call(name, args...)
	if err != nil {
		return nil, fmt.Errorf("[%s].%v(%+v): %v", o.ID, args, err)
	}
	return res, nil
}

func (o *Object) Content(db *badgerhold.Store) (Objects, error) {
	id := uint64(0)
	if o != nil {
		id = o.ID
	}
	res := Objects{}
	if err := db.Find(&res, badgerhold.Where("LocationID").Eq(id).Index("LocationID")); err != nil {
		return nil, err
	}
	return res, nil
}

func (o *Object) Name() (string, error) {
	if o == nil {
		return "void", nil
	}
	res, err := o.call("name")
	if err != nil {
		return "", err
	}
	return fmt.Sprint(res), nil
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
