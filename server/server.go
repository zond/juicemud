package main

import (
	"flag"
	"log"

	"github.com/gliderlabs/ssh"
	"github.com/zond/juicemud/game"
)

func main() {
	iface := flag.String("iface", "127.0.0.1:15000", "Where to listen to SSH connections")
	flag.Parse()

	g := &game.Game{}

	log.Printf("Listening on %q", *iface)
	log.Fatal(ssh.ListenAndServe(*iface, g.HandleSession))
}
