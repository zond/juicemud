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
	id, err := structs.NextObjectID()
	if err != nil {
		t.Fatal(err)
	}
	res.Id = id
	res.SourcePath = userSource
	res.Content = map[string]bool{}
	res.Location = genesisID
	res.State = "{}"
	res.Exits = nil
	if err := g.storage.SetObject(context.Background(), nil, res); err != nil {
		t.Fatal(err)
	}
	return res
}

func populate(t testing.TB, g *Game, obj *structs.Object, num int) []*structs.Object {
	res := []*structs.Object{}
	for i := 0; i < num; i++ {
		child := fakeObject(t, g)
		obj.Content[child.Id] = true
		child.Location = obj.Id
		prevLoc := genesisID
		if err := g.storage.SetObject(context.Background(), &prevLoc, child); err != nil {
			log.Print(juicemud.StackTrace(err))
			t.Fatal(err)
		}
		res = append(res, child)
	}
	return res
}

func connect(t testing.TB, g *Game, obj1, obj2 *structs.Object) {
	obj1.Exits = append(obj1.Exits, structs.Exit{
		Destination: obj2.Id,
	})
	obj2.Exits = append(obj2.Exits, structs.Exit{
		Destination: obj1.Id,
	})
	if err := g.storage.SetObject(context.Background(), nil, obj1); err != nil {
		t.Fatal(err)
	}
	if err := g.storage.SetObject(context.Background(), nil, obj2); err != nil {
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
		neighbour2 := fakeObject(b, g)
		populate(b, g, neighbour2, 5)
		neighbour3 := fakeObject(b, g)
		populate(b, g, neighbour3, 5)
		neighbour4 := fakeObject(b, g)
		populate(b, g, neighbour4, 5)
		b.StartTimer()
		for i := 0; i < b.N; i++ {
			if _, err := g.loadNeighbourhood(context.Background(), self); err != nil {
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
		user := &storage.User{
			Name:         "tester",
			PasswordHash: "blapp",
			Owner:        false,
		}
		if err := g.createUser(ctx, user); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		for i := 0; i < b.N; i++ {
			if err := g.loadRunSave(ctx, user.Object, nil); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
	})
}
