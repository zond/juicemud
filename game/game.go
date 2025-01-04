package game

import (
	"fmt"
	"io"
	"log"

	"github.com/gliderlabs/ssh"
	"github.com/zond/juicemud/storage"
	"golang.org/x/crypto/ssh/terminal"
)

type Game struct {
	storage *storage.Storage
}

func New(s *storage.Storage) *Game {
	return &Game{
		storage: s,
	}
}

func (g *Game) HandleSession(sess ssh.Session) {
	env := &Env{
		game: g,
		term: terminal.NewTerminal(sess, "> "),
		sess: sess,
	}
	if err := env.Connect(); err != nil {
		if err != io.EOF {
			msg := fmt.Sprintf("InternalServerError: %v", err)
			fmt.Fprintf(env.term, msg)
			log.Print(msg)
		}
	}
}
