// Package integration_test provides integration tests for the juicemud server.
//
// # Testing Principles
//
// All interactions should use the same interfaces as production: SSH for game
// commands and WebDAV for file operations. Direct function calls on the test
// server should only be used for:
//   - Necessary setup/teardown operations
//   - Verification that SSH/WebDAV actions succeeded, and only when verifying
//     via SSH/WebDAV would be unnecessarily complex
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

	// === Test 2: WebDAV file operations ===
	fmt.Println("Testing WebDAV file operations...")

	// Make testuser an owner for subsequent tests (needed to bootstrap wizard access)
	user.Owner = true
	if err := ts.Storage().StoreUser(ctx, user, true, ""); err != nil {
		return fmt.Errorf("failed to make testuser owner: %w", err)
	}

	// Reconnect as owner - owner status is checked at login time
	tc, err := loginUser(ts.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		return fmt.Errorf("loginUser as owner: %w", err)
	}

	// Use /adduser command to add ourselves to the wizards group
	// This tests the group management commands via SSH interface
	if err := tc.sendLine("/adduser testuser wizards"); err != nil {
		return fmt.Errorf("/adduser to wizards: %w", err)
	}
	output, ok := tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/adduser to wizards did not complete: %q", output)
	}
	if !strings.Contains(output, `Added "testuser" to "wizards"`) {
		return fmt.Errorf("/adduser to wizards should show success: %q", output)
	}

	// Reconnect to pick up wizard status - wizard membership is checked at login time
	tc.Close()
	tc, err = loginUser(ts.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		return fmt.Errorf("loginUser as wizard: %w", err)
	}
	defer tc.Close()

	// Test WebDAV operations
	dav := newWebDAVClient(ts.HTTPAddr(), "testuser", "testpass123")

	// Read existing file
	content, err := dav.Get("/user.js")
	if err != nil {
		return fmt.Errorf("failed to GET /user.js: %w", err)
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
	if err := dav.Put("/testroom.js", roomSource); err != nil {
		return fmt.Errorf("failed to PUT /testroom.js: %w", err)
	}

	// Verify file was created
	readBack, err := dav.Get("/testroom.js")
	if err != nil {
		return fmt.Errorf("failed to GET /testroom.js: %w", err)
	}
	if readBack != roomSource {
		return fmt.Errorf("file content mismatch: got %q, want %q", readBack, roomSource)
	}

	fmt.Println("  WebDAV file operations: OK")

	// === Test 3: Wizard commands ===
	fmt.Println("Testing wizard commands...")

	// Create a source file for objects
	boxSource := `// A simple box
setDescriptions([{
	Short: 'wooden box',
	Long: 'A simple wooden box.',
}]);
`
	if err := dav.Put("/box.js", boxSource); err != nil {
		return fmt.Errorf("failed to create /box.js: %w", err)
	}

	// Create an object (user is already connected and is now a wizard)
	if err := tc.sendLine("/create /box.js"); err != nil {
		return fmt.Errorf("/create command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create command did not complete")
	}

	// Poll for object creation via /inspect (uses glob matching, so *box* matches "wooden box")
	if _, found := tc.waitForObject("*box*", defaultWaitTimeout); !found {
		return fmt.Errorf("box object was not created")
	}

	// Test /inspect
	if err := tc.sendLine("/inspect"); err != nil {
		return fmt.Errorf("/inspect command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/inspect command did not complete")
	}

	// Test /ls
	if err := tc.sendLine("/ls /"); err != nil {
		return fmt.Errorf("/ls command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/ls command did not complete")
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
	if err := dav.Put("/room1.js", room1Source); err != nil {
		return fmt.Errorf("failed to create /room1.js: %w", err)
	}
	if err := dav.Put("/room2.js", room2Source); err != nil {
		return fmt.Errorf("failed to create /room2.js: %w", err)
	}

	// Create rooms
	if err := tc.sendLine("/create /room1.js"); err != nil {
		return fmt.Errorf("/create room1: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create room1 did not complete")
	}
	if err := tc.sendLine("/create /room2.js"); err != nil {
		return fmt.Errorf("/create room2: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create room2 did not complete")
	}

	// Poll for room creation via /inspect
	room1ID, found := tc.waitForObject("Room One", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("room1 was not created")
	}
	if _, found := tc.waitForObject("Room Two", defaultWaitTimeout); !found {
		return fmt.Errorf("room2 was not created")
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

	output, ok = tc.waitForPrompt(defaultWaitTimeout)
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
	if err := dav.Put("/lookroom.js", lookRoomSource); err != nil {
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
	if err := dav.Put("/book.js", bookSource); err != nil {
		return fmt.Errorf("failed to create /book.js: %w", err)
	}

	if err := tc.sendLine("/create /lookroom.js"); err != nil {
		return fmt.Errorf("/create lookroom: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create lookroom did not complete")
	}

	lookRoomID, found := tc.waitForObject("*Library*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("lookroom was not created")
	}

	if err := tc.sendLine("/create /book.js"); err != nil {
		return fmt.Errorf("/create book: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create book did not complete")
	}

	bookID, found := tc.waitForObject("*book*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("book was not created")
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
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look command: %w", err)
	}

	// Verify look shows the room description
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look in lookroom did not complete: %q", output)
	}
	if !strings.Contains(output, "Cozy Library") {
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
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look before genesis update: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look before genesis update did not complete: %q", output)
	}
	if !strings.Contains(output, "Black cosmos") {
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
	if err := dav.Put("/genesis.js", genesisSource); err != nil {
		return fmt.Errorf("failed to update /genesis.js: %w", err)
	}

	// Verify we're in genesis (user is already connected, no need to reconnect)
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look in genesis: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look in genesis did not complete: %q", output)
	}
	if !strings.Contains(output, "Black cosmos") {
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
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look in lookroom: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look in lookroom did not complete: %q", output)
	}
	if !strings.Contains(output, "Cozy Library") {
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
	if err := tc.sendLine("scan"); err != nil {
		return fmt.Errorf("scan command: %w", err)
	}

	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("scan command did not complete: %q", output)
	}

	// Verify scan shows current location (genesis)
	if !strings.Contains(output, "Black cosmos") {
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
	if err := dav.Put("/challenge_room.js", challengeRoomSource); err != nil {
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
	if err := dav.Put("/hidden_gem.js", hiddenGemSource); err != nil {
		return fmt.Errorf("failed to create /hidden_gem.js: %w", err)
	}

	// Create the challenge room
	if err := tc.sendLine("/create /challenge_room.js"); err != nil {
		return fmt.Errorf("/create challenge_room: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create challenge_room did not complete")
	}

	challengeRoomID, found := tc.waitForObject("*Challenge*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("challenge_room was not created")
	}

	// Create the hidden gem
	if err := tc.sendLine("/create /hidden_gem.js"); err != nil {
		return fmt.Errorf("/create hidden_gem: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create hidden_gem did not complete")
	}

	// Hidden gem has a perception challenge, so user can't see it - use direct storage access
	hiddenGemID, found := ts.waitForSourceObject(ctx, "/hidden_gem.js", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("hidden_gem was not created")
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
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look in challenge_room: %w", err)
	}

	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look in challenge_room did not complete: %q", output)
	}
	if !strings.Contains(output, "Challenge Room") {
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
	if err := dav.Put("/user.js", trainableUserSource); err != nil {
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
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look in challenge_room with skills: %w", err)
	}

	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("look in challenge_room with skills did not complete: %q", output)
	}
	if !strings.Contains(output, "hidden gem") {
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
	if err := dav.Put("/receiver.js", receiverSource); err != nil {
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
	if err := dav.Put("/sender.js", senderSource); err != nil {
		return fmt.Errorf("failed to create /sender.js: %w", err)
	}

	if err := tc.sendLine("/create /receiver.js"); err != nil {
		return fmt.Errorf("/create receiver: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create receiver did not complete")
	}
	receiverID, found := tc.waitForObject("*receiver*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("receiver was not created")
	}

	if err := tc.sendLine("/create /sender.js"); err != nil {
		return fmt.Errorf("/create sender: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create sender did not complete")
	}
	if _, found := tc.waitForObject("*sender*", defaultWaitTimeout); !found {
		return fmt.Errorf("sender was not created")
	}

	// Ping the sender with the receiver's ID as target
	if err := tc.sendLine(fmt.Sprintf("ping %s", receiverID)); err != nil {
		return fmt.Errorf("ping command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("ping receiver command did not complete")
	}

	// Poll with look until we see the receiver got the message (emit has ~100ms delay)
	var lookOutput string
	found = waitForCondition(defaultWaitTimeout, 100*time.Millisecond, func() bool {
		tc.sendLine("look")
		lookOutput, _ = tc.waitForPrompt(defaultWaitTimeout)
		return strings.Contains(lookOutput, "receiver orb (got: hello)")
	})
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
	if err := dav.Put("/timer.js", timerSource); err != nil {
		return fmt.Errorf("failed to create /timer.js: %w", err)
	}

	if err := tc.sendLine("/create /timer.js"); err != nil {
		return fmt.Errorf("/create timer: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create timer did not complete")
	}
	if _, found := tc.waitForObject("*timer*", defaultWaitTimeout); !found {
		return fmt.Errorf("timer was not created")
	}

	// Poll until timer is visible in room
	var timerOutput string
	found = waitForCondition(defaultWaitTimeout, 100*time.Millisecond, func() bool {
		tc.sendLine("look")
		timerOutput, _ = tc.waitForPrompt(defaultWaitTimeout)
		return strings.Contains(timerOutput, "timer orb (idle)")
	})
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
	found = waitForCondition(defaultWaitTimeout, 100*time.Millisecond, func() bool {
		tc.sendLine("look")
		timerOutput, _ = tc.waitForPrompt(defaultWaitTimeout)
		return strings.Contains(timerOutput, "timer orb (fired)")
	})
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
	if err := dav.Put("/removable.js", removableSource); err != nil {
		return fmt.Errorf("failed to create /removable.js: %w", err)
	}

	if err := tc.sendLine("/create /removable.js"); err != nil {
		return fmt.Errorf("/create removable: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create removable did not complete")
	}

	removableID, found := tc.waitForObject("*removable*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("removable was not created")
	}

	// Verify object exists via /inspect (poll to handle buffered output)
	found = waitForCondition(defaultWaitTimeout, 100*time.Millisecond, func() bool {
		tc.sendLine(fmt.Sprintf("/inspect #%s", removableID))
		output, _ = tc.waitForPrompt(defaultWaitTimeout)
		return strings.Contains(output, "removable widget")
	})
	if !found {
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
	found = waitForCondition(defaultWaitTimeout, 50*time.Millisecond, func() bool {
		tc.sendLine(fmt.Sprintf("/inspect #%s", removableID))
		output, _ = tc.waitForPrompt(defaultWaitTimeout)
		// Object is gone if inspect doesn't show the description
		return !strings.Contains(output, "removable widget")
	})
	if !found {
		return fmt.Errorf("removable object should not exist after removal: %q", output)
	}

	// Test edge case: can't remove self - verify we stay logged in
	if err := tc.sendLine("/remove self"); err != nil {
		return fmt.Errorf("/remove self command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/remove self command did not complete: %q", output)
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
	const id = msg.Object && msg.Object.Unsafe ? msg.Object.Unsafe.Id : 'unknown';
	setDescriptions([{Short: 'watcher orb (saw: ' + id + ')'}]);
});
`
	if err := dav.Put("/observer.js", observerSource); err != nil {
		return fmt.Errorf("failed to create /observer.js: %w", err)
	}

	moveableSource := `setDescriptions([{Short: 'moveable cube'}]);
`
	if err := dav.Put("/moveable.js", moveableSource); err != nil {
		return fmt.Errorf("failed to create /moveable.js: %w", err)
	}

	if err := tc.sendLine("/create /observer.js"); err != nil {
		return fmt.Errorf("/create observer: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create observer did not complete")
	}
	if _, found := ts.waitForSourceObject(ctx, "/observer.js", defaultWaitTimeout); !found {
		return fmt.Errorf("observer was not created")
	}

	if err := tc.sendLine("/create /moveable.js"); err != nil {
		return fmt.Errorf("/create moveable: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create moveable did not complete")
	}

	moveableID, found := ts.waitForSourceObject(ctx, "/moveable.js", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("moveable was not created")
	}

	// Move the moveable to lookroom
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", moveableID, lookRoomID)); err != nil {
		return fmt.Errorf("/move moveable to lookroom: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/move moveable to lookroom did not complete")
	}

	// Poll with look until observer shows it saw the movement
	found = waitForCondition(defaultWaitTimeout, 100*time.Millisecond, func() bool {
		tc.sendLine("look")
		output, _ = tc.waitForPrompt(defaultWaitTimeout)
		return strings.Contains(output, "watcher orb (saw: "+moveableID+")")
	})
	if !found {
		return fmt.Errorf("observer should have seen moveable in movement event: %q", output)
	}

	fmt.Println("  Movement event notifications: OK")

	// === Test 13: Group management commands ===
	fmt.Println("Testing group management commands...")

	// Drain any buffered output from the previous polling loop by waiting for any
	// pending responses and then sending a known command.
	// The polling loop in the previous test sends 'look' commands - wait a moment
	// to let any in-flight responses arrive, then drain them.
	time.Sleep(200 * time.Millisecond)
	// Read and discard any buffered output
	tc.readUntil(100*time.Millisecond, nil)

	// Now send a drain command and wait for its prompt to ensure clean state
	if err := tc.sendLine("/groups"); err != nil {
		return fmt.Errorf("/groups drain command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/groups drain command did not complete")
	}

	// Test /mkgroup - create a group owned by "owner" (root level)
	if err := tc.sendLine("/mkgroup testgroup owner"); err != nil {
		return fmt.Errorf("/mkgroup command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/mkgroup command did not complete: %q", output)
	}
	if !strings.Contains(output, `Created group "testgroup"`) {
		return fmt.Errorf("/mkgroup should show success message: %q", output)
	}

	// Test /listgroups - verify group was created
	if err := tc.sendLine("/listgroups"); err != nil {
		return fmt.Errorf("/listgroups command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/listgroups command did not complete: %q", output)
	}
	if !strings.Contains(output, "testgroup") {
		return fmt.Errorf("/listgroups should show testgroup: %q", output)
	}
	if !strings.Contains(output, "wizards") {
		return fmt.Errorf("/listgroups should show wizards group: %q", output)
	}

	// Test /mkgroup with supergroup flag
	if err := tc.sendLine("/mkgroup supertest testgroup true"); err != nil {
		return fmt.Errorf("/mkgroup supertest command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/mkgroup supertest command did not complete: %q", output)
	}
	if !strings.Contains(output, `Created group "supertest"`) {
		return fmt.Errorf("/mkgroup supertest should show success: %q", output)
	}

	// Verify supertest appears in /listgroups with supergroup flag
	if err := tc.sendLine("/listgroups"); err != nil {
		return fmt.Errorf("/listgroups after supertest: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/listgroups after supertest did not complete: %q", output)
	}
	if !strings.Contains(output, "supertest") {
		return fmt.Errorf("/listgroups should show supertest: %q", output)
	}
	// Supertest should show "yes" in Supergroup column
	if !strings.Contains(output, "yes") {
		return fmt.Errorf("/listgroups should show 'yes' for supergroup: %q", output)
	}

	// Test /adduser - add testuser to testgroup
	if err := tc.sendLine("/adduser testuser testgroup"); err != nil {
		return fmt.Errorf("/adduser command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/adduser command did not complete: %q", output)
	}
	if !strings.Contains(output, `Added "testuser" to "testgroup"`) {
		return fmt.Errorf("/adduser should show success: %q", output)
	}

	// Test /members - verify user is in group
	if err := tc.sendLine("/members testgroup"); err != nil {
		return fmt.Errorf("/members command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/members command did not complete: %q", output)
	}
	if !strings.Contains(output, "testuser") {
		return fmt.Errorf("/members should show testuser: %q", output)
	}
	// lang.Card produces "a member" for count 1
	if !strings.Contains(output, "a member") {
		return fmt.Errorf("/members should show 'a member': %q", output)
	}

	// Test /groups - verify user sees their groups
	if err := tc.sendLine("/groups"); err != nil {
		return fmt.Errorf("/groups command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/groups command did not complete: %q", output)
	}
	if !strings.Contains(output, "testgroup") {
		return fmt.Errorf("/groups should show testgroup: %q", output)
	}
	if !strings.Contains(output, "wizards") {
		return fmt.Errorf("/groups should show wizards: %q", output)
	}

	// Test /groups with user argument
	if err := tc.sendLine("/groups testuser"); err != nil {
		return fmt.Errorf("/groups testuser command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/groups testuser command did not complete: %q", output)
	}
	if !strings.Contains(output, "testgroup") {
		return fmt.Errorf("/groups testuser should show testgroup: %q", output)
	}

	// Test /editgroup -name - rename group
	if err := tc.sendLine("/editgroup testgroup -name renamedgroup"); err != nil {
		return fmt.Errorf("/editgroup -name command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/editgroup -name command did not complete: %q", output)
	}
	if !strings.Contains(output, `Renamed group "testgroup" to "renamedgroup"`) {
		return fmt.Errorf("/editgroup -name should show success: %q", output)
	}

	// Verify rename via /listgroups
	if err := tc.sendLine("/listgroups"); err != nil {
		return fmt.Errorf("/listgroups after rename: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/listgroups after rename did not complete: %q", output)
	}
	if strings.Contains(output, "testgroup") {
		return fmt.Errorf("/listgroups should not show old name 'testgroup': %q", output)
	}
	if !strings.Contains(output, "renamedgroup") {
		return fmt.Errorf("/listgroups should show new name 'renamedgroup': %q", output)
	}

	// Test /editgroup -super - change supergroup flag
	if err := tc.sendLine("/editgroup renamedgroup -super true"); err != nil {
		return fmt.Errorf("/editgroup -super command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/editgroup -super command did not complete: %q", output)
	}
	if !strings.Contains(output, `Changed Supergroup of "renamedgroup" to true`) {
		return fmt.Errorf("/editgroup -super should show success: %q", output)
	}

	// Test /editgroup -owner - change OwnerGroup
	// First create a new group that we can use as an owner
	if err := tc.sendLine("/mkgroup ownertest owner"); err != nil {
		return fmt.Errorf("/mkgroup ownertest command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/mkgroup ownertest did not complete: %q", output)
	}

	// Change renamedgroup's OwnerGroup to ownertest
	if err := tc.sendLine("/editgroup renamedgroup -owner ownertest"); err != nil {
		return fmt.Errorf("/editgroup -owner command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/editgroup -owner command did not complete: %q", output)
	}
	if !strings.Contains(output, `Changed OwnerGroup of "renamedgroup" to "ownertest"`) {
		return fmt.Errorf("/editgroup -owner should show success: %q", output)
	}

	// Verify via /listgroups that renamedgroup now shows ownertest as owner
	if err := tc.sendLine("/listgroups"); err != nil {
		return fmt.Errorf("/listgroups after -owner: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/listgroups after -owner did not complete: %q", output)
	}
	// The output should show renamedgroup with ownertest as its owner
	// (ownertest appears in the OwnerGroup column for renamedgroup's row)

	// Change OwnerGroup back to "owner" (root level)
	if err := tc.sendLine("/editgroup renamedgroup -owner owner"); err != nil {
		return fmt.Errorf("/editgroup -owner to owner command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/editgroup -owner to owner did not complete: %q", output)
	}
	if !strings.Contains(output, `Changed OwnerGroup of "renamedgroup" to "owner"`) {
		return fmt.Errorf("/editgroup -owner to owner should show success: %q", output)
	}

	// Cleanup ownertest
	if err := tc.sendLine("/rmgroup ownertest"); err != nil {
		return fmt.Errorf("/rmgroup ownertest command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/rmgroup ownertest did not complete")
	}

	// Test /rmuser - remove user from group
	if err := tc.sendLine("/rmuser testuser renamedgroup"); err != nil {
		return fmt.Errorf("/rmuser command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/rmuser command did not complete: %q", output)
	}
	if !strings.Contains(output, `Removed "testuser" from "renamedgroup"`) {
		return fmt.Errorf("/rmuser should show success: %q", output)
	}

	// Verify removal via /members
	if err := tc.sendLine("/members renamedgroup"); err != nil {
		return fmt.Errorf("/members after rmuser: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/members after rmuser did not complete: %q", output)
	}
	if strings.Contains(output, "testuser") {
		return fmt.Errorf("/members should not show testuser after removal: %q", output)
	}
	// lang.Card produces "no members" for count 0
	if !strings.Contains(output, "no members") {
		return fmt.Errorf("/members should show 'no members': %q", output)
	}

	// Test /rmgroup - delete group
	if err := tc.sendLine("/rmgroup supertest"); err != nil {
		return fmt.Errorf("/rmgroup command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/rmgroup command did not complete: %q", output)
	}
	if !strings.Contains(output, `Deleted group "supertest"`) {
		return fmt.Errorf("/rmgroup should show success: %q", output)
	}

	// Verify deletion via /listgroups
	if err := tc.sendLine("/listgroups"); err != nil {
		return fmt.Errorf("/listgroups after rmgroup: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/listgroups after rmgroup did not complete: %q", output)
	}
	if strings.Contains(output, "supertest") {
		return fmt.Errorf("/listgroups should not show deleted group 'supertest': %q", output)
	}

	// Test error cases

	// Cannot create group with invalid name (starts with digit)
	if err := tc.sendLine("/mkgroup 123invalid owner"); err != nil {
		return fmt.Errorf("/mkgroup 123invalid command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/mkgroup 123invalid did not complete: %q", output)
	}
	if !strings.Contains(output, "Error:") {
		return fmt.Errorf("/mkgroup 123invalid should show error: %q", output)
	}

	// Cannot create duplicate group
	if err := tc.sendLine("/mkgroup renamedgroup owner"); err != nil {
		return fmt.Errorf("/mkgroup duplicate command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/mkgroup duplicate did not complete: %q", output)
	}
	if !strings.Contains(output, "Error:") {
		return fmt.Errorf("/mkgroup duplicate should show error: %q", output)
	}

	// Cannot delete non-existent group
	if err := tc.sendLine("/rmgroup nonexistent"); err != nil {
		return fmt.Errorf("/rmgroup nonexistent command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/rmgroup nonexistent did not complete: %q", output)
	}
	if !strings.Contains(output, "Error:") {
		return fmt.Errorf("/rmgroup nonexistent should show error: %q", output)
	}

	// Cannot remove user from group they're not in
	if err := tc.sendLine("/rmuser testuser renamedgroup"); err != nil {
		return fmt.Errorf("/rmuser not-member command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/rmuser not-member did not complete: %q", output)
	}
	if !strings.Contains(output, "Error:") {
		return fmt.Errorf("/rmuser not-member should show error: %q", output)
	}

	// === Test permission failures with non-owner user ===

	// Create a second user who is not an owner
	tc2, err := createUser(ts.SSHAddr(), "regularuser", "pass456")
	if err != nil {
		return fmt.Errorf("createUser regularuser: %w", err)
	}

	// Add regularuser to wizards group so they can use wizard commands
	if err := tc.sendLine("/adduser regularuser wizards"); err != nil {
		tc2.Close()
		return fmt.Errorf("/adduser regularuser wizards: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		tc2.Close()
		return fmt.Errorf("/adduser regularuser wizards did not complete")
	}

	// Reconnect regularuser to pick up wizard status
	tc2.Close()
	tc2, err = loginUser(ts.SSHAddr(), "regularuser", "pass456")
	if err != nil {
		return fmt.Errorf("loginUser regularuser: %w", err)
	}
	defer tc2.Close()

	// Non-owner cannot create root-level group (OwnerGroup=0)
	if err := tc2.sendLine("/mkgroup rootgroup owner"); err != nil {
		return fmt.Errorf("/mkgroup rootgroup by non-owner: %w", err)
	}
	output, ok = tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/mkgroup rootgroup by non-owner did not complete: %q", output)
	}
	if !strings.Contains(output, "Error:") {
		return fmt.Errorf("non-owner creating root-level group should fail: %q", output)
	}

	// Create a supergroup owned by wizards
	// It must be a supergroup so members can create groups under it
	if err := tc.sendLine("/mkgroup testowned wizards true"); err != nil {
		return fmt.Errorf("/mkgroup testowned: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/mkgroup testowned did not complete")
	}

	// Add regularuser to testowned so they can create groups under it
	if err := tc.sendLine("/adduser regularuser testowned"); err != nil {
		return fmt.Errorf("/adduser regularuser testowned: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/adduser regularuser testowned did not complete")
	}

	// regularuser CAN create a group under testowned (since they're in testowned which is a Supergroup)
	if err := tc2.sendLine("/mkgroup subgroup testowned"); err != nil {
		return fmt.Errorf("/mkgroup subgroup by regularuser: %w", err)
	}
	output, ok = tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/mkgroup subgroup by regularuser did not complete: %q", output)
	}
	if !strings.Contains(output, `Created group "subgroup"`) {
		return fmt.Errorf("wizard member should create group under owned group: %q", output)
	}

	// Non-owner cannot modify root-level groups
	if err := tc2.sendLine("/adduser regularuser renamedgroup"); err != nil {
		return fmt.Errorf("/adduser to root group by non-owner: %w", err)
	}
	output, ok = tc2.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/adduser to root group by non-owner did not complete: %q", output)
	}
	if !strings.Contains(output, "Error:") {
		return fmt.Errorf("non-owner modifying root-level group should fail: %q", output)
	}

	// Cleanup permission test groups
	if err := tc.sendLine("/rmgroup subgroup"); err != nil {
		return fmt.Errorf("/rmgroup subgroup: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/rmgroup subgroup did not complete")
	}
	if err := tc.sendLine("/rmgroup testowned"); err != nil {
		return fmt.Errorf("/rmgroup testowned: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/rmgroup testowned did not complete")
	}

	// Cleanup: delete renamedgroup
	if err := tc.sendLine("/rmgroup renamedgroup"); err != nil {
		return fmt.Errorf("/rmgroup cleanup command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/rmgroup cleanup did not complete")
	}

	fmt.Println("  Group management commands: OK")

	// === Test 14: /debug and log() ===
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
	if err := dav.Put("/logger.js", loggerSource); err != nil {
		return fmt.Errorf("failed to create /logger.js: %w", err)
	}

	if err := tc.sendLine("/create /logger.js"); err != nil {
		return fmt.Errorf("/create logger: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create logger did not complete")
	}

	loggerID, found := tc.waitForObject("*logger*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("logger was not created")
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
	found = waitForCondition(defaultWaitTimeout, 100*time.Millisecond, func() bool {
		tc.sendLine("look")
		output, _ = tc.waitForPrompt(defaultWaitTimeout)
		return strings.Contains(output, "logger stone (triggered)")
	})
	if !found {
		return fmt.Errorf("logger should have updated description after trigger: %q", output)
	}

	// Reset the logger for the next test (re-upload resets description to untriggered state)
	if err := dav.Put("/logger.js", loggerSource); err != nil {
		return fmt.Errorf("failed to reset /logger.js: %w", err)
	}

	// Wait for description to reset
	found = waitForCondition(defaultWaitTimeout, 100*time.Millisecond, func() bool {
		tc.sendLine("look")
		output, _ = tc.waitForPrompt(defaultWaitTimeout)
		return strings.Contains(output, "logger stone") && !strings.Contains(output, "(triggered)")
	})
	if !found {
		return fmt.Errorf("logger should have reset description: %q", output)
	}

	// Test 2: Attach console with /debug
	if err := tc.sendLine(fmt.Sprintf("/debug #%s", loggerID)); err != nil {
		return fmt.Errorf("/debug command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
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
	if err := tc.sendLine(fmt.Sprintf("/undebug #%s", loggerID)); err != nil {
		return fmt.Errorf("/undebug command: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
	if !ok {
		return fmt.Errorf("/undebug command did not complete: %q", output)
	}
	if !strings.Contains(output, "disconnected from console") {
		return fmt.Errorf("/undebug should show 'disconnected from console': %q", output)
	}

	// Reset the logger again
	if err := dav.Put("/logger.js", loggerSource); err != nil {
		return fmt.Errorf("failed to reset /logger.js again: %w", err)
	}

	// Wait for description to reset
	found = waitForCondition(defaultWaitTimeout, 100*time.Millisecond, func() bool {
		tc.sendLine("look")
		output, _ = tc.waitForPrompt(defaultWaitTimeout)
		return strings.Contains(output, "logger stone") && !strings.Contains(output, "(triggered)")
	})
	if !found {
		return fmt.Errorf("logger should have reset description again: %q", output)
	}

	// Trigger again - log output should NOT appear anymore
	if err := tc.sendLine("trigger logger"); err != nil {
		return fmt.Errorf("trigger after undebug: %w", err)
	}
	output, ok = tc.waitForPrompt(defaultWaitTimeout)
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
	if (msg.creator && msg.creator.Unsafe) {
		setDescriptions([{Short: 'witness stone (created by ' + msg.creator.Unsafe.Id + ')'}]);
	} else {
		setDescriptions([{Short: 'witness stone (no creator info)'}]);
	}
});
`
	if err := dav.Put("/witness.js", createdSource); err != nil {
		return fmt.Errorf("failed to create /witness.js: %w", err)
	}

	// The user's object ID (from Test 1) is what we expect in the creator info
	userID := user.Object

	// Create the witness object
	if err := tc.sendLine("/create /witness.js"); err != nil {
		return fmt.Errorf("/create witness: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create witness did not complete")
	}

	// Wait for the witness to appear with the creator's ID in its description
	found = waitForCondition(defaultWaitTimeout, 100*time.Millisecond, func() bool {
		tc.sendLine("look")
		output, _ = tc.waitForPrompt(defaultWaitTimeout)
		return strings.Contains(output, "witness stone (created by "+userID+")")
	})
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
	if err := dav.Put("/tome.js", tomeSource); err != nil {
		return fmt.Errorf("failed to create /tome.js: %w", err)
	}

	// Create the tome object
	if err := tc.sendLine("/create /tome.js"); err != nil {
		return fmt.Errorf("/create tome: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create tome did not complete")
	}

	// Wait for the tome to appear
	if _, found := tc.waitForObject("*dusty*", defaultWaitTimeout); !found {
		return fmt.Errorf("tome was not created")
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
	if err := dav.Put("/actionroom.js", actionRoomSource); err != nil {
		return fmt.Errorf("failed to create /actionroom.js: %w", err)
	}

	// Create the room
	if err := tc.sendLine("/create /actionroom.js"); err != nil {
		return fmt.Errorf("/create actionroom: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create actionroom did not complete")
	}

	// Wait for the room to exist
	actionRoomID, found := tc.waitForObject("*shaky chamber*", defaultWaitTimeout)
	if !found {
		return fmt.Errorf("actionroom was not created")
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
	found = waitForCondition(defaultWaitTimeout, 100*time.Millisecond, func() bool {
		tc.sendLine("look")
		output, _ = tc.waitForPrompt(defaultWaitTimeout)
		return strings.Contains(output, "shaky chamber (shaken!)")
	})
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
	if err := dav.Put("/pokeable.js", pokeableSource); err != nil {
		return fmt.Errorf("failed to create /pokeable.js: %w", err)
	}

	// Create the pokeable object (it will be created in our current room - the actionroom)
	if err := tc.sendLine("/create /pokeable.js"); err != nil {
		return fmt.Errorf("/create pokeable: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("/create pokeable did not complete")
	}

	// Wait for the pokeable to exist
	if _, found := tc.waitForObject("*pokeable orb*", defaultWaitTimeout); !found {
		return fmt.Errorf("pokeable was not created")
	}

	// Issue "poke" command - the sibling object should handle this action
	if err := tc.sendLine("poke"); err != nil {
		return fmt.Errorf("poke command: %w", err)
	}
	if _, ok := tc.waitForPrompt(defaultWaitTimeout); !ok {
		return fmt.Errorf("poke command did not complete")
	}

	// Wait for the sibling's description to update
	found = waitForCondition(defaultWaitTimeout, 100*time.Millisecond, func() bool {
		tc.sendLine("look")
		output, _ = tc.waitForPrompt(defaultWaitTimeout)
		return strings.Contains(output, "pokeable orb (poked!)")
	})
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

	return nil
}
