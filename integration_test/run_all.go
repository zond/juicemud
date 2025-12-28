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

	// NOTE: Test 15 (created event) has been extracted to TestCreatedEvent
	// NOTE: Test 16 (look [target]) has been extracted to TestLookTarget
	// NOTE: Test 17 (/stats wizard command) has been extracted to TestStatsCommand

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

	// NOTE: Test 19 (State persistence) has been extracted to TestStatePersistence

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

	// NOTE: Test 24 (Circular container prevention) has been extracted to TestCircularContainerPrevention

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

	// NOTE: Test 26 (removeCallback()) has been extracted to TestRemoveCallback

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
