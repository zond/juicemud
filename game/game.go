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
	"github.com/zond/juicemud/structs"
	"golang.org/x/term"
)

const (
	connectedEventType = "connected"
)

const (
	emitEventTag = "emit"
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
			o.Id = genesisID
			return nil
		},
	}
)

type Game struct {
	storage *storage.Storage
	queue   *storage.Queue
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
		if err := s.EnsureObject(ctx, idString, setup); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
	result := &Game{
		storage: s,
	}
	var err error
	result.queue = s.Queue(ctx, func(ctx context.Context, ev *storage.Event) {
		go func() {
			if result.loadAndRun(ctx, ev.Object, ev.Call); err != nil {
				log.Printf("trying to execute %+v: %v", ev, err)
			}
		}()
	})
	go func() {
		log.Panic(result.queue.Start(ctx))
	}()
	return result, nil
}

func (g *Game) HandleSession(sess ssh.Session) {
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
	object.Location = genesisID

	user.Object = object.Id
	if err := g.storage.SetUser(ctx, user, false); err != nil {
		return juicemud.WithStack(err)
	}
	if err := g.storage.SetObject(ctx, nil, object); err != nil {
		return juicemud.WithStack(err)
	}
	return nil
}
