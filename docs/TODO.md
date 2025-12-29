# TODO

Known issues and tasks to address.

## Integration Tests

### High Priority

- [x] Extract repeated bidirectional room creation into helper function
  - Created `createBidirectionalRooms(t, ts, tc, baseName, nameA, nameB, exitAtoB, exitBtoA) (idA, idB)`
  - Refactored 4 tests to use it

- [x] Extract genesis entry pattern to helper function
  - Created `ensureInGenesis(t, tc)` method on terminalClient
  - Refactored 4 tests to use it

- [x] Fix missing error check at `tests_test.go:1805`
  - Added proper error check on `tc.sendLine("look")`

- [x] Add negative test cases:
  - Added `TestErrorCases` with subtests for:
    - `/create` with non-existent source path
    - `/inspect` with invalid object ID
    - JS source with syntax errors
    - `/move` with invalid object
    - `/remove` with invalid object
  - [x] Added `TestAuthenticationErrors` with subtests for:
    - Duplicate user creation
    - Invalid login password
    - Non-existent user login

### Medium Priority

- [~] Split long tests using `t.Run()` subtests:
  - Skipped: `TestAddDelWiz`, `TestDebugLog`, `TestChallengeSystem`, `TestSkillConfig` all have sequential dependencies that prevent running subtests independently
  - t.Run() adds boilerplate without benefit when subtests can't run in isolation

- [x] Add table-driven tests for `TestStatsCommand`
  - Converted 7 subcommand tests to table-driven format
  - Reduced ~100 lines to ~50 lines of structured code

- [x] Verify object removal succeeded in cleanup code (after `/remove`, verify object is actually gone)
  - Created `removeObject(t, objectID, verify bool)` helper function
  - Refactored 3 tests to use it with verification enabled

- [x] Simplify nested timeout loop at `tests_test.go:2533-2543`
  - Replaced manual polling loop with `sendCommand` helper

### Low Priority

- [x] Standardize source path naming conventions to snake_case
  - Renamed: `dimreceiver` → `dim_receiver`, `eaglereceiver` → `eagle_receiver`, etc.

- [ ] Add missing test coverage:
  - Server restart state persistence
  - Concurrent access patterns
  - Connection drop mid-command
  - Multiple SSH sessions from same user
  - `/ls` on directories (only file path currently tested)
  - JS API error returns

- [ ] Consider non-greedy regex at `server_test.go:364` (currently relies on single JSON object in output)

## Features

### Wizard Commands

- [ ] Add wizard command to view/update skills of objects
  - Should allow inspecting skill levels on any object
  - Should allow modifying skill levels for testing/debugging

- [ ] Add wizard command to send generic events
  - Should allow sending arbitrary event types to objects
  - Example: `/emit <objectID> message "Hello world"` to send a message event

### Event System

- [ ] Handle 'message' events in handleEmitEvent
  - When an object receives a 'message' event, print the message to connected player terminals
  - Useful for NPC dialogue, system messages, etc.

- [ ] Extend `emitToLocation` to emit to neighbourhood
  - Currently emits only to objects in the specified location
  - Should emit to everything in the neighbourhood (location + neighboring rooms)
  - Use exit transmit challenges to filter what propagates to neighboring rooms
  - Regular challenges parameter still filters individual recipients

- [ ] Add JS function to print to connection
  - Add `printToConnection(message)` JS function available on objects with active connections
  - Allows wizards to configure how sensory events are rendered
  - Example: In `user.js`: `addCallback('smell', ['emit'], (event) => printToConnection(event.description))`
  - Keeps sensory event definitions flexible - wizards decide what senses/skills exist
