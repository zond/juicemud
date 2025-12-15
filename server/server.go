package server

import (
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/gliderlabs/ssh"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/crypto"
	"github.com/zond/juicemud/game"
	"github.com/zond/juicemud/storage"

	gossh "golang.org/x/crypto/ssh"
)

// Config holds the server configuration.
type Config struct {
	SSHAddr string // Address for SSH connections (e.g., "127.0.0.1:15000")
	Dir     string // Directory for database and settings
}

// DefaultConfig returns the default server configuration.
func DefaultConfig() Config {
	return Config{
		SSHAddr: "127.0.0.1:15000",
		Dir:     filepath.Join(os.Getenv("HOME"), ".juicemud"),
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
	// Ensure directory exists
	if _, err := os.Stat(config.Dir); os.IsNotExist(err) {
		if err := os.MkdirAll(config.Dir, 0700); err != nil {
			return nil, err
		}
	}

	// Setup crypto
	cr := crypto.Crypto{
		PrivKeyPath:   filepath.Join(config.Dir, "privKey"),
		SSHPubKeyPath: filepath.Join(config.Dir, "sshPubKey"),
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

// Start begins serving on the configured SSH address.
// This function blocks until the context is cancelled or a server error occurs.
// All resources are cleaned up when Start returns.
func (s *Server) Start(ctx context.Context) error {
	sshLn, err := net.Listen("tcp", s.config.SSHAddr)
	if err != nil {
		return juicemud.WithStack(err)
	}
	defer sshLn.Close()

	return s.startWithListener(ctx, sshLn)
}

// StartWithListener starts the server using the provided listener.
// This is useful for testing where you want to use random ports.
// This function blocks until the context is cancelled or a server error occurs.
// The caller is responsible for closing the listener after this returns.
func (s *Server) StartWithListener(ctx context.Context, sshLn net.Listener) error {
	return s.startWithListener(ctx, sshLn)
}

func (s *Server) startWithListener(ctx context.Context, sshLn net.Listener) error {
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

	fingerprint := gossh.FingerprintSHA256(s.signer.PublicKey())
	log.Printf("Serving SSH on %q with public key %q", sshLn.Addr(), fingerprint)

	errCh := make(chan error, 1)

	go func() {
		errCh <- sshServer.Serve(sshLn)
	}()

	// Wait for context cancellation or server error
	var serverErr error
	select {
	case serverErr = <-errCh:
		// Server failed
	case <-ctx.Done():
		// Context cancelled - graceful shutdown requested
	}

	// Graceful shutdown
	if err := sshServer.Close(); err != nil && serverErr == nil {
		serverErr = err
	}

	return serverErr
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
