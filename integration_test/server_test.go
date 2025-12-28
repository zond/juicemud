package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	cryptossh "golang.org/x/crypto/ssh"
	"github.com/zond/juicemud/server"
)

const (
	// defaultWaitTimeout is the default timeout for wait operations.
	// This is intentionally generous to avoid flaky tests on slow systems.
	// On fast systems, polling returns immediately when the condition is met.
	defaultWaitTimeout = 5 * time.Second
)

// debugSSH enables verbose SSH output when INTEGRATION_DEBUG_SSH=1
var debugSSH = os.Getenv("INTEGRATION_DEBUG_SSH") == "1"

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

// SourcesDir returns the path to the sources directory.
// This returns the actual resolved sources directory (e.g., src/v0.1.0/).
func (ts *TestServer) SourcesDir() string {
	return ts.Storage().SourcesDir()
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

// terminalClient wraps an SSH session for testing.
type terminalClient struct {
	conn      *cryptossh.Client
	session   *cryptossh.Session
	stdin     io.WriteCloser
	stdout    io.Reader
	readCh    chan readResult
	done      chan struct{}
	closeOnce sync.Once
}

// readResult holds data from the background reader goroutine.
type readResult struct {
	data []byte
	err  error
}

func newTerminalClient(addr string) (*terminalClient, error) {
	config := &cryptossh.ClientConfig{
		User: "test",
		Auth: []cryptossh.AuthMethod{cryptossh.Password("ignored")},
		// InsecureIgnoreHostKey is acceptable here because we're connecting to a
		// test server we just started with a freshly generated key.
		HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	conn, err := cryptossh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("failed to dial SSH: %w", err)
	}

	session, err := conn.NewSession()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		conn.Close()
		return nil, fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		conn.Close()
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	if err := session.RequestPty("xterm", 24, 80, cryptossh.TerminalModes{}); err != nil {
		session.Close()
		conn.Close()
		return nil, fmt.Errorf("failed to request pty: %w", err)
	}

	if err := session.Shell(); err != nil {
		session.Close()
		conn.Close()
		return nil, fmt.Errorf("failed to start shell: %w", err)
	}

	tc := &terminalClient{
		conn:    conn,
		session: session,
		stdin:   stdin,
		stdout:  stdout,
		done:    make(chan struct{}),
	}
	tc.startReader()
	return tc, nil
}

func (tc *terminalClient) sendLine(s string) error {
	if debugSSH {
		fmt.Printf("[SSH DEBUG] sending: %q\n", s)
	}
	if _, err := tc.stdin.Write([]byte(s + "\r")); err != nil {
		return fmt.Errorf("write failed: %w", err)
	}
	return nil
}

// startReader starts a background reader goroutine. Must be called once after creating terminalClient.
func (tc *terminalClient) startReader() {
	tc.readCh = make(chan readResult, 100)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := tc.stdout.Read(buf)
			data := make([]byte, n)
			copy(data, buf[:n])
			select {
			case tc.readCh <- readResult{data: data, err: err}:
			case <-tc.done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
}

// readUntil reads from stdout until the timeout expires or the match function returns true.
// Returns all data read. If match is nil, just reads until timeout.
func (tc *terminalClient) readUntil(timeout time.Duration, match func(string) bool) string {
	var result strings.Builder
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}

		select {
		case r := <-tc.readCh:
			if r.err != nil {
				if debugSSH {
					fmt.Printf("[SSH DEBUG] read error: %v\n", r.err)
				}
				return result.String()
			}
			if debugSSH {
				fmt.Printf("[SSH DEBUG] received: %q\n", string(r.data))
			}
			result.Write(r.data)
			if match != nil && match(result.String()) {
				return result.String()
			}
		case <-time.After(remaining):
			if debugSSH {
				fmt.Printf("[SSH DEBUG] timeout waiting, got so far: %q\n", result.String())
			}
			return result.String()
		}
	}
	return result.String()
}

// waitFor reads until the expected string appears or timeout.
func (tc *terminalClient) waitFor(expected string, timeout time.Duration) (string, bool) {
	output := tc.readUntil(timeout, func(s string) bool {
		return strings.Contains(s, expected)
	})
	return output, strings.Contains(output, expected)
}

// waitForPrompt waits for the command prompt to appear, indicating the server is ready for input.
// Returns all output received up to and including the prompt.
// Waits until the output ENDS with a prompt to avoid partial reads.
func (tc *terminalClient) waitForPrompt(timeout time.Duration) (string, bool) {
	output := tc.readUntil(timeout, func(s string) bool {
		return strings.HasSuffix(s, "\n> ") || strings.HasSuffix(s, "\r\n> ")
	})
	return output, strings.HasSuffix(output, "\n> ") || strings.HasSuffix(output, "\r\n> ")
}

// sendCommand sends a command and waits for the response.
// Returns the server's response (everything between the echoed command and the next prompt).
// This function is robust against stale async notifications that may be buffered.
func (tc *terminalClient) sendCommand(cmd string, timeout time.Duration) (string, bool) {
	if err := tc.sendLine(cmd); err != nil {
		return "", false
	}

	// Wait for the echoed command AND the trailing prompt.
	// This ensures we don't match stale notifications that happen to end with "> ".
	output := tc.readUntil(timeout, func(s string) bool {
		hasPrompt := strings.HasSuffix(s, "\n> ") || strings.HasSuffix(s, "\r\n> ")
		hasEcho := strings.Contains(s, cmd+"\r\n")
		return hasPrompt && hasEcho
	})

	// Check if we got a valid response
	if !strings.Contains(output, cmd+"\r\n") {
		return output, false
	}
	if !strings.HasSuffix(output, "\n> ") && !strings.HasSuffix(output, "\r\n> ") {
		return output, false
	}

	// Find the echoed command and extract everything after it.
	// Use LastIndex to handle any stale echoes that might be in the buffer.
	echoIdx := strings.LastIndex(output, cmd+"\r\n")
	if echoIdx >= 0 {
		output = output[echoIdx+len(cmd)+2:]
	}

	// Strip the trailing prompt
	if strings.HasSuffix(output, "\n> ") {
		output = output[:len(output)-3]
	} else if strings.HasSuffix(output, "\r\n> ") {
		output = output[:len(output)-4]
	}
	return output, true
}

func (tc *terminalClient) Close() {
	tc.closeOnce.Do(func() {
		close(tc.done)
	})
	tc.stdin.Close()
	tc.session.Close()
	tc.conn.Close()
}

// inspectResult holds the parsed JSON from /inspect command.
// Only includes fields we need for testing.
// Note: Object.MarshalJSON serializes the Unsafe fields directly (no wrapper).
type inspectResult struct {
	ID         string `json:"Id"`
	Location   string `json:"Location"`
	SourcePath string `json:"SourcePath"`
}

// Helper methods to access fields
func (r *inspectResult) GetID() string         { return r.ID }
func (r *inspectResult) GetLocation() string   { return r.Location }
func (r *inspectResult) GetSourcePath() string { return r.SourcePath }

// jsonExtractor matches the JSON object in /inspect output.
// Uses greedy matching which works correctly here because /inspect outputs
// exactly one well-formed JSON object with no stray braces in the output.
var jsonExtractor = regexp.MustCompile(`(?s)\{.*\}`)

// createdIDExtractor matches the object ID in /create output (e.g., "Created #abc123_XYZ").
// Object IDs use base64url encoding which includes alphanumeric, underscore, and hyphen.
var createdIDExtractor = regexp.MustCompile(`Created #([a-zA-Z0-9_-]+)`)

// createObject runs /create and returns the created object's ID.
// Waits for the object to be fully ready (inspectable by ID) before returning.
// Returns the object ID and nil on success, or empty string and an error on failure.
func (tc *terminalClient) createObject(sourcePath string) (string, error) {
	output, ok := tc.sendCommand(fmt.Sprintf("/create %s", sourcePath), defaultWaitTimeout)
	if !ok {
		return "", fmt.Errorf("/create did not complete: %q", output)
	}
	match := createdIDExtractor.FindStringSubmatch(output)
	if match == nil {
		return "", fmt.Errorf("no object ID in /create output: %q", output)
	}
	objectID := match[1]

	// Wait for the object to be fully ready by polling /inspect #<id>
	// This ensures the object is accessible for subsequent commands like /enter or /move
	found := waitForCondition(defaultWaitTimeout, 50*time.Millisecond, func() bool {
		_, err := tc.inspect(fmt.Sprintf("#%s", objectID))
		return err == nil
	})
	if !found {
		return "", fmt.Errorf("object #%s was created but not inspectable", objectID)
	}
	return objectID, nil
}

// waitForObject polls via /inspect until an object matching the pattern exists in the room.
// The pattern uses glob matching against object descriptions (e.g., "*box*" matches "wooden box").
// Returns the object ID and true if found, or empty string and false on timeout.
func (tc *terminalClient) waitForObject(pattern string, timeout time.Duration) (string, bool) {
	var objectID string
	found := waitForCondition(timeout, 50*time.Millisecond, func() bool {
		result, err := tc.inspect(pattern)
		if err != nil {
			return false
		}
		objectID = result.GetID()
		return objectID != ""
	})
	return objectID, found
}

// inspect runs /inspect on the given target (or "self" if empty) and parses the result.
func (tc *terminalClient) inspect(target string) (*inspectResult, error) {
	cmd := "/inspect"
	if target != "" {
		cmd = fmt.Sprintf("/inspect '%s'", target)
	}
	output, ok := tc.sendCommand(cmd, defaultWaitTimeout)
	if !ok {
		return nil, fmt.Errorf("inspect command did not complete: %q", output)
	}
	// Extract JSON from output
	jsonMatch := jsonExtractor.FindString(output)
	if jsonMatch == "" {
		return nil, fmt.Errorf("no JSON found in inspect output: %q", output)
	}
	var result inspectResult
	if err := json.Unmarshal([]byte(jsonMatch), &result); err != nil {
		return nil, fmt.Errorf("parsing inspect JSON: %w (raw: %q)", err, jsonMatch)
	}
	return &result, nil
}

// waitForLocation polls via /inspect until the object is at the expected location.
// Use empty string as target to inspect the current user's object, or "#<id>" for other objects.
func (tc *terminalClient) waitForLocation(target, expectedLocation string, timeout time.Duration) bool {
	return waitForCondition(timeout, 50*time.Millisecond, func() bool {
		result, err := tc.inspect(target)
		if err != nil {
			return false
		}
		return result.GetLocation() == expectedLocation
	})
}

// getLocation returns the current location of an object via /inspect.
// Use empty string as target to inspect the current user's object, or "#<id>" for other objects.
// Returns empty string on error.
func (tc *terminalClient) getLocation(target string) string {
	result, err := tc.inspect(target)
	if err != nil {
		return ""
	}
	return result.GetLocation()
}

// waitForSourcePath polls via /inspect until the object has the expected SourcePath.
// Use "#<id>" as target to inspect a specific object.
func (tc *terminalClient) waitForSourcePath(target, expectedPath string, timeout time.Duration) bool {
	return waitForCondition(timeout, 50*time.Millisecond, func() bool {
		result, err := tc.inspect(target)
		if err != nil {
			return false
		}
		return result.GetSourcePath() == expectedPath
	})
}

// waitForLookMatch polls with look commands until the output contains the expected string.
// Drains any buffered data after success to prevent interference with subsequent commands.
func (tc *terminalClient) waitForLookMatch(expected string, timeout time.Duration) (string, bool) {
	var lastOutput string
	found := waitForCondition(timeout, 100*time.Millisecond, func() bool {
		output, ok := tc.sendCommand("look", defaultWaitTimeout)
		if !ok {
			return false
		}
		lastOutput = output
		return strings.Contains(output, expected)
	})
	if found {
		// Non-blocking drain of any buffered data that arrived during polling
		tc.readUntil(10*time.Millisecond, nil)
	}
	return lastOutput, found
}

// waitForLookMatchFunc polls with look commands until the match function returns true.
// Drains any buffered data after success to prevent interference with subsequent commands.
func (tc *terminalClient) waitForLookMatchFunc(match func(string) bool, timeout time.Duration) (string, bool) {
	var lastOutput string
	found := waitForCondition(timeout, 100*time.Millisecond, func() bool {
		output, ok := tc.sendCommand("look", defaultWaitTimeout)
		if !ok {
			return false
		}
		lastOutput = output
		return match(output)
	})
	if found {
		// Non-blocking drain of any buffered data that arrived during polling
		tc.readUntil(10*time.Millisecond, nil)
	}
	return lastOutput, found
}

// waitForScanMatch polls with scan commands until the output contains the expected string.
// Drains any buffered data after success to prevent interference with subsequent commands.
// This handles stale movement notifications that may arrive asynchronously.
func (tc *terminalClient) waitForScanMatch(expected string, timeout time.Duration) (string, bool) {
	var lastOutput string
	found := waitForCondition(timeout, 100*time.Millisecond, func() bool {
		output, ok := tc.sendCommand("scan", defaultWaitTimeout)
		if !ok {
			return false
		}
		lastOutput = output
		return strings.Contains(output, expected)
	})
	if found {
		// Non-blocking drain of any buffered data that arrived during polling
		tc.readUntil(10*time.Millisecond, nil)
	}
	return lastOutput, found
}

// waitForCondition polls until the condition returns true or timeout expires.
func waitForCondition(timeout time.Duration, interval time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(interval)
	}
	return false
}

// createUser creates a new user via SSH and returns a terminal client logged in as that user.
func createUser(sshAddr, username, password string) (*terminalClient, error) {
	tc, err := newTerminalClient(sshAddr)
	if err != nil {
		return nil, err
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		tc.Close()
		return nil, fmt.Errorf("did not get initial prompt")
	}
	if err := tc.sendLine("create user"); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		tc.Close()
		return nil, fmt.Errorf("create user prompt did not appear")
	}
	if err := tc.sendLine(username); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		tc.Close()
		return nil, fmt.Errorf("username prompt did not appear")
	}
	if err := tc.sendLine(password); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		tc.Close()
		return nil, fmt.Errorf("password prompt did not appear")
	}
	if err := tc.sendLine(password); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		tc.Close()
		return nil, fmt.Errorf("confirm password prompt did not appear")
	}
	if err := tc.sendLine("y"); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		tc.Close()
		return nil, fmt.Errorf("final prompt after user creation did not appear")
	}
	return tc, nil
}

// loginUser logs in as an existing user via SSH.
func loginUser(sshAddr, username, password string) (*terminalClient, error) {
	tc, err := newTerminalClient(sshAddr)
	if err != nil {
		return nil, err
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		tc.Close()
		return nil, fmt.Errorf("did not get initial prompt")
	}
	if err := tc.sendLine("login user"); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		tc.Close()
		return nil, fmt.Errorf("login user prompt did not appear")
	}
	if err := tc.sendLine(username); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		tc.Close()
		return nil, fmt.Errorf("username prompt did not appear")
	}
	if err := tc.sendLine(password); err != nil {
		tc.Close()
		return nil, err
	}
	// Wait for the prompt to appear, indicating the server is ready for commands
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		tc.Close()
		return nil, fmt.Errorf("login did not complete (no prompt)")
	}
	return tc, nil
}

// enterIsolatedRoom creates a new room inside genesis for the test and moves the player into it.
// This prevents action name collisions between tests since actions only dispatch to siblings.
// Returns the room ID or an error.
func (tc *terminalClient) enterIsolatedRoom(ts *TestServer, testName string) (string, error) {
	// First ensure we're in genesis so the room is created there
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return "", fmt.Errorf("/enter genesis: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return "", fmt.Errorf("/enter genesis did not complete")
	}
	// Verify we're actually in genesis
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		return "", fmt.Errorf("failed to enter genesis")
	}

	roomSource := fmt.Sprintf(`setDescriptions([{Short: '%s room', Long: 'Isolated test room for %s'}]);
setExits([{Name: 'out', Destination: 'genesis'}]);
`, testName, testName)

	sourcePath := fmt.Sprintf("/%s_room.js", testName)
	if err := ts.WriteSource(sourcePath, roomSource); err != nil {
		return "", fmt.Errorf("failed to create %s: %w", sourcePath, err)
	}

	roomID, err := tc.createObject(sourcePath)
	if err != nil {
		return "", fmt.Errorf("create room: %w", err)
	}

	if err := tc.sendLine(fmt.Sprintf("/enter #%s", roomID)); err != nil {
		return "", fmt.Errorf("/enter room: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return "", fmt.Errorf("/enter room did not complete")
	}
	// Verify we're actually in the new room
	if !tc.waitForLocation("", roomID, defaultWaitTimeout) {
		return "", fmt.Errorf("failed to enter %s room", testName)
	}

	return roomID, nil
}
