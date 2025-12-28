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
	// NOTE: User creation, wizard setup, and login are handled by TestMain.
	// We use the package variables testServer, wizardClient, and wizardUser.
	tc := wizardClient

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

	// NOTE: Test 9 (emit inter-object communication) has been extracted to TestEmitInterObject
	// NOTE: Test 10 (setTimeout) has been extracted to TestSetTimeout
	// NOTE: Test 11 (/remove command) has been extracted to TestRemoveCommand

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
	output, found := tc.waitForLookMatch("watcher orb (saw: "+moveableID+")", defaultWaitTimeout)
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

	// NOTE: Test 13 (/debug and log()) has been extracted to TestDebugLog
	// NOTE: Test 15 (created event) has been extracted to TestCreatedEvent
	// NOTE: Test 16 (look [target]) has been extracted to TestLookTarget
	// NOTE: Test 17 (/stats wizard command) has been extracted to TestStatsCommand
	// NOTE: Test 18 (Room and sibling action handlers) has been extracted to TestRoomAndSiblingActionHandlers
	// NOTE: Test 19 (State persistence) has been extracted to TestStatePersistence
	// NOTE: Test 20 (emit with challenges) has been extracted to TestEmitWithChallenges
	// NOTE: Test 21 (emitToLocation) has been extracted to TestEmitToLocation
	// NOTE: Test 22 (emitToLocation with challenges) has been extracted to TestEmitToLocationWithChallenges
	// NOTE: Test 23 (/addwiz and /delwiz commands) has been extracted to TestAddDelWiz
	// NOTE: Test 24 (Circular container prevention) has been extracted to TestCircularContainerPrevention
	// NOTE: Test 25 (getNeighbourhood) has been extracted to TestGetNeighbourhood
	// NOTE: Test 26 (removeCallback()) has been extracted to TestRemoveCallback
	// NOTE: Test 27 (getSkillConfig/casSkillConfig) has been extracted to TestSkillConfig

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

	// NOTE: Test 30 (getSourcePath/setSourcePath) has been extracted to TestGetSetSourcePath
	// NOTE: Test 31 (getLearning/setLearning) has been extracted to TestGetSetLearning
	// NOTE: Test 32 (/exit at universe root) has been extracted to TestExitAtUniverseRoot
	// NOTE: Test 33 (/remove current location) has been extracted to TestRemoveCurrentLocation
	// NOTE: Test 34 (JavaScript imports) has been extracted to TestJavaScriptImports
	// NOTE: Test 35 (exitFailed event) has been extracted to TestExitFailedEvent
	// NOTE: Test (setInterval/clearInterval) has been extracted to TestSetInterval
	// NOTE: Test (/intervals wizard command) has been extracted to TestIntervalsCommand
	// NOTE: Test (createObject/removeObject JS APIs) has been extracted to TestCreateRemoveObject

	return nil
}
