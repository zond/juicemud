package game

import (
	"fmt"
	"log"

	"github.com/gliderlabs/ssh"
	"github.com/zond/juicemud/io"
	"github.com/zond/juicemud/user"
	"golang.org/x/crypto/ssh/terminal"
)

type Game struct{}

func (g *Game) loginUser(term *terminal.Terminal) error {
	return nil
}

func (g *Game) createUser(term *terminal.Terminal) error {
	_, err := user.CreateUser(term)
	if err == user.UserCreationAborted {
		return g.connect(term)
	} else if err != nil {
		return err
	}
	return nil
}

func (g *Game) connect(term *terminal.Terminal) error {
	return io.TerminalExecute(term, map[string]io.TerminalFunc{
		"login user":  g.loginUser,
		"create user": g.createUser,
	})
}

func (g *Game) handleTerminal(term *terminal.Terminal) error {
	fmt.Fprintf(term, "Welcome!\n\n")
	return g.connect(term)
}

func (g *Game) HandleSession(sess ssh.Session) {
	term := terminal.NewTerminal(sess, "> ")
	if err := g.handleTerminal(term); err != nil {
		msg := fmt.Sprintf("InternalServerError: %v", err)
		fmt.Fprintf(term, msg)
		log.Print(msg)
	}
}
