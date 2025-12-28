package integration_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zond/juicemud/storage"
)

var (
	testServer    *TestServer
	wizardClient  *terminalClient
	wizardUser    *storage.User
	sourceCounter atomic.Int64
)

// TestMain sets up the shared test server, wizard user, and connection for all integration tests.
func TestMain(m *testing.M) {
	ctx := context.Background()

	var err error
	testServer, err = NewTestServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create test server: %v\n", err)
		os.Exit(1)
	}
	defer testServer.Close()

	// Create and setup wizard user
	tc, err := createUser(testServer.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create test user: %v\n", err)
		os.Exit(1)
	}
	tc.Close()

	wizardUser, err = testServer.Storage().LoadUser(ctx, "testuser")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load test user: %v\n", err)
		os.Exit(1)
	}
	wizardUser.Owner = true
	wizardUser.Wizard = true
	if err := testServer.Storage().StoreUser(ctx, wizardUser, true, ""); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to make testuser wizard: %v\n", err)
		os.Exit(1)
	}

	// Login as wizard and keep connection for tests
	wizardClient, err = loginUser(testServer.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to login as wizard: %v\n", err)
		os.Exit(1)
	}
	defer wizardClient.Close()

	os.Exit(m.Run())
}

// uniqueSourcePath returns a unique source path for test isolation.
func uniqueSourcePath(base string) string {
	n := sourceCounter.Add(1)
	return fmt.Sprintf("/%s_%d.js", base, n)
}

// TestAll runs all integration tests in sequence.
func TestAll(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	if err := RunAll(testServer); err != nil {
		t.Fatal(err)
	}
}

// TestSetTimeout tests setTimeout() delayed events.
func TestSetTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Timer updates its description when started and when timeout fires
	timerSource := `setDescriptions([{Short: 'timer orb (idle)'}]);
addCallback('start', ['action'], (msg) => {
	setDescriptions([{Short: 'timer orb (started)'}]);
	setTimeout(200, 'timeout', {});
});
addCallback('timeout', ['emit'], (msg) => {
	setDescriptions([{Short: 'timer orb (fired)'}]);
});
`
	sourcePath := uniqueSourcePath("timer")
	if err := testServer.WriteSource(sourcePath, timerSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}

	if _, err := tc.createObject(sourcePath); err != nil {
		t.Fatalf("create timer: %v", err)
	}

	// Poll until timer is visible in room
	timerOutput, found := tc.waitForLookMatch("timer orb (idle)", defaultWaitTimeout)
	if !found {
		t.Fatalf("timer should be idle initially: %q", timerOutput)
	}

	// Start the timer
	if err := tc.sendLine("start timer orb"); err != nil {
		t.Fatalf("start timer orb command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("start timer orb command did not complete")
	}

	// Poll with look until we see the timer fired (setTimeout has 200ms delay)
	timerOutput, found = tc.waitForLookMatch("timer orb (fired)", defaultWaitTimeout)
	if !found {
		t.Fatalf("timer should show (fired) after timeout: %q", timerOutput)
	}
}

// TestStatsCommand tests the /stats wizard command for viewing JS statistics.
func TestStatsCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient

	// Test /stats summary (default)
	if err := tc.sendLine("/stats"); err != nil {
		t.Fatalf("/stats command: %v", err)
	}
	output, ok := tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/stats command did not complete: %q", output)
	}
	// Verify output shows expected fields
	if !strings.Contains(output, "JS Statistics") {
		t.Fatalf("/stats should show 'JS Statistics': %q", output)
	}
	if !strings.Contains(output, "EXECUTIONS") {
		t.Fatalf("/stats should show 'EXECUTIONS': %q", output)
	}
	if !strings.Contains(output, "ERRORS") {
		t.Fatalf("/stats should show 'ERRORS': %q", output)
	}

	// Test /stats summary explicitly
	if err := tc.sendLine("/stats summary"); err != nil {
		t.Fatalf("/stats summary command: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/stats summary did not complete: %q", output)
	}
	if !strings.Contains(output, "JS Statistics") {
		t.Fatalf("/stats summary should show 'JS Statistics': %q", output)
	}

	// Test /stats errors
	if err := tc.sendLine("/stats errors"); err != nil {
		t.Fatalf("/stats errors command: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/stats errors did not complete: %q", output)
	}
	// Should show "Error Summary" header
	if !strings.Contains(output, "Error Summary") {
		t.Fatalf("/stats errors should show 'Error Summary': %q", output)
	}

	// Test /stats errors locations
	if err := tc.sendLine("/stats errors locations"); err != nil {
		t.Fatalf("/stats errors locations command: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/stats errors locations did not complete: %q", output)
	}
	// Either shows "No error locations" or a table with Location header
	if !strings.Contains(output, "No error locations") && !strings.Contains(output, "Location") {
		t.Fatalf("/stats errors locations should show locations or 'No error locations': %q", output)
	}

	// Test /stats scripts
	if err := tc.sendLine("/stats scripts"); err != nil {
		t.Fatalf("/stats scripts command: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/stats scripts did not complete: %q", output)
	}
	// Either shows "No scripts recorded." or a table with Source Path header
	if !strings.Contains(output, "No scripts") && !strings.Contains(output, "Source Path") {
		t.Fatalf("/stats scripts should show scripts or 'No scripts': %q", output)
	}

	// Test /stats objects
	if err := tc.sendLine("/stats objects"); err != nil {
		t.Fatalf("/stats objects command: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/stats objects did not complete: %q", output)
	}
	// Either shows "No objects recorded." or a table with Object ID header
	if !strings.Contains(output, "No objects") && !strings.Contains(output, "Object ID") {
		t.Fatalf("/stats objects should show objects or 'No objects': %q", output)
	}

	// Test /stats perf slow
	if err := tc.sendLine("/stats perf slow"); err != nil {
		t.Fatalf("/stats perf slow command: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/stats perf slow did not complete: %q", output)
	}
	// Either shows "No slow executions recorded." or slow execution records
	if !strings.Contains(output, "No slow executions") && !strings.Contains(output, "]") {
		t.Fatalf("/stats perf slow should show slow execs or 'No slow executions': %q", output)
	}

	// Test /stats reset
	if err := tc.sendLine("/stats reset"); err != nil {
		t.Fatalf("/stats reset command: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/stats reset did not complete: %q", output)
	}
	if !strings.Contains(output, "Statistics reset") {
		t.Fatalf("/stats reset should confirm reset: %q", output)
	}

	// Verify reset worked by checking summary shows zero
	if err := tc.sendLine("/stats summary"); err != nil {
		t.Fatalf("/stats summary after reset: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/stats summary after reset did not complete: %q", output)
	}
	if !strings.Contains(output, "Total: 0") {
		t.Fatalf("/stats summary after reset should show 'Total: 0': %q", output)
	}

	// Test /stats help (unknown subcommand)
	if err := tc.sendLine("/stats help"); err != nil {
		t.Fatalf("/stats help command: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/stats help did not complete: %q", output)
	}
	if !strings.Contains(output, "usage:") {
		t.Fatalf("/stats help should show usage: %q", output)
	}
}

// TestStatePersistence tests that object state persists across executions.
func TestStatePersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Create an object that uses state to count "tap" actions
	counterSource := `setDescriptions([{Short: 'tap counter', Long: 'A counter that tracks taps.'}]);

// Initialize counter if not set
if (state.count === undefined) {
	state.count = 0;
}

addCallback('tap', ['action'], (msg) => {
	state.count += 1;
	setDescriptions([{Short: 'tap counter (' + state.count + ' taps)', Long: 'A counter that has been tapped ' + state.count + ' times.'}]);
});
`
	sourcePath := uniqueSourcePath("counter")
	if err := testServer.WriteSource(sourcePath, counterSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}

	// Create the counter object
	if _, err := tc.createObject(sourcePath); err != nil {
		t.Fatalf("create counter: %v", err)
	}

	// Tap the counter three times
	for i := 1; i <= 3; i++ {
		if err := tc.sendLine("tap"); err != nil {
			t.Fatalf("tap command %d: %v", i, err)
		}
		if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
			t.Fatalf("tap command %d did not complete", i)
		}

		// Wait for the description to update with the correct count
		expectedDesc := fmt.Sprintf("tap counter (%d taps)", i)
		output, found := tc.waitForLookMatch(expectedDesc, defaultWaitTimeout)
		if !found {
			t.Fatalf("state should persist count after tap %d, expected %q in output: %q", i, expectedDesc, output)
		}
	}
}

// TestCreatedEvent tests that the 'created' event is emitted with creator info.
func TestCreatedEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Create an object that captures creator info on creation
	createdSource := `setDescriptions([{Short: 'witness stone (waiting)'}]);
addCallback('created', ['emit'], (msg) => {
	if (msg.creator) {
		setDescriptions([{Short: 'witness stone (created by ' + msg.creator.Id + ')'}]);
	} else {
		setDescriptions([{Short: 'witness stone (no creator info)'}]);
	}
});
`
	sourcePath := uniqueSourcePath("witness")
	if err := testServer.WriteSource(sourcePath, createdSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}

	// The user's object ID is what we expect in the creator info
	userID := wizardUser.Object

	// Create the witness object
	if _, err := tc.createObject(sourcePath); err != nil {
		t.Fatalf("create witness: %v", err)
	}

	// Wait for the witness to appear with the creator's ID in its description
	output, found := tc.waitForLookMatch("witness stone (created by "+userID+")", defaultWaitTimeout)
	if !found {
		t.Fatalf("witness should show creator ID in description: %q", output)
	}
}

// TestLookTarget tests the look command with a target object.
func TestLookTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Create an object with both Short and Long descriptions
	tomeSource := `setDescriptions([{
	Short: 'dusty tome',
	Long: 'An ancient book bound in cracked leather. Strange symbols cover the spine, and the pages smell of forgotten ages.',
}]);
`
	sourcePath := uniqueSourcePath("tome")
	if err := testServer.WriteSource(sourcePath, tomeSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}

	// Create the tome object
	if _, err := tc.createObject(sourcePath); err != nil {
		t.Fatalf("create tome: %v", err)
	}

	// Look at the tome using a single word from its Short description
	if err := tc.sendLine("look tome"); err != nil {
		t.Fatalf("look tome: %v", err)
	}
	output, ok := tc.waitForPrompt(2 * time.Second)
	if !ok {
		t.Fatalf("look dusty tome did not complete: %q", output)
	}

	// Verify the output shows the object name
	if !strings.Contains(output, "dusty tome") {
		t.Fatalf("look should show object name 'dusty tome': %q", output)
	}

	// Verify the output shows the Long description
	if !strings.Contains(output, "ancient book bound in cracked leather") {
		t.Fatalf("look should show Long description: %q", output)
	}
	if !strings.Contains(output, "forgotten ages") {
		t.Fatalf("look should show full Long description: %q", output)
	}
}

// TestRemoveCommand tests the /remove wizard command.
func TestRemoveCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	removableSource := `setDescriptions([{Short: 'removable widget'}]);
`
	sourcePath := uniqueSourcePath("removable")
	if err := testServer.WriteSource(sourcePath, removableSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}

	removableID, err := tc.createObject(sourcePath)
	if err != nil {
		t.Fatalf("create removable: %v", err)
	}

	// Verify object exists via /inspect
	output, ok := tc.sendCommand(fmt.Sprintf("/inspect #%s", removableID), defaultWaitTimeout)
	if !ok || !strings.Contains(output, "removable widget") {
		t.Fatalf("removable object should exist before removal: %q", output)
	}

	// Remove the object
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", removableID)); err != nil {
		t.Fatalf("/remove command: %v", err)
	}
	if _, ok = tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/remove command did not complete")
	}

	// Verify object no longer exists via /inspect (should show error or empty)
	output, ok = tc.sendCommand(fmt.Sprintf("/inspect #%s", removableID), defaultWaitTimeout)
	if strings.Contains(output, "removable widget") {
		t.Fatalf("removable object should not exist after removal: %q", output)
	}

	// Test edge case: can't remove self - verify error message and we stay logged in
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", wizardUser.Object)); err != nil {
		t.Fatalf("/remove self command: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/remove self command did not complete: %q", output)
	}
	if !strings.Contains(output, "Can't remove yourself") {
		t.Fatalf("/remove self should show error 'Can't remove yourself': %q", output)
	}
	// Verify we're still logged in by checking we can look around
	if err := tc.sendLine("look"); err != nil {
		t.Fatalf("look after remove self: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("should still be logged in after failed self-removal: %q", output)
	}
}
