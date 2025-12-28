package integration_test

import (
	"context"
	"fmt"
	"os"
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

	// Cleanup
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", pulsingGemID)); err != nil {
		t.Fatalf("/remove pulsing_gem: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/remove pulsing_gem did not complete")
	}
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

	// Cleanup
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", intervalListerID)); err != nil {
		t.Fatalf("/remove interval_lister: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/remove interval_lister did not complete")
	}
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

	// Cleanup spawner
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", spawnerID)); err != nil {
		t.Fatalf("/remove spawner: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/remove spawner did not complete")
	}
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
	if err := tc.sendLine("/delwiz " + testUsername); err != nil {
		t.Fatalf("/delwiz command: %v", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
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
	actionRoomPath := uniqueSourcePath("actionroom")
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
		t.Fatalf("create actionroom: %v", err)
	}

	// Enter the action room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", actionRoomID)); err != nil {
		t.Fatalf("/enter actionroom: %v", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		t.Fatal("/enter actionroom did not complete")
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
