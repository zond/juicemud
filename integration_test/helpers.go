package integration_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// inspectResult holds the parsed JSON from /inspect command.
// Only includes fields we need for testing.
type inspectResult struct {
	Unsafe struct {
		ID       string `json:"Id"`
		Location string `json:"Location"`
	} `json:"Unsafe"`
}

// Helper methods to access nested fields
func (r *inspectResult) GetID() string       { return r.Unsafe.ID }
func (r *inspectResult) GetLocation() string { return r.Unsafe.Location }

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
		cmd = fmt.Sprintf("/inspect %s", target)
	}
	if err := tc.sendLine(cmd); err != nil {
		return nil, fmt.Errorf("sending inspect command: %w", err)
	}
	output, ok := tc.waitForPrompt(2 * time.Second)
	if !ok {
		return nil, fmt.Errorf("inspect command did not complete: %q", output)
	}
	// Extract JSON from output (skip command echo and prompt)
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
// After finding the expected location, it drains any remaining buffered output to avoid
// interference with subsequent commands.
func (tc *terminalClient) waitForLocation(target, expectedLocation string, timeout time.Duration) bool {
	found := waitForCondition(timeout, 50*time.Millisecond, func() bool {
		result, err := tc.inspect(target)
		if err != nil {
			return false
		}
		return result.GetLocation() == expectedLocation
	})
	if found {
		// Drain any buffered output that might have arrived after the last prompt.
		// This prevents leftover data from the final /inspect from interfering
		// with subsequent commands.
		tc.readUntil(50*time.Millisecond, nil)
	}
	return found
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
	if _, ok := tc.waitForPrompt(5*time.Second); !ok {
		tc.Close()
		return nil, fmt.Errorf("did not get initial prompt")
	}
	if err := tc.sendLine("create user"); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		tc.Close()
		return nil, fmt.Errorf("create user prompt did not appear")
	}
	if err := tc.sendLine(username); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		tc.Close()
		return nil, fmt.Errorf("username prompt did not appear")
	}
	if err := tc.sendLine(password); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		tc.Close()
		return nil, fmt.Errorf("password prompt did not appear")
	}
	if err := tc.sendLine(password); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		tc.Close()
		return nil, fmt.Errorf("confirm password prompt did not appear")
	}
	if err := tc.sendLine("y"); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(5*time.Second); !ok {
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
	if _, ok := tc.waitForPrompt(5*time.Second); !ok {
		tc.Close()
		return nil, fmt.Errorf("did not get initial prompt")
	}
	if err := tc.sendLine("login user"); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		tc.Close()
		return nil, fmt.Errorf("login user prompt did not appear")
	}
	if err := tc.sendLine(username); err != nil {
		tc.Close()
		return nil, err
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		tc.Close()
		return nil, fmt.Errorf("username prompt did not appear")
	}
	if err := tc.sendLine(password); err != nil {
		tc.Close()
		return nil, err
	}
	// Wait for the prompt to appear, indicating the server is ready for commands
	if _, ok := tc.waitForPrompt(5*time.Second); !ok {
		tc.Close()
		return nil, fmt.Errorf("login did not complete (no prompt)")
	}
	return tc, nil
}
