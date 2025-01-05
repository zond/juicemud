package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gliderlabs/ssh"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/crypto"
	"github.com/zond/juicemud/dav"
	"github.com/zond/juicemud/digest"
	"github.com/zond/juicemud/fs"
	"github.com/zond/juicemud/game"
	"github.com/zond/juicemud/storage"

	gossh "golang.org/x/crypto/ssh"
)

type responseWriter struct {
	backend http.ResponseWriter
	status  int
	size    int
}

func (r *responseWriter) Header() http.Header {
	return r.backend.Header()
}

func (r *responseWriter) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	written, err := r.backend.Write(b)
	r.size += written
	return written, err
}

func (r *responseWriter) WriteHeader(status int) {
	r.status = status
	r.backend.WriteHeader(status)
}

func main() {
	sshIface := flag.String("ssh", "127.0.0.1:15000", "Where to listen to SSH connections")
	httpsIface := flag.String("https", "127.0.0.1:8081", "Where to listen to HTTPS connections for WebDAV")
	httpIface := flag.String("http", "127.0.0.1:8080", "Where to listen to HTTP connections for WebDAV")
	hostname := flag.String("hostname", "", "Hostname for HTTPS certificate signatures, will use -https value if empty")
	dir := flag.String("dir", filepath.Join(os.Getenv("HOME"), ".juicemud"), "Where to save database and settings")

	flag.Parse()

	if *hostname == "" {
		*hostname = *httpsIface
	}

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

	crypto := crypto.Crypto{
		Hostname:      *hostname,
		PrivKeyPath:   filepath.Join(*dir, "privKey"),
		SSHPubKeyPath: filepath.Join(*dir, "sshPubKey"),
		HTTPSCertPath: filepath.Join(*dir, "httpsCert"),
	}
	if _, err = os.Stat(crypto.PrivKeyPath); os.IsNotExist(err) {
		if err := crypto.Generate(); err != nil {
			log.Fatal(err)
		}
		log.Printf("Generated crypto keys in %+v", crypto)
	} else if err != nil {
		log.Fatal(err)
	}

	pemBytes, err := os.ReadFile(crypto.PrivKeyPath)
	if err != nil {
		log.Fatal(err)
	}

	signer, err := gossh.ParsePrivateKey(pemBytes)
	if err != nil {
		log.Fatal(err)
	}
	fingerprint := gossh.FingerprintSHA256(signer.PublicKey())

	store, err := storage.New(context.Background(), *dir)
	if err != nil {
		log.Fatal(err)
	}
	g := game.New(store)

	sshServer := &ssh.Server{
		Addr:    *sshIface,
		Handler: g.HandleSession,
	}
	sshServer.AddHostKey(signer)
	log.Printf("Serving SSH on %q with public key %q", *sshIface, fingerprint)

	fs := &fs.Fs{
		Storage: store,
	}
	dav := dav.New(fs)
	auth := digest.NewDigestAuth(juicemud.DAVAuthRealm, store).Wrap(dav)
	logger := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		ww := &responseWriter{backend: w, status: http.StatusOK}
		auth.ServeHTTP(ww, r)
		lapsed := time.Since(t)
		log.Printf("%s\t%s\t%s\t%v\t%vb in\t%vb out\t%s", r.RemoteAddr, r.Method, r.URL, ww.status, r.ContentLength, ww.size, lapsed)
	})

	httpsServer := &http.Server{
		Addr:    *httpsIface,
		Handler: logger,
	}
	log.Printf("Serving HTTPS on %q with public key %q", *httpsIface, fingerprint)

	httpServer := &http.Server{
		Addr:    *httpIface,
		Handler: logger,
	}
	log.Printf("Serving HTTP on %q", *httpIface)

	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		log.Fatal(httpsServer.ListenAndServeTLS(crypto.HTTPSCertPath, crypto.PrivKeyPath))
	}()
	go func() {
		defer wg.Done()
		log.Fatal(httpServer.ListenAndServe())
	}()

	log.Fatal(sshServer.ListenAndServe())
	wg.Wait()
}
