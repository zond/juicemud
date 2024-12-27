package game

import (
	"log"

	"github.com/gliderlabs/ssh"
	"github.com/zond/juicemud/storage"
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
	log.Printf("session!")
}
