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
	sourcePath := uniqueSourcePath("removetestroom")
	if err := testServer.WriteSource(sourcePath, removeTestRoomSource); err != nil {
		t.Fatalf("failed to create %s: %v", sourcePath, err)
	}

	removeTestRoomID, err := tc.createObject(sourcePath)
	if err != nil {
		t.Fatalf("create removetestroom: %v", err)
	}

	// Enter the test room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", removeTestRoomID)); err != nil {
		t.Fatalf("/enter removetestroom: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter removetestroom did not complete")
	}

	// Verify we're in the test room
	if !tc.waitForLocation("", removeTestRoomID, defaultWaitTimeout) {
		t.Fatal("should be in removetestroom")
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
		t.Fatalf("out from removetestroom: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("out from removetestroom did not complete")
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
