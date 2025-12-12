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
	flag.StringVar(&config.HTTPSAddr, "https", config.HTTPSAddr, "Where to listen to HTTPS connections for WebDAV.")
	flag.StringVar(&config.HTTPAddr, "http", config.HTTPAddr, "Where to listen to HTTP connections for WebDAV (requires -enable-http).")
	flag.BoolVar(&config.EnableHTTP, "enable-http", false, "Enable insecure HTTP server (for development only).")
	flag.StringVar(&config.Hostname, "hostname", config.Hostname, "Hostname for HTTPS certificate signatures, will use -https value if empty.")
	flag.StringVar(&config.Dir, "dir", filepath.Join(os.Getenv("HOME"), ".juicemud"), "Where to save database and settings.")

	flag.Parse()

	srv, err := server.New(config)
	if err != nil {
		log.Fatal(err)
	}

	log.Fatal(srv.Start(context.Background()))
}
