package game

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/gliderlabs/ssh"
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
	root          = "/"
	userSource    = "/user.js"
	genesisSource = "/genesis.js"
	bootSource    = "/boot.js"
	emptySource   = "/empty.js"
)

const (
	genesisID = "genesis"
	emptyID   = ""
)

const (
	wizardsGroup = "wizards"
)

var (
	initialDirectories = []string{
		root,
	}
	initialSources = map[string]string{
		bootSource: "// This code is run each time the game server starts.",
		userSource: `// This code runs all connected users.

addCallback('connected', ['emit'], (obj) => {
    state.username = obj.username;
    state.object = obj.object;
	setDescriptions([
		{
			short: obj.username,
		}
	]);
});
`,
		genesisSource: `// This code runs the room where newly created users are dropped.
setDescriptions([
  {
		short: 'Black cosmos',
		long: 'This is the darkness of space before creation. No stars twinkle.',
  },
]);
`,
		emptySource: "// This code runs the top level container of all content.",
	}
	initialObjects = map[string]func(*structs.Object) error{
		genesisID: func(o *structs.Object) error {
			o.Id = genesisID
			o.Location = emptyID
			o.SourcePath = genesisSource
			return nil
		},
		emptyID: func(o *structs.Object) error {
			o.Id = emptyID
			o.Location = emptyID
			o.Content = map[string]bool{genesisID: true}
			o.SourcePath = emptySource
			return nil
		},
	}
	initialGroups = []storage.Group{
		{
			Name: wizardsGroup,
		},
	}
)

type Game struct {
	storage *storage.Storage
}

func New(ctx context.Context, s *storage.Storage) (*Game, error) {
	ctx = juicemud.MakeMainContext(ctx)
	for _, dir := range initialDirectories {
		if err := s.CreateDir(ctx, dir); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
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
	for _, group := range initialGroups {
		if _, err := s.EnsureGroup(ctx, &group); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
	g := &Game{
		storage: s,
	}
	go func() {
		log.Panic(g.storage.StartQueue(ctx, func(ctx context.Context, ev *structs.Event) {
			var call structs.Caller
			if ev.Call.Name != "" {
				call = &ev.Call
			}
			go func() {
				if err := g.loadRunSave(ctx, ev.Object, call); err != nil {
					log.Printf("trying to execute %+v: %v", ev, err)
				}
			}()
		}, g.emitMovement))
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

func (g *Game) createObject(ctx context.Context, f func(*structs.Object) error) error {
	object, err := structs.MakeObject(ctx)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if err := f(object); err != nil {
		return juicemud.WithStack(err)
	}

	if err := g.storage.StoreObject(ctx, nil, object); err != nil {
		return juicemud.WithStack(err)
	}
	return nil
}

func (g *Game) createUser(ctx context.Context, user *storage.User) error {
	return juicemud.WithStack(g.createObject(ctx, func(object *structs.Object) error {
		object.SourcePath = userSource
		object.Location = genesisID
		user.Object = object.Id
		return juicemud.WithStack(g.storage.StoreUser(ctx, user, false))
	}))
}
