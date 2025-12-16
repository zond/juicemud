package integration_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

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

	if err := tc.sendLine(fmt.Sprintf("/create %s", sourcePath)); err != nil {
		return "", fmt.Errorf("/create room: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return "", fmt.Errorf("/create room did not complete")
	}

	roomID, found := tc.waitForObject(fmt.Sprintf("*%s room*", testName), defaultWaitTimeout)
	if !found {
		return "", fmt.Errorf("%s room was not created", testName)
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
