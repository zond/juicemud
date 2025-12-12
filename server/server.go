package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
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
	config Config
	crypto crypto.Crypto
	signer gossh.Signer

	mu      sync.RWMutex
	storage *storage.Storage // Set during Start, nil before/after
	game    *game.Game       // Set during Start, nil before/after
}

// New creates a new server with the given configuration.
// It sets up the directory and crypto keys but does not start any goroutines.
// Call Start() to actually run the server.
func New(config Config) (*Server, error) {
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

	return &Server{
		config: config,
		crypto: cr,
		signer: signer,
	}, nil
}

// Start begins serving on all configured addresses.
// This function blocks until the context is cancelled or a server error occurs.
// All resources are cleaned up when Start returns.
func (s *Server) Start(ctx context.Context) error {
	sshLn, err := net.Listen("tcp", s.config.SSHAddr)
	if err != nil {
		return juicemud.WithStack(err)
	}
	defer sshLn.Close()

	httpsLn, err := net.Listen("tcp", s.config.HTTPSAddr)
	if err != nil {
		return juicemud.WithStack(err)
	}
	defer httpsLn.Close()

	var httpLn net.Listener
	if s.config.EnableHTTP {
		httpLn, err = net.Listen("tcp", s.config.HTTPAddr)
		if err != nil {
			return juicemud.WithStack(err)
		}
		defer httpLn.Close()
	}

	return s.startWithListeners(ctx, sshLn, httpLn, httpsLn)
}

// StartWithListeners starts the server using the provided listeners.
// This is useful for testing where you want to use random ports.
// The httpLn listener is only used if EnableHTTP is true in the config.
// This function blocks until the context is cancelled or a server error occurs.
// The caller is responsible for closing the listeners after this returns.
func (s *Server) StartWithListeners(ctx context.Context, sshLn, httpLn, httpsLn net.Listener) error {
	return s.startWithListeners(ctx, sshLn, httpLn, httpsLn)
}

func (s *Server) startWithListeners(ctx context.Context, sshLn, httpLn, httpsLn net.Listener) error {
	// Create cancellable context - defer cancel ensures cleanup on any exit
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Initialize storage
	store, err := storage.New(ctx, s.config.Dir)
	if err != nil {
		return juicemud.WithStack(err)
	}
	defer store.Close()

	// Initialize game
	g, err := game.New(ctx, store)
	if err != nil {
		return juicemud.WithStack(err)
	}
	defer g.Wait()

	// Set fields for accessor methods (synchronized)
	s.mu.Lock()
	s.storage = store
	s.game = g
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.storage = nil
		s.game = nil
		s.mu.Unlock()
	}()

	// Create SSH server
	sshServer := &ssh.Server{
		Addr:    s.config.SSHAddr,
		Handler: g.HandleSession,
	}
	sshServer.AddHostKey(s.signer)

	// Create WebDAV handler with auth
	fsys := &fs.Fs{Storage: store}
	davHandler := dav.New(ctx, fsys)
	digestAuth := digest.NewDigestAuth(ctx, juicemud.DAVAuthRealm, store)
	auth := digestAuth.Wrap(davHandler)

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
		Addr:    s.config.HTTPSAddr,
		Handler: logger,
	}

	httpServer := &http.Server{
		Addr:    s.config.HTTPAddr,
		Handler: logger,
	}

	fingerprint := gossh.FingerprintSHA256(s.signer.PublicKey())

	log.Printf("Serving SSH on %q with public key %q", sshLn.Addr(), fingerprint)
	log.Printf("Serving HTTPS on %q with public key %q", httpsLn.Addr(), fingerprint)

	numServers := 2
	if s.config.EnableHTTP && httpLn != nil {
		log.Printf("Serving HTTP on %q (insecure, for development only)", httpLn.Addr())
		numServers = 3
	}

	errCh := make(chan error, numServers)

	go func() {
		errCh <- httpsServer.ServeTLS(httpsLn, s.crypto.HTTPSCertPath, s.crypto.PrivKeyPath)
	}()

	if s.config.EnableHTTP && httpLn != nil {
		go func() {
			errCh <- httpServer.Serve(httpLn)
		}()
	}

	go func() {
		errCh <- sshServer.Serve(sshLn)
	}()

	// Wait for context cancellation or server error
	var serverErr error
	select {
	case serverErr = <-errCh:
		// One server failed - trigger shutdown of others
	case <-ctx.Done():
		// Context cancelled - graceful shutdown requested
	}

	// Graceful shutdown of all servers
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	var errs []error
	if serverErr != nil {
		errs = append(errs, serverErr)
	}
	if err := sshServer.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, err)
	}
	if err := httpsServer.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, err)
	}
	if s.config.EnableHTTP {
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Storage returns the server's storage instance.
// Only valid while the server is running; returns nil before Start or after shutdown.
func (s *Server) Storage() *storage.Storage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.storage
}

// Game returns the server's game instance.
// Only valid while the server is running; returns nil before Start or after shutdown.
func (s *Server) Game() *game.Game {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.game
}
