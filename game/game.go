package game

import (
	"fmt"
	"io"
	"log"

	"github.com/gliderlabs/ssh"
	"github.com/timshannon/badgerhold"
	"golang.org/x/crypto/ssh/terminal"
)

// Represents the game world.
type Game struct {
	DB      *badgerhold.Store
	Objects map[uint64]*Object
}

func New(db *badgerhold.Store) *Game {
	return &Game{
		DB:      db,
		Objects: map[uint64]*Object{},
	}
}

func (g *Game) HandleSession(sess ssh.Session) {
	env := &Env{
		Game: g,
		Term: terminal.NewTerminal(sess, "> "),
	}
	if err := env.Connect(); err != nil {
		if err != io.EOF {
			msg := fmt.Sprintf("InternalServerError: %v", err)
			fmt.Fprintf(env.Term, msg)
			log.Print(msg)
		}
	}
}
