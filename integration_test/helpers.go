package integration_test

import (
	"context"
	"fmt"
	"time"
)

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
