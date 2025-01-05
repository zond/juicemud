package game

import (
	"fmt"
	"io"
	"log"

	"github.com/gliderlabs/ssh"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/storage"
	"golang.org/x/term"
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
		term: term.NewTerminal(sess, "> "),
		sess: sess,
	}
	if err := env.Connect(); err != nil {
		if !errors.Is(err, io.EOF) {
			msg := fmt.Sprintf("InternalServerError: %v", err)
			fmt.Fprintln(env.term, msg)
			log.Print(msg)
			log.Print(juicemud.StackTrace(err))
		}
	}
}
