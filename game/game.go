package game

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/gliderlabs/ssh"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/storage"
	"github.com/zond/juicemud/structs"
	"golang.org/x/term"

	goccy "github.com/goccy/go-json"
)

const (
	userSource    = "/user.js"
	genesisSource = "/genesis.js"
)

const (
	genesisID = "genesis"
)

var (
	initialSources = map[string]string{
		userSource:    "// This code runs all connected users.",
		genesisSource: "// This code runs the room where newly created users are dropped.",
	}
	initialObjects = map[string]func(*structs.Object) error{
		genesisID: func(o *structs.Object) error {
			o.Id = []byte(genesisID)
			return nil
		},
	}
)

type Game struct {
	storage *storage.Storage
	queue   *storage.Queue
}

type event struct {
	ID        []byte
	EventType string
	Message   string
}

func (g *Game) enqueueAt(ctx context.Context, ev event, t time.Time) error {
	b, err := goccy.Marshal(ev)
	if err != nil {
		return juicemud.WithStack(err)
	}
	return g.queue.Push(ctx, g.queue.At(t), b)
}

func (g *Game) enqueueAfter(ctx context.Context, ev event, d time.Duration) error {
	b, err := goccy.Marshal(ev)
	if err != nil {
		return juicemud.WithStack(err)
	}
	return g.queue.Push(ctx, g.queue.After(d), b)
}

func New(ctx context.Context, s *storage.Storage) (*Game, error) {
	for path, source := range initialSources {
		if _, created, err := s.EnsureFile(ctx, path); err != nil {
			return nil, juicemud.WithStack(err)
		} else if created {
			if err := s.SetSource(ctx, path, []byte(source)); err != nil {
				return nil, juicemud.WithStack(err)
			}
		}
	}
	for idString, setup := range initialObjects {
		if err := s.EnsureObject(ctx, []byte(idString), setup); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
	result := &Game{
		storage: s,
	}
	var err error
	result.queue = s.Queue(ctx, func(ctx context.Context, b []byte) {
		go func() {
			ev := &event{}
			if err := goccy.Unmarshal(b, ev); err != nil {
				log.Panic(err)
			}
			if loadAndCall(ctx, ev.ID, ev.EventType, ev.Message); err != nil {
				log.Printf("trying to call %+v.%v(%q): %v", ev.ID, ev.EventType, ev.Message, err)
			}
		}()
	})
	go func() {
		log.Panic(result.queue.Start(context.WithValue(ctx, gameContextKey, result)))
	}()
	return result, nil
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

func (g *Game) createUser(ctx context.Context, user *storage.User) error {
	object, err := structs.MakeObject(ctx)
	if err != nil {
		return juicemud.WithStack(err)
	}
	object.SourcePath = userSource
	object.Location = []byte(genesisID)
	if err := call(ctx, object, "", ""); err != nil {
		return juicemud.WithStack(err)
	}

	user.Object = object.Id
	if err := g.storage.SetUser(ctx, user, false); err != nil {
		return juicemud.WithStack(err)
	}
	if err := g.storage.SetObject(ctx, nil, object); err != nil {
		return juicemud.WithStack(err)
	}
	return nil
}
