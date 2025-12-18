package server

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gliderlabs/ssh"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/crypto"
	"github.com/zond/juicemud/game"
	"github.com/zond/juicemud/storage"

	gossh "golang.org/x/crypto/ssh"
)

// Config holds the server configuration.
type Config struct {
	SSHAddr     string // Address for SSH connections (e.g., "127.0.0.1:15000")
	Dir         string // Directory for database and settings
	SourcesPath string // Path to sources directory (relative to Dir, or absolute). Symlinks are resolved.
}

// DefaultConfig returns the default server configuration.
func DefaultConfig() Config {
	return Config{
		SSHAddr:     "127.0.0.1:15000",
		Dir:         filepath.Join(os.Getenv("HOME"), ".juicemud"),
		SourcesPath: "src/current", // Default: resolve src/current symlink
	}
}

// ControlSocketPath returns the path to the control socket.
func (c Config) ControlSocketPath() string {
	return filepath.Join(c.Dir, "control.sock")
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

	// Resolve sources path (follows symlinks)
	sourcesPath := s.config.SourcesPath
	if sourcesPath == "" {
		sourcesPath = "src/current"
	}
	resolvedSourcesDir, err := storage.ResolveSourcePath(s.config.Dir, sourcesPath)
	if err != nil {
		// If symlink doesn't exist yet, fall back to default src/ directory
		// This handles first-time startup before any versions are created
		log.Printf("Could not resolve sources path %q, using default: %v", sourcesPath, err)
		resolvedSourcesDir = store.SourcesDir()
	} else {
		store.SetSourcesDir(resolvedSourcesDir)
		log.Printf("Using sources directory: %s", resolvedSourcesDir)
	}

	// Initialize game (this validates sources exist)
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

	// Start control socket for admin commands
	go func() {
		if err := s.startControlSocket(ctx); err != nil {
			log.Printf("Control socket error: %v", err)
		}
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

// startControlSocket starts the Unix domain socket for admin commands.
// It runs until the context is cancelled.
//
// SECURITY NOTE: This socket is intentionally unauthenticated. Access control is
// delegated to the filesystem - the socket is created with default permissions,
// and system administrators are responsible for securing it appropriately (e.g.,
// restrictive directory permissions, running as a dedicated user). This is a
// standard pattern for local admin sockets (similar to Docker, MySQL, PostgreSQL).
func (s *Server) startControlSocket(ctx context.Context) error {
	socketPath := s.config.ControlSocketPath()

	// Remove stale socket file if it exists
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return juicemud.WithStack(err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return juicemud.WithStack(err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)

	log.Printf("Control socket listening on %s", socketPath)

	// Accept connections until context is cancelled
	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil // Normal shutdown
			default:
				log.Printf("Control socket accept error: %v", err)
				continue
			}
		}
		go s.handleControlConnection(ctx, conn)
	}
}

// writeResponse writes a response to the control socket connection.
// Errors are logged but not returned since the connection is closing anyway.
func writeResponse(conn net.Conn, format string, args ...any) {
	if _, err := fmt.Fprintf(conn, format, args...); err != nil {
		log.Printf("Control socket write error: %v", err)
	}
}

// handleControlConnection handles a single admin connection.
func (s *Server) handleControlConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// Set read deadline for idle timeout, plus context cancellation for shutdown
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
			// Connection handled normally, goroutine exits
		}
	}()

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		writeResponse(conn, "ERROR: read error: %v\n", err)
		return
	}

	line = strings.TrimSpace(line)
	parts := strings.SplitN(line, " ", 2)
	command := parts[0]

	switch command {
	case "SWITCH_SOURCES":
		var targetPath string
		if len(parts) > 1 {
			targetPath = parts[1]
		} else {
			targetPath = s.config.SourcesPath // Default to configured path
		}
		s.handleSwitchSources(ctx, conn, targetPath)

	default:
		writeResponse(conn, "ERROR: unknown command: %s\n", command)
	}
}

// handleSwitchSources handles the SWITCH_SOURCES command.
func (s *Server) handleSwitchSources(ctx context.Context, conn net.Conn, targetPath string) {
	store := s.Storage()
	if store == nil {
		writeResponse(conn, "ERROR: server not running\n")
		return
	}

	// Resolve symlinks to get actual directory
	resolvedPath, err := storage.ResolveSourcePath(s.config.Dir, targetPath)
	if err != nil {
		writeResponse(conn, "ERROR: failed to resolve path %q: %v\n", targetPath, err)
		return
	}

	// Check that it's a directory
	info, err := os.Stat(resolvedPath)
	if err != nil {
		writeResponse(conn, "ERROR: cannot access %q: %v\n", resolvedPath, err)
		return
	}
	if !info.IsDir() {
		writeResponse(conn, "ERROR: %q is not a directory\n", resolvedPath)
		return
	}

	// Validate and switch atomically (holds lock across both operations)
	missing, err := store.ValidateAndSwitchSources(ctx, resolvedPath)
	if err != nil {
		writeResponse(conn, "ERROR: validation failed: %v\n", err)
		return
	}
	if len(missing) > 0 {
		var errMsg strings.Builder
		errMsg.WriteString("ERROR: missing source files:\n")
		for _, m := range missing {
			fmt.Fprintf(&errMsg, "  %s (%d objects)\n", m.Path, len(m.ObjectIDs))
		}
		writeResponse(conn, "%s", errMsg.String())
		return
	}

	log.Printf("Switched sources to %s", resolvedPath)
	writeResponse(conn, "OK\n")
}
