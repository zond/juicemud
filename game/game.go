package game

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gliderlabs/ssh"
	cache "github.com/go-pkgz/expirable-cache/v3"
	goccy "github.com/goccy/go-json"
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

	// renderMovementEventType is sent to a moving object when Movement.Active is false.
	// The callback should return a movementRenderedResponse with the custom message.
	renderMovementEventType = "renderMovement"

	// messageEventType is sent to objects to display a message.
	// When received by an object with an active connection, the message is printed to their terminal.
	// Useful for NPC dialogue, system messages, etc.
	messageEventType = "message"
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
	// maxEventWorkers is the number of worker goroutines that process queue events.
	// This provides natural backpressure: if all workers are busy, the queue handler
	// blocks until a worker is available to receive the event.
	maxEventWorkers = 64

	// maxCreatesPerMinute is the per-object creation rate limit for createObject().
	// This prevents abuse from infinite spawning loops or resource exhaustion.
	maxCreatesPerMinute = 10
)

// createRateLimiter tracks per-object creation counts with auto-expiring entries.
// Uses expirable cache where entries expire after 1 minute, providing natural
// rate limiting without explicit cleanup.
type createRateLimiter struct {
	mu sync.Mutex
	// minuteCounts maps objectID -> count of creates in the current minute.
	// Entries automatically expire after 1 minute.
	minuteCounts cache.Cache[string, int]
}

// newCreateRateLimiter creates a new rate limiter for createObject calls.
func newCreateRateLimiter() *createRateLimiter {
	return &createRateLimiter{
		minuteCounts: cache.NewCache[string, int]().WithTTL(time.Minute),
	}
}

// checkAndRecord atomically checks if creation is allowed and records the attempt.
// Returns true if allowed (and count incremented), false if rate limited.
func (r *createRateLimiter) checkAndRecord(objectID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	count, _ := r.minuteCounts.Get(objectID)
	if count >= maxCreatesPerMinute {
		return false
	}
	r.minuteCounts.Set(objectID, count+1, 0) // 0 = use default TTL
	return true
}

var (
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
	storage              *storage.Storage
	jsStats              *JSStats
	loginRateLimiter     *loginRateLimiter
	createLimiter        *createRateLimiter                      // Rate limiter for createObject() JS API
	workChan             chan *structs.Event                     // Unbuffered channel for event handoff to workers
	workerWG             sync.WaitGroup                          // Tracks in-flight event workers
	connectionByObjectID *juicemud.SyncMap[string, *Connection]  // Maps object ID to active connection
	consoleSwitchboard   *Switchboard                            // Debug console routing and buffering
}

// ServerConfig holds server-wide configuration stored in the root object's state.
// The root object (empty ID "") contains this config to define server behavior.
type ServerConfig struct {
	Spawn struct {
		Container string
	}
	SkillConfigs map[string]structs.SkillConfig
}

// getServerConfig loads the server config from the root object's state.
func (g *Game) getServerConfig(ctx context.Context) (*ServerConfig, error) {
	root, err := g.storage.AccessObject(ctx, emptyID, nil)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	config := &ServerConfig{}
	state := root.GetState()
	if state != "" && state != "{}" {
		if err := goccy.Unmarshal([]byte(state), config); err != nil {
			// Log but don't fail - use defaults
			log.Printf("Warning: failed to parse root object state as ServerConfig: %v", err)
		}
	}
	return config, nil
}

// updateServerConfig atomically reads, modifies, and writes the server config.
// The update function receives the current config and should modify it in place.
// The root object is locked during the entire operation.
func (g *Game) updateServerConfig(ctx context.Context, update func(*ServerConfig)) error {
	root, err := g.storage.AccessObject(ctx, emptyID, nil)
	if err != nil {
		return juicemud.WithStack(err)
	}
	return structs.UpdateState(root, func(config *ServerConfig) error {
		update(config)
		return nil
	})
}

// getSpawnLocation returns the configured spawn location for new users.
// Falls back to genesis if not configured or if configured location doesn't exist.
func (g *Game) getSpawnLocation(ctx context.Context) string {
	config, err := g.getServerConfig(ctx)
	if err != nil {
		log.Printf("Warning: failed to load server config, using default spawn: %v", err)
		return genesisID
	}
	if config.Spawn.Container == "" {
		return genesisID
	}
	// Verify the spawn location exists
	if _, err := g.storage.AccessObject(ctx, config.Spawn.Container, nil); err != nil {
		log.Printf("Warning: configured spawn location %q not found, using genesis: %v", config.Spawn.Container, err)
		return genesisID
	}
	return config.Spawn.Container
}

// loadSkillConfigs loads skill configs from the root object and populates the in-memory store.
// Called at startup to restore skill configurations.
func (g *Game) loadSkillConfigs(ctx context.Context) error {
	config, err := g.getServerConfig(ctx)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if config.SkillConfigs != nil {
		structs.SkillConfigs.Replace(config.SkillConfigs)
		if len(config.SkillConfigs) > 0 {
			log.Printf("Loaded %d skill configs from server config", len(config.SkillConfigs))
		}
	}
	return nil
}

// New creates a new Game instance.
// If firstStartup is true (server directory was just created), it creates all
// initial source files. On all startups, it ensures fundamental objects exist
// (genesis, empty) so the server can function, but uses SetIfMissing to preserve
// any admin customizations to existing objects.
func New(ctx context.Context, s *storage.Storage, firstStartup bool) (*Game, error) {
	ctx = juicemud.MakeMainContext(ctx)

	sourcesDir := s.SourcesDir()

	if firstStartup {
		// First startup: create sources directory and all initial source files
		if err := os.MkdirAll(sourcesDir, 0700); err != nil {
			return nil, juicemud.WithStack(err)
		}
		for sourcePath, source := range initialSources {
			fullPath := filepath.Join(sourcesDir, sourcePath)
			// Create parent directory if needed
			if dir := filepath.Dir(fullPath); dir != sourcesDir {
				if err := os.MkdirAll(dir, 0700); err != nil {
					return nil, juicemud.WithStack(err)
				}
			}
			if err := os.WriteFile(fullPath, []byte(source), 0644); err != nil {
				return nil, juicemud.WithStack(err)
			}
		}
		log.Println("First startup: created initial source files")
	}

	// Always ensure fundamental objects exist (failsafe).
	// Uses SetIfMissing: won't overwrite existing objects, preserving admin customizations.
	// If an object is missing, it's recreated with the default source path.
	// ValidateSources below will catch if the source file doesn't exist.
	for _, obj := range initialObjects() {
		o := &structs.Object{Unsafe: obj}
		o.Unsafe.PostUnmarshal()
		if err := s.UNSAFEEnsureObject(ctx, o); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}

	// Validate all objects have their source files
	missing, err := s.ValidateSources(ctx, sourcesDir)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	if len(missing) > 0 {
		var errMsg strings.Builder
		errMsg.WriteString("refusing to start: missing source files\n")
		for _, m := range missing {
			fmt.Fprintf(&errMsg, "  %s (%d objects)\n", m.Path, len(m.ObjectIDs))
		}
		return nil, errors.New(errMsg.String())
	}

	g := &Game{
		storage:              s,
		jsStats:              NewJSStats(ctx, s.ImportResolver()),
		loginRateLimiter:     newLoginRateLimiter(ctx),
		createLimiter:        newCreateRateLimiter(),
		workChan:             make(chan *structs.Event), // Unbuffered for synchronous handoff
		connectionByObjectID: juicemud.NewSyncMap[string, *Connection](),
		consoleSwitchboard:   NewSwitchboard(ctx),
	}

	// Start event workers. They process events until context is cancelled.
	for i := 0; i < maxEventWorkers; i++ {
		g.workerWG.Add(1)
		go func() {
			defer g.workerWG.Done()
			for {
				select {
				case ev := <-g.workChan:
					// For interval events, check if the interval still exists.
					// If it was cleared (via clearInterval), skip running the handler.
					// This handles the race where an event was already enqueued before clearInterval.
					if ev.IntervalID != "" && !g.storage.Intervals().Has(ev.Object, ev.IntervalID) {
						continue // Interval was cleared, skip this event
					}

					var call structs.Caller
					if ev.Call.Name != "" {
						call = &ev.Call
					}
					// Check if this is an interval event for stats tracking
					var intervalInfo *IntervalExecInfo
					if ev.IntervalID != "" {
						intervalInfo = &IntervalExecInfo{IntervalID: ev.IntervalID, EventName: ev.Call.Name}
					}

					// run() and loadRun() handle recording execution time and errors in jsStats
					_, _, err := g.loadRun(ctx, ev.Object, call, intervalInfo)
					if err != nil {
						// Skip logging for deleted objects - this is normal when events
						// are queued for objects that get removed before events fire
						if !errors.Is(err, os.ErrNotExist) {
							log.Printf("trying to execute %+v: %v", ev, err)
							log.Printf("%v", juicemud.StackTrace(err))
						}
					}

					// Handle interval re-enqueueing after handler execution
					if ev.IntervalID != "" {
						// If object doesn't exist, delete the interval instead of re-enqueueing
						if errors.Is(err, os.ErrNotExist) {
							if delErr := g.storage.Intervals().Del(ev.Object, ev.IntervalID); delErr != nil && !errors.Is(delErr, os.ErrNotExist) {
								log.Printf("deleting orphaned interval %s for missing object %s: %v", ev.IntervalID, ev.Object, delErr)
							}
						} else if err := g.reEnqueueInterval(ctx, ev.Object, ev.IntervalID); err != nil {
							g.jsStats.RecordRecoveryError(ev.Object, ev.IntervalID, err)
							log.Printf("re-enqueueing interval %s for %s: %v", ev.IntervalID, ev.Object, err)
						}
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Start the queue. The handler sends events to workers via unbuffered channel,
	// blocking until a worker receives. This provides natural backpressure.
	g.workerWG.Add(1)
	go func() {
		defer g.workerWG.Done()
		err := g.storage.Queue().Start(ctx, func(ctx context.Context, ev *structs.Event) error {
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

	// Recover intervals from persistent storage
	if err := g.RecoverIntervals(ctx); err != nil {
		return nil, juicemud.WithStack(err)
	}

	// Load skill configs from root object state
	if err := g.loadSkillConfigs(ctx); err != nil {
		return nil, juicemud.WithStack(err)
	}

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
		g.jsStats.RecordBootError(err)
		log.Printf("trying to run %q: %v", bootSource, err)
		log.Println(juicemud.StackTrace(err))
		return g, nil
	}
	return g, nil
}

// Wait blocks until all Game goroutines have stopped.
// The caller must cancel the context first to signal shutdown.
func (g *Game) Wait() {
	g.workerWG.Wait()
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
