// Package integration_test provides integration tests for the juicemud server.
//
// # Testing Principles
//
// All interactions should use the same interfaces as production: SSH for game
// commands. Source files are written directly to the filesystem. Direct function
// calls on the test server should only be used for:
//   - Necessary setup/teardown operations
//   - Verification that actions succeeded, and only when verifying
//     via SSH would be unnecessarily complex
//
// # Test Structure
//
// The integration test runs through a comprehensive happy path covering as much
// server functionality as possible. All objects, rooms, and connections created
// during the test should form a coherent game world - rooms should be connected
// via exits, objects should be placed meaningfully, etc.
//
// # Debugging Support
//
// A separate binary (bin/integration_test/main.go) runs these tests and leaves
// the server running afterward, allowing developers to connect via SSH and
// inspect the test world. This is much more useful when the created rooms and
// objects are properly interconnected rather than isolated test fixtures.
package integration_test

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// defaultWaitTimeout is the default timeout for wait operations.
	// This is intentionally generous to avoid flaky tests on slow systems.
	// On fast systems, polling returns immediately when the condition is met.
	defaultWaitTimeout = 5 * time.Second
)

// RunAll runs all integration tests in sequence on a single server.
// Returns nil on success, or an error describing what failed.
func RunAll(ts *TestServer) error {
	ctx := context.Background()

	// === Test 1: User creation and login ===
	fmt.Println("Testing user creation and login...")

	// Create user and verify initial state
	if err := func() error {
		tc, err := createUser(ts.SSHAddr(), "testuser", "testpass123")
		if err != nil {
			return fmt.Errorf("createUser: %w", err)
		}
		defer tc.Close()

		if err := tc.sendLine("look"); err != nil {
			return fmt.Errorf("look command: %w", err)
		}
		// Verify "look" command produces output containing the genesis room description
		if output, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
			return fmt.Errorf("look command did not complete: %q", output)
		} else if !strings.Contains(output, "Black cosmos") {
			return fmt.Errorf("look command did not show genesis room: %q", output)
		}
		return nil
	}(); err != nil {
		return err
	}

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

	// Test reconnection - this specifically tests that login works after disconnect
	if err := func() error {
		tc, err := loginUser(ts.SSHAddr(), "testuser", "testpass123")
		if err != nil {
			return fmt.Errorf("loginUser: %w", err)
		}
		defer tc.Close()

		if err := tc.sendLine("look"); err != nil {
			return fmt.Errorf("look command on reconnect: %w", err)
		}
		if output, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
			return fmt.Errorf("look command on reconnect did not complete: %q", output)
		} else if !strings.Contains(output, "Black cosmos") {
			return fmt.Errorf("look command on reconnect did not show genesis room: %q", output)
		}

		// Verify same object
		user2, err := ts.Storage().LoadUser(ctx, "testuser")
		if err != nil {
			return fmt.Errorf("user not found on reconnect: %w", err)
		}
		if user2.Object != user.Object {
			return fmt.Errorf("user object changed: %s -> %s", user.Object, user2.Object)
		}
		return nil
	}(); err != nil {
		return err
	}

	fmt.Println("  User creation and login: OK")

	// === Test 2: Wizard setup ===
	fmt.Println("Setting up wizard access...")

	// Make testuser an owner and wizard for subsequent tests
	user.Owner = true
	user.Wizard = true
	if err := ts.Storage().StoreUser(ctx, user, true, ""); err != nil {
		return fmt.Errorf("failed to make testuser owner/wizard: %w", err)
	}

	// Reconnect to pick up owner/wizard status
	tc, err := loginUser(ts.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		return fmt.Errorf("loginUser as wizard: %w", err)
	}
	defer tc.Close()

	// Verify we can read existing source file
	content, err := ts.ReadSource("/user.js")
	if err != nil {
		return fmt.Errorf("failed to read /user.js: %w", err)
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
	if err := ts.WriteSource("/testroom.js", roomSource); err != nil {
		return fmt.Errorf("failed to write /testroom.js: %w", err)
	}

	// Verify file was created
	readBack, err := ts.ReadSource("/testroom.js")
	if err != nil {
		return fmt.Errorf("failed to read /testroom.js: %w", err)
	}
	if readBack != roomSource {
		return fmt.Errorf("file content mismatch: got %q, want %q", readBack, roomSource)
	}

	fmt.Println("  Wizard setup: OK")

	// === Test 3: Wizard commands ===
	fmt.Println("Testing wizard commands...")

	// Create a source file for objects
	boxSource := `// A simple box
setDescriptions([{
	Short: 'wooden box',
	Long: 'A simple wooden box.',
}]);
`
	if err := ts.WriteSource("/box.js", boxSource); err != nil {
		return fmt.Errorf("failed to create /box.js: %w", err)
	}

	// Create an object (user is already connected and is now a wizard)
	if _, err := tc.createObject("/box.js"); err != nil {
		return fmt.Errorf("create box: %w", err)
	}

	// Test /inspect
	if err := tc.sendLine("/inspect"); err != nil {
		return fmt.Errorf("/inspect command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/inspect command did not complete")
	}

	// Test /ls - verify output contains box.js which was just created
	if err := tc.sendLine("/ls /"); err != nil {
		return fmt.Errorf("/ls command: %w", err)
	}
	lsOutput, ok := tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/ls command did not complete")
	}
	if !strings.Contains(lsOutput, "box.js") {
		return fmt.Errorf("/ls output should contain box.js: %q", lsOutput)
	}

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
	if err := ts.WriteSource("/room1.js", room1Source); err != nil {
		return fmt.Errorf("failed to create /room1.js: %w", err)
	}
	if err := ts.WriteSource("/room2.js", room2Source); err != nil {
		return fmt.Errorf("failed to create /room2.js: %w", err)
	}

	// Create rooms
	room1ID, err := tc.createObject("/room1.js")
	if err != nil {
		return fmt.Errorf("create room1: %w", err)
	}
	if _, err := tc.createObject("/room2.js"); err != nil {
		return fmt.Errorf("create room2: %w", err)
	}

	// Move into room1 using /enter
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", room1ID)); err != nil {
		return fmt.Errorf("/enter command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter command did not complete")
	}

	// Poll for user to be in room1
	if !tc.waitForLocation("", room1ID, defaultWaitTimeout) {
		return fmt.Errorf("user did not move to room1")
	}

	// Exit back out
	if err := tc.sendLine("/exit"); err != nil {
		return fmt.Errorf("/exit command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/exit command did not complete")
	}

	// Poll for user to be back in genesis
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		return fmt.Errorf("user did not return to genesis")
	}

	fmt.Println("  Movement: OK")

	// === Test 5: Look command ===
	fmt.Println("Testing look command...")

	// Look at genesis - should show "Black cosmos" and the long description
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look in genesis: %w", err)
	}

	output, ok := tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look in genesis did not complete: %q", output)
	}
	if !strings.Contains(output, "Black cosmos") {
		return fmt.Errorf("look did not show genesis room name: %q", output)
	}

	// Verify genesis long description is shown
	if !strings.Contains(output, "darkness") {
		return fmt.Errorf("look did not show genesis long description: %q", output)
	}

	// Now create a room with a detailed description and exit back to genesis
	lookRoomSource := `// Look test room
setDescriptions([{
	Short: 'Cozy Library',
	Unique: true,
	Long: 'A warm library filled with ancient tomes and comfortable chairs.',
}]);
setExits([{
	Descriptions: [{Short: 'north'}],
	Destination: 'genesis',
}]);
`
	if err := ts.WriteSource("/lookroom.js", lookRoomSource); err != nil {
		return fmt.Errorf("failed to create /lookroom.js: %w", err)
	}

	// Create an object to place in the room
	bookSource := `// A book
setDescriptions([{
	Short: 'dusty book',
	Unique: false,
	Long: 'An old book covered in dust.',
}]);
`
	if err := ts.WriteSource("/book.js", bookSource); err != nil {
		return fmt.Errorf("failed to create /book.js: %w", err)
	}

	lookRoomID, err := tc.createObject("/lookroom.js")
	if err != nil {
		return fmt.Errorf("create lookroom: %w", err)
	}

	bookID, err := tc.createObject("/book.js")
	if err != nil {
		return fmt.Errorf("create book: %w", err)
	}

	// Move the book into the look room
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", bookID, lookRoomID)); err != nil {
		return fmt.Errorf("/move book to lookroom: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/move book to lookroom did not complete")
	}

	// Enter the room using wizard command
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", lookRoomID)); err != nil {
		return fmt.Errorf("/enter lookroom: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter lookroom did not complete")
	}

	// Verify user is in the room
	if !tc.waitForLocation("", lookRoomID, defaultWaitTimeout) {
		return fmt.Errorf("user did not move to lookroom")
	}

	// Now test the look command
	// Use waitForLookMatch to handle any stale /inspect output from waitForLocation
	output, ok = tc.waitForLookMatch("Cozy Library", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look did not show room name: %q", output)
	}

	// Verify look shows the long description
	if !strings.Contains(output, "ancient tomes") {
		return fmt.Errorf("look did not show long description: %q", output)
	}

	// Verify look shows the book in the room
	if !strings.Contains(output, "dusty book") {
		return fmt.Errorf("look did not show book in room: %q", output)
	}

	// Verify look shows the exit
	if !strings.Contains(output, "north") {
		return fmt.Errorf("look did not show exit 'north': %q", output)
	}

	// Test non-wizard movement: use the north exit to go back to genesis
	if err := tc.sendLine("north"); err != nil {
		return fmt.Errorf("north command: %w", err)
	}
	// Wait for prompt to confirm command completed
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("north command did not complete")
	}

	// Verify player moved back to genesis
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		return fmt.Errorf("user did not move to genesis via 'north' command")
	}

	fmt.Println("  Look command: OK")

	// === Test 6: Bidirectional non-wizard movement ===
	fmt.Println("Testing bidirectional movement...")

	// Debug: verify look works before updating genesis.js
	// Use waitForLookMatch to handle any stale async notifications
	output, ok = tc.waitForLookMatch("Black cosmos", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look before genesis update failed - not in genesis: %q", output)
	}

	// Update genesis to have an exit south to the lookroom
	genesisSource := fmt.Sprintf(`// Genesis room - the starting location
setDescriptions([{
	Short: 'Black cosmos',
	Unique: true,
	Long: 'An infinite void of darkness stretches in all directions.',
}]);
setExits([{
	Descriptions: [{Short: 'south'}],
	Destination: '%s',
}]);
`, lookRoomID)
	if err := ts.WriteSource("/genesis.js", genesisSource); err != nil {
		return fmt.Errorf("failed to update /genesis.js: %w", err)
	}

	// Verify we're in genesis (user is already connected, no need to reconnect)
	// Use waitForLookMatch to handle any stale async notifications
	output, ok = tc.waitForLookMatch("Black cosmos", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("not in genesis: %q", output)
	}

	// Move south to lookroom using non-wizard movement
	if err := tc.sendLine("south"); err != nil {
		return fmt.Errorf("south command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("south command did not complete")
	}

	// Verify player moved to lookroom
	if !tc.waitForLocation("", lookRoomID, defaultWaitTimeout) {
		return fmt.Errorf("user did not move to lookroom via 'south' command")
	}

	// Look to verify we're in lookroom
	// Use waitForLookMatch to handle any stale async notifications
	output, ok = tc.waitForLookMatch("Cozy Library", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("not in Cozy Library after south: %q", output)
	}

	// Move north back to genesis
	if err := tc.sendLine("north"); err != nil {
		return fmt.Errorf("north command back to genesis: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("north command back to genesis did not complete")
	}

	// Verify player moved back to genesis
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		return fmt.Errorf("user did not move back to genesis via 'north' command")
	}

	fmt.Println("  Bidirectional movement: OK")

	// === Test 7: Scan command ===
	fmt.Println("Testing scan command...")

	// From genesis, scan should show genesis and the lookroom (via south exit)
	// Use waitForScanMatch to handle any stale movement notifications
	output, ok = tc.waitForScanMatch("Black cosmos", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("scan did not show current location: %q", output)
	}

	// Verify scan shows neighboring room through exit
	if !strings.Contains(output, "Via exit south") {
		return fmt.Errorf("scan did not show 'Via exit south': %q", output)
	}

	// Verify scan shows the neighboring room's name
	if !strings.Contains(output, "Cozy Library") {
		return fmt.Errorf("scan did not show neighboring room 'Cozy Library': %q", output)
	}

	fmt.Println("  Scan command: OK")

	// === Test 8: Challenge system ===
	fmt.Println("Testing challenge system...")

	// Create a room with a hidden object and a skill-gated exit
	challengeRoomSource := `// Challenge test room
setDescriptions([{
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
	if err := ts.WriteSource("/challenge_room.js", challengeRoomSource); err != nil {
		return fmt.Errorf("failed to create /challenge_room.js: %w", err)
	}

	// Create a hidden gem that requires high perception to see
	hiddenGemSource := `// A hidden gem
setDescriptions([{
	Short: 'hidden gem',
	Long: 'A sparkling gem hidden in the shadows.',
	Challenges: [{Skill: 'perception', Level: 100}],
}]);
`
	if err := ts.WriteSource("/hidden_gem.js", hiddenGemSource); err != nil {
		return fmt.Errorf("failed to create /hidden_gem.js: %w", err)
	}

	// Create the challenge room
	challengeRoomID, err := tc.createObject("/challenge_room.js")
	if err != nil {
		return fmt.Errorf("create challenge_room: %w", err)
	}

	// Create the hidden gem (ID returned by /create since perception challenge makes it invisible)
	hiddenGemID, err := tc.createObject("/hidden_gem.js")
	if err != nil {
		return fmt.Errorf("create hidden_gem: %w", err)
	}

	// Move the gem into the challenge room
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", hiddenGemID, challengeRoomID)); err != nil {
		return fmt.Errorf("/move gem to challenge_room: %w", err)
	}
	// Wait for prompt to confirm command was processed
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/move command did not complete")
	}

	// Wait for gem to be moved before entering room
	if !tc.waitForLocation(fmt.Sprintf("#%s", hiddenGemID), challengeRoomID, defaultWaitTimeout) {
		// Debug: check where the gem actually is
		gemInspect, err := tc.inspect(fmt.Sprintf("#%s", hiddenGemID))
		if err != nil {
			return fmt.Errorf("hidden gem did not move to challenge_room (inspect failed: %v)", err)
		}
		return fmt.Errorf("hidden gem did not move to challenge_room, it is in %q", gemInspect.GetLocation())
	}

	// Enter the challenge room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", challengeRoomID)); err != nil {
		return fmt.Errorf("/enter challenge_room: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter challenge_room did not complete")
	}

	// Verify user is in the challenge room
	if !tc.waitForLocation("", challengeRoomID, defaultWaitTimeout) {
		return fmt.Errorf("user did not move to challenge_room")
	}

	// Test 1: Look should NOT show the hidden gem (user has no perception skill)
	// Use waitForLookMatch to handle any stale /inspect output from waitForLocation
	output, ok = tc.waitForLookMatch("Challenge Room", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look did not show challenge room: %q", output)
	}
	if strings.Contains(output, "hidden gem") {
		return fmt.Errorf("look should NOT show hidden gem without perception skill: %q", output)
	}

	// Test 2: Verify both exits are visible (no perception challenge on exit descriptions)
	if !strings.Contains(output, "easy") {
		return fmt.Errorf("look did not show 'easy' exit: %q", output)
	}
	if !strings.Contains(output, "locked") {
		return fmt.Errorf("look did not show 'locked' exit: %q", output)
	}

	// Test 3: Try to use the locked exit (should fail - no strength skill)
	if err := tc.sendLine("locked"); err != nil {
		return fmt.Errorf("locked exit command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("locked exit command did not complete")
	}

	// Verify user is still in challenge room (movement failed)
	selfInspect, err := tc.inspect("")
	if err != nil {
		return fmt.Errorf("failed to inspect self: %w", err)
	}
	if selfInspect.GetLocation() != challengeRoomID {
		return fmt.Errorf("user should still be in challenge_room after failed exit, but is in %q", selfInspect.GetLocation())
	}

	// Test 4: Use the easy exit (should succeed - no challenge)
	if err := tc.sendLine("easy"); err != nil {
		return fmt.Errorf("easy exit command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("easy exit command did not complete")
	}

	// Verify user moved to genesis
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		return fmt.Errorf("user did not move to genesis via 'easy' exit")
	}

	// Test 5: Give user skills via a "train" command and verify challenges now pass
	// Update user.js to register a "train" command that grants skills
	// The game will reload the source when the command is called since mod time changed
	trainableUserSource := `// User object with trainable skills
addCallback('connected', ['emit'], (msg) => {});
addCallback('train', ['command'], (msg) => {
	setSkills({
		perception: {Practical: 200, Theoretical: 200},
		strength: {Practical: 200, Theoretical: 200},
	});
});
`
	if err := ts.WriteSource("/user.js", trainableUserSource); err != nil {
		return fmt.Errorf("failed to update /user.js with train command: %w", err)
	}

	// Use the "train" command to gain skills (game reloads source automatically)
	if err := tc.sendLine("train"); err != nil {
		return fmt.Errorf("train command: %w", err)
	}
	// Wait for prompt to confirm command was processed
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("train command did not complete")
	}

	// Enter the challenge room again
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", challengeRoomID)); err != nil {
		return fmt.Errorf("/enter challenge_room with skills: %w", err)
	}
	// Wait for prompt to confirm command was processed
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter challenge_room with skills did not complete")
	}

	if !tc.waitForLocation("", challengeRoomID, defaultWaitTimeout) {
		return fmt.Errorf("user did not move to challenge_room for skilled test")
	}

	// Test 5a: Look should now show the hidden gem (user has perception skill)
	// Use waitForLookMatch to handle any stale async notifications in the buffer
	output, ok = tc.waitForLookMatch("hidden gem", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look SHOULD show hidden gem with perception skill: %q", output)
	}

	// Test 5b: Use the locked exit (should succeed now - user has strength skill)
	if err := tc.sendLine("locked"); err != nil {
		return fmt.Errorf("locked exit command with skills: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("locked exit command with skills did not complete")
	}

	// Verify user moved to genesis via the locked exit
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		return fmt.Errorf("user should have moved to genesis via 'locked' exit with strength skill")
	}

	fmt.Println("  Challenge system: OK")

	// === Test 9: emit() inter-object communication ===
	fmt.Println("Testing emit() inter-object communication...")

	// Ensure we're in genesis before creating objects
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis did not complete")
	}

	// Receiver updates its description when it receives a pong
	receiverSource := `setDescriptions([{Short: 'receiver orb (waiting)'}]);
addCallback('pong', ['emit'], (msg) => {
	setDescriptions([{Short: 'receiver orb (got: ' + msg.message + ')'}]);
});
`
	if err := ts.WriteSource("/receiver.js", receiverSource); err != nil {
		return fmt.Errorf("failed to create /receiver.js: %w", err)
	}

	// Sender takes target ID from msg.line and emits to it
	senderSource := `setDescriptions([{Short: 'sender orb'}]);
addCallback('ping', ['action'], (msg) => {
	const targetId = msg.line.replace(/^ping\s+/, '');
	emit(targetId, 'pong', {message: 'hello'});
	setDescriptions([{Short: 'sender orb (sent)'}]);
});
`
	if err := ts.WriteSource("/sender.js", senderSource); err != nil {
		return fmt.Errorf("failed to create /sender.js: %w", err)
	}

	receiverID, err := tc.createObject("/receiver.js")
	if err != nil {
		return fmt.Errorf("create receiver: %w", err)
	}

	if _, err := tc.createObject("/sender.js"); err != nil {
		return fmt.Errorf("create sender: %w", err)
	}

	// Ping the sender with the receiver's ID as target
	if err := tc.sendLine(fmt.Sprintf("ping %s", receiverID)); err != nil {
		return fmt.Errorf("ping command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("ping receiver command did not complete")
	}

	// Poll with look until we see the receiver got the message (emit has ~100ms delay)
	lookOutput, found := tc.waitForLookMatch("receiver orb (got: hello)", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("receiver did not update description after receiving emit: %q", lookOutput)
	}
	if !strings.Contains(lookOutput, "sender orb (sent)") {
		return fmt.Errorf("sender did not update description after emit: %q", lookOutput)
	}

	fmt.Println("  emit() inter-object communication: OK")

	// === Test 10: setTimeout() delayed events ===
	fmt.Println("Testing setTimeout() delayed events...")

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for timer: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for timer did not complete")
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
	if err := ts.WriteSource("/timer.js", timerSource); err != nil {
		return fmt.Errorf("failed to create /timer.js: %w", err)
	}

	if _, err := tc.createObject("/timer.js"); err != nil {
		return fmt.Errorf("create timer: %w", err)
	}

	// Poll until timer is visible in room
	timerOutput, found := tc.waitForLookMatch("timer orb (idle)", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("timer should be idle initially: %q", timerOutput)
	}

	// Start the timer
	if err := tc.sendLine("start timer orb"); err != nil {
		return fmt.Errorf("start timer orb command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("start timer orb command did not complete")
	}

	// Poll with look until we see the timer fired (setTimeout has 200ms delay)
	timerOutput, found = tc.waitForLookMatch("timer orb (fired)", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("timer should show (fired) after timeout: %q", timerOutput)
	}

	fmt.Println("  setTimeout() delayed events: OK")

	// === Test 11: /remove command ===
	fmt.Println("Testing /remove command...")

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for remove: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for remove did not complete")
	}

	removableSource := `setDescriptions([{Short: 'removable widget'}]);
`
	if err := ts.WriteSource("/removable.js", removableSource); err != nil {
		return fmt.Errorf("failed to create /removable.js: %w", err)
	}

	removableID, err := tc.createObject("/removable.js")
	if err != nil {
		return fmt.Errorf("create removable: %w", err)
	}

	// Verify object exists via /inspect
	output, ok = tc.sendCommand(fmt.Sprintf("/inspect #%s", removableID), defaultWaitTimeout)
	if !ok || !strings.Contains(output, "removable widget") {
		return fmt.Errorf("removable object should exist before removal: %q", output)
	}

	// Remove the object
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", removableID)); err != nil {
		return fmt.Errorf("/remove command: %w", err)
	}
	if _, ok = tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/remove command did not complete")
	}

	// Verify object no longer exists via /inspect (should show error or empty)
	output, ok = tc.sendCommand(fmt.Sprintf("/inspect #%s", removableID), defaultWaitTimeout)
	if strings.Contains(output, "removable widget") {
		return fmt.Errorf("removable object should not exist after removal: %q", output)
	}

	// Test edge case: can't remove self - verify error message and we stay logged in
	// Use #<user-object-id> since "self" isn't a special keyword
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", user.Object)); err != nil {
		return fmt.Errorf("/remove self command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/remove self command did not complete: %q", output)
	}
	if !strings.Contains(output, "Can't remove yourself") {
		return fmt.Errorf("/remove self should show error 'Can't remove yourself': %q", output)
	}
	// Verify we're still logged in by checking we can look around
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look after remove self: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("should still be logged in after failed self-removal: %q", output)
	}

	fmt.Println("  /remove command: OK")

	// === Test 12: Movement events ===
	fmt.Println("Testing movement event notifications...")

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for movement: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for movement did not complete")
	}

	// Observer updates its description when it sees movement
	observerSource := `setDescriptions([{Short: 'watcher orb (watching)'}]);
addCallback('movement', ['emit'], (msg) => {
	const id = msg.Object ? msg.Object.Id : 'unknown';
	setDescriptions([{Short: 'watcher orb (saw: ' + id + ')'}]);
});
`
	if err := ts.WriteSource("/observer.js", observerSource); err != nil {
		return fmt.Errorf("failed to create /observer.js: %w", err)
	}

	moveableSource := `setDescriptions([{Short: 'moveable cube'}]);
`
	if err := ts.WriteSource("/moveable.js", moveableSource); err != nil {
		return fmt.Errorf("failed to create /moveable.js: %w", err)
	}

	if _, err := tc.createObject("/observer.js"); err != nil {
		return fmt.Errorf("create observer: %w", err)
	}

	moveableID, err := tc.createObject("/moveable.js")
	if err != nil {
		return fmt.Errorf("create moveable: %w", err)
	}

	// Move the moveable to lookroom
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", moveableID, lookRoomID)); err != nil {
		return fmt.Errorf("/move moveable to lookroom: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/move moveable to lookroom did not complete")
	}

	// Poll with look until observer shows it saw the movement
	output, found = tc.waitForLookMatch("watcher orb (saw: "+moveableID+")", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("observer should have seen moveable in movement event: %q", output)
	}

	fmt.Println("  Movement event notifications: OK")

	// === Test 12b: Custom movement verb ===
	fmt.Println("Testing custom movement verb...")

	// Create an object with a custom movement verb "scurries"
	scurrySource := `setDescriptions([{Short: 'tiny mouse'}]);
setMovement({Active: true, Verb: 'scurries'});
`
	if err := ts.WriteSource("/scurry.js", scurrySource); err != nil {
		return fmt.Errorf("failed to create /scurry.js: %w", err)
	}

	scurryID, err := tc.createObject("/scurry.js")
	if err != nil {
		return fmt.Errorf("create scurry object: %w", err)
	}

	// Move the scurry object to lookroom - we should see "A tiny mouse scurries south."
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", scurryID, lookRoomID)); err != nil {
		return fmt.Errorf("/move scurry to lookroom: %w", err)
	}
	// Wait for prompt, then wait for the async movement message
	tc.waitForPrompt(defaultWaitTimeout)
	output = tc.readUntil(500*time.Millisecond, func(s string) bool {
		return strings.Contains(s, "scurries")
	})
	if !strings.Contains(output, "scurries") {
		return fmt.Errorf("movement message should contain custom verb 'scurries': %q", output)
	}

	fmt.Println("  Custom movement verb: OK")

	// === Test 12c: JS-based movement rendering ===
	fmt.Println("Testing JS-based movement rendering...")

	// Create an object that handles its own movement rendering via JS
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
	if err := ts.WriteSource("/jsorb.js", jsRenderSource); err != nil {
		return fmt.Errorf("failed to create /jsorb.js: %w", err)
	}

	jsOrbID, err := tc.createObject("/jsorb.js")
	if err != nil {
		return fmt.Errorf("create jsorb object: %w", err)
	}

	// Move the orb to lookroom - we should see the custom JS-rendered message
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", jsOrbID, lookRoomID)); err != nil {
		return fmt.Errorf("/move jsorb to lookroom: %w", err)
	}
	// Wait for prompt, then wait for the async movement message
	tc.waitForPrompt(defaultWaitTimeout)
	output = tc.readUntil(500*time.Millisecond, func(s string) bool {
		return strings.Contains(s, "floats away") || strings.Contains(s, "eerie hum")
	})
	if !strings.Contains(output, "floats away") && !strings.Contains(output, "eerie hum") {
		return fmt.Errorf("movement message should contain JS-rendered text: %q", output)
	}

	fmt.Println("  JS-based movement rendering: OK")

	// === Test 13: /debug and log() ===
	fmt.Println("Testing /debug and log()...")

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for debug: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for debug did not complete")
	}

	// Create an object that logs when triggered via an action
	loggerSource := `setDescriptions([{Short: 'logger stone'}]);
addCallback('trigger', ['action'], (msg) => {
	log('DEBUG: trigger received with line:', msg.line);
	setDescriptions([{Short: 'logger stone (triggered)'}]);
});
`
	if err := ts.WriteSource("/logger.js", loggerSource); err != nil {
		return fmt.Errorf("failed to create /logger.js: %w", err)
	}

	loggerID, err := tc.createObject("/logger.js")
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}

	// Test 1: Without /debug, log output should NOT appear
	if err := tc.sendLine("trigger logger"); err != nil {
		return fmt.Errorf("trigger without debug: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("trigger without debug did not complete: %q", output)
	}
	// Verify no DEBUG message appears (we're not attached to console)
	if strings.Contains(output, "DEBUG:") {
		return fmt.Errorf("log output should NOT appear without /debug: %q", output)
	}

	// Wait for object to update its description
	output, found = tc.waitForLookMatch("logger stone (triggered)", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("logger should have updated description after trigger: %q", output)
	}

	// Reset the logger for the next test (re-upload resets description to untriggered state)
	if err := ts.WriteSource("/logger.js", loggerSource); err != nil {
		return fmt.Errorf("failed to reset /logger.js: %w", err)
	}

	// Wait for description to reset
	output, found = tc.waitForLookMatchFunc(func(s string) bool {
		return strings.Contains(s, "logger stone") && !strings.Contains(s, "(triggered)")
	}, defaultWaitTimeout)
	if !found {
		return fmt.Errorf("logger should have reset description: %q", output)
	}

	// Test 2: Attach console with /debug
	output, ok = tc.sendCommand(fmt.Sprintf("/debug #%s", loggerID), defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/debug command did not complete: %q", output)
	}
	if !strings.Contains(output, "connected to console") {
		return fmt.Errorf("/debug should show 'connected to console': %q", output)
	}

	// Trigger the logger - now log output should appear
	if err := tc.sendLine("trigger logger"); err != nil {
		return fmt.Errorf("trigger with debug: %w", err)
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
		return fmt.Errorf("log output should appear with /debug attached: %q", output)
	}

	// Wait for prompt after the action completes
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		// Prompt might already have been received in the readUntil above
	}

	// Test 3: Detach with /undebug
	output, ok = tc.sendCommand(fmt.Sprintf("/undebug #%s", loggerID), defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/undebug command did not complete: %q", output)
	}
	if !strings.Contains(output, "disconnected from console") {
		return fmt.Errorf("/undebug should show 'disconnected from console': %q", output)
	}

	// Reset the logger again
	if err := ts.WriteSource("/logger.js", loggerSource); err != nil {
		return fmt.Errorf("failed to reset /logger.js again: %w", err)
	}

	// Wait for description to reset
	output, found = tc.waitForLookMatchFunc(func(s string) bool {
		return strings.Contains(s, "logger stone") && !strings.Contains(s, "(triggered)")
	}, defaultWaitTimeout)
	if !found {
		return fmt.Errorf("logger should have reset description again: %q", output)
	}

	// Trigger again - log output should NOT appear anymore
	output, ok = tc.sendCommand("trigger logger", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("trigger after undebug did not complete: %q", output)
	}
	if strings.Contains(output, "DEBUG:") {
		return fmt.Errorf("log output should NOT appear after /undebug: %q", output)
	}

	fmt.Println("  /debug and log(): OK")

	// === Test 15: created event ===
	fmt.Println("Testing created event...")

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for created: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for created did not complete")
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
	if err := ts.WriteSource("/witness.js", createdSource); err != nil {
		return fmt.Errorf("failed to create /witness.js: %w", err)
	}

	// The user's object ID (from Test 1) is what we expect in the creator info
	userID := user.Object

	// Create the witness object
	if _, err := tc.createObject("/witness.js"); err != nil {
		return fmt.Errorf("create witness: %w", err)
	}

	// Wait for the witness to appear with the creator's ID in its description
	output, found = tc.waitForLookMatch("witness stone (created by "+userID+")", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("witness should show creator ID in description: %q", output)
	}

	fmt.Println("  created event: OK")

	// === Test 16: look [target] ===
	fmt.Println("Testing look [target]...")

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for look target: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for look target did not complete")
	}

	// Create an object with both Short and Long descriptions
	tomeSource := `setDescriptions([{
	Short: 'dusty tome',
	Long: 'An ancient book bound in cracked leather. Strange symbols cover the spine, and the pages smell of forgotten ages.',
}]);
`
	if err := ts.WriteSource("/tome.js", tomeSource); err != nil {
		return fmt.Errorf("failed to create /tome.js: %w", err)
	}

	// Create the tome object
	if _, err := tc.createObject("/tome.js"); err != nil {
		return fmt.Errorf("create tome: %w", err)
	}

	// Look at the tome using a single word from its Short description
	if err := tc.sendLine("look tome"); err != nil {
		return fmt.Errorf("look tome: %w", err)
	}
	output, ok = tc.waitForPrompt(2 * time.Second)
	if !ok {
		return fmt.Errorf("look dusty tome did not complete: %q", output)
	}

	// Verify the output shows the object name
	if !strings.Contains(output, "dusty tome") {
		return fmt.Errorf("look should show object name 'dusty tome': %q", output)
	}

	// Verify the output shows the Long description
	if !strings.Contains(output, "ancient book bound in cracked leather") {
		return fmt.Errorf("look should show Long description: %q", output)
	}
	if !strings.Contains(output, "forgotten ages") {
		return fmt.Errorf("look should show full Long description: %q", output)
	}

	fmt.Println("  look [target]: OK")

	// === Test 17: /queuestats wizard command ===
	fmt.Println("Testing /queuestats wizard command...")

	// Test /queuestats summary (default)
	if err := tc.sendLine("/queuestats"); err != nil {
		return fmt.Errorf("/queuestats command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/queuestats command did not complete: %q", output)
	}
	// Verify output shows expected fields
	if !strings.Contains(output, "Queue Statistics") {
		return fmt.Errorf("/queuestats should show 'Queue Statistics': %q", output)
	}
	if !strings.Contains(output, "Total events:") {
		return fmt.Errorf("/queuestats should show 'Total events:': %q", output)
	}
	if !strings.Contains(output, "Event rates:") {
		return fmt.Errorf("/queuestats should show 'Event rates:': %q", output)
	}

	// Test /queuestats summary explicitly
	if err := tc.sendLine("/queuestats summary"); err != nil {
		return fmt.Errorf("/queuestats summary command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/queuestats summary did not complete: %q", output)
	}
	if !strings.Contains(output, "Queue Statistics") {
		return fmt.Errorf("/queuestats summary should show 'Queue Statistics': %q", output)
	}

	// Test /queuestats categories
	if err := tc.sendLine("/queuestats categories"); err != nil {
		return fmt.Errorf("/queuestats categories command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/queuestats categories did not complete: %q", output)
	}
	// Either shows "No errors recorded." or a table with Category header
	if !strings.Contains(output, "No errors") && !strings.Contains(output, "Category") {
		return fmt.Errorf("/queuestats categories should show errors or 'No errors': %q", output)
	}

	// Test /queuestats locations
	if err := tc.sendLine("/queuestats locations"); err != nil {
		return fmt.Errorf("/queuestats locations command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/queuestats locations did not complete: %q", output)
	}
	// Either shows "No errors recorded." or a table with Location header
	if !strings.Contains(output, "No errors") && !strings.Contains(output, "Location") {
		return fmt.Errorf("/queuestats locations should show errors or 'No errors': %q", output)
	}

	// Test /queuestats objects
	if err := tc.sendLine("/queuestats objects"); err != nil {
		return fmt.Errorf("/queuestats objects command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/queuestats objects did not complete: %q", output)
	}
	// Either shows "No objects recorded." or a table with Object header
	if !strings.Contains(output, "No objects") && !strings.Contains(output, "Object") {
		return fmt.Errorf("/queuestats objects should show objects or 'No objects': %q", output)
	}

	// Test /queuestats recent
	if err := tc.sendLine("/queuestats recent"); err != nil {
		return fmt.Errorf("/queuestats recent command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/queuestats recent did not complete: %q", output)
	}
	// Either shows "No recent errors." or error records
	if !strings.Contains(output, "No recent errors") && !strings.Contains(output, "]") {
		return fmt.Errorf("/queuestats recent should show errors or 'No recent errors': %q", output)
	}

	// Test /queuestats reset
	if err := tc.sendLine("/queuestats reset"); err != nil {
		return fmt.Errorf("/queuestats reset command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/queuestats reset did not complete: %q", output)
	}
	if !strings.Contains(output, "Queue statistics reset") {
		return fmt.Errorf("/queuestats reset should confirm reset: %q", output)
	}

	// Verify reset worked by checking summary shows zero errors
	if err := tc.sendLine("/queuestats summary"); err != nil {
		return fmt.Errorf("/queuestats summary after reset: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/queuestats summary after reset did not complete: %q", output)
	}
	if !strings.Contains(output, "Total errors: 0") {
		return fmt.Errorf("/queuestats summary after reset should show 'Total errors: 0': %q", output)
	}

	// Test /queuestats help (unknown subcommand)
	if err := tc.sendLine("/queuestats help"); err != nil {
		return fmt.Errorf("/queuestats help command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/queuestats help did not complete: %q", output)
	}
	if !strings.Contains(output, "usage:") {
		return fmt.Errorf("/queuestats help should show usage: %q", output)
	}

	fmt.Println("  /queuestats wizard command: OK")

	// === Test 17b: /jsstats wizard command ===
	fmt.Println("Testing /jsstats wizard command...")

	// Test /jsstats summary (default)
	if err := tc.sendLine("/jsstats"); err != nil {
		return fmt.Errorf("/jsstats command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/jsstats command did not complete: %q", output)
	}
	// Verify output shows expected fields
	if !strings.Contains(output, "JS Execution Statistics") {
		return fmt.Errorf("/jsstats should show 'JS Execution Statistics': %q", output)
	}
	if !strings.Contains(output, "Total executions:") {
		return fmt.Errorf("/jsstats should show 'Total executions:': %q", output)
	}
	if !strings.Contains(output, "Execution rates:") {
		return fmt.Errorf("/jsstats should show 'Execution rates:': %q", output)
	}
	if !strings.Contains(output, "Time rates") {
		return fmt.Errorf("/jsstats should show 'Time rates': %q", output)
	}

	// Test /jsstats summary explicitly
	if err := tc.sendLine("/jsstats summary"); err != nil {
		return fmt.Errorf("/jsstats summary command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/jsstats summary did not complete: %q", output)
	}
	if !strings.Contains(output, "JS Execution Statistics") {
		return fmt.Errorf("/jsstats summary should show 'JS Execution Statistics': %q", output)
	}

	// Test /jsstats scripts
	if err := tc.sendLine("/jsstats scripts"); err != nil {
		return fmt.Errorf("/jsstats scripts command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/jsstats scripts did not complete: %q", output)
	}
	// Either shows "No scripts recorded." or a table with Source Path header
	if !strings.Contains(output, "No scripts") && !strings.Contains(output, "Source Path") {
		return fmt.Errorf("/jsstats scripts should show scripts or 'No scripts': %q", output)
	}

	// Test /jsstats objects
	if err := tc.sendLine("/jsstats objects"); err != nil {
		return fmt.Errorf("/jsstats objects command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/jsstats objects did not complete: %q", output)
	}
	// Either shows "No objects recorded." or a table with Object ID header
	if !strings.Contains(output, "No objects") && !strings.Contains(output, "Object ID") {
		return fmt.Errorf("/jsstats objects should show objects or 'No objects': %q", output)
	}

	// Test /jsstats slow
	if err := tc.sendLine("/jsstats slow"); err != nil {
		return fmt.Errorf("/jsstats slow command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/jsstats slow did not complete: %q", output)
	}
	// Either shows "No slow executions recorded." or slow execution records
	if !strings.Contains(output, "No slow executions") && !strings.Contains(output, "]") {
		return fmt.Errorf("/jsstats slow should show slow execs or 'No slow executions': %q", output)
	}

	// Test /jsstats reset
	if err := tc.sendLine("/jsstats reset"); err != nil {
		return fmt.Errorf("/jsstats reset command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/jsstats reset did not complete: %q", output)
	}
	if !strings.Contains(output, "JS statistics reset") {
		return fmt.Errorf("/jsstats reset should confirm reset: %q", output)
	}

	// Verify reset worked by checking summary shows zero executions
	if err := tc.sendLine("/jsstats summary"); err != nil {
		return fmt.Errorf("/jsstats summary after reset: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/jsstats summary after reset did not complete: %q", output)
	}
	if !strings.Contains(output, "Total executions: 0") {
		return fmt.Errorf("/jsstats summary after reset should show 'Total executions: 0': %q", output)
	}

	// Test /jsstats help (unknown subcommand)
	if err := tc.sendLine("/jsstats help"); err != nil {
		return fmt.Errorf("/jsstats help command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/jsstats help did not complete: %q", output)
	}
	if !strings.Contains(output, "usage:") {
		return fmt.Errorf("/jsstats help should show usage: %q", output)
	}

	fmt.Println("  /jsstats wizard command: OK")

	// === Test 18: Room and sibling action handlers ===
	fmt.Println("Testing room and sibling action handlers...")

	// Ensure we're in genesis as a wizard
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for action handlers: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for action handlers did not complete")
	}

	// Create a room that handles the "shake" action
	actionRoomSource := `setDescriptions([{Short: 'shaky chamber', Long: 'A small room with unstable walls.'}]);
setExits([{Name: 'out', Destination: 'genesis'}]);
addCallback('shake', ['action'], (msg) => {
	setDescriptions([{Short: 'shaky chamber (shaken!)', Long: 'The walls have just been shaken!'}]);
});
`
	if err := ts.WriteSource("/actionroom.js", actionRoomSource); err != nil {
		return fmt.Errorf("failed to create /actionroom.js: %w", err)
	}

	// Create the room
	actionRoomID, err := tc.createObject("/actionroom.js")
	if err != nil {
		return fmt.Errorf("create actionroom: %w", err)
	}

	// Enter the action room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", actionRoomID)); err != nil {
		return fmt.Errorf("/enter actionroom: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter actionroom did not complete")
	}

	// Issue "shake" command - the room should handle this action
	if err := tc.sendLine("shake"); err != nil {
		return fmt.Errorf("shake command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("shake command did not complete")
	}

	// Wait for the room's description to update (action handlers have a small delay)
	output, found = tc.waitForLookMatch("shaky chamber (shaken!)", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("room action handler should have updated description: %q", output)
	}

	fmt.Println("  room action handler: OK")

	// Now test sibling action handler
	// Create an object that handles the "poke" action
	pokeableSource := `setDescriptions([{Short: 'pokeable orb'}]);
addCallback('poke', ['action'], (msg) => {
	setDescriptions([{Short: 'pokeable orb (poked!)'}]);
});
`
	if err := ts.WriteSource("/pokeable.js", pokeableSource); err != nil {
		return fmt.Errorf("failed to create /pokeable.js: %w", err)
	}

	// Create the pokeable object (it will be created in our current room - the actionroom)
	if _, err := tc.createObject("/pokeable.js"); err != nil {
		return fmt.Errorf("create pokeable: %w", err)
	}

	// Issue "poke" command - the sibling object should handle this action
	if err := tc.sendLine("poke"); err != nil {
		return fmt.Errorf("poke command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("poke command did not complete")
	}

	// Wait for the sibling's description to update
	output, found = tc.waitForLookMatch("pokeable orb (poked!)", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("sibling action handler should have updated description: %q", output)
	}

	fmt.Println("  sibling action handler: OK")

	// Return to genesis for any subsequent tests
	if err := tc.sendLine("out"); err != nil {
		return fmt.Errorf("out command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("out command did not complete")
	}

	// === Test 19: State persistence ===
	fmt.Println("Testing state persistence...")

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for state test: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for state test did not complete")
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
	if err := ts.WriteSource("/counter.js", counterSource); err != nil {
		return fmt.Errorf("failed to create /counter.js: %w", err)
	}

	// Create the counter object
	if _, err := tc.createObject("/counter.js"); err != nil {
		return fmt.Errorf("create counter: %w", err)
	}

	// Tap the counter three times
	for i := 1; i <= 3; i++ {
		if err := tc.sendLine("tap"); err != nil {
			return fmt.Errorf("tap command %d: %w", i, err)
		}
		if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
			return fmt.Errorf("tap command %d did not complete", i)
		}

		// Wait for the description to update with the correct count
		expectedDesc := fmt.Sprintf("tap counter (%d taps)", i)
		output, found := tc.waitForLookMatch(expectedDesc, defaultWaitTimeout)
		if !found {
			return fmt.Errorf("state should persist count after tap %d, expected %q in output: %q", i, expectedDesc, output)
		}
	}

	fmt.Println("  state persistence: OK")

	// === Test 20: emit() with challenges ===
	fmt.Println("Testing emit() with challenges...")

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for emit challenges: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for emit challenges did not complete")
	}

	// High-perception receiver - has 200 perception, should receive challenged emit
	highPercReceiverSource := `setDescriptions([{Short: 'eagle orb (waiting)'}]);
setSkills({perception: {Practical: 200, Theoretical: 200}});
addCallback('secret', ['emit'], (msg) => {
	setDescriptions([{Short: 'eagle orb (got: ' + msg.secret + ')'}]);
});
`
	if err := ts.WriteSource("/eaglereceiver.js", highPercReceiverSource); err != nil {
		return fmt.Errorf("failed to create /eaglereceiver.js: %w", err)
	}

	// Low-perception receiver - has 1 perception, should NOT receive challenged emit with level 50
	lowPercReceiverSource := `setDescriptions([{Short: 'dim orb (waiting)'}]);
setSkills({perception: {Practical: 1, Theoretical: 1}});
addCallback('secret', ['emit'], (msg) => {
	setDescriptions([{Short: 'dim orb (got: ' + msg.secret + ')'}]);
});
`
	if err := ts.WriteSource("/dimreceiver.js", lowPercReceiverSource); err != nil {
		return fmt.Errorf("failed to create /dimreceiver.js: %w", err)
	}

	// Sender emits with perception challenge at level 50
	challengeSenderSource := `setDescriptions([{Short: 'whisperer orb'}]);
addCallback('whisper', ['action'], (msg) => {
	const parts = msg.line.split(' ');
	const targetId = parts[1];
	emit(targetId, 'secret', {secret: 'hidden'}, [{Skill: 'perception', Level: 50}]);
	setDescriptions([{Short: 'whisperer orb (sent)'}]);
});
`
	if err := ts.WriteSource("/challengesender.js", challengeSenderSource); err != nil {
		return fmt.Errorf("failed to create /challengesender.js: %w", err)
	}

	// Create receivers
	eagleID, err := tc.createObject("/eaglereceiver.js")
	if err != nil {
		return fmt.Errorf("create eaglereceiver: %w", err)
	}

	dimID, err := tc.createObject("/dimreceiver.js")
	if err != nil {
		return fmt.Errorf("create dimreceiver: %w", err)
	}

	// Create sender
	if _, err := tc.createObject("/challengesender.js"); err != nil {
		return fmt.Errorf("create challengesender: %w", err)
	}

	// Whisper to the high-perception receiver - should succeed
	if err := tc.sendLine(fmt.Sprintf("whisper %s", eagleID)); err != nil {
		return fmt.Errorf("whisper to eagle: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("whisper to eagle did not complete")
	}

	// Eagle should receive the secret
	output, found = tc.waitForLookMatch("eagle orb (got: hidden)", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("high-perception receiver should have received challenged emit: %q", output)
	}

	// Whisper to the low-perception receiver - should fail (no event received)
	// Emit processing is synchronous, so by the time the command completes,
	// the emit has either been delivered or filtered by the challenge system.
	if err := tc.sendLine(fmt.Sprintf("whisper %s", dimID)); err != nil {
		return fmt.Errorf("whisper to dim: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("whisper to dim did not complete")
	}

	// Dim should NOT have received the secret (should still be waiting)
	tc.sendLine("look")
	output, _ = tc.waitForPrompt(defaultWaitTimeout)
	if strings.Contains(output, "dim orb (got:") {
		return fmt.Errorf("low-perception receiver should NOT have received challenged emit: %q", output)
	}
	if !strings.Contains(output, "dim orb (waiting)") {
		return fmt.Errorf("dim orb should still be waiting: %q", output)
	}

	fmt.Println("  emit() with challenges: OK")

	// === Test 21: emitToLocation() ===
	fmt.Println("Testing emitToLocation()...")

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for emitToLocation: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for emitToLocation did not complete")
	}

	// Create a broadcast room
	broadcastRoomSource := `setDescriptions([{Short: 'broadcast chamber', Long: 'A room for broadcasting.'}]);
setExits([{Name: 'out', Destination: 'genesis'}]);
`
	if err := ts.WriteSource("/broadcastroom.js", broadcastRoomSource); err != nil {
		return fmt.Errorf("failed to create /broadcastroom.js: %w", err)
	}
	broadcastRoomID, err := tc.createObject("/broadcastroom.js")
	if err != nil {
		return fmt.Errorf("create broadcastroom: %w", err)
	}

	// Enter the broadcast room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", broadcastRoomID)); err != nil {
		return fmt.Errorf("/enter broadcast room: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter broadcast room did not complete")
	}

	// Listener 1 - receives broadcasts
	listener1Source := `setDescriptions([{Short: 'listener alpha (idle)'}]);
addCallback('announce', ['emit'], (msg) => {
	setDescriptions([{Short: 'listener alpha (heard: ' + msg.message + ')'}]);
});
`
	if err := ts.WriteSource("/listener1.js", listener1Source); err != nil {
		return fmt.Errorf("failed to create /listener1.js: %w", err)
	}
	if _, err := tc.createObject("/listener1.js"); err != nil {
		return fmt.Errorf("create listener1: %w", err)
	}

	// Listener 2 - also receives broadcasts
	listener2Source := `setDescriptions([{Short: 'listener beta (idle)'}]);
addCallback('announce', ['emit'], (msg) => {
	setDescriptions([{Short: 'listener beta (heard: ' + msg.message + ')'}]);
});
`
	if err := ts.WriteSource("/listener2.js", listener2Source); err != nil {
		return fmt.Errorf("failed to create /listener2.js: %w", err)
	}
	if _, err := tc.createObject("/listener2.js"); err != nil {
		return fmt.Errorf("create listener2: %w", err)
	}

	// Broadcaster - uses emitToLocation to broadcast to all in the room
	broadcasterSource := `setDescriptions([{Short: 'broadcaster orb'}]);
addCallback('broadcast', ['action'], (msg) => {
	emitToLocation(getLocation(), 'announce', {message: 'hello all'});
	setDescriptions([{Short: 'broadcaster orb (sent)'}]);
});
addCallback('announce', ['emit'], (msg) => {
	// Broadcaster should also receive its own broadcast
	log('Broadcaster received own announcement');
});
`
	if err := ts.WriteSource("/broadcaster.js", broadcasterSource); err != nil {
		return fmt.Errorf("failed to create /broadcaster.js: %w", err)
	}
	if _, err := tc.createObject("/broadcaster.js"); err != nil {
		return fmt.Errorf("create broadcaster: %w", err)
	}

	// Issue the broadcast command
	if err := tc.sendLine("broadcast"); err != nil {
		return fmt.Errorf("broadcast command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("broadcast command did not complete")
	}

	// Both listeners should receive the broadcast
	output, found = tc.waitForLookMatchFunc(func(s string) bool {
		return strings.Contains(s, "listener alpha (heard: hello all)") &&
			strings.Contains(s, "listener beta (heard: hello all)")
	}, defaultWaitTimeout)
	if !found {
		return fmt.Errorf("both listeners should have received broadcast: %q", output)
	}

	fmt.Println("  emitToLocation(): OK")

	// === Test 22: emitToLocation() with challenges ===
	fmt.Println("Testing emitToLocation() with challenges...")

	// Stay in the broadcast room for this test

	// Reset listeners for next test
	listener1ChallengeSource := `setDescriptions([{Short: 'sensitive ear (idle)'}]);
setSkills({telepathy: {Practical: 150, Theoretical: 150}});
addCallback('mindcast', ['emit'], (msg) => {
	setDescriptions([{Short: 'sensitive ear (heard: ' + msg.thought + ')'}]);
});
`
	if err := ts.WriteSource("/listener1.js", listener1ChallengeSource); err != nil {
		return fmt.Errorf("failed to update /listener1.js: %w", err)
	}

	listener2ChallengeSource := `setDescriptions([{Short: 'deaf ear (idle)'}]);
setSkills({telepathy: {Practical: 5, Theoretical: 5}});
addCallback('mindcast', ['emit'], (msg) => {
	setDescriptions([{Short: 'deaf ear (heard: ' + msg.thought + ')'}]);
});
`
	if err := ts.WriteSource("/listener2.js", listener2ChallengeSource); err != nil {
		return fmt.Errorf("failed to update /listener2.js: %w", err)
	}

	// Wait for descriptions to update
	output, found = tc.waitForLookMatchFunc(func(s string) bool {
		return strings.Contains(s, "sensitive ear (idle)") &&
			strings.Contains(s, "deaf ear (idle)")
	}, defaultWaitTimeout)
	if !found {
		return fmt.Errorf("listeners should have reset for challenge test: %q", output)
	}

	// Telepathic broadcaster - uses emitToLocation with telepathy challenge
	telepathSource := `setDescriptions([{Short: 'telepath orb'}]);
addCallback('mindspeak', ['action'], (msg) => {
	emitToLocation(getLocation(), 'mindcast', {thought: 'secret thought'}, [{Skill: 'telepathy', Level: 50}]);
	setDescriptions([{Short: 'telepath orb (sent)'}]);
});
`
	if err := ts.WriteSource("/broadcaster.js", telepathSource); err != nil {
		return fmt.Errorf("failed to update /broadcaster.js: %w", err)
	}

	output, found = tc.waitForLookMatch("telepath orb", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("broadcaster should have updated to telepath: %q", output)
	}

	// Issue the telepathic broadcast
	if err := tc.sendLine("mindspeak"); err != nil {
		return fmt.Errorf("mindspeak command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("mindspeak command did not complete")
	}

	// Only the sensitive ear should receive the telepathic broadcast
	output, found = tc.waitForLookMatch("sensitive ear (heard: secret thought)", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("high-telepathy listener should have received challenged broadcast: %q", output)
	}

	// The deaf ear should NOT have received the broadcast (still idle)
	if strings.Contains(output, "deaf ear (heard:") {
		return fmt.Errorf("low-telepathy listener should NOT have received challenged broadcast: %q", output)
	}
	if !strings.Contains(output, "deaf ear (idle)") {
		return fmt.Errorf("deaf ear should still be idle: %q", output)
	}

	fmt.Println("  emitToLocation() with challenges: OK")

	// === Test 23: /addwiz and /delwiz commands ===
	fmt.Println("Testing /addwiz and /delwiz commands...")

	// Create a second user to test granting/revoking wizard status
	tc2, err := createUser(ts.SSHAddr(), "wizardtest", "wizpass123")
	if err != nil {
		return fmt.Errorf("createUser wizardtest: %w", err)
	}
	defer tc2.Close()

	// Verify new user is not a wizard (can't use /inspect)
	if err := tc2.sendLine("/inspect"); err != nil {
		return fmt.Errorf("/inspect as non-wizard: %w", err)
	}
	output, ok = tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/inspect did not complete: %q", output)
	}
	// Non-wizards should NOT see JSON output from /inspect (check for Id field in JSON)
	if strings.Contains(output, "\"Id\":") {
		return fmt.Errorf("non-wizard should not see /inspect output: %q", output)
	}

	// Grant wizard status using /addwiz (tc is the owner)
	if err := tc.sendLine("/addwiz wizardtest"); err != nil {
		return fmt.Errorf("/addwiz command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/addwiz command did not complete: %q", output)
	}
	if !strings.Contains(output, "Granted wizard privileges") {
		return fmt.Errorf("/addwiz should confirm grant: %q", output)
	}

	// Reconnect as wizardtest to pick up wizard status
	tc2.Close()
	tc2, err = loginUser(ts.SSHAddr(), "wizardtest", "wizpass123")
	if err != nil {
		return fmt.Errorf("loginUser wizardtest after /addwiz: %w", err)
	}
	defer tc2.Close()

	// Verify wizardtest can now use /inspect (wizard command)
	if err := tc2.sendLine("/inspect"); err != nil {
		return fmt.Errorf("/inspect as wizard: %w", err)
	}
	output, ok = tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/inspect as wizard did not complete: %q", output)
	}
	// Wizards should see JSON output from /inspect (check for Id field in JSON)
	if !strings.Contains(output, "\"Id\":") {
		return fmt.Errorf("wizard should see /inspect output: %q", output)
	}

	// Revoke wizard status using /delwiz
	if err := tc.sendLine("/delwiz wizardtest"); err != nil {
		return fmt.Errorf("/delwiz command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/delwiz command did not complete: %q", output)
	}
	if !strings.Contains(output, "Revoked wizard privileges") {
		return fmt.Errorf("/delwiz should confirm revoke: %q", output)
	}

	// Reconnect to pick up revoked status
	tc2.Close()
	tc2, err = loginUser(ts.SSHAddr(), "wizardtest", "wizpass123")
	if err != nil {
		return fmt.Errorf("loginUser wizardtest after /delwiz: %w", err)
	}
	defer tc2.Close()

	// Verify wizardtest can no longer use /inspect
	if err := tc2.sendLine("/inspect"); err != nil {
		return fmt.Errorf("/inspect after revoke: %w", err)
	}
	output, ok = tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/inspect after revoke did not complete: %q", output)
	}
	// Revoked user should NOT see JSON output from /inspect (check for Id field in JSON)
	if strings.Contains(output, "\"Id\":") {
		return fmt.Errorf("revoked user should not see /inspect output: %q", output)
	}

	// Test that non-owner cannot use /addwiz
	// Grant wizard back to wizardtest (but not owner)
	if err := tc.sendLine("/addwiz wizardtest"); err != nil {
		return fmt.Errorf("/addwiz to restore: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/addwiz to restore did not complete")
	}

	// Reconnect wizardtest as wizard
	tc2.Close()
	tc2, err = loginUser(ts.SSHAddr(), "wizardtest", "wizpass123")
	if err != nil {
		return fmt.Errorf("loginUser wizardtest as wizard: %w", err)
	}
	defer tc2.Close()

	// Try to use /addwiz as non-owner wizard
	if err := tc2.sendLine("/addwiz testuser"); err != nil {
		return fmt.Errorf("/addwiz as non-owner: %w", err)
	}
	output, ok = tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/addwiz as non-owner did not complete: %q", output)
	}
	if !strings.Contains(output, "Only owners") {
		return fmt.Errorf("non-owner should be denied /addwiz: %q", output)
	}

	// Try to use /delwiz as non-owner wizard
	if err := tc2.sendLine("/delwiz testuser"); err != nil {
		return fmt.Errorf("/delwiz as non-owner: %w", err)
	}
	output, ok = tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/delwiz as non-owner did not complete: %q", output)
	}
	if !strings.Contains(output, "Only owners") {
		return fmt.Errorf("non-owner should be denied /delwiz: %q", output)
	}

	fmt.Println("  /addwiz and /delwiz commands: OK")

	// === Test 24: Circular container prevention ===
	fmt.Println("Testing circular container prevention...")

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
	if err := ts.WriteSource("/containerA.js", containerASource); err != nil {
		return fmt.Errorf("failed to create /containerA.js: %w", err)
	}
	if err := ts.WriteSource("/containerB.js", containerBSource); err != nil {
		return fmt.Errorf("failed to create /containerB.js: %w", err)
	}

	// Create container A (outer box)
	containerAID, err := tc.createObject("/containerA.js")
	if err != nil {
		return fmt.Errorf("create container A: %w", err)
	}

	// Create container B (inner box)
	containerBID, err := tc.createObject("/containerB.js")
	if err != nil {
		return fmt.Errorf("create container B: %w", err)
	}

	// Get container A's original location before moving B into it
	containerAOriginalLoc := tc.getLocation(fmt.Sprintf("#%s", containerAID))
	if containerAOriginalLoc == "" {
		return fmt.Errorf("could not determine container A's location")
	}

	// Move B into A (should succeed)
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", containerBID, containerAID)); err != nil {
		return fmt.Errorf("/move B into A: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/move B into A did not complete: %q", output)
	}
	// Verify B is now inside A
	if !tc.waitForLocation(fmt.Sprintf("#%s", containerBID), containerAID, defaultWaitTimeout) {
		return fmt.Errorf("container B did not move into A")
	}

	// Try to move A into B (should fail - circular)
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", containerAID, containerBID)); err != nil {
		return fmt.Errorf("/move A into B: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/move A into B did not complete: %q", output)
	}
	// Should contain error about circular containment
	if !strings.Contains(output, "cannot move object into itself") {
		return fmt.Errorf("circular move should fail with error, got: %q", output)
	}

	// Verify A is still in its original location
	if !tc.waitForLocation(fmt.Sprintf("#%s", containerAID), containerAOriginalLoc, defaultWaitTimeout) {
		return fmt.Errorf("container A should still be in %s after failed circular move", containerAOriginalLoc)
	}

	// Test self-move: try to move A into A (should fail)
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", containerAID, containerAID)); err != nil {
		return fmt.Errorf("/move A into A: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/move A into A did not complete: %q", output)
	}
	if !strings.Contains(output, "cannot move object into itself") {
		return fmt.Errorf("self-move should fail with error, got: %q", output)
	}

	// Test deeper nesting: A contains B, B contains C, try to move A into C
	containerCSource := `// Container C
setDescriptions([{
	Short: 'deep box',
	Long: 'The deepest container.',
}]);
`
	if err := ts.WriteSource("/containerC.js", containerCSource); err != nil {
		return fmt.Errorf("failed to create /containerC.js: %w", err)
	}
	containerCID, err := tc.createObject("/containerC.js")
	if err != nil {
		return fmt.Errorf("create container C: %w", err)
	}

	// Move C into B (so now A > B > C)
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", containerCID, containerBID)); err != nil {
		return fmt.Errorf("/move C into B: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/move C into B did not complete")
	}
	if !tc.waitForLocation(fmt.Sprintf("#%s", containerCID), containerBID, defaultWaitTimeout) {
		return fmt.Errorf("container C did not move into B")
	}

	// Try to move A into C (should fail - C is inside B which is inside A)
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", containerAID, containerCID)); err != nil {
		return fmt.Errorf("/move A into C: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/move A into C did not complete: %q", output)
	}
	if !strings.Contains(output, "cannot move object into itself") {
		return fmt.Errorf("deep circular move should fail with error, got: %q", output)
	}

	fmt.Println("  Circular container prevention: OK")

	// === Test 25: getNeighbourhood() ===
	fmt.Println("Testing getNeighbourhood()...")

	// Ensure we're in genesis where we have exits to other rooms
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for getNeighbourhood: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for getNeighbourhood did not complete")
	}

	// Create a scout object that uses getNeighbourhood() on a "survey" action
	// and reports what it finds in its description
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
	if err := ts.WriteSource("/scout.js", scoutSource); err != nil {
		return fmt.Errorf("failed to create /scout.js: %w", err)
	}
	if _, err := tc.createObject("/scout.js"); err != nil {
		return fmt.Errorf("create scout: %w", err)
	}

	// Trigger the survey action
	if err := tc.sendLine("survey"); err != nil {
		return fmt.Errorf("survey command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("survey command did not complete")
	}

	// Wait for the scout to update its description with neighbourhood info
	// It should see genesis as location and have at least 1 neighbor (lookroom via south exit)
	// The pattern checks for: location info, content count, and neighbor info
	_, found = tc.waitForObject("*scout*loc:*content:*neighbors:*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("scout did not report neighbourhood info")
	}

	// Verify it found neighbors (not neighbors:0) by checking for at least 1 neighbor
	_, found = tc.waitForObject("*scout*neighbors:1*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("scout should see at least 1 neighbor via exits")
	}

	// Verify it got a real neighbor name (not "first:none")
	_, found = tc.waitForObject("*scout*first:*", defaultWaitTimeout)
	_, foundNone := tc.waitForObject("*scout*first:none*", 100*time.Millisecond)
	if foundNone {
		return fmt.Errorf("scout should see actual neighbor info, not 'none'")
	}
	if !found {
		return fmt.Errorf("scout should report first neighbor name")
	}

	fmt.Println("  getNeighbourhood(): OK")

	// === Test 26: removeCallback() ===
	fmt.Println("Testing removeCallback()...")

	// Use isolated room to prevent action name collisions with other tests
	if _, err := tc.enterIsolatedRoom(ts, "removeCallback"); err != nil {
		return fmt.Errorf("failed to enter isolated room for removeCallback test: %w", err)
	}

	// Create an object that has a callback, then removes it
	// Uses a "proof" callback to verify the action was dispatched even after removeCallback
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
	if err := ts.WriteSource("/callback_test.js", removeCallbackSource); err != nil {
		return fmt.Errorf("failed to create /callback_test.js: %w", err)
	}
	if _, err := tc.createObject("/callback_test.js"); err != nil {
		return fmt.Errorf("create callback_test: %w", err)
	}

	// First verify ping callback works - also triggers pingproof (proof:0->1)
	if err := tc.sendLine("ping"); err != nil {
		return fmt.Errorf("ping command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("ping command did not complete")
	}
	// Also trigger pingproof to update proof count
	if err := tc.sendLine("pingproof"); err != nil {
		return fmt.Errorf("pingproof command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("pingproof command did not complete")
	}
	// Check proof via checkproof action (should be 1 after pingproof)
	if err := tc.sendLine("checkproof"); err != nil {
		return fmt.Errorf("checkproof command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("checkproof command did not complete")
	}
	_, found = tc.waitForObject("*callback test*checked*proof:1*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("callback test object should show proof:1 after pingproof")
	}

	// Now disable the callback (removes 'ping' but not 'pingproof')
	if err := tc.sendLine("disable"); err != nil {
		return fmt.Errorf("disable command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("disable command did not complete")
	}
	_, found = tc.waitForObject("*callback test*ping disabled*proof:1*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("callback test object did not confirm ping disabled with proof:1")
	}

	// Ping again - should NOT change description since callback was removed
	if err := tc.sendLine("ping"); err != nil {
		return fmt.Errorf("second ping command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("second ping command did not complete")
	}
	// pingproof again to prove action was dispatched (proof:1->2)
	if err := tc.sendLine("pingproof"); err != nil {
		return fmt.Errorf("second pingproof command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("second pingproof command did not complete")
	}
	// Verify via checkproof: proof should be 2 AND description should still be "ping disabled"
	if err := tc.sendLine("checkproof"); err != nil {
		return fmt.Errorf("final checkproof command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("final checkproof command did not complete")
	}
	// proof:2 proves pingproof was called (action dispatched), but description shows "checked" not "ping received"
	// which proves the 'ping' callback was removed
	_, found = tc.waitForObject("*callback test*checked*proof:2*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("callback test object should show 'checked' with proof:2, proving ping action was dispatched but callback removed")
	}

	fmt.Println("  removeCallback(): OK")

	// === Test 27: getSkillConfig() / casSkillConfig() ===
	fmt.Println("Testing getSkillConfig() / casSkillConfig()...")

	// Create an object that can query and set skill configs
	skillConfigSource := `setDescriptions([{Short: 'skill config tester (ready)'}]);
addCallback('getconfig', ['action'], (msg) => {
	const config = getSkillConfig('TestConfigSkill');
	if (config === null) {
		setDescriptions([{Short: 'skill config tester (config:null)'}]);
	} else {
		setDescriptions([{Short: 'skill config tester (config:forget=' + config.Forget + ')'}]);
	}
});
addCallback('setconfig', ['action'], (msg) => {
	// Use CAS to set config - null as old value means "doesn't exist yet"
	const newConfig = {Forget: 3600, Recharge: 1000};
	const success = casSkillConfig('TestConfigSkill', null, newConfig);
	setDescriptions([{Short: 'skill config tester (set:' + success + ')'}]);
});
addCallback('updateconfig', ['action'], (msg) => {
	// Use CAS to update existing config
	const oldConfig = getSkillConfig('TestConfigSkill');
	if (oldConfig === null) {
		setDescriptions([{Short: 'skill config tester (update:noexist)'}]);
		return;
	}
	const newConfig = {Forget: 7200, Recharge: oldConfig.Recharge};
	const success = casSkillConfig('TestConfigSkill', oldConfig, newConfig);
	setDescriptions([{Short: 'skill config tester (update:' + success + ')'}]);
});
`
	if err := ts.WriteSource("/skill_config_test.js", skillConfigSource); err != nil {
		return fmt.Errorf("failed to create /skill_config_test.js: %w", err)
	}
	if _, err := tc.createObject("/skill_config_test.js"); err != nil {
		return fmt.Errorf("create skill_config_test: %w", err)
	}

	// First query - should be null (doesn't exist yet)
	if err := tc.sendLine("getconfig"); err != nil {
		return fmt.Errorf("getconfig command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("getconfig command did not complete")
	}
	_, found = tc.waitForObject("*skill config tester*config:null*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("skill config should be null initially")
	}

	// Set the config
	if err := tc.sendLine("setconfig"); err != nil {
		return fmt.Errorf("setconfig command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("setconfig command did not complete")
	}
	_, found = tc.waitForObject("*skill config tester*set:true*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("casSkillConfig should return true for new config")
	}

	// Query again - should now have value
	if err := tc.sendLine("getconfig"); err != nil {
		return fmt.Errorf("second getconfig command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("second getconfig command did not complete")
	}
	_, found = tc.waitForObject("*skill config tester*config:forget=3600*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("skill config should have Forget=3600 after set")
	}

	// Update the config using CAS
	if err := tc.sendLine("updateconfig"); err != nil {
		return fmt.Errorf("updateconfig command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("updateconfig command did not complete")
	}
	_, found = tc.waitForObject("*skill config tester*update:true*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("casSkillConfig should return true for valid update")
	}

	// Verify update took effect
	if err := tc.sendLine("getconfig"); err != nil {
		return fmt.Errorf("third getconfig command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("third getconfig command did not complete")
	}
	_, found = tc.waitForObject("*skill config tester*config:forget=7200*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("skill config should have Forget=7200 after update")
	}

	fmt.Println("  getSkillConfig() / casSkillConfig(): OK")

	// === Test 28: getLocation() and moveObject() ===
	fmt.Println("Testing getLocation() and moveObject()...")

	// Create a teleporter object that can move itself using moveObject
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
	if err := ts.WriteSource("/teleporter.js", teleportSource); err != nil {
		return fmt.Errorf("failed to create /teleporter.js: %w", err)
	}
	teleporterID, err := tc.createObject("/teleporter.js")
	if err != nil {
		return fmt.Errorf("create teleporter: %w", err)
	}

	// Get current location
	if err := tc.sendLine("report"); err != nil {
		return fmt.Errorf("report command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("report command did not complete")
	}
	_, found = tc.waitForObject("*teleporter*at:*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("teleporter should report location")
	}

	// Move to a different room first (lookroom)
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", lookRoomID)); err != nil {
		return fmt.Errorf("/enter lookroom: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter lookroom did not complete")
	}
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", teleporterID, lookRoomID)); err != nil {
		return fmt.Errorf("/move teleporter: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/move teleporter did not complete")
	}

	// Now teleport it back to genesis using moveObject from JS
	if err := tc.sendLine("teleport"); err != nil {
		return fmt.Errorf("teleport command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("teleport command did not complete")
	}

	// Verify it moved by checking its location via /inspect
	if !tc.waitForLocation(fmt.Sprintf("#%s", teleporterID), "genesis", defaultWaitTimeout) {
		return fmt.Errorf("teleporter should be at genesis after moveObject")
	}

	fmt.Println("  getLocation() and moveObject(): OK")

	// === Test 29: getContent() ===
	fmt.Println("Testing getContent()...")

	// Go back to genesis for this test
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for content test: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for content test did not complete")
	}

	// Create a container that can report its content (getContent is read-only)
	containerSource := `setDescriptions([{Short: 'content container (ready)'}]);
addCallback('countitems', ['action'], (msg) => {
	const content = getContent();
	const count = content ? Object.keys(content).length : 0;
	setDescriptions([{Short: 'content container (items:' + count + ')'}]);
});
`
	if err := ts.WriteSource("/content_container.js", containerSource); err != nil {
		return fmt.Errorf("failed to create /content_container.js: %w", err)
	}
	containerID, err := tc.createObject("/content_container.js")
	if err != nil {
		return fmt.Errorf("create content_container: %w", err)
	}

	// Count items (should be 0)
	if err := tc.sendLine("countitems"); err != nil {
		return fmt.Errorf("countitems command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("countitems command did not complete")
	}
	_, found = tc.waitForObject("*content container*items:0*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("container should have 0 items initially")
	}

	// Create a small item and put it in the container using /move
	itemSource := `setDescriptions([{Short: 'tiny item'}]);`
	if err := ts.WriteSource("/tiny_item.js", itemSource); err != nil {
		return fmt.Errorf("failed to create /tiny_item.js: %w", err)
	}
	itemID, err := tc.createObject("/tiny_item.js")
	if err != nil {
		return fmt.Errorf("create tiny_item: %w", err)
	}

	// Move item into container using /move command
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", itemID, containerID)); err != nil {
		return fmt.Errorf("/move item into container: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/move item did not complete")
	}

	// Count items again (should be 1)
	if err := tc.sendLine("countitems"); err != nil {
		return fmt.Errorf("second countitems command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("second countitems command did not complete")
	}
	_, found = tc.waitForObject("*content container*items:1*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("container should have 1 item after move")
	}

	fmt.Println("  getContent(): OK")

	// === Test 30: getSourcePath() / setSourcePath() ===
	fmt.Println("Testing getSourcePath() / setSourcePath()...")

	// Use isolated room to prevent action name collisions with other tests
	if _, err := tc.enterIsolatedRoom(ts, "sourcePath"); err != nil {
		return fmt.Errorf("failed to enter isolated room for sourcePath test: %w", err)
	}

	// Create an object that can report and change its source path
	sourcePathSource := `setDescriptions([{Short: 'source path tester (ready)'}]);
addCallback('getpath', ['action'], (msg) => {
	const path = getSourcePath();
	setDescriptions([{Short: 'source path tester (path:' + path + ')'}]);
});
addCallback('setpath', ['action'], (msg) => {
	setSourcePath('/new_source.js');
	// Note: description will be reset on next reload, so we verify via /inspect
});
`
	if err := ts.WriteSource("/source_path_test.js", sourcePathSource); err != nil {
		return fmt.Errorf("failed to create /source_path_test.js: %w", err)
	}
	// Create the new source file so the object can still run after path change
	if err := ts.WriteSource("/new_source.js", sourcePathSource); err != nil {
		return fmt.Errorf("failed to create /new_source.js: %w", err)
	}
	sourcePathObjID, err := tc.createObject("/source_path_test.js")
	if err != nil {
		return fmt.Errorf("create source_path_test: %w", err)
	}

	// Test getSourcePath() - verify it returns the correct path via description
	if err := tc.sendLine("getpath"); err != nil {
		return fmt.Errorf("getpath command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("getpath command did not complete")
	}
	_, found = tc.waitForObject("*source path tester*path:/source_path_test.js*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("getSourcePath() should return /source_path_test.js initially")
	}

	// Also verify via /inspect
	if !tc.waitForSourcePath(fmt.Sprintf("#%s", sourcePathObjID), "/source_path_test.js", defaultWaitTimeout) {
		return fmt.Errorf("source path should be /source_path_test.js initially via /inspect")
	}

	// Test setSourcePath - change the path
	if err := tc.sendLine("setpath"); err != nil {
		return fmt.Errorf("setpath command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("setpath command did not complete")
	}

	// Verify SourcePath changed via /inspect (description gets reset on reload)
	if !tc.waitForSourcePath(fmt.Sprintf("#%s", sourcePathObjID), "/new_source.js", defaultWaitTimeout) {
		return fmt.Errorf("source path should be /new_source.js after setSourcePath")
	}

	fmt.Println("  getSourcePath() / setSourcePath(): OK")

	// === Test 31: getLearning() / setLearning() ===
	fmt.Println("Testing getLearning() / setLearning()...")

	// Create an object that can toggle learning mode
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
	if err := ts.WriteSource("/learning_test.js", learningSource); err != nil {
		return fmt.Errorf("failed to create /learning_test.js: %w", err)
	}
	if _, err := tc.createObject("/learning_test.js"); err != nil {
		return fmt.Errorf("create learning_test: %w", err)
	}

	// Check initial learning state (should be false)
	if err := tc.sendLine("checklearn"); err != nil {
		return fmt.Errorf("checklearn command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("checklearn command did not complete")
	}
	_, found = tc.waitForObject("*learning tester*learning:false*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("learning should be false initially")
	}

	// Enable learning
	if err := tc.sendLine("enablelearn"); err != nil {
		return fmt.Errorf("enablelearn command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("enablelearn command did not complete")
	}
	_, found = tc.waitForObject("*learning tester*enabled*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("learning tester should confirm enabled")
	}

	// Check learning state again (should be true)
	if err := tc.sendLine("checklearn"); err != nil {
		return fmt.Errorf("second checklearn command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("second checklearn command did not complete")
	}
	_, found = tc.waitForObject("*learning tester*learning:true*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("learning should be true after enable")
	}

	// Disable learning
	if err := tc.sendLine("disablelearn"); err != nil {
		return fmt.Errorf("disablelearn command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("disablelearn command did not complete")
	}
	_, found = tc.waitForObject("*learning tester*disabled*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("learning tester should confirm disabled")
	}

	// Check learning state again (should be false)
	if err := tc.sendLine("checklearn"); err != nil {
		return fmt.Errorf("third checklearn command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("third checklearn command did not complete")
	}
	_, found = tc.waitForObject("*learning tester*learning:false*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("learning should be false after disable")
	}

	fmt.Println("  getLearning() / setLearning(): OK")

	// === Test 32: /exit at universe root (edge case) ===
	fmt.Println("Testing /exit at universe root...")

	// First, ensure we're in genesis (which has no parent location)
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for exit test: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for exit test did not complete")
	}

	// Verify we're in genesis
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		return fmt.Errorf("should be in genesis for exit test")
	}

	// Drain any stale notifications before the command
	tc.readUntil(50*time.Millisecond, nil)

	// Try to /exit from genesis - should fail with friendly message
	// Genesis is in the root container, so you can't exit further.
	if err := tc.sendLine("/exit"); err != nil {
		return fmt.Errorf("/exit at genesis: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/exit at genesis did not complete: %q", output)
	}
	if !strings.Contains(output, "Unable to leave the universe") {
		return fmt.Errorf("/exit at genesis should fail: %q", output)
	}

	// Verify we're still in genesis (didn't move)
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		return fmt.Errorf("should still be in genesis after failed /exit")
	}

	fmt.Println("  /exit at universe root: OK")

	// === Test 33: /remove current location (edge case) ===
	fmt.Println("Testing /remove current location...")

	// Create a room to test with
	removeTestRoomSource := `setDescriptions([{Short: 'remove test room', Long: 'A room for testing /remove edge case.'}]);
setExits([{Name: 'out', Destination: 'genesis'}]);
`
	if err := ts.WriteSource("/removetestroom.js", removeTestRoomSource); err != nil {
		return fmt.Errorf("failed to create /removetestroom.js: %w", err)
	}

	removeTestRoomID, err := tc.createObject("/removetestroom.js")
	if err != nil {
		return fmt.Errorf("create removetestroom: %w", err)
	}

	// Enter the test room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", removeTestRoomID)); err != nil {
		return fmt.Errorf("/enter removetestroom: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter removetestroom did not complete")
	}

	// Verify we're in the test room
	if !tc.waitForLocation("", removeTestRoomID, defaultWaitTimeout) {
		return fmt.Errorf("should be in removetestroom")
	}

	// Drain any stale notifications before the command
	tc.readUntil(50*time.Millisecond, nil)

	// Try to /remove our current location - should fail
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", removeTestRoomID)); err != nil {
		return fmt.Errorf("/remove current location: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/remove current location did not complete: %q", output)
	}
	if !strings.Contains(output, "Can't remove current location") {
		return fmt.Errorf("/remove current location should fail with 'Can't remove current location': %q", output)
	}

	// Verify the room still exists (wasn't removed)
	_, err = tc.inspect(fmt.Sprintf("#%s", removeTestRoomID))
	if err != nil {
		return fmt.Errorf("room should still exist after failed /remove: %w", err)
	}

	// Return to genesis and clean up
	if err := tc.sendLine("out"); err != nil {
		return fmt.Errorf("out from removetestroom: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("out from removetestroom did not complete")
	}

	// Now we can remove the test room since we're no longer in it
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", removeTestRoomID)); err != nil {
		return fmt.Errorf("/remove test room cleanup: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/remove test room cleanup did not complete")
	}

	fmt.Println("  /remove current location: OK")

	// === Test 34: JavaScript imports (@import directive) ===
	fmt.Println("Testing JavaScript imports...")

	// Create a library file that exports a utility function
	libSource := `// /lib/greeter.js - A library that provides greeting functionality
var greeter = greeter || {};
greeter.hello = function(name) {
    return 'Hello, ' + name + '!';
};
`
	if err := ts.WriteSource("/lib/greeter.js", libSource); err != nil {
		return fmt.Errorf("failed to create /lib/greeter.js: %w", err)
	}

	// Create an object that imports the library
	// Note: Using 'action' tag since this object is a sibling, not the player
	importerSource := `// @import /lib/greeter.js

addCallback('greet', ['action'], function(msg) {
    // Use the imported greeter library
    var greeting = greeter.hello('World');
    setDescriptions([{Short: 'greeter (' + greeting + ')'}]);
    log('Greeting result: ' + greeting);
    return true;
});

setDescriptions([{Short: 'greeter (idle)'}]);
`
	if err := ts.WriteSource("/importer.js", importerSource); err != nil {
		return fmt.Errorf("failed to create /importer.js: %w", err)
	}

	// Create an object using the importer source
	importerID, err := tc.createObject("/importer.js")
	if err != nil {
		return fmt.Errorf("create importer object: %w", err)
	}

	// Verify the object was created with initial description
	output, ok = tc.sendCommand("look", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look for importer did not complete")
	}
	if !strings.Contains(output, "greeter (idle)") {
		return fmt.Errorf("importer should show initial 'greeter (idle)' description: %q", output)
	}

	// Invoke the greet command which uses the imported library
	output, ok = tc.sendCommand("greet", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("greet command did not complete")
	}

	// Verify the command worked by checking the updated description
	greetLookOutput, ok := tc.waitForLookMatch("greeter (Hello, World!)", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("greet command should update description to show greeting, got: %q", greetLookOutput)
	}

	// Test relative imports - create a chain of imports
	baseSource := `// /mobs/base.js - Base functionality for mobs
var mobBase = mobBase || {};
mobBase.species = 'unknown';
mobBase.describe = function() {
    return 'A ' + this.species + ' creature';
};
`
	if err := ts.WriteSource("/mobs/base.js", baseSource); err != nil {
		return fmt.Errorf("failed to create /mobs/base.js: %w", err)
	}

	dogSource := `// @import ./base.js

// Override species
mobBase.species = 'canine';

addCallback('bark', ['action'], function(msg) {
    setDescriptions([{Short: 'dog (' + mobBase.describe() + ')'}]);
    return true;
});

setDescriptions([{Short: 'dog (sleeping)'}]);
`
	if err := ts.WriteSource("/mobs/dog.js", dogSource); err != nil {
		return fmt.Errorf("failed to create /mobs/dog.js: %w", err)
	}

	// Create a dog object
	dogID, err := tc.createObject("/mobs/dog.js")
	if err != nil {
		return fmt.Errorf("create dog object: %w", err)
	}

	// Verify initial state
	output, ok = tc.sendCommand("look", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look for dog did not complete")
	}
	if !strings.Contains(output, "dog (sleeping)") {
		return fmt.Errorf("dog should show initial 'dog (sleeping)' description: %q", output)
	}

	// Invoke bark command which uses the relatively imported base
	output, ok = tc.sendCommand("bark", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("bark command did not complete")
	}

	// Verify the command worked with the imported base functionality
	_, ok = tc.waitForLookMatch("dog (A canine creature)", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("bark command should update description using imported base")
	}

	// Clean up test objects
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", importerID)); err != nil {
		return fmt.Errorf("/remove importer: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/remove importer did not complete")
	}

	if err := tc.sendLine(fmt.Sprintf("/remove #%s", dogID)); err != nil {
		return fmt.Errorf("/remove dog: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/remove dog did not complete")
	}

	fmt.Println("  JavaScript imports: OK")

	// === Test 35: exitFailed event ===
	fmt.Println("Testing exitFailed event...")

	// Ensure we're in genesis
	if err := tc.sendLine("/enter #genesis"); err != nil {
		return fmt.Errorf("/enter genesis for exitFailed: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter genesis for exitFailed did not complete")
	}
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		return fmt.Errorf("should be in genesis for exitFailed test")
	}

	// Create a room that:
	// 1. Has an exit with UseChallenges and a Message
	// 2. Registers callback for 'exitFailed' to update description
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
	if err := ts.WriteSource("/exitfailed_room.js", exitFailedRoomSource); err != nil {
		return fmt.Errorf("failed to create /exitfailed_room.js: %w", err)
	}

	// Create the room
	exitFailedRoomID, err := tc.createObject("/exitfailed_room.js")
	if err != nil {
		return fmt.Errorf("create exitfailed_room: %w", err)
	}

	// Enter the room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", exitFailedRoomID)); err != nil {
		return fmt.Errorf("/enter exitfailed_room: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/enter exitfailed_room did not complete")
	}
	if !tc.waitForLocation("", exitFailedRoomID, defaultWaitTimeout) {
		return fmt.Errorf("user did not move to exitfailed_room")
	}

	// Verify initial room description
	output, ok = tc.waitForLookMatch("Exit Failed Test Room (idle)", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look did not show initial exitfailed room description: %q", output)
	}

	// Try to use the blocked exit (should fail - no telekinesis skill)
	output, ok = tc.sendCommand("blocked", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("blocked exit command did not complete")
	}

	// Verify the challenge message was printed
	if !strings.Contains(output, "The door remains firmly shut.") {
		return fmt.Errorf("blocked exit should print challenge message: %q", output)
	}

	// Verify user is still in the room
	selfInspect, err = tc.inspect("")
	if err != nil {
		return fmt.Errorf("failed to inspect self after exitFailed: %w", err)
	}
	if selfInspect.GetLocation() != exitFailedRoomID {
		return fmt.Errorf("user should still be in exitfailed_room after failed exit, but is in %q", selfInspect.GetLocation())
	}

	// Wait for the room's description to change (confirms exitFailed event was received)
	output, ok = tc.waitForLookMatch("Exit Failed Test Room (saw: blocked", defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("room should have received exitFailed event and updated description: %q", output)
	}

	// Cleanup: exit back to genesis
	if err := tc.sendLine("out"); err != nil {
		return fmt.Errorf("out exit command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("out exit command did not complete")
	}
	if !tc.waitForLocation("", "genesis", defaultWaitTimeout) {
		return fmt.Errorf("user did not move to genesis via 'out' exit")
	}

	// Remove the test room
	if err := tc.sendLine(fmt.Sprintf("/remove #%s", exitFailedRoomID)); err != nil {
		return fmt.Errorf("/remove exitfailed_room: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/remove exitfailed_room did not complete")
	}

	fmt.Println("  exitFailed event: OK")

	return nil
}
