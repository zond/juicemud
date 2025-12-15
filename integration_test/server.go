package integration_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/zond/juicemud/server"
)

// TestServer wraps a server instance for testing.
type TestServer struct {
	*server.Server
	tmpDir      string
	sshListener net.Listener
	cancel      context.CancelFunc
	done        chan struct{} // closed when server goroutine exits
}

// NewTestServer creates a new test server with random ports.
func NewTestServer() (*TestServer, error) {
	tmpDir, err := os.MkdirTemp("", "juicemud-integration-*")
	if err != nil {
		return nil, err
	}

	config := server.Config{
		SSHAddr: "127.0.0.1:0",
		Dir:     tmpDir,
	}

	srv, err := server.New(config)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, err
	}

	// Create listener with random port
	sshLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	ts := &TestServer{
		Server:      srv,
		tmpDir:      tmpDir,
		sshListener: sshLn,
		cancel:      cancel,
		done:        done,
	}

	// Start server in background
	go func() {
		defer close(done)
		srv.StartWithListener(ctx, sshLn)
	}()

	// Wait for server to be ready by polling the SSH port
	ready := waitForCondition(5*time.Second, 50*time.Millisecond, func() bool {
		conn, err := net.DialTimeout("tcp", ts.SSHAddr(), 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	})
	if !ready {
		ts.Close()
		return nil, fmt.Errorf("server did not become ready")
	}

	return ts, nil
}

// Close shuts down the test server and cleans up.
func (ts *TestServer) Close() {
	ts.cancel()
	<-ts.done // wait for server to fully shut down
	ts.sshListener.Close()
	os.RemoveAll(ts.tmpDir)
}

// SSHAddr returns the SSH address.
func (ts *TestServer) SSHAddr() string {
	return ts.sshListener.Addr().String()
}

// waitForSourceObject polls until an object with the given source path exists.
// Use this for objects that can't be seen via /inspect (e.g., hidden objects with challenges).
// Returns the object ID and true if found, or empty string and false on timeout.
func (ts *TestServer) waitForSourceObject(ctx context.Context, sourcePath string, timeout time.Duration) (string, bool) {
	var objectID string
	found := waitForCondition(timeout, 50*time.Millisecond, func() bool {
		for id, err := range ts.Storage().EachSourceObject(ctx, sourcePath) {
			if err != nil {
				return false
			}
			objectID = id
			return true
		}
		return false
	})
	return objectID, found
}

// SourcesDir returns the path to the sources directory.
func (ts *TestServer) SourcesDir() string {
	return filepath.Join(ts.tmpDir, "src")
}

// WriteSource writes a source file to the sources directory.
// The path should start with "/" (e.g., "/user.js").
func (ts *TestServer) WriteSource(path, content string) error {
	fullPath := filepath.Join(ts.SourcesDir(), path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(fullPath, []byte(content), 0644)
}

// ReadSource reads a source file from the sources directory.
// The path should start with "/" (e.g., "/user.js").
func (ts *TestServer) ReadSource(path string) (string, error) {
	fullPath := filepath.Join(ts.SourcesDir(), path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}
