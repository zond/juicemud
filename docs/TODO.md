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

- [ ] Add negative test cases:
  - `/create` with non-existent source path
  - `/inspect` with invalid object ID
  - JS source with syntax errors
  - Duplicate user creation
  - Invalid login credentials

### Medium Priority

- [ ] Split long tests using `t.Run()` subtests:
  - `TestAddDelWiz` (~150 lines)
  - `TestDebugLog` (~125 lines)
  - `TestChallengeSystem` (~125 lines)
  - `TestSkillConfig` (~115 lines)

- [ ] Add table-driven tests for `TestStatsCommand`

- [ ] Verify object removal succeeded in cleanup code (after `/remove`, verify object is actually gone)

- [x] Simplify nested timeout loop at `tests_test.go:2533-2543`
  - Replaced manual polling loop with `sendCommand` helper

### Low Priority

- [ ] Standardize source path naming conventions (currently mixed: `skill_config_test`, `dimreceiver`, `entertest_room`)

- [ ] Add missing test coverage:
  - Server restart state persistence
  - Concurrent access patterns
  - Connection drop mid-command
  - Multiple SSH sessions from same user
  - `/ls` on directories (only file path currently tested)
  - JS API error returns

- [ ] Consider non-greedy regex at `server_test.go:364` (currently relies on single JSON object in output)
