package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/zond/juicemud/server"
)

func main() {
	config := server.DefaultConfig()

	flag.StringVar(&config.SSHAddr, "ssh", config.SSHAddr, "Where to listen to SSH connections.")
	flag.StringVar(&config.Dir, "dir", filepath.Join(os.Getenv("HOME"), ".juicemud"), "Where to save database and settings.")

	flag.Parse()

	srv, err := server.New(config)
	if err != nil {
		log.Fatal(err)
	}

	log.Fatal(srv.Start(context.Background()))
}
