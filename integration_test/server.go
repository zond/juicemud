package integration_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/zond/juicemud/server"
)

// TestServer wraps a server instance for testing.
type TestServer struct {
	*server.Server
	tmpDir        string
	sshListener   net.Listener
	httpListener  net.Listener
	httpsListener net.Listener
}

// NewTestServer creates a new test server with random ports.
func NewTestServer() (*TestServer, error) {
	tmpDir, err := os.MkdirTemp("", "juicemud-integration-*")
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	config := server.Config{
		SSHAddr:   "127.0.0.1:0",
		HTTPSAddr: "127.0.0.1:0",
		HTTPAddr:  "127.0.0.1:0",
		Hostname:  "localhost",
		Dir:       tmpDir,
	}

	srv, err := server.New(ctx, config)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, err
	}

	// Create listeners with random ports
	sshLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		srv.Close()
		os.RemoveAll(tmpDir)
		return nil, err
	}

	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		sshLn.Close()
		srv.Close()
		os.RemoveAll(tmpDir)
		return nil, err
	}

	httpsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		httpLn.Close()
		sshLn.Close()
		srv.Close()
		os.RemoveAll(tmpDir)
		return nil, err
	}

	ts := &TestServer{
		Server:        srv,
		tmpDir:        tmpDir,
		sshListener:   sshLn,
		httpListener:  httpLn,
		httpsListener: httpsLn,
	}

	// Start server in background
	go func() {
		srv.StartWithListeners(sshLn, httpLn, httpsLn)
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
	ts.Server.Close()
	os.RemoveAll(ts.tmpDir)
}

// SSHAddr returns the SSH address.
func (ts *TestServer) SSHAddr() string {
	return ts.sshListener.Addr().String()
}

// HTTPAddr returns the HTTP address.
func (ts *TestServer) HTTPAddr() string {
	return ts.httpListener.Addr().String()
}

// waitForSourceObject polls until an object with the given source path exists.
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

// waitForObjectLocation polls until the object is at the expected location.
func (ts *TestServer) waitForObjectLocation(ctx context.Context, objectID, expectedLocation string, timeout time.Duration) bool {
	return waitForCondition(timeout, 50*time.Millisecond, func() bool {
		obj, err := ts.Storage().AccessObject(ctx, objectID, nil)
		if err != nil {
			return false
		}
		return obj.GetLocation() == expectedLocation
	})
}
