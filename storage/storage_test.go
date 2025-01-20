package storage

import (
	"log"
	"testing"

	"github.com/bxcodec/faker/v4"
	"github.com/bxcodec/faker/v4/pkg/options"
	"github.com/sugawarayuuta/sonnet"
	"github.com/zond/juicemud/structs"
	"rogchap.com/v8go"

	goccy "github.com/goccy/go-json"
)

var (
	fakeObjectJSON []byte
)

func init() {
	fakeObject := &structs.Object{}
	err := faker.FakeData(fakeObject, options.WithRandomMapAndSliceMaxSize(10))
	if err != nil {
		log.Panic(err)
	}
	if fakeObjectJSON, err = goccy.Marshal(fakeObject); err != nil {
		log.Panic(err)
	}
}

func BenchmarkV8JSON(b *testing.B) {
	b.StopTimer()
	iso := v8go.NewIsolate()
	ctx := v8go.NewContext(iso)
	b.StartTimer()
	js := string(fakeObjectJSON)
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

func BenchmarkSonnet(b *testing.B) {
	b.StopTimer()
	o := &structs.Object{}
	js := fakeObjectJSON
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		if err := sonnet.Unmarshal(js, o); err != nil {
			b.Fatal(err)
		}
		by, err := sonnet.Marshal(o)
		if err != nil {
			b.Fatal(err)
		}
		js = by
	}
}

func BenchmarkGoccy(b *testing.B) {
	b.StopTimer()
	o := &structs.Object{}
	js := fakeObjectJSON
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		if err := goccy.Unmarshal(js, o); err != nil {
			b.Fatal(err)
		}
		by, err := goccy.Marshal(o)
		if err != nil {
			b.Fatal(err)
		}
		js = by
	}
}
