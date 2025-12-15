package storage

import (
	"log"
	"testing"

	"github.com/bxcodec/faker/v4"
	"github.com/bxcodec/faker/v4/pkg/options"
	"github.com/zond/juicemud/structs"
	"rogchap.com/v8go"

	goccy "github.com/goccy/go-json"
)

var (
	fakeObject structs.Object
)

func init() {
	err := faker.FakeData(&fakeObject, options.WithRandomMapAndSliceMaxSize(10))
	if err != nil {
		log.Panic(err)
	}
}

func BenchmarkV8JSON(b *testing.B) {
	b.StopTimer()
	iso := v8go.NewIsolate()
	ctx := v8go.NewContext(iso)
	by, err := goccy.Marshal(&fakeObject)
	if err != nil {
		b.Fatal(err)
	}
	js := string(by)
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		o, err := v8go.JSONParse(ctx, js)
		if err != nil {
			b.Fatal(err)
		}
		ser, err := v8go.JSONStringify(ctx, o)
		if err != nil {
			b.Fatal(err)
		}
		js = ser
	}
}

func BenchmarkBenc(b *testing.B) {
	o := &structs.Object{}
	for i := 0; i < b.N; i++ {
		by := make([]byte, fakeObject.Size())
		fakeObject.Marshal(by)
		if err := o.Unmarshal(by); err != nil {
			b.Fatal(err)
		}
	}

}

func BenchmarkGoccy(b *testing.B) {
	o := &structs.Object{}
	for i := 0; i < b.N; i++ {
		by, err := goccy.Marshal(&fakeObject)
		if err != nil {
			b.Fatal(err)
		}
		if err := goccy.Unmarshal(by, o); err != nil {
			b.Fatal(err)
		}
	}
}
