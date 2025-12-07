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

	// Create user
	tc, err := createUser(ts.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		return fmt.Errorf("createUser: %w", err)
	}
	if err := tc.sendLine("look"); err != nil {
		tc.Close()
		return fmt.Errorf("look command: %w", err)
	}
	// Verify "look" command produces output containing the genesis room description
	if output, ok := tc.waitFor("\n> ", 2*time.Second); !ok {
		tc.Close()
		return fmt.Errorf("look command did not complete: %q", output)
	} else if !strings.Contains(output, "Black cosmos") {
		tc.Close()
		return fmt.Errorf("look command did not show genesis room: %q", output)
	}
	tc.Close()

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

	// Test reconnection
	tc, err = loginUser(ts.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		return fmt.Errorf("loginUser: %w", err)
	}
	if err := tc.sendLine("look"); err != nil {
		tc.Close()
		return fmt.Errorf("look command on reconnect: %w", err)
	}
	if output, ok := tc.waitFor("\n> ", 2*time.Second); !ok {
		tc.Close()
		return fmt.Errorf("look command on reconnect did not complete: %q", output)
	} else if !strings.Contains(output, "Black cosmos") {
		tc.Close()
		return fmt.Errorf("look command on reconnect did not show genesis room: %q", output)
	}
	tc.Close()

	// Verify same object
	user2, err := ts.Storage().LoadUser(ctx, "testuser")
	if err != nil {
		return fmt.Errorf("user not found on reconnect: %w", err)
	}
	if user2.Object != user.Object {
		return fmt.Errorf("user object changed: %s -> %s", user.Object, user2.Object)
	}

	fmt.Println("  User creation and login: OK")

	// === Test 2: WebDAV file operations ===
	fmt.Println("Testing WebDAV file operations...")

	// Make testuser an owner for file system access
	user.Owner = true
	if err := ts.Storage().StoreUser(ctx, user, true); err != nil {
		return fmt.Errorf("failed to make testuser owner: %w", err)
	}

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

	// Make testuser a wizard
	if err := makeUserWizard(ts, "testuser"); err != nil {
		return err
	}

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

	// Login as wizard to test wizard commands
	tc, err = loginUser(ts.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		return fmt.Errorf("loginUser as wizard: %w", err)
	}

	// Create an object
	if err := tc.sendLine("/create /box.js"); err != nil {
		tc.Close()
		return fmt.Errorf("/create command: %w", err)
	}
	tc.drain()

	// Poll for object creation
	if _, found := ts.waitForSourceObject(ctx, "/box.js", 2*time.Second); !found {
		tc.Close()
		return fmt.Errorf("box object was not created")
	}

	// Test /inspect
	if err := tc.sendLine("/inspect"); err != nil {
		tc.Close()
		return fmt.Errorf("/inspect command: %w", err)
	}
	tc.drain()

	// Test /ls
	if err := tc.sendLine("/ls /"); err != nil {
		tc.Close()
		return fmt.Errorf("/ls command: %w", err)
	}
	tc.drain()

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
		tc.Close()
		return fmt.Errorf("failed to create /room1.js: %w", err)
	}
	if err := dav.Put("/room2.js", room2Source); err != nil {
		tc.Close()
		return fmt.Errorf("failed to create /room2.js: %w", err)
	}

	// Create rooms
	if err := tc.sendLine("/create /room1.js"); err != nil {
		tc.Close()
		return fmt.Errorf("/create room1: %w", err)
	}
	tc.drain()
	if err := tc.sendLine("/create /room2.js"); err != nil {
		tc.Close()
		return fmt.Errorf("/create room2: %w", err)
	}
	tc.drain()

	// Poll for room creation
	room1ID, found := ts.waitForSourceObject(ctx, "/room1.js", 2*time.Second)
	if !found {
		tc.Close()
		return fmt.Errorf("room1 was not created")
	}
	if _, found := ts.waitForSourceObject(ctx, "/room2.js", 2*time.Second); !found {
		tc.Close()
		return fmt.Errorf("room2 was not created")
	}

	// Move into room1 using /enter
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", room1ID)); err != nil {
		tc.Close()
		return fmt.Errorf("/enter command: %w", err)
	}
	tc.drain()

	// Poll for user to be in room1
	if !ts.waitForObjectLocation(ctx, user.Object, room1ID, 2*time.Second) {
		tc.Close()
		return fmt.Errorf("user did not move to room1")
	}

	// Exit back out
	if err := tc.sendLine("/exit"); err != nil {
		tc.Close()
		return fmt.Errorf("/exit command: %w", err)
	}
	tc.drain()

	// Poll for user to be back in genesis
	if !ts.waitForObjectLocation(ctx, user.Object, "genesis", 2*time.Second) {
		tc.Close()
		return fmt.Errorf("user did not return to genesis")
	}

	tc.Close()

	fmt.Println("  Movement: OK")

	// === Test 5: Look command ===
	fmt.Println("Testing look command...")

	// First verify look works with genesis (which already has proper descriptions)
	tc, err = loginUser(ts.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		return fmt.Errorf("loginUser for look test: %w", err)
	}

	// Look at genesis - should show "Black cosmos" and the long description
	if err := tc.sendLine("look"); err != nil {
		tc.Close()
		return fmt.Errorf("look in genesis: %w", err)
	}

	output, ok := tc.waitFor("\n> ", 2*time.Second)
	if !ok {
		tc.Close()
		return fmt.Errorf("look in genesis did not complete: %q", output)
	}
	if !strings.Contains(output, "Black cosmos") {
		tc.Close()
		return fmt.Errorf("look did not show genesis room name: %q", output)
	}

	// Verify genesis long description is shown
	if !strings.Contains(output, "darkness") {
		tc.Close()
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
		tc.Close()
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
		tc.Close()
		return fmt.Errorf("failed to create /book.js: %w", err)
	}

	if err := tc.sendLine("/create /lookroom.js"); err != nil {
		tc.Close()
		return fmt.Errorf("/create lookroom: %w", err)
	}
	tc.drain()

	lookRoomID, found := ts.waitForSourceObject(ctx, "/lookroom.js", 2*time.Second)
	if !found {
		tc.Close()
		return fmt.Errorf("lookroom was not created")
	}

	if err := tc.sendLine("/create /book.js"); err != nil {
		tc.Close()
		return fmt.Errorf("/create book: %w", err)
	}
	tc.drain()

	bookID, found := ts.waitForSourceObject(ctx, "/book.js", 2*time.Second)
	if !found {
		tc.Close()
		return fmt.Errorf("book was not created")
	}

	// Move the book into the look room
	if err := tc.sendLine(fmt.Sprintf("/move #%s #%s", bookID, lookRoomID)); err != nil {
		tc.Close()
		return fmt.Errorf("/move book to lookroom: %w", err)
	}
	tc.drain()

	// Enter the room using wizard command
	if err := tc.sendLine(fmt.Sprintf("/enter #%s", lookRoomID)); err != nil {
		tc.Close()
		return fmt.Errorf("/enter lookroom: %w", err)
	}
	tc.drain()

	// Verify user is in the room
	if !ts.waitForObjectLocation(ctx, user.Object, lookRoomID, 2*time.Second) {
		tc.Close()
		return fmt.Errorf("user did not move to lookroom")
	}

	// Now test the look command
	if err := tc.sendLine("look"); err != nil {
		tc.Close()
		return fmt.Errorf("look command: %w", err)
	}

	// Verify look shows the room description
	output, ok = tc.waitFor("\n> ", 2*time.Second)
	if !ok {
		tc.Close()
		return fmt.Errorf("look in lookroom did not complete: %q", output)
	}
	if !strings.Contains(output, "Cozy Library") {
		tc.Close()
		return fmt.Errorf("look did not show room name: %q", output)
	}

	// Verify look shows the long description
	if !strings.Contains(output, "ancient tomes") {
		tc.Close()
		return fmt.Errorf("look did not show long description: %q", output)
	}

	// Verify look shows the book in the room
	if !strings.Contains(output, "dusty book") {
		tc.Close()
		return fmt.Errorf("look did not show book in room: %q", output)
	}

	// Verify look shows the exit
	if !strings.Contains(output, "north") {
		tc.Close()
		return fmt.Errorf("look did not show exit 'north': %q", output)
	}

	// Test non-wizard movement: use the north exit to go back to genesis
	if err := tc.sendLine("north"); err != nil {
		tc.Close()
		return fmt.Errorf("north command: %w", err)
	}
	tc.drain()

	// Verify player moved back to genesis
	if !ts.waitForObjectLocation(ctx, user.Object, "genesis", 2*time.Second) {
		tc.Close()
		return fmt.Errorf("user did not move to genesis via 'north' command")
	}

	tc.Close()

	fmt.Println("  Look command: OK")

	// === Test 6: Bidirectional non-wizard movement ===
	fmt.Println("Testing bidirectional movement...")

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

	// Login and test bidirectional movement
	tc, err = loginUser(ts.SSHAddr(), "testuser", "testpass123")
	if err != nil {
		return fmt.Errorf("loginUser for bidirectional test: %w", err)
	}

	// Verify we're in genesis
	if err := tc.sendLine("look"); err != nil {
		tc.Close()
		return fmt.Errorf("look in genesis: %w", err)
	}
	output, ok = tc.waitFor("\n> ", 2*time.Second)
	if !ok {
		tc.Close()
		return fmt.Errorf("look in genesis did not complete: %q", output)
	}
	if !strings.Contains(output, "Black cosmos") {
		tc.Close()
		return fmt.Errorf("not in genesis: %q", output)
	}

	// Move south to lookroom using non-wizard movement
	if err := tc.sendLine("south"); err != nil {
		tc.Close()
		return fmt.Errorf("south command: %w", err)
	}
	tc.drain()

	// Verify player moved to lookroom
	if !ts.waitForObjectLocation(ctx, user.Object, lookRoomID, 2*time.Second) {
		tc.Close()
		return fmt.Errorf("user did not move to lookroom via 'south' command")
	}

	// Look to verify we're in lookroom
	if err := tc.sendLine("look"); err != nil {
		tc.Close()
		return fmt.Errorf("look in lookroom: %w", err)
	}
	output, ok = tc.waitFor("\n> ", 2*time.Second)
	if !ok {
		tc.Close()
		return fmt.Errorf("look in lookroom did not complete: %q", output)
	}
	if !strings.Contains(output, "Cozy Library") {
		tc.Close()
		return fmt.Errorf("not in Cozy Library after south: %q", output)
	}

	// Move north back to genesis
	if err := tc.sendLine("north"); err != nil {
		tc.Close()
		return fmt.Errorf("north command back to genesis: %w", err)
	}
	tc.drain()

	// Verify player moved back to genesis
	if !ts.waitForObjectLocation(ctx, user.Object, "genesis", 2*time.Second) {
		tc.Close()
		return fmt.Errorf("user did not move back to genesis via 'north' command")
	}

	tc.Close()

	fmt.Println("  Bidirectional movement: OK")

	return nil
}
