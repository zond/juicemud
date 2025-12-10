package game

import (
	"context"
	"log"
	"os"
	"testing"

	"github.com/bxcodec/faker/v4"
	"github.com/bxcodec/faker/v4/pkg/options"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/storage"
	"github.com/zond/juicemud/structs"
)

func fakeObject(t testing.TB, g *Game) *structs.Object {
	res := &structs.Object{}
	err := faker.FakeData(res, options.WithRandomMapAndSliceMaxSize(10))
	if err != nil {
		log.Panic(err)
	}
	res.Unsafe.Id = juicemud.NextUniqueID()
	res.Unsafe.SourcePath = userSource
	res.Unsafe.Content = map[string]bool{}
	res.Unsafe.Location = genesisID
	res.Unsafe.State = "{}"
	res.Unsafe.Exits = nil
	if err := g.storage.CreateObject(context.Background(), res); err != nil {
		t.Fatal(err)
	}
	return res
}

func populate(t testing.TB, g *Game, obj *structs.Object, num int) []*structs.Object {
	res := []*structs.Object{}
	for range num {
		child := fakeObject(t, g)
		if err := g.moveObject(context.Background(), child, obj.Unsafe.Id); err != nil {
			t.Fatal(err)
		}
		res = append(res, child)
	}
	return res
}

func connect(t testing.TB, g *Game, obj1, obj2 *structs.Object) {
	obj1.Unsafe.Exits = append(obj1.Unsafe.Exits, structs.Exit{
		Destination: obj2.Unsafe.Id,
	})
	obj2.Unsafe.Exits = append(obj2.Unsafe.Exits, structs.Exit{
		Destination: obj1.Unsafe.Id,
	})
	if err := g.storage.CreateObject(context.Background(), obj1); err != nil {
		t.Fatal(err)
	}
	if err := g.storage.CreateObject(context.Background(), obj2); err != nil {
		t.Fatal(err)
	}
}

func withGame(b *testing.B, f func(*Game)) {
	b.Helper()
	tmpFile, err := os.CreateTemp("", "")
	if err != nil {
		b.Fatal(err)
	}
	tmpFile.Close()
	if err := os.Remove(tmpFile.Name()); err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(tmpFile.Name(), 0700); err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tmpFile.Name())
	ctx := context.Background()
	s, err := storage.New(ctx, tmpFile.Name())
	if err != nil {
		b.Fatal(err)
	}
	g, err := New(ctx, s)
	if err != nil {
		b.Fatal(err)
	}
	f(g)
}

func BenchmarkLoadNeighbourhood(b *testing.B) {
	b.StopTimer()
	withGame(b, func(g *Game) {
		container := fakeObject(b, g)
		pop := populate(b, g, container, 5)
		self := pop[0]
		neighbour1 := fakeObject(b, g)
		populate(b, g, neighbour1, 5)
		connect(b, g, self, neighbour1)
		neighbour2 := fakeObject(b, g)
		populate(b, g, neighbour2, 5)
		connect(b, g, self, neighbour2)
		neighbour3 := fakeObject(b, g)
		populate(b, g, neighbour3, 5)
		connect(b, g, self, neighbour3)
		neighbour4 := fakeObject(b, g)
		populate(b, g, neighbour4, 5)
		connect(b, g, self, neighbour4)
		b.StartTimer()
		for i := 0; i < b.N; i++ {
			if _, _, err := g.loadDeepNeighbourhoodOf(context.Background(), self.Unsafe.Id); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
	})
}

func BenchmarkCall(b *testing.B) {
	b.StopTimer()
	ctx := context.Background()
	withGame(b, func(g *Game) {
		obj, err := structs.MakeObject(ctx)
		if err != nil {
			b.Fatal(err)
		}
		obj.Unsafe.SourcePath = userSource
		obj.Unsafe.Location = genesisID
		b.StartTimer()
		for i := 0; i < b.N; i++ {
			if _, _, err := g.loadRun(ctx, obj.Unsafe.Id, &structs.AnyCall{
				Name:    connectedEventType,
				Tag:     emitEventTag,
				Content: map[string]any{},
			}); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
	})
}
