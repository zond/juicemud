package game

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
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
	createdEventType   = "created"

	// movementEventType is sent to objects that successfully DETECT a moving object.
	// Subject to skill challenges - only objects passing perception checks receive this.
	// Use for game/roleplay purposes where detection abilities matter.
	movementEventType = "movement"

	// receivedEventType is sent to containers when they gain content.
	// Hardwired notification, NOT subject to skill challenges.
	// Use for programmatic bookkeeping where containers need reliable content tracking.
	receivedEventType = "received"

	// transmittedEventType is sent to containers when they lose content.
	// Hardwired notification, NOT subject to skill challenges.
	// Use for programmatic bookkeeping where containers need reliable content tracking.
	transmittedEventType = "transmitted"
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

const (
	// maxEventWorkers is the number of worker goroutines that process queue events.
	// This provides natural backpressure: if all workers are busy, the queue handler
	// blocks until a worker is available to receive the event.
	maxEventWorkers = 64
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
	storage     *storage.Storage
	queueStats  *QueueStats
	workChan    chan *structs.Event // Unbuffered channel for event handoff to workers
	workerWG    sync.WaitGroup      // Tracks in-flight event workers
	queueCancel context.CancelFunc  // Cancels queue context to initiate shutdown
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

	// Create child context for queue - cancelling this stops the queue.
	queueCtx, queueCancel := context.WithCancel(ctx)

	g := &Game{
		storage:     s,
		queueStats:  NewQueueStats(),
		workChan:    make(chan *structs.Event), // Unbuffered for synchronous handoff
		queueCancel: queueCancel,
	}

	// processEvent handles a single event. Extracted to avoid duplication.
	processEvent := func(ev *structs.Event) {
		var call structs.Caller
		if ev.Call.Name != "" {
			call = &ev.Call
		}
		g.queueStats.RecordEvent(ev.Object)
		if _, _, err := g.loadRun(ctx, ev.Object, call); err != nil {
			g.queueStats.RecordError(ev.Object, err)
			log.Printf("trying to execute %+v: %v", ev, err)
			log.Printf("%v", juicemud.StackTrace(err))
		}
	}

	// Start event workers. They process events until context is cancelled.
	for i := 0; i < maxEventWorkers; i++ {
		g.workerWG.Add(1)
		go func() {
			defer g.workerWG.Done()
			for {
				select {
				case ev := <-g.workChan:
					processEvent(ev)
				case <-queueCtx.Done():
					return
				}
			}
		}()
	}

	go func() {
		if err := g.storage.StartObjects(ctx); err != nil {
			log.Panic(err)
		}
	}()
	go g.queueStats.Start()

	// Start the queue. The handler sends events to workers via unbuffered channel,
	// blocking until a worker receives. This provides natural backpressure.
	go func() {
		err := g.storage.Queue().Start(queueCtx, func(ctx context.Context, ev *structs.Event) error {
			select {
			case g.workChan <- ev:
				return nil // Successfully handed off to worker
			case <-ctx.Done():
				return ctx.Err() // Shutdown requested, event stays in queue
			}
		})
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
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

// Close stops any background goroutines associated with the Game and waits
// for in-flight event handlers to complete.
func (g *Game) Close() {
	// Cancel queue context. This stops the queue and all workers.
	g.queueCancel()
	// Wait for all workers to complete their current event.
	g.workerWG.Wait()

	loginRateLimiter.Close()
	g.queueStats.Close()
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
