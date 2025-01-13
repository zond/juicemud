package game

import (
	"context"
	"os"
	"testing"

	"github.com/zond/juicemud/storage"
)

func withGame(b *testing.B, f func(*Game, context.Context)) {
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
	f(g, context.WithValue(ctx, gameContextKey, g))
}

func BenchmarkCall(b *testing.B) {
	b.StopTimer()
	withGame(b, func(g *Game, ctx context.Context) {
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
			if err := loadAndCall(ctx, user.Object, "", ""); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
	})
}
