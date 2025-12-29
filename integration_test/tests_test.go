package integration_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	goccy "github.com/goccy/go-json"
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

	// Table-driven tests for /stats subcommands
	tests := []struct {
		name     string
		cmd      string
		contains []string // All must be present
		either   []string // At least one must be present (if non-empty)
	}{
		{
			name:     "default shows summary",
			cmd:      "/stats",
			contains: []string{"JS Statistics", "EXECUTIONS", "ERRORS"},
		},
		{
			name:     "summary subcommand",
			cmd:      "/stats summary",
			contains: []string{"JS Statistics"},
		},
		{
			name:     "errors subcommand",
			cmd:      "/stats errors",
			contains: []string{"Error Summary"},
		},
		{
			name:   "errors locations",
			cmd:    "/stats errors locations",
			either: []string{"No error locations", "Location"},
		},
		{
			name:   "scripts subcommand",
			cmd:    "/stats scripts",
			either: []string{"No scripts", "Source Path"},
		},
		{
			name:   "objects subcommand",
			cmd:    "/stats objects",
			either: []string{"No objects", "Object ID"},
		},
		{
			name:   "perf slow",
			cmd:    "/stats perf slow",
			// Output is either "No slow executions recorded." or "[HH:MM:SS] #objID path Xms"
			either: []string{"No slow executions", "ms"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, ok := tc.sendCommand(tt.cmd, defaultWaitTimeout)
			if !ok {
				t.Fatalf("%s did not complete: %q", tt.cmd, output)
			}

			// Check all required strings are present
			for _, s := range tt.contains {
				if !strings.Contains(output, s) {
					t.Errorf("%s should contain %q: %q", tt.cmd, s, output)
				}
			}

			// Check at least one of the either strings is present
			if len(tt.either) > 0 {
				found := false
				for _, s := range tt.either {
					if strings.Contains(output, s) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("%s should contain one of %v: %q", tt.cmd, tt.either, output)
				}
			}
		})
	}

	// Test /stats reset separately since it changes state
	t.Run("reset clears statistics", func(t *testing.T) {
		output, ok := tc.sendCommand("/stats reset", defaultWaitTimeout)
		if !ok {
			t.Fatalf("/stats reset did not complete: %q", output)
		}
		if !strings.Contains(output, "Statistics reset") {
			t.Fatalf("/stats reset should confirm reset: %q", output)
		}

		// Verify reset worked
		output, ok = tc.sendCommand("/stats summary", defaultWaitTimeout)
		if !ok {
			t.Fatalf("/stats summary after reset did not complete: %q", output)
		}
		if !strings.Contains(output, "Total: 0") {
			t.Fatalf("/stats summary after reset should show 'Total: 0': %q", output)
		}
	})

	// Test /stats help (unknown subcommand)
	t.Run("help shows usage", func(t *testing.T) {
		output, ok := tc.sendCommand("/stats help", defaultWaitTimeout)
		if !ok {
			t.Fatalf("/stats help did not complete: %q", output)
		}
		if !strings.Contains(output, "usage:") {
			t.Fatalf("/stats help should show usage: %q", output)
		}
	})
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

// TestEmitInterObject tests emit() for inter-object communication.
func TestEmitInterObject(t *testing.T) {
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

	// Receiver updates its description when it receives a pong
	receiverSource := `setDescriptions([{Short: 'receiver orb (waiting)'}]);
addCallback('pong', ['emit'], (msg) => {
	setDescriptions([{Short: 'receiver orb (got: ' + msg.message + ')'}]);
});
`
	receiverPath := uniqueSourcePath("receiver")
	if err := testServer.WriteSource(receiverPath, receiverSource); err != nil {
		t.Fatalf("failed to create %s: %v", receiverPath, err)
	}

	// Sender takes target ID from msg.line and emits to it
	senderSource := `setDescriptions([{Short: 'sender orb'}]);
addCallback('ping', ['action'], (msg) => {
	const targetId = msg.line.replace(/^ping\s+/, '');
	emit(targetId, 'pong', {message: 'hello'});
	setDescriptions([{Short: 'sender orb (sent)'}]);
});
`
	senderPath := uniqueSourcePath("sender")
	if err := testServer.WriteSource(senderPath, senderSource); err != nil {
		t.Fatalf("failed to create %s: %v", senderPath, err)
	}

	receiverID, err := tc.createObject(receiverPath)
	if err != nil {
		t.Fatalf("create receiver: %v", err)
	}

	if _, err := tc.createObject(senderPath); err != nil {
		t.Fatalf("create sender: %v", err)
	}

	// Ping the sender with the receiver's ID as target
	if err := tc.sendLine(fmt.Sprintf("ping %s", receiverID)); err != nil {
		t.Fatalf("ping command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("ping receiver command did not complete")
	}

	// Poll with look until we see the receiver got the message (emit has ~100ms delay)
	lookOutput, found := tc.waitForLookMatch("receiver orb (got: hello)", defaultWaitTimeout)
	if !found {
		t.Fatalf("receiver did not update description after receiving emit: %q", lookOutput)
	}
	if !strings.Contains(lookOutput, "sender orb (sent)") {
		t.Fatalf("sender did not update description after emit: %q", lookOutput)
	}
}

// TestCircularContainerPrevention tests that circular container relationships are prevented.
func TestCircularContainerPrevention(t *testing.T) {
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

	// Create two container objects with distinct names
	containerASource := `// Container A
setDescriptions([{
	Short: 'outer box',
	Long: 'The outer container.',
}]);
`
	containerBSource := `// Container B
setDescriptions([{
	Short: 'inner box',
	Long: 'The inner container.',
}]);
`
	containerAPath := uniqueSourcePath("containerA")
	containerBPath := uniqueSourcePath("containerB")
	if err := testServer.WriteSource(containerAPath, containerASource); err != nil {
		t.Fatalf("failed to create %s: %v", containerAPath, err)
	}
	if err := testServer.WriteSource(containerBPath, containerBSource); err != nil {
		t.Fatalf("failed to create %s: %v", containerBPath, err)
	}

	// Create container A (outer box)
	containerAID, err := tc.createObject(containerAPath)
	if err != nil {
		t.Fatalf("create container A: %v", err)
	}

	// Create container B (inner box)
	containerBID, err := tc.createObject(containerBPath)
	if err != nil {
		t.Fatalf("create container B: %v", err)
	}

	// Get container A's original location before moving B into it
	containerAOriginalLoc := tc.getLocation(fmt.Sprintf("#%s", containerAID))
	if containerAOriginalLoc == "" {
		t.Fatal("could not determine container A's location")
	}

	// Move B into A (should succeed)
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", containerBID, containerAID)); err != nil {
		t.Fatalf("/move B into A: %v", err)
	}
	output, ok := tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/move B into A did not complete: %q", output)
	}
	// Verify B is now inside A
	if !tc.waitForLocation(fmt.Sprintf("#%s", containerBID), containerAID, defaultWaitTimeout) {
		t.Fatal("container B did not move into A")
	}

	// Try to move A into B (should fail - circular)
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", containerAID, containerBID)); err != nil {
		t.Fatalf("/move A into B: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/move A into B did not complete: %q", output)
	}
	// Should contain error about circular containment
	if !strings.Contains(output, "cannot move object into itself") {
		t.Fatalf("circular move should fail with error, got: %q", output)
	}

	// Verify A is still in its original location
	if !tc.waitForLocation(fmt.Sprintf("#%s", containerAID), containerAOriginalLoc, defaultWaitTimeout) {
		t.Fatalf("container A should still be in %s after failed circular move", containerAOriginalLoc)
	}

	// Test self-move: try to move A into A (should fail)
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", containerAID, containerAID)); err != nil {
		t.Fatalf("/move A into A: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/move A into A did not complete: %q", output)
	}
	if !strings.Contains(output, "cannot move object into itself") {
		t.Fatalf("self-move should fail with error, got: %q", output)
	}

	// Test deeper nesting: A contains B, B contains C, try to move A into C
	containerCSource := `// Container C
setDescriptions([{
	Short: 'deep box',
	Long: 'The deepest container.',
}]);
`
	containerCPath := uniqueSourcePath("containerC")
	if err := testServer.WriteSource(containerCPath, containerCSource); err != nil {
		t.Fatalf("failed to create %s: %v", containerCPath, err)
	}
	containerCID, err := tc.createObject(containerCPath)
	if err != nil {
		t.Fatalf("create container C: %v", err)
	}

	// Move C into B (so now A > B > C)
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", containerCID, containerBID)); err != nil {
		t.Fatalf("/move C into B: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/move C into B did not complete")
	}
	if !tc.waitForLocation(fmt.Sprintf("#%s", containerCID), containerBID, defaultWaitTimeout) {
		t.Fatal("container C did not move into B")
	}

	// Try to move A into C (should fail - C is inside B which is inside A)
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", containerAID, containerCID)); err != nil {
		t.Fatalf("/move A into C: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/move A into C did not complete: %q", output)
	}
	if !strings.Contains(output, "cannot move object into itself") {
		t.Fatalf("deep circular move should fail with error, got: %q", output)
	}
}

// TestExitAtUniverseRoot tests that /exit at genesis fails gracefully.
func TestExitAtUniverseRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient

	// First, ensure we're in genesis (which has no parent location)
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Verify we're in genesis
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		t.Fatal("should be in genesis for exit test")
	}

	// Drain any stale notifications before the command
	tc.readUntil(50*time.Millisecond, nil)

	// Try to /exit from genesis - should fail with friendly message
	if err := tc.sendLine("/exit"); err != nil {
		t.Fatalf("/exit at genesis: %v", err)
	}
	output, ok := tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/exit at genesis did not complete: %q", output)
	}
	if !strings.Contains(output, "Unable to leave the universe") {
		t.Fatalf("/exit at genesis should fail: %q", output)
	}

	// Verify we're still in genesis (didn't move)
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		t.Fatal("should still be in genesis after failed /exit")
	}
}

// TestRemoveCurrentLocation tests that /remove current location fails gracefully.
func TestRemoveCurrentLocation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient

	// Ensure we're in genesis first
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Create a room to test with
	removeTestRoomSource := `setDescriptions([{Short: 'remove test room', Long: 'A room for testing /remove edge case.'}]);
setExits([{Name: 'out', Destination: 'genesis'}]);
`
	sourcePath := uniqueSourcePath("remove_test_room")
	if err := testServer.WriteSource(sourcePath, removeTestRoomSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}

	removeTestRoomID, err := tc.createObject(sourcePath)
	if err != nil {
		t.Fatalf("create remove_test_room: %v", err)
	}

	// Enter the test room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", removeTestRoomID)); err != nil {
		t.Fatalf("/enter remove_test_room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter remove_test_room did not complete")
	}

	// Verify we're in the test room
	if !tc.waitForLocation("", removeTestRoomID, defaultWaitTimeout) {
		t.Fatal("should be in remove_test_room")
	}

	// Drain any stale notifications before the command
	tc.readUntil(50*time.Millisecond, nil)

	// Try to /remove our current location - should fail
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", removeTestRoomID)); err != nil {
		t.Fatalf("/remove current location: %v", err)
	}
	output, ok := tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/remove current location did not complete: %q", output)
	}
	if !strings.Contains(output, "Can't remove current location") {
		t.Fatalf("/remove current location should fail with 'Can't remove current location': %q", output)
	}

	// Verify the room still exists (wasn't removed)
	_, err = tc.inspect(fmt.Sprintf("#%s", removeTestRoomID))
	if err != nil {
		t.Fatalf("room should still exist after failed /remove: %v", err)
	}

	// Return to genesis
	if err := tc.sendLine("out"); err != nil {
		t.Fatalf("out from remove_test_room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("out from remove_test_room did not complete")
	}
}

// TestJavaScriptImports tests the @import directive for JavaScript modules.
func TestJavaScriptImports(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Create a library file that exports a utility function
	libPath := uniqueSourcePath("lib/greeter")
	libSource := `// Library that provides greeting functionality
var greeter = greeter || {};
greeter.hello = function(name) {
    return 'Hello, ' + name + '!';
};
`
	if err := ts.WriteSource(libPath, libSource); err != nil {
		t.Fatalf("failed to create %s: %v", libPath, err)
	}

	// Create an object that imports the library
	importerPath := uniqueSourcePath("importer")
	importerSource := fmt.Sprintf(`// @import %s

addCallback('greet', ['action'], function(msg) {
    // Use the imported greeter library
    var greeting = greeter.hello('World');
    setDescriptions([{Short: 'greeter (' + greeting + ')'}]);
    log('Greeting result: ' + greeting);
    return true;
});

setDescriptions([{Short: 'greeter (idle)'}]);
`, libPath)
	if err := ts.WriteSource(importerPath, importerSource); err != nil {
		t.Fatalf("failed to create %s: %v", importerPath, err)
	}

	// Create an object using the importer source
	importerID, err := tc.createObject(importerPath)
	if err != nil {
		t.Fatalf("create importer object: %v", err)
	}

	// Verify the object was created with initial description
	output, ok := tc.sendCommand("look", defaultWaitTimeout)
	if !ok {
		t.Fatal("look for importer did not complete")
	}
	if !strings.Contains(output, "greeter (idle)") {
		t.Fatalf("importer should show initial 'greeter (idle)' description: %q", output)
	}

	// Invoke the greet command which uses the imported library
	output, ok = tc.sendCommand("greet", defaultWaitTimeout)
	if !ok {
		t.Fatal("greet command did not complete")
	}

	// Verify the command worked by checking the updated description
	greetLookOutput, ok := tc.waitForLookMatch("greeter (Hello, World!)", defaultWaitTimeout)
	if !ok {
		t.Fatalf("greet command should update description to show greeting, got: %q", greetLookOutput)
	}

	// Test relative imports - create a chain of imports
	mobsBasePath := uniqueSourcePath("mobs/base")
	baseSource := `// Base functionality for mobs
var mobBase = mobBase || {};
mobBase.species = 'unknown';
mobBase.describe = function() {
    return 'A ' + this.species + ' creature';
};
`
	if err := ts.WriteSource(mobsBasePath, baseSource); err != nil {
		t.Fatalf("failed to create %s: %v", mobsBasePath, err)
	}

	// Dog uses relative import to base - need to compute relative path
	// Since both are in unique paths, we'll use absolute path for simplicity
	dogPath := uniqueSourcePath("mobs/dog")
	dogSource := fmt.Sprintf(`// @import %s

// Override species
mobBase.species = 'canine';

addCallback('bark', ['action'], (msg) => {
    setDescriptions([{Short: 'dog (' + mobBase.describe() + ')'}]);
    return true;
});

setDescriptions([{Short: 'dog (sleeping)'}]);
`, mobsBasePath)
	if err := ts.WriteSource(dogPath, dogSource); err != nil {
		t.Fatalf("failed to create %s: %v", dogPath, err)
	}

	// Create a dog object
	dogID, err := tc.createObject(dogPath)
	if err != nil {
		t.Fatalf("create dog object: %v", err)
	}

	// Verify initial state
	output, ok = tc.sendCommand("look", defaultWaitTimeout)
	if !ok {
		t.Fatal("look for dog did not complete")
	}
	if !strings.Contains(output, "dog (sleeping)") {
		t.Fatalf("dog should show initial 'dog (sleeping)' description: %q", output)
	}

	// Invoke bark command which uses the imported base
	output, ok = tc.sendCommand("bark", defaultWaitTimeout)
	if !ok {
		t.Fatal("bark command did not complete")
	}

	// Verify the command worked with the imported base functionality
	_, ok = tc.waitForLookMatch("dog (A canine creature)", defaultWaitTimeout)
	if !ok {
		t.Fatal("bark command should update description using imported base")
	}

	// Clean up test objects
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", importerID)); err != nil {
		t.Fatalf("/remove importer: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/remove importer did not complete")
	}

	if err := tc.sendLine(fmt.Sprintf("/remove #%s", dogID)); err != nil {
		t.Fatalf("/remove dog: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/remove dog did not complete")
	}
}

// TestExitFailedEvent tests the exitFailed event when exit challenges fail.
func TestExitFailedEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis for exitFailed: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis for exitFailed did not complete")
	}
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		t.Fatal("should be in genesis for exitFailed test")
	}

	// Create a room that:
	// 1. Has an exit with UseChallenges and a Message
	// 2. Registers callback for 'exitFailed' to update description
	exitFailedRoomPath := uniqueSourcePath("exitfailed_room")
	exitFailedRoomSource := `// Room for testing exitFailed event
setDescriptions([{
	Short: 'Exit Failed Test Room (idle)',
	Unique: true,
	Long: 'A room for testing the exitFailed event.',
}]);
setExits([
	{
		Descriptions: [{Short: 'out'}],
		Destination: 'genesis',
	},
	{
		Descriptions: [{Short: 'blocked'}],
		Destination: 'genesis',
		UseChallenges: [{Skill: 'telekinesis', Level: 100, Message: 'The door remains firmly shut.'}],
	},
]);
addCallback('exitFailed', ['emit'], (msg) => {
	// Update room description to confirm we received the event
	var exitName = msg.exit && msg.exit.Descriptions && msg.exit.Descriptions[0] ? msg.exit.Descriptions[0].Short : 'unknown';
	var subjectId = msg.subject && msg.subject.Id ? msg.subject.Id : 'unknown';
	setDescriptions([{
		Short: 'Exit Failed Test Room (saw: ' + exitName + ', by: ' + subjectId.substring(0, 8) + ')',
		Unique: true,
		Long: 'A room for testing the exitFailed event.',
	}]);
});
`
	if err := ts.WriteSource(exitFailedRoomPath, exitFailedRoomSource); err != nil {
		t.Fatalf("failed to create %s: %v", exitFailedRoomPath, err)
	}

	// Create the room
	exitFailedRoomID, err := tc.createObject(exitFailedRoomPath)
	if err != nil {
		t.Fatalf("create exitfailed_room: %v", err)
	}

	// Enter the room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", exitFailedRoomID)); err != nil {
		t.Fatalf("/enter exitfailed_room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter exitfailed_room did not complete")
	}
	if !tc.waitForLocation("", exitFailedRoomID, defaultWaitTimeout) {
		t.Fatal("user did not move to exitfailed_room")
	}

	// Verify initial room description
	output, ok := tc.waitForLookMatch("Exit Failed Test Room (idle)", defaultWaitTimeout)
	if !ok {
		t.Fatalf("look did not show initial exitfailed room description: %q", output)
	}

	// Try to use the blocked exit (should fail - no telekinesis skill)
	output, ok = tc.sendCommand("blocked", defaultWaitTimeout)
	if !ok {
		t.Fatal("blocked exit command did not complete")
	}

	// Verify the challenge message was printed
	if !strings.Contains(output, "The door remains firmly shut.") {
		t.Fatalf("blocked exit should print challenge message: %q", output)
	}

	// Verify user is still in the room
	selfInspect, err := tc.inspect("")
	if err != nil {
		t.Fatalf("failed to inspect self after exitFailed: %v", err)
	}
	if selfInspect.GetLocation() != exitFailedRoomID {
		t.Fatalf("user should still be in exitfailed_room after failed exit, but is in %q", selfInspect.GetLocation())
	}

	// Wait for the room's description to change (confirms exitFailed event was received)
	output, ok = tc.waitForLookMatch("Exit Failed Test Room (saw: blocked", defaultWaitTimeout)
	if !ok {
		t.Fatalf("room should have received exitFailed event and updated description: %q", output)
	}

	// Cleanup: exit back to genesis
	if err := tc.sendLine("out"); err != nil {
		t.Fatalf("out exit command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("out exit command did not complete")
	}
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		t.Fatal("user did not move to genesis via 'out' exit")
	}

	// Remove the test room
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", exitFailedRoomID)); err != nil {
		t.Fatalf("/remove exitfailed_room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/remove exitfailed_room did not complete")
	}
}

// TestSetInterval tests setInterval() and clearInterval() for periodic events.
func TestSetInterval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis for interval: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis for interval did not complete")
	}

	// Create an object that uses setInterval to count ticks
	// Minimum interval is 5000ms (5 seconds)
	sourcePath := uniqueSourcePath("pulsing_gem")
	intervalSource := `
// Only initialize on first run (state.tickCount will be undefined)
if (state.tickCount === undefined) {
	state.tickCount = 0;
	state.intervalId = setInterval(5000, 'tick', {});
	setDescriptions([{Short: 'pulsing gem (0 pulses)'}]);
}

addCallback('tick', ['emit'], (msg) => {
	state.tickCount = (state.tickCount || 0) + 1;
	setDescriptions([{Short: 'pulsing gem (' + state.tickCount + ' pulses)'}]);
});

addCallback('halt', ['action'], (msg) => {
	if (state.intervalId) {
		clearInterval(state.intervalId);
		state.intervalId = null;
		setDescriptions([{Short: 'dormant gem (halted at ' + state.tickCount + ')'}]);
	}
});
`
	if err := ts.WriteSource(sourcePath, intervalSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}

	pulsingGemID, err := tc.createObject(sourcePath)
	if err != nil {
		t.Fatalf("create pulsing_gem: %v", err)
	}

	// Verify object starts with 0 pulses
	output, found := tc.waitForLookMatch("pulsing gem (0 pulses)", defaultWaitTimeout)
	if !found {
		t.Fatalf("pulsing gem should start with 0 pulses: %q", output)
	}

	// Wait for the first tick (interval is 5000ms, need ~6s to see 1 pulse)
	intervalWaitTimeout := 12 * time.Second
	output, found = tc.waitForLookMatch("1 pulses", intervalWaitTimeout)
	if !found {
		t.Fatalf("interval should have fired at least once: %q", output)
	}

	// Halt the interval
	if err := tc.sendLine("halt"); err != nil {
		t.Fatalf("halt command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("halt command did not complete")
	}

	// Verify it shows dormant/halted
	output, found = tc.waitForLookMatch("dormant gem (halted at", defaultWaitTimeout)
	if !found {
		t.Fatalf("gem should show halted: %q", output)
	}

	// Record the pulse count when halted
	var haltedPulses int
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "halted at") {
			parts := strings.Split(line, "halted at ")
			if len(parts) > 1 {
				numStr := strings.TrimSuffix(strings.TrimSpace(parts[1]), ")")
				fmt.Sscanf(numStr, "%d", &haltedPulses)
			}
		}
	}

	// Wait longer than the interval period to verify no more pulses occurred.
	// NOTE: A fixed sleep is unavoidable here because we're testing that an event
	// does NOT fire - there's nothing to poll for or wait on.
	time.Sleep(6 * time.Second)
	output, ok := tc.sendCommand("look", defaultWaitTimeout)
	if !ok {
		t.Fatal("look after halt did not complete")
	}
	// The description should still show the same halted pulse count
	if !strings.Contains(output, fmt.Sprintf("halted at %d", haltedPulses)) {
		t.Fatalf("interval should not fire after clearInterval: %q (expected halted at %d)", output, haltedPulses)
	}

	// Cleanup with verification
	tc.removeObject(t, pulsingGemID, true)
}

// TestIntervalsCommand tests the /intervals wizard command for listing intervals.
func TestIntervalsCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Create a new interval object (minimum interval is 5000ms)
	sourcePath := uniqueSourcePath("interval_lister")
	intervalListSource := `
setDescriptions([{Short: 'interval lister'}]);
if (state.intervalId === undefined) {
	state.intervalId = setInterval(5000, 'heartbeat', {type: 'beat'});
}
`
	if err := ts.WriteSource(sourcePath, intervalListSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}

	intervalListerID, err := tc.createObject(sourcePath)
	if err != nil {
		t.Fatalf("create interval_lister: %v", err)
	}

	// Check /intervals command shows the interval
	output, ok := tc.sendCommand("/intervals", defaultWaitTimeout)
	if !ok {
		t.Fatal("/intervals command did not complete")
	}
	if !strings.Contains(output, intervalListerID) {
		t.Fatalf("/intervals should show object ID: %q", output)
	}
	if !strings.Contains(output, "heartbeat") {
		t.Fatalf("/intervals should show event name 'heartbeat': %q", output)
	}
	if !strings.Contains(output, "5000") {
		t.Fatalf("/intervals should show interval ms: %q", output)
	}

	// Cleanup with verification
	tc.removeObject(t, intervalListerID, true)
}

// TestCreateRemoveObject tests createObject() and removeObject() JS APIs.
func TestCreateRemoveObject(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer
	ctx := context.Background()

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Create a coin source that records its creator
	coinPath := uniqueSourcePath("test_coin")
	coinSource := `
setDescriptions([{Short: 'gold coin'}]);

addCallback('created', ['emit'], (msg) => {
	state.creatorId = msg.creatorId;
});
`
	if err := ts.WriteSource(coinPath, coinSource); err != nil {
		t.Fatalf("failed to create %s: %v", coinPath, err)
	}

	// Create a spawner that can create and remove coins
	spawnerPath := uniqueSourcePath("coin_spawner")
	spawnerSource := fmt.Sprintf(`
setDescriptions([{Short: 'coin spawner'}]);

addCallback('spawn', ['action'], (msg) => {
	var coinId = createObject('%s', getLocation());
	state.lastSpawned = coinId;
	state.spawnCount = (state.spawnCount || 0) + 1;
});

addCallback('cleanup', ['action'], (msg) => {
	if (state.lastSpawned) {
		removeObject(state.lastSpawned);
		state.lastSpawned = null;
	}
});
`, coinPath)
	if err := ts.WriteSource(spawnerPath, spawnerSource); err != nil {
		t.Fatalf("failed to create %s: %v", spawnerPath, err)
	}

	// Create the spawner object (created in the current room as a sibling)
	spawnerID, err := tc.createObject(spawnerPath)
	if err != nil {
		t.Fatalf("create coin_spawner: %v", err)
	}

	// Type "spawn" command - routed to sibling's action callback
	if err := tc.sendLine("spawn"); err != nil {
		t.Fatalf("spawn command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("spawn command did not complete")
	}

	// Wait for the coin to be created and verify it exists
	var coinID string
	if !waitForCondition(defaultWaitTimeout, 50*time.Millisecond, func() bool {
		spawner, err := ts.Storage().AccessObject(ctx, spawnerID, nil)
		if err != nil {
			return false
		}
		var spawnerState map[string]any
		if err := goccy.Unmarshal([]byte(spawner.GetState()), &spawnerState); err != nil {
			return false
		}
		if id, ok := spawnerState["lastSpawned"].(string); ok && id != "" {
			coinID = id
			return true
		}
		return false
	}) {
		t.Fatal("coin not spawned within timeout")
	}

	// Verify the coin exists and has the creator set
	coin, err := ts.Storage().AccessObject(ctx, coinID, nil)
	if err != nil {
		t.Fatalf("coin object not found: %v", err)
	}
	if coin.GetSourcePath() != coinPath {
		t.Fatalf("coin has wrong source path: %q", coin.GetSourcePath())
	}

	// Look should show the coin
	output, ok := tc.sendCommand("look", defaultWaitTimeout)
	if !ok {
		t.Fatal("look command did not complete")
	}
	if !strings.Contains(output, "gold coin") {
		t.Fatalf("look should show spawned coin: %q", output)
	}

	// Type "cleanup" command - routed to sibling's action callback
	if err := tc.sendLine("cleanup"); err != nil {
		t.Fatalf("cleanup command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("cleanup command did not complete")
	}

	// Wait for the coin to be removed
	if !waitForCondition(defaultWaitTimeout, 50*time.Millisecond, func() bool {
		_, err := ts.Storage().AccessObject(ctx, coinID, nil)
		return err != nil // Object should not exist
	}) {
		t.Fatal("coin not removed within timeout")
	}

	// Look should no longer show the coin
	output, ok = tc.sendCommand("look", defaultWaitTimeout)
	if !ok {
		t.Fatal("look command did not complete")
	}
	if strings.Contains(output, "gold coin") {
		t.Fatalf("look should not show removed coin: %q", output)
	}

	// Test self-removal: create an object that removes itself
	selfRemovePath := uniqueSourcePath("self_remover")
	selfRemoveSource := `
setDescriptions([{Short: 'ephemeral object'}]);

addCallback('vanish', ['action'], (msg) => {
	removeObject(getId());
});
`
	if err := ts.WriteSource(selfRemovePath, selfRemoveSource); err != nil {
		t.Fatalf("failed to create %s: %v", selfRemovePath, err)
	}

	ephemeralID, err := tc.createObject(selfRemovePath)
	if err != nil {
		t.Fatalf("create self_remover: %v", err)
	}

	// Verify it exists
	if _, err := ts.Storage().AccessObject(ctx, ephemeralID, nil); err != nil {
		t.Fatalf("ephemeral object not found: %v", err)
	}

	// Type "vanish" command - routed to sibling's action callback, object removes itself
	if err := tc.sendLine("vanish"); err != nil {
		t.Fatalf("vanish command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("vanish command did not complete")
	}

	// Wait for self-removal
	if !waitForCondition(defaultWaitTimeout, 50*time.Millisecond, func() bool {
		_, err := ts.Storage().AccessObject(ctx, ephemeralID, nil)
		return err != nil // Object should not exist
	}) {
		t.Fatal("self-removal failed within timeout")
	}

	// Cleanup spawner with verification
	tc.removeObject(t, spawnerID, true)
}

// TestRemoveCallback tests the removeCallback() JS API.
func TestRemoveCallback(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions with other tests
	if _, err := tc.enterIsolatedRoom(ts, "removeCallback"); err != nil {
		t.Fatalf("failed to enter isolated room for removeCallback test: %v", err)
	}

	// Create an object that has a callback, then removes it
	sourcePath := uniqueSourcePath("callback_test")
	removeCallbackSource := `setDescriptions([{Short: 'callback test object (has callback) proof:0'}]);
addCallback('ping', ['action'], (msg) => {
	// This callback will be removed by 'disable'
	setDescriptions([{Short: 'callback test object (ping received) proof:' + (state.proofCount || 0)}]);
});
addCallback('pingproof', ['action'], (msg) => {
	// This callback is never removed, proves the action was dispatched
	state.proofCount = (state.proofCount || 0) + 1;
});
addCallback('disable', ['action'], (msg) => {
	removeCallback('ping');
	setDescriptions([{Short: 'callback test object (ping disabled) proof:' + (state.proofCount || 0)}]);
});
addCallback('checkproof', ['action'], (msg) => {
	// Used to verify proof count without triggering ping
	setDescriptions([{Short: 'callback test object (checked) proof:' + (state.proofCount || 0)}]);
});
`
	if err := ts.WriteSource(sourcePath, removeCallbackSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}
	if _, err := tc.createObject(sourcePath); err != nil {
		t.Fatalf("create callback_test: %v", err)
	}

	// First verify ping callback works - also triggers pingproof (proof:0->1)
	if err := tc.sendLine("ping"); err != nil {
		t.Fatalf("ping command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("ping command did not complete")
	}
	// Also trigger pingproof to update proof count
	if err := tc.sendLine("pingproof"); err != nil {
		t.Fatalf("pingproof command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("pingproof command did not complete")
	}
	// Check proof via checkproof action (should be 1 after pingproof)
	if err := tc.sendLine("checkproof"); err != nil {
		t.Fatalf("checkproof command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("checkproof command did not complete")
	}
	_, found := tc.waitForObject("*callback test*checked*proof:1*", defaultWaitTimeout)
	if !found {
		t.Fatal("callback test object should show proof:1 after pingproof")
	}

	// Now disable the callback (removes 'ping' but not 'pingproof')
	if err := tc.sendLine("disable"); err != nil {
		t.Fatalf("disable command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("disable command did not complete")
	}
	_, found = tc.waitForObject("*callback test*ping disabled*proof:1*", defaultWaitTimeout)
	if !found {
		t.Fatal("callback test object did not confirm ping disabled with proof:1")
	}

	// Ping again - should NOT change description since callback was removed
	if err := tc.sendLine("ping"); err != nil {
		t.Fatalf("second ping command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("second ping command did not complete")
	}
	// pingproof again to prove action was dispatched (proof:1->2)
	if err := tc.sendLine("pingproof"); err != nil {
		t.Fatalf("second pingproof command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("second pingproof command did not complete")
	}
	// Verify via checkproof: proof should be 2 AND description should still be "ping disabled"
	if err := tc.sendLine("checkproof"); err != nil {
		t.Fatalf("final checkproof command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("final checkproof command did not complete")
	}
	// proof:2 proves pingproof was called (action dispatched), but description shows "checked" not "ping received"
	// which proves the 'ping' callback was removed
	_, found = tc.waitForObject("*callback test*checked*proof:2*", defaultWaitTimeout)
	if !found {
		t.Fatal("callback test object should show 'checked' with proof:2, proving ping action was dispatched but callback removed")
	}
}

// TestGetSetSourcePath tests getSourcePath() and setSourcePath() JS APIs.
func TestGetSetSourcePath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions with other tests
	if _, err := tc.enterIsolatedRoom(ts, "sourcePath"); err != nil {
		t.Fatalf("failed to enter isolated room for sourcePath test: %v", err)
	}

	// Create an object that can report and change its source path
	sourcePath := uniqueSourcePath("source_path_test")
	newSourcePath := uniqueSourcePath("new_source")
	sourcePathSource := fmt.Sprintf(`setDescriptions([{Short: 'source path tester (ready)'}]);
addCallback('getpath', ['action'], (msg) => {
	const path = getSourcePath();
	setDescriptions([{Short: 'source path tester (path:' + path + ')'}]);
});
addCallback('setpath', ['action'], (msg) => {
	setSourcePath('%s');
	// Note: description will be reset on next reload, so we verify via /inspect
});
`, newSourcePath)
	if err := ts.WriteSource(sourcePath, sourcePathSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}
	// Create the new source file so the object can still run after path change
	if err := ts.WriteSource(newSourcePath, sourcePathSource); err != nil {
		t.Fatalf("failed to create %s: %v", newSourcePath, err)
	}
	sourcePathObjID, err := tc.createObject(sourcePath)
	if err != nil {
		t.Fatalf("create source_path_test: %v", err)
	}

	// Test getSourcePath() - verify it returns the correct path via description
	if err := tc.sendLine("getpath"); err != nil {
		t.Fatalf("getpath command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("getpath command did not complete")
	}
	_, found := tc.waitForObject("*source path tester*path:"+sourcePath+"*", defaultWaitTimeout)
	if !found {
		t.Fatalf("getSourcePath() should return %s initially", sourcePath)
	}

	// Also verify via /inspect
	if !tc.waitForSourcePath(fmt.Sprintf("#%s", sourcePathObjID), sourcePath, defaultWaitTimeout) {
		t.Fatalf("source path should be %s initially via /inspect", sourcePath)
	}

	// Test setSourcePath - change the path
	if err := tc.sendLine("setpath"); err != nil {
		t.Fatalf("setpath command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("setpath command did not complete")
	}

	// Verify SourcePath changed via /inspect (description gets reset on reload)
	if !tc.waitForSourcePath(fmt.Sprintf("#%s", sourcePathObjID), newSourcePath, defaultWaitTimeout) {
		t.Fatalf("source path should be %s after setSourcePath", newSourcePath)
	}
}

// TestGetSetLearning tests getLearning() and setLearning() JS APIs.
func TestGetSetLearning(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions
	if _, err := tc.enterIsolatedRoom(ts, "learning"); err != nil {
		t.Fatalf("failed to enter isolated room for learning test: %v", err)
	}

	// Create an object that can toggle learning mode
	sourcePath := uniqueSourcePath("learning_test")
	learningSource := `setDescriptions([{Short: 'learning tester (ready)'}]);
addCallback('checklearn', ['action'], (msg) => {
	const learning = getLearning();
	setDescriptions([{Short: 'learning tester (learning:' + learning + ')'}]);
});
addCallback('enablelearn', ['action'], (msg) => {
	setLearning(true);
	setDescriptions([{Short: 'learning tester (enabled)'}]);
});
addCallback('disablelearn', ['action'], (msg) => {
	setLearning(false);
	setDescriptions([{Short: 'learning tester (disabled)'}]);
});
`
	if err := ts.WriteSource(sourcePath, learningSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}
	if _, err := tc.createObject(sourcePath); err != nil {
		t.Fatalf("create learning_test: %v", err)
	}

	// Check initial learning state (should be false)
	if err := tc.sendLine("checklearn"); err != nil {
		t.Fatalf("checklearn command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("checklearn command did not complete")
	}
	_, found := tc.waitForObject("*learning tester*learning:false*", defaultWaitTimeout)
	if !found {
		t.Fatal("learning should be false initially")
	}

	// Enable learning
	if err := tc.sendLine("enablelearn"); err != nil {
		t.Fatalf("enablelearn command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("enablelearn command did not complete")
	}
	_, found = tc.waitForObject("*learning tester*enabled*", defaultWaitTimeout)
	if !found {
		t.Fatal("learning tester should confirm enabled")
	}

	// Check learning state again (should be true)
	if err := tc.sendLine("checklearn"); err != nil {
		t.Fatalf("second checklearn command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("second checklearn command did not complete")
	}
	_, found = tc.waitForObject("*learning tester*learning:true*", defaultWaitTimeout)
	if !found {
		t.Fatal("learning should be true after enable")
	}

	// Disable learning
	if err := tc.sendLine("disablelearn"); err != nil {
		t.Fatalf("disablelearn command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("disablelearn command did not complete")
	}
	_, found = tc.waitForObject("*learning tester*disabled*", defaultWaitTimeout)
	if !found {
		t.Fatal("learning tester should confirm disabled")
	}

	// Check learning state again (should be false)
	if err := tc.sendLine("checklearn"); err != nil {
		t.Fatalf("third checklearn command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("third checklearn command did not complete")
	}
	_, found = tc.waitForObject("*learning tester*learning:false*", defaultWaitTimeout)
	if !found {
		t.Fatal("learning should be false after disable")
	}
}

// TestEmitWithChallenges tests emit() with skill challenges.
func TestEmitWithChallenges(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions
	if _, err := tc.enterIsolatedRoom(ts, "emitChallenges"); err != nil {
		t.Fatalf("failed to enter isolated room: %v", err)
	}

	// High-perception receiver - has 200 perception, should receive challenged emit
	highPercPath := uniqueSourcePath("eagle_receiver")
	highPercReceiverSource := `setDescriptions([{Short: 'eagle orb (waiting)'}]);
setSkills({perception: {Practical: 200, Theoretical: 200}});
addCallback('secret', ['emit'], (msg) => {
	setDescriptions([{Short: 'eagle orb (got: ' + msg.secret + ')'}]);
});
`
	if err := ts.WriteSource(highPercPath, highPercReceiverSource); err != nil {
		t.Fatalf("failed to create %s: %v", highPercPath, err)
	}

	// Low-perception receiver - has 1 perception, should NOT receive challenged emit with level 50
	lowPercPath := uniqueSourcePath("dim_receiver")
	lowPercReceiverSource := `setDescriptions([{Short: 'dim orb (waiting)'}]);
setSkills({perception: {Practical: 1, Theoretical: 1}});
addCallback('secret', ['emit'], (msg) => {
	setDescriptions([{Short: 'dim orb (got: ' + msg.secret + ')'}]);
});
`
	if err := ts.WriteSource(lowPercPath, lowPercReceiverSource); err != nil {
		t.Fatalf("failed to create %s: %v", lowPercPath, err)
	}

	// Sender emits with perception challenge at level 50
	senderPath := uniqueSourcePath("challenge_sender")
	challengeSenderSource := `setDescriptions([{Short: 'whisperer orb'}]);
addCallback('whisper', ['action'], (msg) => {
	const parts = msg.line.split(' ');
	const targetId = parts[1];
	emit(targetId, 'secret', {secret: 'hidden'}, [{Skill: 'perception', Level: 50}]);
	setDescriptions([{Short: 'whisperer orb (sent)'}]);
});
`
	if err := ts.WriteSource(senderPath, challengeSenderSource); err != nil {
		t.Fatalf("failed to create %s: %v", senderPath, err)
	}

	// Create receivers
	eagleID, err := tc.createObject(highPercPath)
	if err != nil {
		t.Fatalf("create eagle_receiver: %v", err)
	}

	dimID, err := tc.createObject(lowPercPath)
	if err != nil {
		t.Fatalf("create dim_receiver: %v", err)
	}

	// Create sender
	if _, err := tc.createObject(senderPath); err != nil {
		t.Fatalf("create challenge_sender: %v", err)
	}

	// Whisper to the high-perception receiver - should succeed
	if err := tc.sendLine(fmt.Sprintf("whisper %s", eagleID)); err != nil {
		t.Fatalf("whisper to eagle: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("whisper to eagle did not complete")
	}

	// Eagle should receive the secret
	output, found := tc.waitForLookMatch("eagle orb (got: hidden)", defaultWaitTimeout)
	if !found {
		t.Fatalf("high-perception receiver should have received challenged emit: %q", output)
	}

	// Whisper to the low-perception receiver - should fail (no event received)
	if err := tc.sendLine(fmt.Sprintf("whisper %s", dimID)); err != nil {
		t.Fatalf("whisper to dim: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("whisper to dim did not complete")
	}

	// Dim should NOT have received the secret (should still be waiting)
	if err := tc.sendLine("look"); err != nil {
		t.Fatalf("look command: %v", err)
	}
	output, _ = tc.waitForPrompt(defaultWaitTimeout)
	if strings.Contains(output, "dim orb (got:") {
		t.Fatalf("low-perception receiver should NOT have received challenged emit: %q", output)
	}
	if !strings.Contains(output, "dim orb (waiting)") {
		t.Fatalf("dim orb should still be waiting: %q", output)
	}
}

// TestEmitToLocation tests emitToLocation() JS API.
func TestEmitToLocation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions
	if _, err := tc.enterIsolatedRoom(ts, "emitToLoc"); err != nil {
		t.Fatalf("failed to enter isolated room: %v", err)
	}

	// Create a broadcast room
	broadcastRoomPath := uniqueSourcePath("broadcast_room")
	broadcastRoomSource := `setDescriptions([{Short: 'broadcast chamber', Long: 'A room for broadcasting.'}]);
setExits([{Descriptions: [{Short: 'out'}], Destination: 'genesis'}]);
`
	if err := ts.WriteSource(broadcastRoomPath, broadcastRoomSource); err != nil {
		t.Fatalf("failed to create %s: %v", broadcastRoomPath, err)
	}
	broadcastRoomID, err := tc.createObject(broadcastRoomPath)
	if err != nil {
		t.Fatalf("create broadcast_room: %v", err)
	}

	// Enter the broadcast room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", broadcastRoomID)); err != nil {
		t.Fatalf("/enter broadcast room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter broadcast room did not complete")
	}

	// Listener 1 - receives broadcasts
	listener1Path := uniqueSourcePath("listener1")
	listener1Source := `setDescriptions([{Short: 'listener alpha (idle)'}]);
addCallback('announce', ['emit'], (msg) => {
	setDescriptions([{Short: 'listener alpha (heard: ' + msg.message + ')'}]);
});
`
	if err := ts.WriteSource(listener1Path, listener1Source); err != nil {
		t.Fatalf("failed to create %s: %v", listener1Path, err)
	}
	if _, err := tc.createObject(listener1Path); err != nil {
		t.Fatalf("create listener1: %v", err)
	}

	// Listener 2 - also receives broadcasts
	listener2Path := uniqueSourcePath("listener2")
	listener2Source := `setDescriptions([{Short: 'listener beta (idle)'}]);
addCallback('announce', ['emit'], (msg) => {
	setDescriptions([{Short: 'listener beta (heard: ' + msg.message + ')'}]);
});
`
	if err := ts.WriteSource(listener2Path, listener2Source); err != nil {
		t.Fatalf("failed to create %s: %v", listener2Path, err)
	}
	if _, err := tc.createObject(listener2Path); err != nil {
		t.Fatalf("create listener2: %v", err)
	}

	// Broadcaster - uses emitToLocation to broadcast to all in the room
	broadcasterPath := uniqueSourcePath("broadcaster")
	broadcasterSource := `setDescriptions([{Short: 'broadcaster orb'}]);
addCallback('broadcast', ['action'], (msg) => {
	emitToLocation(getLocation(), 'announce', {message: 'hello all'});
	setDescriptions([{Short: 'broadcaster orb (sent)'}]);
});
`
	if err := ts.WriteSource(broadcasterPath, broadcasterSource); err != nil {
		t.Fatalf("failed to create %s: %v", broadcasterPath, err)
	}
	if _, err := tc.createObject(broadcasterPath); err != nil {
		t.Fatalf("create broadcaster: %v", err)
	}

	// Issue the broadcast command
	if err := tc.sendLine("broadcast"); err != nil {
		t.Fatalf("broadcast command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("broadcast command did not complete")
	}

	// Both listeners should receive the broadcast
	output, found := tc.waitForLookMatchFunc(func(s string) bool {
		return strings.Contains(s, "listener alpha (heard: hello all)") &&
			strings.Contains(s, "listener beta (heard: hello all)")
	}, defaultWaitTimeout)
	if !found {
		t.Fatalf("both listeners should have received broadcast: %q", output)
	}
}

// TestEmitToLocationWithChallenges tests emitToLocation() with skill challenges.
func TestEmitToLocationWithChallenges(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions
	if _, err := tc.enterIsolatedRoom(ts, "emitToLocChal"); err != nil {
		t.Fatalf("failed to enter isolated room: %v", err)
	}

	// Create a broadcast room for this test
	broadcastRoomPath := uniqueSourcePath("chal_broadcast_room")
	broadcastRoomSource := `setDescriptions([{Short: 'telepathy chamber', Long: 'A room for telepathic broadcasts.'}]);
setExits([{Descriptions: [{Short: 'out'}], Destination: 'genesis'}]);
`
	if err := ts.WriteSource(broadcastRoomPath, broadcastRoomSource); err != nil {
		t.Fatalf("failed to create %s: %v", broadcastRoomPath, err)
	}
	broadcastRoomID, err := tc.createObject(broadcastRoomPath)
	if err != nil {
		t.Fatalf("create chal_broadcast_room: %v", err)
	}

	// Enter the broadcast room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", broadcastRoomID)); err != nil {
		t.Fatalf("/enter broadcast room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter broadcast room did not complete")
	}

	// Sensitive ear - high telepathy, should receive
	sensitiveEarPath := uniqueSourcePath("sensitive_ear")
	sensitiveEarSource := `setDescriptions([{Short: 'sensitive ear (idle)'}]);
setSkills({telepathy: {Practical: 150, Theoretical: 150}});
addCallback('mindcast', ['emit'], (msg) => {
	setDescriptions([{Short: 'sensitive ear (heard: ' + msg.thought + ')'}]);
});
`
	if err := ts.WriteSource(sensitiveEarPath, sensitiveEarSource); err != nil {
		t.Fatalf("failed to create %s: %v", sensitiveEarPath, err)
	}
	if _, err := tc.createObject(sensitiveEarPath); err != nil {
		t.Fatalf("create sensitive_ear: %v", err)
	}

	// Deaf ear - low telepathy, should NOT receive
	deafEarPath := uniqueSourcePath("deaf_ear")
	deafEarSource := `setDescriptions([{Short: 'deaf ear (idle)'}]);
setSkills({telepathy: {Practical: 5, Theoretical: 5}});
addCallback('mindcast', ['emit'], (msg) => {
	setDescriptions([{Short: 'deaf ear (heard: ' + msg.thought + ')'}]);
});
`
	if err := ts.WriteSource(deafEarPath, deafEarSource); err != nil {
		t.Fatalf("failed to create %s: %v", deafEarPath, err)
	}
	if _, err := tc.createObject(deafEarPath); err != nil {
		t.Fatalf("create deaf_ear: %v", err)
	}

	// Telepathic broadcaster - uses emitToLocation with telepathy challenge
	telepathPath := uniqueSourcePath("telepath")
	telepathSource := `setDescriptions([{Short: 'telepath orb'}]);
addCallback('mindspeak', ['action'], (msg) => {
	emitToLocation(getLocation(), 'mindcast', {thought: 'secret thought'}, [{Skill: 'telepathy', Level: 50}]);
	setDescriptions([{Short: 'telepath orb (sent)'}]);
});
`
	if err := ts.WriteSource(telepathPath, telepathSource); err != nil {
		t.Fatalf("failed to create %s: %v", telepathPath, err)
	}
	if _, err := tc.createObject(telepathPath); err != nil {
		t.Fatalf("create telepath: %v", err)
	}

	// Issue the telepathic broadcast
	if err := tc.sendLine("mindspeak"); err != nil {
		t.Fatalf("mindspeak command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("mindspeak command did not complete")
	}

	// Only the sensitive ear should receive the telepathic broadcast
	output, found := tc.waitForLookMatch("sensitive ear (heard: secret thought)", defaultWaitTimeout)
	if !found {
		t.Fatalf("high-telepathy listener should have received challenged broadcast: %q", output)
	}

	// The deaf ear should NOT have received the broadcast (still idle)
	if strings.Contains(output, "deaf ear (heard:") {
		t.Fatalf("low-telepathy listener should NOT have received challenged broadcast: %q", output)
	}
	if !strings.Contains(output, "deaf ear (idle)") {
		t.Fatalf("deaf ear should still be idle: %q", output)
	}
}

// TestMovementEvents tests movement event notifications.
func TestMovementEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions
	if _, err := tc.enterIsolatedRoom(ts, "movementEvents"); err != nil {
		t.Fatalf("failed to enter isolated room: %v", err)
	}

	// Create a destination room for movement
	destRoomPath := uniqueSourcePath("movement_dest")
	destRoomSource := `setDescriptions([{Short: 'Movement Destination', Unique: true}]);
setExits([{Descriptions: [{Short: 'out'}], Destination: 'genesis'}]);
`
	if err := ts.WriteSource(destRoomPath, destRoomSource); err != nil {
		t.Fatalf("failed to create %s: %v", destRoomPath, err)
	}
	destRoomID, err := tc.createObject(destRoomPath)
	if err != nil {
		t.Fatalf("create dest room: %v", err)
	}

	// Observer updates its description when it sees movement
	observerPath := uniqueSourcePath("observer")
	observerSource := `setDescriptions([{Short: 'watcher orb (watching)'}]);
addCallback('movement', ['emit'], (msg) => {
	const id = msg.Object ? msg.Object.Id : 'unknown';
	setDescriptions([{Short: 'watcher orb (saw: ' + id + ')'}]);
});
`
	if err := ts.WriteSource(observerPath, observerSource); err != nil {
		t.Fatalf("failed to create %s: %v", observerPath, err)
	}

	moveablePath := uniqueSourcePath("moveable")
	moveableSource := `setDescriptions([{Short: 'moveable cube'}]);
`
	if err := ts.WriteSource(moveablePath, moveableSource); err != nil {
		t.Fatalf("failed to create %s: %v", moveablePath, err)
	}

	if _, err := tc.createObject(observerPath); err != nil {
		t.Fatalf("create observer: %v", err)
	}

	moveableID, err := tc.createObject(moveablePath)
	if err != nil {
		t.Fatalf("create moveable: %v", err)
	}

	// Move the moveable to the destination room
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", moveableID, destRoomID)); err != nil {
		t.Fatalf("/move moveable to dest: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/move moveable to dest did not complete")
	}

	// Poll with look until observer shows it saw the movement
	output, found := tc.waitForLookMatch("watcher orb (saw: "+moveableID+")", defaultWaitTimeout)
	if !found {
		t.Fatalf("observer should have seen moveable in movement event: %q", output)
	}
}

// TestCustomMovementVerb tests custom movement verbs.
func TestCustomMovementVerb(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	tc.ensureInGenesis(t)

	// Create two rooms with bidirectional exits so observer sees both source and destination
	roomAID, roomBID := createBidirectionalRooms(t, ts, tc, "scurry", "Mouse Room A", "Mouse Room B", "north", "south")

	// Enter room A to create the object there
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", roomAID)); err != nil {
		t.Fatalf("/enter room A: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter room A did not complete")
	}

	// Create an object with a custom movement verb "scurries" in room A
	scurryPath := uniqueSourcePath("scurry")
	scurrySource := `setDescriptions([{Short: 'tiny mouse'}]);
setMovement({Active: true, Verb: 'scurries'});
`
	if err := ts.WriteSource(scurryPath, scurrySource); err != nil {
		t.Fatalf("failed to create %s: %v", scurryPath, err)
	}
	scurryID, err := tc.createObject(scurryPath)
	if err != nil {
		t.Fatalf("create scurry object: %v", err)
	}

	// Move wizard to room B (destination) to observe movement
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", roomBID)); err != nil {
		t.Fatalf("/enter room B: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter room B did not complete")
	}

	// Move the scurry object from room A to room B
	// Wizard should see "A tiny mouse scurries in from south."
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", scurryID, roomBID)); err != nil {
		t.Fatalf("/move scurry: %v", err)
	}
	promptOutput, ok := tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatal("/move scurry did not complete")
	}
	additionalOutput := tc.readUntil(500*time.Millisecond, func(s string) bool {
		return strings.Contains(s, "scurries")
	})
	combined := promptOutput + additionalOutput
	if !strings.Contains(combined, "scurries") {
		t.Fatalf("movement message should contain custom verb 'scurries': %q", combined)
	}
}

// TestJSMovementRendering tests JS-based movement rendering.
func TestJSMovementRendering(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	tc.ensureInGenesis(t)

	// Create two rooms with bidirectional exits so observer sees both source and destination
	roomAID, roomBID := createBidirectionalRooms(t, ts, tc, "js_orb", "Orb Room A", "Orb Room B", "north", "south")

	// Enter room A to create the object there
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", roomAID)); err != nil {
		t.Fatalf("/enter room A: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter room A did not complete")
	}

	// Create an object that handles its own movement rendering via JS in room A
	jsOrbPath := uniqueSourcePath("js_orb")
	jsRenderSource := `setDescriptions([{Short: 'glowing orb'}]);
setMovement({Active: false, Verb: ''});

addCallback('renderMovement', ['emit'], (msg) => {
	var text;
	if (msg.Source && msg.Source.Here && msg.Destination && msg.Destination.Exit) {
		text = 'The glowing orb floats away ' + msg.Destination.Exit + ' with an eerie hum.';
	} else if (msg.Destination && msg.Destination.Here && msg.Source && msg.Source.Exit) {
		text = 'A glowing orb drifts in from ' + msg.Source.Exit + ', humming softly.';
	} else if (msg.Destination && msg.Destination.Here) {
		text = 'A glowing orb materializes with a soft pop.';
	} else if (msg.Source && msg.Source.Here) {
		text = 'The glowing orb fades from existence.';
	} else {
		text = 'A glowing orb does something mysterious.';
	}
	emit(msg.Observer, 'movementRendered', {Message: text});
});
`
	if err := ts.WriteSource(jsOrbPath, jsRenderSource); err != nil {
		t.Fatalf("failed to create %s: %v", jsOrbPath, err)
	}
	jsOrbID, err := tc.createObject(jsOrbPath)
	if err != nil {
		t.Fatalf("create js_orb object: %v", err)
	}

	// Move wizard to room B (destination) to observe movement
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", roomBID)); err != nil {
		t.Fatalf("/enter room B: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter room B did not complete")
	}

	// Move the orb from room A to room B
	// Wizard should see JS-rendered "A glowing orb drifts in from south, humming softly."
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", jsOrbID, roomBID)); err != nil {
		t.Fatalf("/move js_orb: %v", err)
	}
	promptOutput, ok := tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatal("/move js_orb did not complete")
	}
	additionalOutput := tc.readUntil(500*time.Millisecond, func(s string) bool {
		return strings.Contains(s, "drifts in") || strings.Contains(s, "humming softly")
	})
	combined := promptOutput + additionalOutput
	if !strings.Contains(combined, "drifts in") && !strings.Contains(combined, "humming softly") {
		t.Fatalf("movement message should contain JS-rendered text: %q", combined)
	}
}

// TestGetLocationAndMoveObject tests getLocation() and moveObject() JS APIs.
func TestGetLocationAndMoveObject(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions
	if _, err := tc.enterIsolatedRoom(ts, "getLocMove"); err != nil {
		t.Fatalf("failed to enter isolated room: %v", err)
	}

	// Create a destination room for the teleporter to move from
	destRoomPath := uniqueSourcePath("teleport_dest")
	destRoomSource := `setDescriptions([{Short: 'Teleport Destination', Unique: true}]);
setExits([{Descriptions: [{Short: 'out'}], Destination: 'genesis'}]);
`
	if err := ts.WriteSource(destRoomPath, destRoomSource); err != nil {
		t.Fatalf("failed to create %s: %v", destRoomPath, err)
	}
	destRoomID, err := tc.createObject(destRoomPath)
	if err != nil {
		t.Fatalf("create dest room: %v", err)
	}

	// Create a teleporter object that can report location and move itself
	teleportPath := uniqueSourcePath("teleporter")
	teleportSource := `setDescriptions([{Short: 'teleporter (ready)'}]);
addCallback('report', ['action'], (msg) => {
	const loc = getLocation();
	setDescriptions([{Short: 'teleporter (at:' + loc.substring(0, 8) + ')'}]);
});
addCallback('teleport', ['action'], (msg) => {
	// Move self to genesis using moveObject (safe, validated movement)
	moveObject(getId(), 'genesis');
	setDescriptions([{Short: 'teleporter (teleported)'}]);
});
`
	if err := ts.WriteSource(teleportPath, teleportSource); err != nil {
		t.Fatalf("failed to create %s: %v", teleportPath, err)
	}
	teleporterID, err := tc.createObject(teleportPath)
	if err != nil {
		t.Fatalf("create teleporter: %v", err)
	}

	// Test getLocation() - report current location
	if err := tc.sendLine("report"); err != nil {
		t.Fatalf("report command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("report command did not complete")
	}
	_, found := tc.waitForObject("*teleporter*at:*", defaultWaitTimeout)
	if !found {
		t.Fatal("teleporter should report location")
	}

	// Move teleporter to the destination room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", destRoomID)); err != nil {
		t.Fatalf("/enter dest room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter dest room did not complete")
	}
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", teleporterID, destRoomID)); err != nil {
		t.Fatalf("/move teleporter: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/move teleporter did not complete")
	}

	// Now teleport it back to genesis using moveObject from JS
	if err := tc.sendLine("teleport"); err != nil {
		t.Fatalf("teleport command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("teleport command did not complete")
	}

	// Verify it moved by checking its location via /inspect
	if !tc.waitForLocation(fmt.Sprintf("#%s", teleporterID), "genesis", defaultWaitTimeout) {
		t.Fatal("teleporter should be at genesis after moveObject")
	}
}

// TestGetContent tests getContent() JS API.
func TestGetContent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions
	if _, err := tc.enterIsolatedRoom(ts, "getContent"); err != nil {
		t.Fatalf("failed to enter isolated room: %v", err)
	}

	// Create a container that can report its content count
	containerPath := uniqueSourcePath("content_container")
	containerSource := `setDescriptions([{Short: 'content container (ready)'}]);
addCallback('countitems', ['action'], (msg) => {
	const content = getContent();
	const count = content ? Object.keys(content).length : 0;
	setDescriptions([{Short: 'content container (items:' + count + ')'}]);
});
`
	if err := ts.WriteSource(containerPath, containerSource); err != nil {
		t.Fatalf("failed to create %s: %v", containerPath, err)
	}
	containerID, err := tc.createObject(containerPath)
	if err != nil {
		t.Fatalf("create content_container: %v", err)
	}

	// Count items (should be 0)
	if err := tc.sendLine("countitems"); err != nil {
		t.Fatalf("countitems command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("countitems command did not complete")
	}
	_, found := tc.waitForObject("*content container*items:0*", defaultWaitTimeout)
	if !found {
		t.Fatal("container should have 0 items initially")
	}

	// Create a small item and put it in the container
	itemPath := uniqueSourcePath("tiny_item")
	itemSource := `setDescriptions([{Short: 'tiny item'}]);`
	if err := ts.WriteSource(itemPath, itemSource); err != nil {
		t.Fatalf("failed to create %s: %v", itemPath, err)
	}
	itemID, err := tc.createObject(itemPath)
	if err != nil {
		t.Fatalf("create tiny_item: %v", err)
	}

	// Move item into container using /move command
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", itemID, containerID)); err != nil {
		t.Fatalf("/move item into container: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/move item did not complete")
	}

	// Count items again (should be 1)
	if err := tc.sendLine("countitems"); err != nil {
		t.Fatalf("second countitems command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("second countitems command did not complete")
	}
	_, found = tc.waitForObject("*content container*items:1*", defaultWaitTimeout)
	if !found {
		t.Fatal("container should have 1 item after move")
	}
}

// TestAddDelWiz tests /addwiz and /delwiz commands.
func TestAddDelWiz(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Create a unique second user to test granting/revoking wizard status
	testUsername := fmt.Sprintf("wiztest_%d", sourceCounter.Add(1))
	testPassword := "wizpass123"

	tc2, err := createUser(ts.SSHAddr(), testUsername, testPassword)
	if err != nil {
		t.Fatalf("createUser %s: %v", testUsername, err)
	}
	defer tc2.Close()

	// Verify new user is not a wizard (can't use /inspect)
	if err := tc2.sendLine("/inspect"); err != nil {
		t.Fatalf("/inspect as non-wizard: %v", err)
	}
	output, ok := tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/inspect did not complete: %q", output)
	}
	// Non-wizards should NOT see JSON output from /inspect (check for Id field in JSON)
	if strings.Contains(output, "\"Id\":") {
		t.Fatalf("non-wizard should not see /inspect output: %q", output)
	}

	// Grant wizard status using /addwiz (tc is the owner)
	if err := tc.sendLine("/addwiz " + testUsername); err != nil {
		t.Fatalf("/addwiz command: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/addwiz command did not complete: %q", output)
	}
	if !strings.Contains(output, "Granted wizard privileges") {
		t.Fatalf("/addwiz should confirm grant: %q", output)
	}

	// Reconnect as the test user to pick up wizard status
	tc2.Close()
	tc2, err = loginUser(ts.SSHAddr(), testUsername, testPassword)
	if err != nil {
		t.Fatalf("loginUser %s after /addwiz: %v", testUsername, err)
	}
	defer tc2.Close()

	// Verify the test user can now use /inspect (wizard command)
	if err := tc2.sendLine("/inspect"); err != nil {
		t.Fatalf("/inspect as wizard: %v", err)
	}
	output, ok = tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/inspect as wizard did not complete: %q", output)
	}
	// Wizards should see JSON output from /inspect
	if !strings.Contains(output, "\"Id\":") {
		t.Fatalf("wizard should see /inspect output: %q", output)
	}

	// Revoke wizard status using /delwiz
	output, ok = tc.sendCommand("/delwiz "+testUsername, defaultWaitTimeout)
	if !ok {
		t.Fatalf("/delwiz command did not complete: %q", output)
	}
	if !strings.Contains(output, "Revoked wizard privileges") {
		t.Fatalf("/delwiz should confirm revoke: %q", output)
	}

	// Reconnect to pick up revoked status
	tc2.Close()
	tc2, err = loginUser(ts.SSHAddr(), testUsername, testPassword)
	if err != nil {
		t.Fatalf("loginUser %s after /delwiz: %v", testUsername, err)
	}
	defer tc2.Close()

	// Verify the test user can no longer use /inspect
	if err := tc2.sendLine("/inspect"); err != nil {
		t.Fatalf("/inspect after revoke: %v", err)
	}
	output, ok = tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/inspect after revoke did not complete: %q", output)
	}
	// Revoked user should NOT see JSON output from /inspect
	if strings.Contains(output, "\"Id\":") {
		t.Fatalf("revoked user should not see /inspect output: %q", output)
	}

	// Test that non-owner cannot use /addwiz
	// Grant wizard back to test user (but not owner)
	if err := tc.sendLine("/addwiz " + testUsername); err != nil {
		t.Fatalf("/addwiz to restore: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/addwiz to restore did not complete")
	}

	// Reconnect test user as wizard
	tc2.Close()
	tc2, err = loginUser(ts.SSHAddr(), testUsername, testPassword)
	if err != nil {
		t.Fatalf("loginUser %s as wizard: %v", testUsername, err)
	}
	defer tc2.Close()

	// Try to use /addwiz as non-owner wizard (trying to modify the main test user)
	if err := tc2.sendLine("/addwiz testuser"); err != nil {
		t.Fatalf("/addwiz as non-owner: %v", err)
	}
	output, ok = tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/addwiz as non-owner did not complete: %q", output)
	}
	if !strings.Contains(output, "Only owners") {
		t.Fatalf("non-owner should be denied /addwiz: %q", output)
	}

	// Try to use /delwiz as non-owner wizard
	if err := tc2.sendLine("/delwiz testuser"); err != nil {
		t.Fatalf("/delwiz as non-owner: %v", err)
	}
	output, ok = tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("/delwiz as non-owner did not complete: %q", output)
	}
	if !strings.Contains(output, "Only owners") {
		t.Fatalf("non-owner should be denied /delwiz: %q", output)
	}
}

// TestRoomAndSiblingActionHandlers tests room and sibling action handlers.
func TestRoomAndSiblingActionHandlers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions
	if _, err := tc.enterIsolatedRoom(ts, "actionHandlers"); err != nil {
		t.Fatalf("failed to enter isolated room: %v", err)
	}

	// Create a room that handles the "shake" action
	actionRoomPath := uniqueSourcePath("action_room")
	actionRoomSource := `setDescriptions([{Short: 'shaky chamber', Long: 'A small room with unstable walls.'}]);
setExits([{Descriptions: [{Short: 'out'}], Destination: 'genesis'}]);
addCallback('shake', ['action'], (msg) => {
	setDescriptions([{Short: 'shaky chamber (shaken!)', Long: 'The walls have just been shaken!'}]);
});
`
	if err := ts.WriteSource(actionRoomPath, actionRoomSource); err != nil {
		t.Fatalf("failed to create %s: %v", actionRoomPath, err)
	}

	// Create the room
	actionRoomID, err := tc.createObject(actionRoomPath)
	if err != nil {
		t.Fatalf("create action_room: %v", err)
	}

	// Enter the action room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", actionRoomID)); err != nil {
		t.Fatalf("/enter action_room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter action_room did not complete")
	}

	// Issue "shake" command - the room should handle this action
	if err := tc.sendLine("shake"); err != nil {
		t.Fatalf("shake command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("shake command did not complete")
	}

	// Wait for the room's description to update
	output, found := tc.waitForLookMatch("shaky chamber (shaken!)", defaultWaitTimeout)
	if !found {
		t.Fatalf("room action handler should have updated description: %q", output)
	}

	// Now test sibling action handler
	// Create an object that handles the "poke" action
	pokeablePath := uniqueSourcePath("pokeable")
	pokeableSource := `setDescriptions([{Short: 'pokeable orb'}]);
addCallback('poke', ['action'], (msg) => {
	setDescriptions([{Short: 'pokeable orb (poked!)'}]);
});
`
	if err := ts.WriteSource(pokeablePath, pokeableSource); err != nil {
		t.Fatalf("failed to create %s: %v", pokeablePath, err)
	}

	// Create the pokeable object (it will be created in our current room)
	if _, err := tc.createObject(pokeablePath); err != nil {
		t.Fatalf("create pokeable: %v", err)
	}

	// Issue "poke" command - the sibling object should handle this action
	if err := tc.sendLine("poke"); err != nil {
		t.Fatalf("poke command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("poke command did not complete")
	}

	// Wait for the sibling's description to update
	output, found = tc.waitForLookMatch("pokeable orb (poked!)", defaultWaitTimeout)
	if !found {
		t.Fatalf("sibling action handler should have updated description: %q", output)
	}

	// Return to genesis
	if err := tc.sendLine("out"); err != nil {
		t.Fatalf("out command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("out command did not complete")
	}
}

// TestGetNeighbourhood tests the getNeighbourhood() JS API.
func TestGetNeighbourhood(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Ensure we're in genesis where we have exits to other rooms
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Create a test room connected to genesis
	neighborRoomPath := uniqueSourcePath("neighbor_room")
	neighborRoomSource := `setDescriptions([{Short: 'Neighbor Room', Unique: true, Long: 'A neighboring room.'}]);
setExits([{Descriptions: [{Short: 'back'}], Destination: 'genesis'}]);
`
	if err := ts.WriteSource(neighborRoomPath, neighborRoomSource); err != nil {
		t.Fatalf("failed to create %s: %v", neighborRoomPath, err)
	}

	neighborRoomID, err := tc.createObject(neighborRoomPath)
	if err != nil {
		t.Fatalf("create neighbor_room: %v", err)
	}

	// Create a scout room (the room we'll be in) with an exit to the neighbor room
	scoutRoomPath := uniqueSourcePath("scout_room")
	scoutRoomSource := fmt.Sprintf(`setDescriptions([{Short: 'Scout Room', Unique: true, Long: 'A room for scouting.'}]);
setExits([
	{Descriptions: [{Short: 'north'}], Destination: '%s'},
	{Descriptions: [{Short: 'out'}], Destination: 'genesis'},
]);
`, neighborRoomID)
	if err := ts.WriteSource(scoutRoomPath, scoutRoomSource); err != nil {
		t.Fatalf("failed to create %s: %v", scoutRoomPath, err)
	}

	scoutRoomID, err := tc.createObject(scoutRoomPath)
	if err != nil {
		t.Fatalf("create scout_room: %v", err)
	}

	// Enter the scout room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", scoutRoomID)); err != nil {
		t.Fatalf("/enter scout_room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter scout_room did not complete")
	}

	// Create a scout object that uses getNeighbourhood() on a "survey" action
	scoutPath := uniqueSourcePath("scout")
	scoutSource := `setDescriptions([{Short: 'scout drone (idle)'}]);
addCallback('survey', ['action'], (msg) => {
	const hood = getNeighbourhood();
	// Build a report of what we see
	const locName = hood.Location.Container.Descriptions[0].Short;
	const contentCount = hood.Location.Content ? Object.keys(hood.Location.Content).length : 0;
	const neighborKeys = Object.keys(hood.Neighbours || {});
	const neighborCount = neighborKeys.length;
	// Get first neighbor's name if any
	let neighborInfo = 'none';
	if (neighborCount > 0) {
		const firstNeighborLoc = hood.Neighbours[neighborKeys[0]];
		if (firstNeighborLoc && firstNeighborLoc.Container) {
			neighborInfo = firstNeighborLoc.Container.Descriptions[0].Short;
		}
	}
	setDescriptions([{
		Short: 'scout drone (loc:' + locName + ' content:' + contentCount + ' neighbors:' + neighborCount + ' first:' + neighborInfo + ')'
	}]);
});
`
	if err := ts.WriteSource(scoutPath, scoutSource); err != nil {
		t.Fatalf("failed to create %s: %v", scoutPath, err)
	}
	if _, err := tc.createObject(scoutPath); err != nil {
		t.Fatalf("create scout: %v", err)
	}

	// Trigger the survey action
	if err := tc.sendLine("survey"); err != nil {
		t.Fatalf("survey command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("survey command did not complete")
	}

	// Wait for the scout to update its description with neighbourhood info
	// It should see Scout Room as location and have 2 neighbors (neighborRoom and genesis)
	_, found := tc.waitForObject("*scout*loc:Scout Room*content:*neighbors:2*", defaultWaitTimeout)
	if !found {
		t.Fatal("scout did not report neighbourhood info correctly - expected 2 neighbors")
	}

	// Cleanup
	if err := tc.sendLine("out"); err != nil {
		t.Fatalf("out command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("out command did not complete")
	}

	if err := tc.sendLine(fmt.Sprintf("/remove #%s", scoutRoomID)); err != nil {
		t.Fatalf("/remove scout_room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/remove scout_room did not complete")
	}

	if err := tc.sendLine(fmt.Sprintf("/remove #%s", neighborRoomID)); err != nil {
		t.Fatalf("/remove neighbor_room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/remove neighbor_room did not complete")
	}
}

// TestDebugLog tests /debug and log() functionality.
func TestDebugLog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions
	if _, err := tc.enterIsolatedRoom(ts, "debugLog"); err != nil {
		t.Fatalf("failed to enter isolated room for debugLog test: %v", err)
	}

	// Create an object that logs when triggered via an action
	sourcePath := uniqueSourcePath("logger")
	loggerSource := `setDescriptions([{Short: 'logger stone'}]);
addCallback('trigger', ['action'], (msg) => {
	log('DEBUG: trigger received with line:', msg.line);
	setDescriptions([{Short: 'logger stone (triggered)'}]);
});
`
	if err := ts.WriteSource(sourcePath, loggerSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}

	loggerID, err := tc.createObject(sourcePath)
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}

	// Test 1: Without /debug, log output should NOT appear
	if err := tc.sendLine("trigger logger"); err != nil {
		t.Fatalf("trigger without debug: %v", err)
	}
	output, ok := tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatalf("trigger without debug did not complete: %q", output)
	}
	// Verify no DEBUG message appears (we're not attached to console)
	if strings.Contains(output, "DEBUG:") {
		t.Fatalf("log output should NOT appear without /debug: %q", output)
	}

	// Wait for object to update its description
	_, found := tc.waitForLookMatch("logger stone (triggered)", defaultWaitTimeout)
	if !found {
		t.Fatal("logger should have updated description after trigger")
	}

	// Reset the logger for the next test (re-upload resets description to untriggered state)
	if err := ts.WriteSource(sourcePath, loggerSource); err != nil {
		t.Fatalf("failed to reset %s: %v", sourcePath, err)
	}

	// Wait for description to reset
	_, found = tc.waitForLookMatchFunc(func(s string) bool {
		return strings.Contains(s, "logger stone") && !strings.Contains(s, "(triggered)")
	}, defaultWaitTimeout)
	if !found {
		t.Fatal("logger should have reset description")
	}

	// Test 2: Attach console with /debug
	output, ok = tc.sendCommand(fmt.Sprintf("/debug #%s", loggerID), defaultWaitTimeout)
	if !ok {
		t.Fatalf("/debug command did not complete: %q", output)
	}
	if !strings.Contains(output, "connected to console") {
		t.Fatalf("/debug should show 'connected to console': %q", output)
	}

	// Trigger the logger - now log output should appear
	if err := tc.sendLine("trigger logger"); err != nil {
		t.Fatalf("trigger with debug: %v", err)
	}

	// Wait for output that includes the DEBUG message
	// The log output appears asynchronously, so we poll
	found = waitForCondition(defaultWaitTimeout, 100*time.Millisecond, func() bool {
		output = tc.readUntil(200*time.Millisecond, func(s string) bool {
			return strings.Contains(s, "DEBUG:")
		})
		return strings.Contains(output, "DEBUG: trigger received with line:")
	})
	if !found {
		t.Fatalf("log output should appear with /debug attached: %q", output)
	}

	// Wait for prompt after the action completes
	tc.waitForPrompt(defaultWaitTimeout)

	// Test 3: Detach with /undebug
	output, ok = tc.sendCommand(fmt.Sprintf("/undebug #%s", loggerID), defaultWaitTimeout)
	if !ok {
		t.Fatalf("/undebug command did not complete: %q", output)
	}
	if !strings.Contains(output, "disconnected from console") {
		t.Fatalf("/undebug should show 'disconnected from console': %q", output)
	}

	// Reset the logger again
	if err := ts.WriteSource(sourcePath, loggerSource); err != nil {
		t.Fatalf("failed to reset %s again: %v", sourcePath, err)
	}

	// Wait for description to reset
	_, found = tc.waitForLookMatchFunc(func(s string) bool {
		return strings.Contains(s, "logger stone") && !strings.Contains(s, "(triggered)")
	}, defaultWaitTimeout)
	if !found {
		t.Fatal("logger should have reset description again")
	}

	// Trigger again - log output should NOT appear anymore
	output, ok = tc.sendCommand("trigger logger", defaultWaitTimeout)
	if !ok {
		t.Fatalf("trigger after undebug did not complete: %q", output)
	}
	if strings.Contains(output, "DEBUG:") {
		t.Fatalf("log output should NOT appear after /undebug: %q", output)
	}
}

// TestSkillConfig tests getSkillConfig() and casSkillConfig() JS APIs.
func TestSkillConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions
	if _, err := tc.enterIsolatedRoom(ts, "skillConfig"); err != nil {
		t.Fatalf("failed to enter isolated room for skillConfig test: %v", err)
	}

	// Create an object that can query and set skill configs
	sourcePath := uniqueSourcePath("skill_config_test")
	// Use unique skill name to avoid conflicts with other tests
	skillName := fmt.Sprintf("TestConfigSkill_%d", sourceCounter.Load())
	skillConfigSource := fmt.Sprintf(`setDescriptions([{Short: 'skill config tester (ready)'}]);
addCallback('getconfig', ['action'], (msg) => {
	const config = getSkillConfig('%s');
	if (config === null) {
		setDescriptions([{Short: 'skill config tester (config:null)'}]);
	} else {
		setDescriptions([{Short: 'skill config tester (config:forget=' + config.Forget + ')'}]);
	}
});
addCallback('setconfig', ['action'], (msg) => {
	// Use CAS to set config - null as old value means "doesn't exist yet"
	const newConfig = {Forget: 3600, Recharge: 1000};
	const success = casSkillConfig('%s', null, newConfig);
	setDescriptions([{Short: 'skill config tester (set:' + success + ')'}]);
});
addCallback('updateconfig', ['action'], (msg) => {
	// Use CAS to update existing config
	const oldConfig = getSkillConfig('%s');
	if (oldConfig === null) {
		setDescriptions([{Short: 'skill config tester (update:noexist)'}]);
		return;
	}
	const newConfig = {Forget: 7200, Recharge: oldConfig.Recharge};
	const success = casSkillConfig('%s', oldConfig, newConfig);
	setDescriptions([{Short: 'skill config tester (update:' + success + ')'}]);
});
`, skillName, skillName, skillName, skillName)
	if err := ts.WriteSource(sourcePath, skillConfigSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}
	if _, err := tc.createObject(sourcePath); err != nil {
		t.Fatalf("create skill_config_test: %v", err)
	}

	// First query - should be null (doesn't exist yet)
	if err := tc.sendLine("getconfig"); err != nil {
		t.Fatalf("getconfig command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("getconfig command did not complete")
	}
	_, found := tc.waitForObject("*skill config tester*config:null*", defaultWaitTimeout)
	if !found {
		t.Fatal("skill config should be null initially")
	}

	// Set the config
	if err := tc.sendLine("setconfig"); err != nil {
		t.Fatalf("setconfig command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("setconfig command did not complete")
	}
	_, found = tc.waitForObject("*skill config tester*set:true*", defaultWaitTimeout)
	if !found {
		t.Fatal("casSkillConfig should return true for new config")
	}

	// Query again - should now have value
	if err := tc.sendLine("getconfig"); err != nil {
		t.Fatalf("second getconfig command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("second getconfig command did not complete")
	}
	_, found = tc.waitForObject("*skill config tester*config:forget=3600*", defaultWaitTimeout)
	if !found {
		t.Fatal("skill config should have Forget=3600 after set")
	}

	// Update the config using CAS
	if err := tc.sendLine("updateconfig"); err != nil {
		t.Fatalf("updateconfig command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("updateconfig command did not complete")
	}
	_, found = tc.waitForObject("*skill config tester*update:true*", defaultWaitTimeout)
	if !found {
		t.Fatal("casSkillConfig should return true for valid update")
	}

	// Verify update took effect
	if err := tc.sendLine("getconfig"); err != nil {
		t.Fatalf("third getconfig command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("third getconfig command did not complete")
	}
	_, found = tc.waitForObject("*skill config tester*config:forget=7200*", defaultWaitTimeout)
	if !found {
		t.Fatal("skill config should have Forget=7200 after update")
	}
}

// TestWizardCommands tests basic wizard commands: /inspect, /ls, /create.
func TestWizardCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Create a source file for an object
	boxPath := uniqueSourcePath("box")
	boxSource := `// A simple box
setDescriptions([{
	Short: 'wooden box',
	Long: 'A simple wooden box.',
}]);
`
	if err := ts.WriteSource(boxPath, boxSource); err != nil {
		t.Fatalf("failed to create %s: %v", boxPath, err)
	}

	// Test /create
	boxID, err := tc.createObject(boxPath)
	if err != nil {
		t.Fatalf("create box: %v", err)
	}

	// Test /inspect on the created object
	if err := tc.sendLine(fmt.Sprintf("/inspect #%s", boxID)); err != nil {
		t.Fatalf("/inspect command: %v", err)
	}
	inspectOutput, ok := tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatal("/inspect command did not complete")
	}
	if !strings.Contains(inspectOutput, "wooden box") {
		t.Fatalf("/inspect should show object description: %q", inspectOutput)
	}

	// Test /ls - verify output contains the box source file
	if err := tc.sendLine(fmt.Sprintf("/ls %s", boxPath)); err != nil {
		t.Fatalf("/ls command: %v", err)
	}
	lsOutput, ok := tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		t.Fatal("/ls command did not complete")
	}
	// /ls on a file path should show the file
	if !strings.Contains(lsOutput, "box") {
		t.Fatalf("/ls output should reference the box file: %q", lsOutput)
	}
}

// TestEnterExitCommands tests /enter and /exit wizard movement commands.
func TestEnterExitCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Create a test room
	roomPath := uniqueSourcePath("enter_test_room")
	roomSource := `setDescriptions([{
	Short: 'Enter Test Room',
	Unique: true,
	Long: 'A room for testing /enter and /exit.',
}]);
`
	if err := ts.WriteSource(roomPath, roomSource); err != nil {
		t.Fatalf("failed to create %s: %v", roomPath, err)
	}
	roomID, err := tc.createObject(roomPath)
	if err != nil {
		t.Fatalf("create room: %v", err)
	}

	// Move into the room using /enter
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", roomID)); err != nil {
		t.Fatalf("/enter command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter command did not complete")
	}

	// Verify user is in the room
	if !tc.waitForLocation("", roomID, defaultWaitTimeout) {
		t.Fatal("user did not move to room via /enter")
	}

	// Exit back out using /exit
	if err := tc.sendLine("/exit"); err != nil {
		t.Fatalf("/exit command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/exit command did not complete")
	}

	// Verify user is back in genesis
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		t.Fatal("user did not return to genesis via /exit")
	}
}

// TestLookCommand tests the look command with room descriptions, contents, and exits.
func TestLookCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Ensure we're in genesis first
	if err := tc.sendLine("/enter #genesis"); err != nil {
		t.Fatalf("/enter genesis: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter genesis did not complete")
	}

	// Test look in genesis
	output, ok := tc.waitForLookMatch("Black cosmos", defaultWaitTimeout)
	if !ok {
		t.Fatalf("look did not show genesis room name: %q", output)
	}
	if !strings.Contains(output, "darkness") {
		t.Fatalf("look did not show genesis long description: %q", output)
	}

	// Create a room with details and an exit
	lookRoomPath := uniqueSourcePath("look_test_room")
	lookRoomSource := `setDescriptions([{
	Short: 'Cozy Library',
	Unique: true,
	Long: 'A warm library filled with ancient tomes and comfortable chairs.',
}]);
setExits([{
	Descriptions: [{Short: 'north'}],
	Destination: 'genesis',
}]);
`
	if err := ts.WriteSource(lookRoomPath, lookRoomSource); err != nil {
		t.Fatalf("failed to create %s: %v", lookRoomPath, err)
	}
	lookRoomID, err := tc.createObject(lookRoomPath)
	if err != nil {
		t.Fatalf("create lookroom: %v", err)
	}

	// Create an object to place in the room
	bookPath := uniqueSourcePath("book")
	bookSource := `setDescriptions([{
	Short: 'dusty book',
	Unique: false,
	Long: 'An old book covered in dust.',
}]);
`
	if err := ts.WriteSource(bookPath, bookSource); err != nil {
		t.Fatalf("failed to create %s: %v", bookPath, err)
	}
	bookID, err := tc.createObject(bookPath)
	if err != nil {
		t.Fatalf("create book: %v", err)
	}

	// Move the book into the look room
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", bookID, lookRoomID)); err != nil {
		t.Fatalf("/move book to lookroom: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/move book to lookroom did not complete")
	}

	// Enter the room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", lookRoomID)); err != nil {
		t.Fatalf("/enter lookroom: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter lookroom did not complete")
	}
	if !tc.waitForLocation("", lookRoomID, defaultWaitTimeout) {
		t.Fatal("user did not move to lookroom")
	}

	// Test look command
	output, ok = tc.waitForLookMatch("Cozy Library", defaultWaitTimeout)
	if !ok {
		t.Fatalf("look did not show room name: %q", output)
	}
	if !strings.Contains(output, "ancient tomes") {
		t.Fatalf("look did not show long description: %q", output)
	}
	if !strings.Contains(output, "dusty book") {
		t.Fatalf("look did not show book in room: %q", output)
	}
	if !strings.Contains(output, "north") {
		t.Fatalf("look did not show exit 'north': %q", output)
	}

	// Test non-wizard movement: use the north exit to go back to genesis
	if err := tc.sendLine("north"); err != nil {
		t.Fatalf("north command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("north command did not complete")
	}
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		t.Fatal("user did not move to genesis via 'north' command")
	}
}

// TestBidirectionalMovement tests non-wizard movement through bidirectional exits.
func TestBidirectionalMovement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	tc.ensureInGenesis(t)

	// Create two rooms with bidirectional exits
	roomAID, roomBID := createBidirectionalRooms(t, ts, tc, "bidir", "Room Alpha", "Room Beta", "east", "west")

	// Enter room A
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", roomAID)); err != nil {
		t.Fatalf("/enter room A: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter room A did not complete")
	}

	// Verify we're in room A
	output, ok := tc.waitForLookMatch("Room Alpha", defaultWaitTimeout)
	if !ok {
		t.Fatalf("not in Room Alpha: %q", output)
	}

	// Move east to room B using non-wizard movement
	if err := tc.sendLine("east"); err != nil {
		t.Fatalf("east command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("east command did not complete")
	}
	if !tc.waitForLocation("", roomBID, defaultWaitTimeout) {
		t.Fatal("user did not move to room B via 'east' command")
	}

	// Verify we're in room B
	output, ok = tc.waitForLookMatch("Room Beta", defaultWaitTimeout)
	if !ok {
		t.Fatalf("not in Room Beta: %q", output)
	}

	// Move west back to room A
	if err := tc.sendLine("west"); err != nil {
		t.Fatalf("west command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("west command did not complete")
	}
	if !tc.waitForLocation("", roomAID, defaultWaitTimeout) {
		t.Fatal("user did not move back to room A via 'west' command")
	}
}

// TestScanCommand tests the scan command which shows the neighborhood.
func TestScanCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	tc.ensureInGenesis(t)

	// Create two rooms with bidirectional exits
	roomAID, _ := createBidirectionalRooms(t, ts, tc, "scan", "Scan Room", "Neighbor Room", "south", "north")

	// Enter room A
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", roomAID)); err != nil {
		t.Fatalf("/enter room A: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter room A did not complete")
	}

	// Test scan command
	output, ok := tc.waitForScanMatch("Scan Room", defaultWaitTimeout)
	if !ok {
		t.Fatalf("scan did not show current location: %q", output)
	}
	if !strings.Contains(output, "Via exit south") {
		t.Fatalf("scan did not show 'Via exit south': %q", output)
	}
	if !strings.Contains(output, "Neighbor Room") {
		t.Fatalf("scan did not show neighboring room: %q", output)
	}
}

// TestChallengeSystem tests perception and strength skill challenges.
func TestChallengeSystem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	// Use isolated room to prevent action name collisions
	if _, err := tc.enterIsolatedRoom(ts, "challenge"); err != nil {
		t.Fatalf("failed to enter isolated room: %v", err)
	}

	// Create a challenge room with a hidden object and a skill-gated exit
	challengeRoomPath := uniqueSourcePath("challenge_room")
	challengeRoomSource := `setDescriptions([{
	Short: 'Challenge Room',
	Unique: true,
	Long: 'A room for testing the challenge system.',
}]);
setExits([
	{
		Descriptions: [{Short: 'easy'}],
		Destination: 'genesis',
	},
	{
		Descriptions: [{Short: 'locked'}],
		Destination: 'genesis',
		UseChallenges: [{Skill: 'strength', Level: 100}],
	},
]);
`
	if err := ts.WriteSource(challengeRoomPath, challengeRoomSource); err != nil {
		t.Fatalf("failed to create %s: %v", challengeRoomPath, err)
	}
	challengeRoomID, err := tc.createObject(challengeRoomPath)
	if err != nil {
		t.Fatalf("create challenge_room: %v", err)
	}

	// Create a hidden gem that requires high perception to see
	hiddenGemPath := uniqueSourcePath("hidden_gem")
	hiddenGemSource := `setDescriptions([{
	Short: 'hidden gem',
	Long: 'A sparkling gem hidden in the shadows.',
	Challenges: [{Skill: 'perception', Level: 100}],
}]);
`
	if err := ts.WriteSource(hiddenGemPath, hiddenGemSource); err != nil {
		t.Fatalf("failed to create %s: %v", hiddenGemPath, err)
	}
	hiddenGemID, err := tc.createObject(hiddenGemPath)
	if err != nil {
		t.Fatalf("create hidden_gem: %v", err)
	}

	// Move the gem into the challenge room
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", hiddenGemID, challengeRoomID)); err != nil {
		t.Fatalf("/move gem to challenge_room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/move gem did not complete")
	}
	if !tc.waitForLocation(fmt.Sprintf("#%s", hiddenGemID), challengeRoomID, defaultWaitTimeout) {
		t.Fatal("hidden gem did not move to challenge_room")
	}

	// Enter the challenge room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", challengeRoomID)); err != nil {
		t.Fatalf("/enter challenge_room: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter challenge_room did not complete")
	}
	if !tc.waitForLocation("", challengeRoomID, defaultWaitTimeout) {
		t.Fatal("user did not move to challenge_room")
	}

	// Test 1: Look should NOT show the hidden gem (user has no perception skill)
	output, ok := tc.waitForLookMatch("Challenge Room", defaultWaitTimeout)
	if !ok {
		t.Fatalf("look did not show challenge room: %q", output)
	}
	if strings.Contains(output, "hidden gem") {
		t.Fatalf("look should NOT show hidden gem without perception skill: %q", output)
	}

	// Test 2: Verify both exits are visible
	if !strings.Contains(output, "easy") {
		t.Fatalf("look did not show 'easy' exit: %q", output)
	}
	if !strings.Contains(output, "locked") {
		t.Fatalf("look did not show 'locked' exit: %q", output)
	}

	// Test 3: Try to use the locked exit (should fail - no strength skill)
	if err := tc.sendLine("locked"); err != nil {
		t.Fatalf("locked exit command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("locked exit command did not complete")
	}
	// Verify user is still in challenge room
	selfInspect, err := tc.inspect("")
	if err != nil {
		t.Fatalf("failed to inspect self: %v", err)
	}
	if selfInspect.GetLocation() != challengeRoomID {
		t.Fatalf("user should still be in challenge_room after failed exit, but is in %q", selfInspect.GetLocation())
	}

	// Test 4: Use the easy exit (should succeed)
	if err := tc.sendLine("easy"); err != nil {
		t.Fatalf("easy exit command: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("easy exit command did not complete")
	}
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		t.Fatal("user did not move to genesis via 'easy' exit")
	}

	// Clean up - ensure we're back in genesis
	tc.ensureInGenesis(t)
}

// TestSkillsCommand tests the /skills wizard command.
func TestSkillsCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	tc.ensureInGenesis(t)

	// Create a test object
	skillTestPath := uniqueSourcePath("skill_test_obj")
	skillTestSource := `setDescriptions([{Short: 'skill test object'}]);`
	if err := ts.WriteSource(skillTestPath, skillTestSource); err != nil {
		t.Fatalf("failed to create %s: %v", skillTestPath, err)
	}
	objID, err := tc.createObject(skillTestPath)
	if err != nil {
		t.Fatalf("create skill_test_obj: %v", err)
	}

	// Test 1: View skills on object with no skills
	output, ok := tc.sendCommand(fmt.Sprintf("/skills #%s", objID), defaultWaitTimeout)
	if !ok {
		t.Fatalf("/skills command did not complete: %q", output)
	}
	if !strings.Contains(output, "has no skills") {
		t.Fatalf("/skills should show 'has no skills' for object without skills: %q", output)
	}

	// Test 2: Set a skill
	output, ok = tc.sendCommand(fmt.Sprintf("/skills #%s perception 75.5 50.0", objID), defaultWaitTimeout)
	if !ok {
		t.Fatalf("/skills set command did not complete: %q", output)
	}
	if !strings.Contains(output, "Set perception") || !strings.Contains(output, "75.5") {
		t.Fatalf("/skills set should confirm skill was set: %q", output)
	}

	// Test 3: View skills to verify it was set
	output, ok = tc.sendCommand(fmt.Sprintf("/skills #%s", objID), defaultWaitTimeout)
	if !ok {
		t.Fatalf("/skills view command did not complete: %q", output)
	}
	if !strings.Contains(output, "perception") || !strings.Contains(output, "75.5") || !strings.Contains(output, "50.0") {
		t.Fatalf("/skills should show the set skill: %q", output)
	}

	// Test 4: Set another skill
	output, ok = tc.sendCommand(fmt.Sprintf("/skills #%s strength 100 80", objID), defaultWaitTimeout)
	if !ok {
		t.Fatalf("/skills set strength did not complete: %q", output)
	}

	// Test 5: View both skills
	output, ok = tc.sendCommand(fmt.Sprintf("/skills #%s", objID), defaultWaitTimeout)
	if !ok {
		t.Fatalf("/skills view both did not complete: %q", output)
	}
	if !strings.Contains(output, "perception") || !strings.Contains(output, "strength") {
		t.Fatalf("/skills should show both skills: %q", output)
	}

	// Clean up
	tc.removeObject(t, objID, false)
}

// TestErrorCases tests error handling for various invalid inputs.
func TestErrorCases(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tc := wizardClient
	ts := testServer

	tc.ensureInGenesis(t)

	t.Run("CreateNonExistentPath", func(t *testing.T) {
		// Try to create an object from a non-existent source path
		output, ok := tc.sendCommand("/create /nonexistent/path.js", defaultWaitTimeout)
		if !ok {
			t.Fatalf("/create did not complete: %q", output)
		}
		// Should get an error message, not a success with object ID
		if strings.Contains(output, "Created") {
			t.Fatalf("/create should fail for non-existent path: %q", output)
		}
	})

	t.Run("InspectInvalidObjectID", func(t *testing.T) {
		// Try to inspect a non-existent object ID
		output, ok := tc.sendCommand("/inspect #invalid-object-id-12345", defaultWaitTimeout)
		if !ok {
			t.Fatalf("/inspect did not complete: %q", output)
		}
		// Should get an error, not valid JSON output
		if strings.Contains(output, `"Id":`) {
			t.Fatalf("/inspect should fail for invalid ID: %q", output)
		}
	})

	t.Run("JSSyntaxError", func(t *testing.T) {
		// Create a source file with JavaScript syntax errors
		badSourcePath := uniqueSourcePath("syntax_error")
		badSource := `setDescriptions([{
	Short: 'broken object',
	// Missing closing bracket and syntax error
	Long: 'This is broken
}]);
function unclosed( {
`
		if err := ts.WriteSource(badSourcePath, badSource); err != nil {
			t.Fatalf("failed to create %s: %v", badSourcePath, err)
		}

		// Try to create an object from the bad source
		// With proper JS syntax errors, /create should fail or show an error
		output, ok := tc.sendCommand("/create "+badSourcePath, defaultWaitTimeout)
		if !ok {
			t.Fatalf("/create did not complete: %q", output)
		}

		// Check the actual behavior:
		// 1. If /create reports an error (doesn't show "Created"), that's correct
		// 2. If /create shows "Created", the syntax error was deferred
		if !strings.Contains(output, "Created") {
			// Good - syntax error was caught during creation
			t.Logf("Syntax error correctly caught during /create: %q", output)
			return
		}

		// Object was created - extract ID and verify the broken state
		idMatch := regexp.MustCompile(`Created #(\S+)`).FindStringSubmatch(output)
		if len(idMatch) < 2 {
			t.Fatalf("could not extract object ID from: %q", output)
		}
		brokenID := idMatch[1]
		t.Logf("Object created with deferred syntax error: ID=%s", brokenID)

		// The object exists but has broken JS - verify /inspect still works
		inspectOutput, ok := tc.sendCommand("/inspect #"+brokenID, defaultWaitTimeout)
		if !ok {
			t.Fatalf("/inspect broken object did not complete: %q", inspectOutput)
		}
		// Should show the source path pointing to our bad source
		if !strings.Contains(inspectOutput, badSourcePath) {
			t.Fatalf("broken object should reference bad source path: %q", inspectOutput)
		}

		// Clean up the broken object
		tc.removeObject(t, brokenID, false)
	})

	t.Run("MoveInvalidObject", func(t *testing.T) {
		// Try to move a non-existent object
		output, ok := tc.sendCommand("/move #nonexistent-123 #genesis", defaultWaitTimeout)
		if !ok {
			t.Fatalf("/move did not complete: %q", output)
		}
		// Should get an error
		if strings.Contains(output, "Moved") {
			t.Fatalf("/move should fail for invalid object: %q", output)
		}
	})

	t.Run("RemoveInvalidObject", func(t *testing.T) {
		// Try to remove a non-existent object
		output, ok := tc.sendCommand("/remove #nonexistent-456", defaultWaitTimeout)
		if !ok {
			t.Fatalf("/remove did not complete: %q", output)
		}
		// Should get an error
		if strings.Contains(output, "Removed") {
			t.Fatalf("/remove should fail for invalid object: %q", output)
		}
	})
}

// TestAuthenticationErrors tests authentication error handling.
// Note: The server loops on auth errors rather than returning errors,
// so we need to check the output messages and abort cleanly.
func TestAuthenticationErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer

	t.Run("DuplicateUserCreation", func(t *testing.T) {
		// Create a unique user first
		username := fmt.Sprintf("duptest_%d", sourceCounter.Add(1))
		password := "testpass123"

		tc1, err := createUser(ts.SSHAddr(), username, password)
		if err != nil {
			t.Fatalf("first createUser: %v", err)
		}
		tc1.Close()

		// Start a new connection and try to create the same user
		tc2, err := newTerminalClient(ts.SSHAddr())
		if err != nil {
			t.Fatalf("newTerminalClient: %v", err)
		}
		defer tc2.Close()

		if _, ok := tc2.waitForPrompt(defaultWaitTimeout); !ok {
			t.Fatal("did not get initial prompt")
		}
		if err := tc2.sendLine("create user"); err != nil {
			t.Fatalf("sendLine create user: %v", err)
		}
		if _, ok := tc2.waitForPrompt(defaultWaitTimeout); !ok {
			t.Fatal("create user prompt did not appear")
		}
		// Send the duplicate username
		if err := tc2.sendLine(username); err != nil {
			t.Fatalf("sendLine username: %v", err)
		}

		// Should see "Username already exists!" and loop back for another username
		output, ok := tc2.waitForPrompt(defaultWaitTimeout)
		if !ok {
			t.Fatal("prompt after duplicate username did not appear")
		}
		if !strings.Contains(output, "Username already exists") {
			t.Errorf("expected 'Username already exists' error, got: %q", output)
		}

		// Abort to clean up
		if err := tc2.sendLine("abort"); err != nil {
			t.Fatalf("sendLine abort: %v", err)
		}
	})

	t.Run("InvalidPassword", func(t *testing.T) {
		// Create a user
		username := fmt.Sprintf("passtest_%d", sourceCounter.Add(1))
		password := "correctpass"

		tc1, err := createUser(ts.SSHAddr(), username, password)
		if err != nil {
			t.Fatalf("createUser: %v", err)
		}
		tc1.Close()

		// Start a new connection and try to login with wrong password
		tc2, err := newTerminalClient(ts.SSHAddr())
		if err != nil {
			t.Fatalf("newTerminalClient: %v", err)
		}
		defer tc2.Close()

		if _, ok := tc2.waitForPrompt(defaultWaitTimeout); !ok {
			t.Fatal("did not get initial prompt")
		}
		if err := tc2.sendLine("login user"); err != nil {
			t.Fatalf("sendLine login user: %v", err)
		}
		if _, ok := tc2.waitForPrompt(defaultWaitTimeout); !ok {
			t.Fatal("login user prompt did not appear")
		}
		// Send username
		if err := tc2.sendLine(username); err != nil {
			t.Fatalf("sendLine username: %v", err)
		}
		if _, ok := tc2.waitForPrompt(defaultWaitTimeout); !ok {
			t.Fatal("password prompt did not appear")
		}
		// Send wrong password
		if err := tc2.sendLine("wrongpassword"); err != nil {
			t.Fatalf("sendLine wrong password: %v", err)
		}

		// Should see "Invalid credentials!" and loop back
		output, ok := tc2.waitForPrompt(defaultWaitTimeout)
		if !ok {
			t.Fatal("prompt after invalid password did not appear")
		}
		if !strings.Contains(output, "Invalid credentials") {
			t.Errorf("expected 'Invalid credentials' error, got: %q", output)
		}

		// Abort to clean up
		if err := tc2.sendLine("abort"); err != nil {
			t.Fatalf("sendLine abort: %v", err)
		}
	})

	t.Run("NonExistentUser", func(t *testing.T) {
		// Start a new connection and try to login as non-existent user
		tc, err := newTerminalClient(ts.SSHAddr())
		if err != nil {
			t.Fatalf("newTerminalClient: %v", err)
		}
		defer tc.Close()

		if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
			t.Fatal("did not get initial prompt")
		}
		if err := tc.sendLine("login user"); err != nil {
			t.Fatalf("sendLine login user: %v", err)
		}
		if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
			t.Fatal("login user prompt did not appear")
		}
		// Send non-existent username
		if err := tc.sendLine("nonexistent_user_xyz"); err != nil {
			t.Fatalf("sendLine username: %v", err)
		}
		if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
			t.Fatal("password prompt did not appear")
		}
		// Send any password
		if err := tc.sendLine("anypassword"); err != nil {
			t.Fatalf("sendLine password: %v", err)
		}

		// Should see "Invalid credentials!" (same message as wrong password for security)
		output, ok := tc.waitForPrompt(defaultWaitTimeout)
		if !ok {
			t.Fatal("prompt after non-existent user did not appear")
		}
		if !strings.Contains(output, "Invalid credentials") {
			t.Errorf("expected 'Invalid credentials' error, got: %q", output)
		}

		// Abort to clean up
		if err := tc.sendLine("abort"); err != nil {
			t.Fatalf("sendLine abort: %v", err)
		}
	})
}
