package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/gliderlabs/ssh"
	"github.com/zond/juicemud/game"
	"github.com/zond/juicemud/pemfile"
	"github.com/zond/juicemud/storage"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/net/webdav"
)

func main() {
	sshIface := flag.String("ssh", "127.0.0.1:15000", "Where to listen to SSH connections")
	hostname := flag.String("hostname", "localhost", "Hostname for HTTPS certificate signatures")
	httpIface := flag.String("webdav", "127.0.0.1:8080", "Where to listen to HTTPS connections for WebDAV")
	dir := flag.String("dir", filepath.Join(os.Getenv("HOME"), ".juicemud"), "Where to save database and settings")

	flag.Parse()

	dirFile, err := os.Open(*dir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(*dir, 0700); err != nil {
			log.Fatal(err)
		}
	} else if err != nil {
		log.Fatal(err)
	} else {
		dirFile.Close()
	}

	keyPath := filepath.Join(*dir, "key")
	sshPubKeyPath := filepath.Join(*dir, "sshPubKey")
	httpsCertPath := filepath.Join(*dir, "httpsCert")
	if _, err = os.Stat(keyPath); os.IsNotExist(err) {
		params := pemfile.KeyParams{
			Hostname:      *hostname,
			KeyPath:       keyPath,
			SSHPubKeyPath: sshPubKeyPath,
			HTTPSCertPath: httpsCertPath,
		}
		if err := params.Generate(); err != nil {
			log.Fatal(err)
		}
		log.Printf("Generated crypto keys in %+v", params)
	} else if err != nil {
		log.Fatal(err)
	}

	pemBytes, err := os.ReadFile(keyPath)
	if err != nil {
		log.Fatal(err)
	}

	signer, err := gossh.ParsePrivateKey(pemBytes)
	if err != nil {
		log.Fatal(err)
	}
	fingerprint := gossh.FingerprintSHA256(signer.PublicKey())

	dbPath := filepath.Join(*dir, "sqlite.db")
	store, err := storage.New(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	g := game.New(store)

	sshServer := &ssh.Server{
		Addr:    *sshIface,
		Handler: g.HandleSession,
		PtyCallback: func(ssh.Context, ssh.Pty) bool {
			return true
		},
	}
	sshServer.AddHostKey(signer)
	log.Printf("Serving SSH on %q with public key %q", *sshIface, fingerprint)

	httpServer := &http.Server{
		Addr: *httpIface,
		Handler: &webdav.Handler{
			Prefix:     "",
			FileSystem: store,
		},
	}
	log.Printf("Serving HTTP on %q with public key %q", *httpIface, fingerprint)

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Fatal(sshServer.ListenAndServe())
	}()
	log.Fatal(httpServer.ListenAndServe())
	wg.Wait()
}
