package game

import (
	"fmt"
	"io"
	"log"

	"github.com/asdine/storm"
	"github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh/terminal"
)

// Represents the game world.
type Game struct {
	DB *storm.DB
}

func (g *Game) HandleSession(sess ssh.Session) {
	env := &Env{
		DB:   g.DB,
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
