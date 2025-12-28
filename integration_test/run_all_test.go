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

	// NOTE: Test 3 (Wizard commands) has been extracted to TestWizardCommands
	// NOTE: Test 4 (Movement between rooms) has been extracted to TestEnterExitCommands
	// NOTE: Test 5 (Look command) has been extracted to TestLookCommand
	// NOTE: Test 6 (Bidirectional movement) has been extracted to TestBidirectionalMovement
	// NOTE: Test 7 (Scan command) has been extracted to TestScanCommand
	// NOTE: Test 8 (Challenge system) has been extracted to TestChallengeSystem

	// Create lookroom for bin/integration_test compatibility (used by interactive testing)
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
	lookRoomID, err := tc.createObject("/lookroom.js")
	if err != nil {
		return fmt.Errorf("create lookroom: %w", err)
	}

	// Update genesis to have an exit south to the lookroom (for interactive testing)
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

	fmt.Println("  Core tests: OK (extracted to individual TestXxx functions)")

	// NOTE: Test 9 (emit inter-object communication) has been extracted to TestEmitInterObject
	// NOTE: Test 10 (setTimeout) has been extracted to TestSetTimeout
	// NOTE: Test 11 (/remove command) has been extracted to TestRemoveCommand
	// NOTE: Test 12 (Movement events) has been extracted to TestMovementEvents
	// NOTE: Test 12b (Custom movement verb) has been extracted to TestCustomMovementVerb
	// NOTE: Test 12c (JS-based movement rendering) has been extracted to TestJSMovementRendering
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
	// NOTE: Test 28 (getLocation/moveObject) has been extracted to TestGetLocationAndMoveObject
	// NOTE: Test 29 (getContent) has been extracted to TestGetContent
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
