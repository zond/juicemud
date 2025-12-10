package server

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	if written > 0 {
		r.size += written
	}
	return written, err
}

func (r *responseWriter) WriteHeader(status int) {
	r.status = status
	r.backend.WriteHeader(status)
}

type sizeBody struct {
	backend io.ReadCloser
	size    int
}

func (s *sizeBody) Read(b []byte) (int, error) {
	read, err := s.backend.Read(b)
	if read > 0 {
		s.size += read
	}
	return read, err
}

func (s *sizeBody) Close() error {
	return s.backend.Close()
}

// Config holds the server configuration.
type Config struct {
	SSHAddr    string // Address for SSH connections (e.g., "127.0.0.1:15000")
	HTTPSAddr  string // Address for HTTPS WebDAV (e.g., "127.0.0.1:8081")
	HTTPAddr   string // Address for HTTP WebDAV (e.g., "127.0.0.1:8080")
	EnableHTTP bool   // Enable HTTP server (insecure, for development only)
	Hostname   string // Hostname for HTTPS certificate
	Dir        string // Directory for database and settings
}

// DefaultConfig returns the default server configuration.
func DefaultConfig() Config {
	return Config{
		SSHAddr:   "127.0.0.1:15000",
		HTTPSAddr: "127.0.0.1:8081",
		HTTPAddr:  "127.0.0.1:8080",
		Hostname:  "",
		Dir:       filepath.Join(os.Getenv("HOME"), ".juicemud"),
	}
}

// Server represents a running JuiceMUD server instance.
type Server struct {
	config        Config
	storage       *storage.Storage
	game          *game.Game
	sshServer     *ssh.Server
	httpsServer   *http.Server
	httpServer    *http.Server
	sshListener   net.Listener
	httpListener  net.Listener
	httpsListener net.Listener
	crypto        crypto.Crypto
	davHandler    *dav.Handler
}

// New creates a new server with the given configuration.
// It initializes storage, game, and all listeners but does not start serving.
func New(ctx context.Context, config Config) (*Server, error) {
	if config.Hostname == "" {
		config.Hostname = config.HTTPSAddr
	}

	// Ensure directory exists
	if _, err := os.Stat(config.Dir); os.IsNotExist(err) {
		if err := os.MkdirAll(config.Dir, 0700); err != nil {
			return nil, err
		}
	}

	// Setup crypto
	cr := crypto.Crypto{
		Hostname:      config.Hostname,
		PrivKeyPath:   filepath.Join(config.Dir, "privKey"),
		SSHPubKeyPath: filepath.Join(config.Dir, "sshPubKey"),
		HTTPSCertPath: filepath.Join(config.Dir, "httpsCert"),
	}
	if _, err := os.Stat(cr.PrivKeyPath); os.IsNotExist(err) {
		if err := cr.Generate(); err != nil {
			return nil, err
		}
		log.Printf("Generated crypto keys in %+v", cr)
	}

	pemBytes, err := os.ReadFile(cr.PrivKeyPath)
	if err != nil {
		return nil, err
	}

	signer, err := gossh.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, err
	}

	// Initialize storage and game
	store, err := storage.New(ctx, config.Dir)
	if err != nil {
		return nil, err
	}

	g, err := game.New(ctx, store)
	if err != nil {
		store.Close()
		return nil, err
	}

	// Create SSH server
	sshServer := &ssh.Server{
		Addr:    config.SSHAddr,
		Handler: g.HandleSession,
	}
	sshServer.AddHostKey(signer)

	// Create WebDAV handler with auth
	fsys := &fs.Fs{Storage: store}
	davHandler := dav.New(fsys)
	auth := digest.NewDigestAuth(juicemud.DAVAuthRealm, store).Wrap(davHandler)

	logger := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		ww := &responseWriter{backend: w, status: http.StatusOK}
		sb := &sizeBody{backend: r.Body}
		r.Body = sb
		auth.ServeHTTP(ww, r)
		elapsed := time.Since(t)
		log.Printf("%s\t%s\t%s\t%v\t%vb in\t%vb out\t%s", r.RemoteAddr, r.Method, r.URL, ww.status, sb.size, ww.size, elapsed)
	})

	httpsServer := &http.Server{
		Addr:    config.HTTPSAddr,
		Handler: logger,
	}

	httpServer := &http.Server{
		Addr:    config.HTTPAddr,
		Handler: logger,
	}

	return &Server{
		config:      config,
		storage:     store,
		game:        g,
		sshServer:   sshServer,
		httpsServer: httpsServer,
		httpServer:  httpServer,
		crypto:      cr,
		davHandler:  davHandler,
	}, nil
}

// Start begins serving on all configured addresses.
// This function blocks until the server is shut down.
func (s *Server) Start() error {
	sshLn, err := net.Listen("tcp", s.config.SSHAddr)
	if err != nil {
		return juicemud.WithStack(err)
	}

	httpsLn, err := net.Listen("tcp", s.config.HTTPSAddr)
	if err != nil {
		sshLn.Close()
		return juicemud.WithStack(err)
	}

	var httpLn net.Listener
	if s.config.EnableHTTP {
		httpLn, err = net.Listen("tcp", s.config.HTTPAddr)
		if err != nil {
			sshLn.Close()
			httpsLn.Close()
			return juicemud.WithStack(err)
		}
	}

	return s.StartWithListeners(sshLn, httpLn, httpsLn)
}

// StartWithListeners starts the server using the provided listeners.
// This is useful for testing where you want to use random ports.
// The httpLn listener is only used if EnableHTTP is true in the config.
func (s *Server) StartWithListeners(sshLn, httpLn, httpsLn net.Listener) error {
	s.sshListener = sshLn
	s.httpListener = httpLn
	s.httpsListener = httpsLn

	fingerprint := ""
	if pemBytes, err := os.ReadFile(s.crypto.PrivKeyPath); err == nil {
		if signer, err := gossh.ParsePrivateKey(pemBytes); err == nil {
			fingerprint = gossh.FingerprintSHA256(signer.PublicKey())
		}
	}

	log.Printf("Serving SSH on %q with public key %q", sshLn.Addr(), fingerprint)
	log.Printf("Serving HTTPS on %q with public key %q", httpsLn.Addr(), fingerprint)

	numServers := 2
	if s.config.EnableHTTP && httpLn != nil {
		log.Printf("Serving HTTP on %q (insecure, for development only)", httpLn.Addr())
		numServers = 3
	}

	errCh := make(chan error, numServers)

	go func() {
		errCh <- s.httpsServer.ServeTLS(httpsLn, s.crypto.HTTPSCertPath, s.crypto.PrivKeyPath)
	}()

	if s.config.EnableHTTP && httpLn != nil {
		go func() {
			errCh <- s.httpServer.Serve(httpLn)
		}()
	}

	go func() {
		errCh <- s.sshServer.Serve(sshLn)
	}()

	return <-errCh
}

// Close shuts down all servers and closes storage.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var errs []error

	if err := s.sshServer.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := s.httpsServer.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := s.httpServer.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	s.davHandler.Close()
	s.game.Close()
	if err := s.storage.Close(); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// SSHAddr returns the actual SSH address (useful when using port 0).
func (s *Server) SSHAddr() string {
	if s.sshListener != nil {
		return s.sshListener.Addr().String()
	}
	return s.config.SSHAddr
}

// HTTPAddr returns the actual HTTP address (useful when using port 0).
func (s *Server) HTTPAddr() string {
	if s.httpListener != nil {
		return s.httpListener.Addr().String()
	}
	return s.config.HTTPAddr
}

// HTTPSAddr returns the actual HTTPS address (useful when using port 0).
func (s *Server) HTTPSAddr() string {
	if s.httpsListener != nil {
		return s.httpsListener.Addr().String()
	}
	return s.config.HTTPSAddr
}

// Storage returns the server's storage instance.
func (s *Server) Storage() *storage.Storage {
	return s.storage
}

// Game returns the server's game instance.
func (s *Server) Game() *game.Game {
	return s.game
}
