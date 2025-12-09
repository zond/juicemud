package integration_test

import (
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	cryptossh "golang.org/x/crypto/ssh"
)

// terminalClient wraps an SSH session for testing.
type terminalClient struct {
	conn    *cryptossh.Client
	session *cryptossh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	readCh  chan readResult
	done    chan struct{}
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
	close(tc.done)
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
