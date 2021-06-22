package storage

import (
	"fmt"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
	"github.com/timshannon/badgerhold/v2"
	"github.com/zond/gojuice/machine"
)

type Code struct {
	ID   uint64 `badgerhold:"key"`
	Text string
}

type object struct {
	ID         uint64 `badgerhold:"key"`
	LocationID uint64 `badgerhold:"index"`
	CodeID     uint64 `badgerhold:"index"`
	State      map[string]interface{}

	storage *Storage
	runtime *machine.Runtime
}

func (o *object) Location() (Object, error) {
	return o.storage.GetObject(o.LocationID)
}

func (o *object) reload() error {
	code := &Code{}
	if err := o.storage.db.Get(o.CodeID, code); err != nil {
		return err
	}
	ast, err := js.Parse(parse.NewInputString(code.Text))
	if err != nil {
		return err
	}
	o.runtime = machine.New().NewRuntime()
	return o.runtime.Run(ast)
}

func (o *object) UID() uint64 {
	return o.ID
}

func (o *object) call(name string, args ...interface{}) (interface{}, error) {
	if o == nil {
		return nil, fmt.Errorf("void is uncallable")
	}
	if o.runtime == nil {
		return nil, fmt.Errorf("%v missing runtime", o.ID)
	}
	res, err := o.runtime.Call(name, args...)
	if err != nil {
		return nil, fmt.Errorf("[%v].%v(%+v): %v", o.ID, name, args, err)
	}
	return res, nil
}

func (o *object) Content() (Objects, error) {
	objs := []object{}
	err := o.storage.db.Find(&objs, badgerhold.Where("LocationID").Eq(o.ID).Index("LocationID"))
	if err != nil {
		return nil, err
	}
	res := make(Objects, len(objs))
	for idx := range res {
		if res[idx], err = o.storage.GetObject(objs[idx].ID); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func (o *object) Tags() ([]string, error) {
	tags, err := o.call("tags")
	if err != nil {
		return nil, err
	}
	switch v := tags.(type) {
	case []interface{}:
		res := make([]string, len(v))
		for idx := range res {
			res[idx] = fmt.Sprint(v[idx])
		}
		return res, nil
	}
	return nil, fmt.Errorf("%#v.tags() didn't return an array, it returned %#v", o, tags)
}

func (o *object) Name(definite bool) (string, error) {
	res, err := o.call("name", definite)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(res), nil
}

func (o *object) ShortDescription() (string, error) {
	res, err := o.call("shortDescription")
	if err != nil {
		return "", err
	}
	return fmt.Sprint(res), nil
}

func (o *object) LongDescription() (string, error) {
	res, err := o.call("longDescription")
	if err != nil {
		return "", err
	}
	return fmt.Sprint(res), nil
}
