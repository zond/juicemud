package game

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math"
	"math/rand"
	"time"

	"github.com/gliderlabs/ssh"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
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
	g := &Game{
		storage: s,
	}
	go func() {
		log.Panic(g.storage.Start(ctx, func(ctx context.Context, ev *structs.Event) {
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
	return g, nil
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

type skill struct {
	subject  string
	object   string
	name     string
	level    float32
	duration time.Duration
}

func (s *skill) check(challenge float32) bool {
	// Create rng based on subject, object, name, and time step.
	h := fnv.New64()
	h.Write([]byte(s.subject))
	h.Write([]byte(s.object))
	h.Write([]byte(s.name))
	now1 := time.Now().UnixNano() / s.duration.Nanoseconds()
	by := make([]byte, binary.Size(now1))
	binary.BigEndian.PutUint64(by, uint64(now1))
	h.Write(by)
	rng1 := rand.New(rand.NewSource(int64(h.Sum64())))

	// Offset the time step with the rng, and create another one.
	offset := rng1.Int63n(s.duration.Nanoseconds())
	now2 := (time.Now().UnixNano() + offset) / s.duration.Microseconds()
	binary.BigEndian.PutUint64(by, uint64(now2))
	h.Write(by)
	rng2 := rand.New(rand.NewSource(int64(h.Sum64())))

	// Win rate is ELO with 10 instead of 400 as "90% likely to win delta".
	winRate := 1.0 / (1.0 + math.Pow(10, float64(challenge-s.level)*0.1))
	return rng2.Float64() > winRate
}
