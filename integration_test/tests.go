package integration_test

import (
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zond/juicemud/server"
	cryptossh "golang.org/x/crypto/ssh"
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

// terminalClient wraps an SSH session for testing.
type terminalClient struct {
	conn    *cryptossh.Client
	session *cryptossh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	readCh  chan readResult
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
	}
	tc.startReader()
	return tc, nil
}

func (tc *terminalClient) sendLine(s string) error {
	if _, err := tc.stdin.Write([]byte(s + "\r")); err != nil {
		return fmt.Errorf("write failed: %w", err)
	}
	return nil
}

// terminalClient uses a background goroutine to continuously read from stdout
// into a buffer, which allows non-blocking reads with timeouts.
type readResult struct {
	data []byte
	err  error
}

// startReader starts a background reader goroutine. Must be called once after creating terminalClient.
func (tc *terminalClient) startReader() {
	tc.readCh = make(chan readResult, 100)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := tc.stdout.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				tc.readCh <- readResult{data: data}
			}
			if err != nil {
				tc.readCh <- readResult{err: err}
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
				return result.String()
			}
			result.Write(r.data)
			if match != nil && match(result.String()) {
				return result.String()
			}
		case <-time.After(remaining):
			return result.String()
		}
	}
	return result.String()
}

// drain reads and discards output for a short time.
func (tc *terminalClient) drain() string {
	return tc.readUntil(200*time.Millisecond, nil)
}

// waitFor reads until the expected string appears or timeout.
func (tc *terminalClient) waitFor(expected string, timeout time.Duration) (string, bool) {
	output := tc.readUntil(timeout, func(s string) bool {
		return strings.Contains(s, expected)
	})
	return output, strings.Contains(output, expected)
}

func (tc *terminalClient) Close() {
	tc.stdin.Close()
	tc.session.Close()
	tc.conn.Close()
}

// webDAVClient wraps HTTP client for WebDAV operations with digest auth.
type webDAVClient struct {
	baseURL  string
	username string
	password string
	client   *http.Client
}

func newWebDAVClient(addr, username, password string) *webDAVClient {
	return &webDAVClient{
		baseURL:  "http://" + addr,
		username: username,
		password: password,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

func (w *webDAVClient) doWithAuth(method, path string, body io.Reader) (*http.Response, error) {
	url := w.baseURL + path

	// First request to get the challenge
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	resp.Body.Close()

	// Parse WWW-Authenticate header
	authHeader := resp.Header.Get("WWW-Authenticate")
	if authHeader == "" {
		return nil, fmt.Errorf("no WWW-Authenticate header")
	}

	params := parseDigestChallenge(authHeader)

	// Create authenticated request
	req, err = http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	// Compute digest response
	ha1 := md5Hash(fmt.Sprintf("%s:%s:%s", w.username, params["realm"], w.password))
	ha2 := md5Hash(fmt.Sprintf("%s:%s", method, path))
	nc := "00000001"
	cnonce := "abcdef"
	response := md5Hash(fmt.Sprintf("%s:%s:%s:%s:%s:%s", ha1, params["nonce"], nc, cnonce, "auth", ha2))

	authValue := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", qop=auth, nc=%s, cnonce="%s", response="%s", opaque="%s"`,
		w.username, params["realm"], params["nonce"], path, nc, cnonce, response, params["opaque"])

	req.Header.Set("Authorization", authValue)

	return w.client.Do(req)
}

func (w *webDAVClient) Put(path, content string) error {
	resp, err := w.doWithAuth("PUT", path, strings.NewReader(content))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PUT %s failed: %d %s", path, resp.StatusCode, string(body))
	}
	return nil
}

func (w *webDAVClient) Get(path string) (string, error) {
	resp, err := w.doWithAuth("GET", path, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s failed: %d", path, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func parseDigestChallenge(header string) map[string]string {
	params := make(map[string]string)
	header = strings.TrimPrefix(header, "Digest ")
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			params[strings.ToLower(kv[0])] = strings.Trim(kv[1], "\"")
		}
	}
	return params
}

func md5Hash(data string) string {
	hash := md5.Sum([]byte(data))
	return hex.EncodeToString(hash[:])
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

// createUser creates a new user via SSH and returns a terminal client logged in as that user.
func createUser(sshAddr, username, password string) (*terminalClient, error) {
	tc, err := newTerminalClient(sshAddr)
	if err != nil {
		return nil, err
	}
	tc.drain()
	if err := tc.sendLine("create user"); err != nil {
		tc.Close()
		return nil, err
	}
	tc.drain()
	if err := tc.sendLine(username); err != nil {
		tc.Close()
		return nil, err
	}
	tc.drain()
	if err := tc.sendLine(password); err != nil {
		tc.Close()
		return nil, err
	}
	tc.drain()
	if err := tc.sendLine(password); err != nil {
		tc.Close()
		return nil, err
	}
	tc.drain()
	if err := tc.sendLine("y"); err != nil {
		tc.Close()
		return nil, err
	}
	tc.drain()
	return tc, nil
}

// makeUserWizard grants wizard and owner privileges to a user.
func makeUserWizard(ts *TestServer, username string) error {
	ctx := context.Background()
	user, err := ts.Storage().LoadUser(ctx, username)
	if err != nil {
		return fmt.Errorf("failed to load user %s: %w", username, err)
	}
	user.Owner = true
	if err := ts.Storage().StoreUser(ctx, user, true); err != nil {
		return fmt.Errorf("failed to make user %s owner: %w", username, err)
	}
	if err := ts.Storage().AddUserToGroup(ctx, user, "wizards"); err != nil {
		return fmt.Errorf("failed to add user %s to wizards: %w", username, err)
	}
	return nil
}

// loginUser logs in as an existing user via SSH.
func loginUser(sshAddr, username, password string) (*terminalClient, error) {
	tc, err := newTerminalClient(sshAddr)
	if err != nil {
		return nil, err
	}
	tc.drain()
	if err := tc.sendLine("login user"); err != nil {
		tc.Close()
		return nil, err
	}
	tc.drain()
	if err := tc.sendLine(username); err != nil {
		tc.Close()
		return nil, err
	}
	tc.drain()
	if err := tc.sendLine(password); err != nil {
		tc.Close()
		return nil, err
	}
	// Wait for the prompt to appear, indicating the server is ready for commands
	if _, ok := tc.waitFor(">", 5*time.Second); !ok {
		tc.Close()
		return nil, fmt.Errorf("login did not complete (no prompt)")
	}
	return tc, nil
}

// RunAll runs all integration tests in sequence on a single server.
// Returns nil on success, or an error describing what failed.
func RunAll(ts *TestServer) error {
	ctx := context.Background()

	// === Test 1: User creation and login ===
	fmt.Println("Testing user creation and login...")

	// Create user
	tc, err := createUser(ts.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		return fmt.Errorf("createUser: %w", err)
	}
	if err := tc.sendLine("look"); err != nil {
		tc.Close()
		return fmt.Errorf("look command: %w", err)
	}
	// Verify "look" command produces output containing the genesis room description
	if output, ok := tc.waitFor("Black cosmos", 2*time.Second); !ok {
		tc.Close()
		return fmt.Errorf("look command did not show genesis room: %q", output)
	}
	tc.Close()

	// Verify user was persisted
	user, err := ts.Storage().LoadUser(ctx, "testuser")
	if err != nil {
		return fmt.Errorf("user not persisted: %w", err)
	}
	if user.Object == "" {
		return fmt.Errorf("user has no associated object")
	}

	// Verify object exists in genesis
	obj, err := ts.Storage().AccessObject(ctx, user.Object, nil)
	if err != nil {
		return fmt.Errorf("user object not found: %w", err)
	}
	if obj.GetLocation() != "genesis" {
		return fmt.Errorf("user object not in genesis: got %q", obj.GetLocation())
	}

	// Test reconnection
	tc, err = loginUser(ts.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		return fmt.Errorf("loginUser: %w", err)
	}
	if err := tc.sendLine("look"); err != nil {
		tc.Close()
		return fmt.Errorf("look command on reconnect: %w", err)
	}
	if output, ok := tc.waitFor("Black cosmos", 2*time.Second); !ok {
		tc.Close()
		return fmt.Errorf("look command on reconnect did not show genesis room: %q", output)
	}
	tc.Close()

	// Verify same object
	user2, err := ts.Storage().LoadUser(ctx, "testuser")
	if err != nil {
		return fmt.Errorf("user not found on reconnect: %w", err)
	}
	if user2.Object != user.Object {
		return fmt.Errorf("user object changed: %s -> %s", user.Object, user2.Object)
	}

	fmt.Println("  User creation and login: OK")

	// === Test 2: WebDAV file operations ===
	fmt.Println("Testing WebDAV file operations...")

	// Make testuser an owner for file system access
	user.Owner = true
	if err := ts.Storage().StoreUser(ctx, user, true); err != nil {
		return fmt.Errorf("failed to make testuser owner: %w", err)
	}

	// Test WebDAV operations
	dav := newWebDAVClient(ts.HTTPAddr(), "testuser", "testpass123")

	// Read existing file
	content, err := dav.Get("/user.js")
	if err != nil {
		return fmt.Errorf("failed to GET /user.js: %w", err)
	}
	if !strings.Contains(content, "connected") {
		return fmt.Errorf("user.js doesn't contain expected content: %s", content)
	}

	// Create a new source file
	roomSource := `// Test room
setDescriptions([{
	Short: 'Test Room',
	Unique: true,
	Long: 'A room created for testing.',
}]);
`
	if err := dav.Put("/testroom.js", roomSource); err != nil {
		return fmt.Errorf("failed to PUT /testroom.js: %w", err)
	}

	// Verify file was created
	readBack, err := dav.Get("/testroom.js")
	if err != nil {
		return fmt.Errorf("failed to GET /testroom.js: %w", err)
	}
	if readBack != roomSource {
		return fmt.Errorf("file content mismatch: got %q, want %q", readBack, roomSource)
	}

	fmt.Println("  WebDAV file operations: OK")

	// === Test 3: Wizard commands ===
	fmt.Println("Testing wizard commands...")

	// Make testuser a wizard
	if err := makeUserWizard(ts, "testuser"); err != nil {
		return err
	}

	// Create a source file for objects
	boxSource := `// A simple box
setDescriptions([{
	Short: 'wooden box',
	Long: 'A simple wooden box.',
}]);
`
	if err := dav.Put("/box.js", boxSource); err != nil {
		return fmt.Errorf("failed to create /box.js: %w", err)
	}

	// Login as wizard to test wizard commands
	tc, err = loginUser(ts.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		return fmt.Errorf("loginUser as wizard: %w", err)
	}

	// Create an object
	if err := tc.sendLine("/create /box.js"); err != nil {
		tc.Close()
		return fmt.Errorf("/create command: %w", err)
	}
	tc.drain()

	// Poll for object creation
	if _, found := ts.waitForSourceObject(ctx, "/box.js", 2*time.Second); !found {
		tc.Close()
		return fmt.Errorf("box object was not created")
	}

	// Test /inspect
	if err := tc.sendLine("/inspect"); err != nil {
		tc.Close()
		return fmt.Errorf("/inspect command: %w", err)
	}
	tc.drain()

	// Test /ls
	if err := tc.sendLine("/ls /"); err != nil {
		tc.Close()
		return fmt.Errorf("/ls command: %w", err)
	}
	tc.drain()

	fmt.Println("  Wizard commands: OK")

	// === Test 4: Movement between rooms ===
	fmt.Println("Testing movement...")

	// Create room sources
	room1Source := `// Room 1
setDescriptions([{
	Short: 'Room One',
	Unique: true,
	Long: 'The first test room.',
}]);
`
	room2Source := `// Room 2
setDescriptions([{
	Short: 'Room Two',
	Unique: true,
	Long: 'The second test room.',
}]);
`
	if err := dav.Put("/room1.js", room1Source); err != nil {
		tc.Close()
		return fmt.Errorf("failed to create /room1.js: %w", err)
	}
	if err := dav.Put("/room2.js", room2Source); err != nil {
		tc.Close()
		return fmt.Errorf("failed to create /room2.js: %w", err)
	}

	// Create rooms
	if err := tc.sendLine("/create /room1.js"); err != nil {
		tc.Close()
		return fmt.Errorf("/create room1: %w", err)
	}
	tc.drain()
	if err := tc.sendLine("/create /room2.js"); err != nil {
		tc.Close()
		return fmt.Errorf("/create room2: %w", err)
	}
	tc.drain()

	// Poll for room creation
	room1ID, found := ts.waitForSourceObject(ctx, "/room1.js", 2*time.Second)
	if !found {
		tc.Close()
		return fmt.Errorf("room1 was not created")
	}
	if _, found := ts.waitForSourceObject(ctx, "/room2.js", 2*time.Second); !found {
		tc.Close()
		return fmt.Errorf("room2 was not created")
	}

	// Move into room1 using /enter
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", room1ID)); err != nil {
		tc.Close()
		return fmt.Errorf("/enter command: %w", err)
	}
	tc.drain()

	// Poll for user to be in room1
	if !ts.waitForObjectLocation(ctx, user.Object, room1ID, 2*time.Second) {
		tc.Close()
		return fmt.Errorf("user did not move to room1")
	}

	// Exit back out
	if err := tc.sendLine("/exit"); err != nil {
		tc.Close()
		return fmt.Errorf("/exit command: %w", err)
	}
	tc.drain()

	// Poll for user to be back in genesis
	if !ts.waitForObjectLocation(ctx, user.Object, "genesis", 2*time.Second) {
		tc.Close()
		return fmt.Errorf("user did not return to genesis")
	}

	tc.Close()

	fmt.Println("  Movement: OK")

	return nil
}
