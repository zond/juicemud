package main

import (
	"flag"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"

	"github.com/gliderlabs/ssh"
	"github.com/timshannon/badgerhold"
	"github.com/zond/juicemud/game"
	"github.com/zond/juicemud/pemfile"

	gossh "golang.org/x/crypto/ssh"
)

func main() {
	iface := flag.String("iface", "127.0.0.1:15000", "Where to listen to SSH connections")
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

	privatePEMPath := filepath.Join(*dir, "private.pem")
	publicPEMPath := filepath.Join(*dir, "public.pem")
	privatePEMFile, err := os.Open(privatePEMPath)
	if os.IsNotExist(err) {
		if err := pemfile.GenKeyPair(privatePEMPath, publicPEMPath); err != nil {
			log.Fatal(err)
		}
		log.Printf("Generated server key pair in %q", *dir)
	} else if err != nil {
		log.Fatal(err)
	} else {
		privatePEMFile.Close()
	}

	pemBytes, err := ioutil.ReadFile(privatePEMPath)
	if err != nil {
		log.Fatal(err)
	}

	signer, err := gossh.ParsePrivateKey(pemBytes)
	if err != nil {
		log.Fatal(err)
	}

	dbPath := filepath.Join(*dir, "badgerhold")
	dbOptions := badgerhold.DefaultOptions
	dbOptions.Dir = filepath.Join(dbPath, "dir")
	if err := os.MkdirAll(dbOptions.Dir, 0700); err != nil {
		log.Fatal(err)
	}
	dbOptions.ValueDir = filepath.Join(dbPath, "valueDir")
	if err := os.MkdirAll(dbOptions.ValueDir, 0700); err != nil {
		log.Fatal(err)
	}

	db, err := badgerhold.Open(dbOptions)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	g := game.New(db)

	s := &ssh.Server{
		Addr:    *iface,
		Handler: g.HandleSession,
	}
	s.AddHostKey(signer)

	log.Printf("Serving %q on %q with public key %q", dbPath, *iface, gossh.FingerprintSHA256(signer.PublicKey()))
	log.Fatal(s.ListenAndServe())
}
