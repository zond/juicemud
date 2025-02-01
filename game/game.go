package game

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/gliderlabs/ssh"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/js"
	"github.com/zond/juicemud/storage"
	"github.com/zond/juicemud/structs"
	"golang.org/x/term"
)

const (
	connectedEventType = "connected"
	movementEventType  = "movement"
)

const (
	emitEventTag = "emit"
)

const (
	userSource    = "/user.js"
	genesisSource = "/genesis.js"
	bootSource    = "/boot.js"
)

const (
	genesisID = "genesis"
)

var (
	initialSources = map[string]string{
		bootSource: "// This code is run each time the game server starts.",
		userSource: "// This code runs all connected users.",
		genesisSource: `// This code runs the room where newly created users are dropped.
setDescriptions([
  {
		short: 'Black cosmos',
		long: 'This is the darkness of space before creation. No stars twinkle.',
  },
]);
`,
	}
	initialObjects = map[string]func(*structs.Object) error{
		genesisID: func(o *structs.Object) error {
			o.Id = genesisID
			o.SourcePath = genesisSource
			return nil
		},
	}
)

type Game struct {
	storage *storage.Storage
}

func New(ctx context.Context, s *storage.Storage) (*Game, error) {
	for path, source := range initialSources {
		if _, created, err := s.EnsureFile(ctx, path); err != nil {
			return nil, juicemud.WithStack(err)
		} else if created {
			if err := s.StoreSource(ctx, path, []byte(source)); err != nil {
				return nil, juicemud.WithStack(err)
			}
		}
	}
	for idString, setup := range initialObjects {
		if err := s.EnsureObject(ctx, idString, setup); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
	g := &Game{
		storage: s,
	}
	go func() {
		log.Panic(g.storage.StartQueue(ctx, func(ctx context.Context, ev *structs.Event) {
			var call *structs.Call
			if ev.Call.Name != "" {
				call = &ev.Call
			}
			go func() {
				if err := g.loadRunSave(ctx, ev.Object, call); err != nil {
					log.Printf("trying to execute %+v: %v", ev, err)
				}
			}()
		}, g.emitMovementToNeighbourhood))
	}()
	bootJS, _, err := g.storage.LoadSource(ctx, bootSource)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	callbacks := js.Callbacks{}
	g.addGlobalCallbacks(ctx, callbacks)
	bootTarget := js.Target{
		Source:    string(bootJS),
		Origin:    bootSource,
		State:     "{}",
		Callbacks: callbacks,
		Console:   os.Stderr,
	}
	if _, err := bootTarget.Run(ctx, nil, time.Second); err != nil {
		log.Printf("trying to run %q: %v", bootSource, err)
		log.Println(juicemud.StackTrace(err))
		return g, nil
	}
	return g, nil
}

func (g *Game) HandleSession(sess ssh.Session) {
	env := &Connection{
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
	if err := g.storage.StoreUser(ctx, user, false); err != nil {
		return juicemud.WithStack(err)
	}
	if err := g.storage.StoreObject(ctx, nil, object); err != nil {
		return juicemud.WithStack(err)
	}
	return nil
}
