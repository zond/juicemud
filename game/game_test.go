package game

import (
	"context"
	"os"
	"testing"

	"github.com/zond/juicemud/js"
	"github.com/zond/juicemud/storage"
)

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
			if err := g.loadRunSave(ctx, user.Object, &js.Call{}); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
	})
}
