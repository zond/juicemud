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
	createdEventType   = "created"
)

const (
	// Tag for events that are game infrastructure.
	emitEventTag = "emit"
	// Tag for events that are commands from a player to their object.
	commandEventTag = "command"
	// Tag for events that are actions an object takes on other objects.
	actionEventTag = "action"
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
			Short: obj.username,
			Unique: true,
		}
	]);
	setLearning(true);
});
`,
		genesisSource: `// This code runs the room where newly created users are dropped.
setDescriptions([
  {
		Short: 'Black cosmos',
		Unique: true,
		Long: 'This is the darkness of space before creation. No stars twinkle.',
  },
]);
`,
		emptySource: "// This code runs the top level container of all content.",
	}
	initialGroups = []storage.Group{
		{
			Name: wizardsGroup,
		},
	}
)

func initialObjects() map[string]*structs.ObjectDO {
	return map[string]*structs.ObjectDO{
		genesisID: {
			Id:         genesisID,
			Location:   emptyID,
			SourcePath: genesisSource,
		},
		emptyID: {
			Id:         emptyID,
			Location:   emptyID,
			Content:    map[string]bool{genesisID: true},
			SourcePath: emptySource,
		},
	}
}

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
	for _, obj := range initialObjects() {
		o := &structs.Object{Unsafe: obj}
		o.Unsafe.PostUnmarshal()
		if err := s.UNSAFEEnsureObject(ctx, o); err != nil {
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
		if err := g.storage.StartObjects(ctx); err != nil {
			log.Panic(err)
		}
	}()
	go func() {
		if err := g.storage.Queue().Start(ctx, func(ctx context.Context, ev *structs.Event) {
			var call structs.Caller
			if ev.Call.Name != "" {
				call = &ev.Call
			}
			go func() {
				if _, _, err := g.loadRun(ctx, ev.Object, call); err != nil {
					log.Printf("trying to execute %+v: %v", ev, err)
					log.Printf("%v", juicemud.StackTrace(err))
				}
			}()
		}); err != nil {
			log.Panic(err)
		}
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
		ctx:  sess.Context(), // Initialize from session, will be updated with session ID and user
	}
	if err := env.Connect(); err != nil {
		if !errors.Is(err, io.EOF) {
			fmt.Fprintf(env.term, "InternalServerError: %v\n", err)
			log.Println(err)
			log.Println(juicemud.StackTrace(err))
		}
	}
	// Log session end if user was authenticated
	if env.user != nil {
		g.storage.AuditLog(env.ctx, "SESSION_END", storage.AuditSessionEnd{
			User:   storage.Ref(env.user.Id, env.user.Name),
			Remote: sess.RemoteAddr().String(),
		})
	}
}
