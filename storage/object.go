package storage

import (
	"capnproto.org/go/capnp/v3"
	"github.com/zond/juicemud/glue"
)

type ObjectHelper struct {
	*Object
}

func OH(o *Object) ObjectHelper {
	return ObjectHelper{o}
}

func (o ObjectHelper) Content() glue.ListHelper[[]byte, capnp.DataList] {
	return glue.ListHelper[[]byte, capnp.DataList]{
		Get: o.Object.Content,
		New: o.Object.NewContent,
	}
}

func (o ObjectHelper) Callbacks() glue.ListHelper[string, capnp.TextList] {
	return glue.ListHelper[string, capnp.TextList]{
		Get: o.Object.Callbacks,
		New: o.Object.NewCallbacks,
	}
}
