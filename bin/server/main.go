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
	var logFile string

	flag.StringVar(&config.SSHAddr, "ssh", config.SSHAddr, "Where to listen to SSH connections.")
	flag.StringVar(&config.Dir, "dir", filepath.Join(os.Getenv("HOME"), ".juicemud"), "Where to save database and settings.")
	flag.StringVar(&logFile, "logfile", "", "Path to log file (default: stderr).")

	flag.Parse()

	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.Fatalf("Failed to open log file: %v", err)
		}
		defer f.Close()
		log.SetOutput(f)
	}

	srv, err := server.New(config)
	if err != nil {
		log.Fatal(err)
	}

	log.Fatal(srv.Start(context.Background()))
}
