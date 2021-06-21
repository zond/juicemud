package game

import (
	"fmt"
	"io"
	"log"

	"github.com/gliderlabs/ssh"
	"github.com/zond/juicemud/storage"
	"golang.org/x/crypto/ssh/terminal"
)

// Represents the game world.
type Game struct {
	Storage *storage.Storage
}

func New(storage *storage.Storage) *Game {
	return &Game{
		Storage: storage,
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
