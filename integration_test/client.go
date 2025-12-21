package integration_test

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	cryptossh "golang.org/x/crypto/ssh"
)

// debugSSH enables verbose SSH output when INTEGRATION_DEBUG_SSH=1
var debugSSH = os.Getenv("INTEGRATION_DEBUG_SSH") == "1"

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
