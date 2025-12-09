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
		if output, ok := tc.waitForPrompt(2*time.Second); !ok {
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
		if output, ok := tc.waitForPrompt(2*time.Second); !ok {
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

	// Make testuser an owner and wizard for subsequent tests
	user.Owner = true
	if err := ts.Storage().StoreUser(ctx, user, true); err != nil {
		return fmt.Errorf("failed to make testuser owner: %w", err)
	}
	if err := makeUserWizard(ts, "testuser"); err != nil {
		return err
	}

	// Reconnect as wizard - wizard status is checked at login time
	tc, err := loginUser(ts.SSHAddr(), "testuser", "testpass123")
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
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/create command did not complete")
	}

	// Poll for object creation
	if _, found := ts.waitForSourceObject(ctx, "/box.js", 2*time.Second); !found {
		return fmt.Errorf("box object was not created")
	}

	// Test /inspect
	if err := tc.sendLine("/inspect"); err != nil {
		return fmt.Errorf("/inspect command: %w", err)
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/inspect command did not complete")
	}

	// Test /ls
	if err := tc.sendLine("/ls /"); err != nil {
		return fmt.Errorf("/ls command: %w", err)
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
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
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/create room1 did not complete")
	}
	if err := tc.sendLine("/create /room2.js"); err != nil {
		return fmt.Errorf("/create room2: %w", err)
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/create room2 did not complete")
	}

	// Poll for room creation
	room1ID, found := ts.waitForSourceObject(ctx, "/room1.js", 2*time.Second)
	if !found {
		return fmt.Errorf("room1 was not created")
	}
	if _, found := ts.waitForSourceObject(ctx, "/room2.js", 2*time.Second); !found {
		return fmt.Errorf("room2 was not created")
	}

	// Move into room1 using /enter
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", room1ID)); err != nil {
		return fmt.Errorf("/enter command: %w", err)
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/enter command did not complete")
	}

	// Poll for user to be in room1
	if !ts.waitForObjectLocation(ctx, user.Object, room1ID, 2*time.Second) {
		return fmt.Errorf("user did not move to room1")
	}

	// Exit back out
	if err := tc.sendLine("/exit"); err != nil {
		return fmt.Errorf("/exit command: %w", err)
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/exit command did not complete")
	}

	// Poll for user to be back in genesis
	if !ts.waitForObjectLocation(ctx, user.Object, "genesis", 2*time.Second) {
		return fmt.Errorf("user did not return to genesis")
	}

	fmt.Println("  Movement: OK")

	// === Test 5: Look command ===
	fmt.Println("Testing look command...")

	// Look at genesis - should show "Black cosmos" and the long description
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look in genesis: %w", err)
	}

	output, ok := tc.waitForPrompt(2*time.Second)
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
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/create lookroom did not complete")
	}

	lookRoomID, found := ts.waitForSourceObject(ctx, "/lookroom.js", 2*time.Second)
	if !found {
		return fmt.Errorf("lookroom was not created")
	}

	if err := tc.sendLine("/create /book.js"); err != nil {
		return fmt.Errorf("/create book: %w", err)
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/create book did not complete")
	}

	bookID, found := ts.waitForSourceObject(ctx, "/book.js", 2*time.Second)
	if !found {
		return fmt.Errorf("book was not created")
	}

	// Move the book into the look room
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", bookID, lookRoomID)); err != nil {
		return fmt.Errorf("/move book to lookroom: %w", err)
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/move book to lookroom did not complete")
	}

	// Enter the room using wizard command
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", lookRoomID)); err != nil {
		return fmt.Errorf("/enter lookroom: %w", err)
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/enter lookroom did not complete")
	}

	// Verify user is in the room
	if !ts.waitForObjectLocation(ctx, user.Object, lookRoomID, 2*time.Second) {
		return fmt.Errorf("user did not move to lookroom")
	}

	// Now test the look command
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look command: %w", err)
	}

	// Verify look shows the room description
	output, ok = tc.waitForPrompt(2*time.Second)
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
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("north command did not complete")
	}

	// Verify player moved back to genesis
	if !ts.waitForObjectLocation(ctx, user.Object, "genesis", 2*time.Second) {
		return fmt.Errorf("user did not move to genesis via 'north' command")
	}

	fmt.Println("  Look command: OK")

	// === Test 6: Bidirectional non-wizard movement ===
	fmt.Println("Testing bidirectional movement...")

	// Debug: verify look works before updating genesis.js
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look before genesis update: %w", err)
	}
	output, ok = tc.waitForPrompt(2*time.Second)
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
	output, ok = tc.waitForPrompt(2*time.Second)
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
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("south command did not complete")
	}

	// Verify player moved to lookroom
	if !ts.waitForObjectLocation(ctx, user.Object, lookRoomID, 2*time.Second) {
		return fmt.Errorf("user did not move to lookroom via 'south' command")
	}

	// Look to verify we're in lookroom
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look in lookroom: %w", err)
	}
	output, ok = tc.waitForPrompt(2*time.Second)
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
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("north command back to genesis did not complete")
	}

	// Verify player moved back to genesis
	if !ts.waitForObjectLocation(ctx, user.Object, "genesis", 2*time.Second) {
		return fmt.Errorf("user did not move back to genesis via 'north' command")
	}

	fmt.Println("  Bidirectional movement: OK")

	// === Test 7: Scan command ===
	fmt.Println("Testing scan command...")

	// From genesis, scan should show genesis and the lookroom (via south exit)
	if err := tc.sendLine("scan"); err != nil {
		return fmt.Errorf("scan command: %w", err)
	}

	output, ok = tc.waitForPrompt(2*time.Second)
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
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/create challenge_room did not complete")
	}

	challengeRoomID, found := ts.waitForSourceObject(ctx, "/challenge_room.js", 2*time.Second)
	if !found {
		return fmt.Errorf("challenge_room was not created")
	}

	// Create the hidden gem
	if err := tc.sendLine("/create /hidden_gem.js"); err != nil {
		return fmt.Errorf("/create hidden_gem: %w", err)
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/create hidden_gem did not complete")
	}

	hiddenGemID, found := ts.waitForSourceObject(ctx, "/hidden_gem.js", 2*time.Second)
	if !found {
		return fmt.Errorf("hidden_gem was not created")
	}

	// Move the gem into the challenge room
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", hiddenGemID, challengeRoomID)); err != nil {
		return fmt.Errorf("/move gem to challenge_room: %w", err)
	}
	// Wait for prompt to confirm command was processed
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/move command did not complete")
	}

	// Wait for gem to be moved before entering room
	if !ts.waitForObjectLocation(ctx, hiddenGemID, challengeRoomID, 3*time.Second) {
		// Debug: check where the gem actually is
		gemObj, _ := ts.Storage().AccessObject(ctx, hiddenGemID, nil)
		if gemObj != nil {
			return fmt.Errorf("hidden gem did not move to challenge_room, it is in %q", gemObj.GetLocation())
		}
		return fmt.Errorf("hidden gem did not move to challenge_room (gem not found)")
	}

	// Enter the challenge room
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", challengeRoomID)); err != nil {
		return fmt.Errorf("/enter challenge_room: %w", err)
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/enter challenge_room did not complete")
	}

	// Verify user is in the challenge room
	if !ts.waitForObjectLocation(ctx, user.Object, challengeRoomID, 3*time.Second) {
		return fmt.Errorf("user did not move to challenge_room")
	}

	// Test 1: Look should NOT show the hidden gem (user has no perception skill)
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look in challenge_room: %w", err)
	}

	output, ok = tc.waitForPrompt(2*time.Second)
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
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("locked exit command did not complete")
	}

	// Verify user is still in challenge room (movement failed)
	obj, err = ts.Storage().AccessObject(ctx, user.Object, nil)
	if err != nil {
		return fmt.Errorf("failed to access user object: %w", err)
	}
	if obj.GetLocation() != challengeRoomID {
		return fmt.Errorf("user should still be in challenge_room after failed exit, but is in %q", obj.GetLocation())
	}

	// Test 4: Use the easy exit (should succeed - no challenge)
	if err := tc.sendLine("easy"); err != nil {
		return fmt.Errorf("easy exit command: %w", err)
	}
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("easy exit command did not complete")
	}

	// Verify user moved to genesis
	if !ts.waitForObjectLocation(ctx, user.Object, "genesis", 2*time.Second) {
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
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("train command did not complete")
	}

	// Enter the challenge room again
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", challengeRoomID)); err != nil {
		return fmt.Errorf("/enter challenge_room with skills: %w", err)
	}
	// Wait for prompt to confirm command was processed
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("/enter challenge_room with skills did not complete")
	}

	if !ts.waitForObjectLocation(ctx, user.Object, challengeRoomID, 2*time.Second) {
		return fmt.Errorf("user did not move to challenge_room for skilled test")
	}

	// Test 5a: Look should now show the hidden gem (user has perception skill)
	if err := tc.sendLine("look"); err != nil {
		return fmt.Errorf("look in challenge_room with skills: %w", err)
	}

	output, ok = tc.waitForPrompt(2*time.Second)
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
	if _, ok := tc.waitForPrompt(2*time.Second); !ok {
		return fmt.Errorf("locked exit command with skills did not complete")
	}

	// Verify user moved to genesis via the locked exit
	if !ts.waitForObjectLocation(ctx, user.Object, "genesis", 2*time.Second) {
		return fmt.Errorf("user should have moved to genesis via 'locked' exit with strength skill")
	}

	fmt.Println("  Challenge system: OK")

	return nil
}
