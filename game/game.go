package game

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/gliderlabs/ssh"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/storage"
	"golang.org/x/term"
)

const (
	userSource    = "/user.js"
	genesisSource = "/genesis.js"
)

var (
	initialContent = map[string]string{
		userSource:    "// This code runs all connected users.",
		genesisSource: "// This code runs the room where newly created users are dropped.",
	}
)

type Game struct {
	storage *storage.Storage
}

func New(ctx context.Context, s *storage.Storage) (*Game, error) {
	for _, path := range []string{userSource, genesisSource} {
		if _, created, err := s.EnsureFile(ctx, path); err != nil {
			return nil, juicemud.WithStack(err)
		} else if created {
			if err := s.SetSource(ctx, path, []byte(initialContent[path])); err != nil {
				return nil, juicemud.WithStack(err)
			}
		}
	}
	return &Game{
		storage: s,
	}, nil
}

type contextKey int

var (
	gameContextKey contextKey = 0
)

func GetGame(ctx context.Context) (*Game, error) {
	contextValue := ctx.Value(gameContextKey)
	if contextValue == nil {
		return nil, errors.New("context doesn't contain a game instance")
	}
	if game, ok := contextValue.(*Game); ok {
		return game, nil
	}
	return nil, errors.Errorf("context value at game key %v isn't a game instance", contextValue)
}

func (g *Game) HandleSession(sess ssh.Session) {
	sess.Context().SetValue(gameContextKey, g)
	env := &Env{
		game: g,
		term: term.NewTerminal(sess, "> "),
		sess: sess,
	}
	if err := env.Connect(); err != nil {
		if !errors.Is(err, io.EOF) {
			fmt.Fprintf(env.term, "InternalServerError: %v\n", err)
			log.Println(err)
			log.Println(juicemud.StackTrace(err))
		}
	}
}
