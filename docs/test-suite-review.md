# Test Suite Review

This document summarizes findings from a comprehensive review of the test suite conducted December 2025.

**Status**: All high and medium priority issues have been fixed (December 2025).

## Overview

The codebase contains:
- **Integration tests**: 31 test sections in `integration_test/run_all.go`
- **Unit tests**: 9 test files across `js`, `game`, `storage`, and `structs` packages

## Integration Test Issues

### High Priority

#### Test 28: Invalid Object Reference - FIXED
~~The test used `#lookroom` as an object ID, but the codebase doesn't support `#lookroom` as a valid ID reference.~~

**Fix**: Changed to use the `lookRoomID` variable that was already captured earlier in the test file.

#### Documentation Claims Non-Existent Features - FIXED
~~The documentation claimed Test 13 tested group commands that don't exist.~~

**Fix**: Removed incorrect group command entries from `docs/integration-test-coverage.md` and fixed test numbers.

### Medium Priority

#### Test 3: Unverified Output - FIXED
~~The test ran `/ls` but didn't verify the output.~~

**Fix**: Added verification that output contains `box.js` which was created in the test.

#### Test 20: Fixed Sleep Instead of Polling - FIXED
~~Used `time.Sleep(300 * time.Millisecond)` instead of polling.~~

**Fix**: Removed the sleep since emit processing is synchronous - by the time the command completes, any emits have already been processed.

#### Test 11: Missing Error Message Verification - FIXED
~~The test didn't verify the "Can't remove yourself" error message.~~

**Fix**: Added verification of the error message. Also fixed to use `#<user-object-id>` instead of "self" since "self" isn't a special keyword.

#### Test 12: Unnecessary Go API Usage - FIXED
~~Used `ts.waitForSourceObject` for visible objects that could be verified through SSH commands.~~

**Fix**: Added `createObject` helper that parses object ID from `/create` command output and waits for the object to be inspectable via `/inspect #<id>`. Replaced all `/create` + `waitForObject` patterns throughout the test suite with this helper. Removed the `waitForSourceObject` Go API function entirely.

#### Race Mode Timing Issues - FIXED
~~Integration tests would fail intermittently with `-race` due to async notifications interfering with command output parsing.~~

**Fix**:
- Increased test timeout from 30s to 60s (race mode is ~3-4x slower)
- Added buffer drain in `createObject` helper before sending commands
- Replaced `sendLine("look") + waitForPrompt` with `waitForLookMatch` pattern to handle stale output from `/inspect` polling

**Note**: Running with `-race` requires disabling checkptr due to a bug in the tkrzw-go third-party library:
```
go test -race -gcflags=all=-d=checkptr=0 ./integration_test/...
```

### Low Priority

- Some tests could benefit from more descriptive error messages
- Test comments could be more detailed about what each section verifies
- Some pattern matching could be more specific

## Unit Test Assessment

### js/js_test.go
**Quality**: Good
- 3 tests + 2 benchmarks
- Tests cover basic functionality and edge cases
- No issues identified

### game/game_test.go
**Quality**: Good (previously a gap, now addressed)
- Contains 2 benchmarks (`BenchmarkLoadNeighbourhood`, `BenchmarkCall`)
- Helper functions for test object creation

### game/validation_test.go (NEW)
**Quality**: Good
- 5 tests covering username validation and password hashing
- Tests security-critical functions: `validateUsername`, `hashPassword`, `verifyPassword`
- Includes edge cases for malformed hashes and PHC format verification
- 2 benchmarks for password operations

### game/switchboard_test.go (NEW)
**Quality**: Good
- 9 tests covering debug console broadcast functionality
- Tests attach/detach, nil handling, concurrent access, auto-detach on failure
- Race detector verified

### game/ratelimiter_test.go (NEW)
**Quality**: Good
- 6 tests covering login rate limiting
- Tests record/clear, multiple users, concurrent access, context cancellation
- Race detector verified

### storage/queue/queue_test.go
**Quality**: Good
- 4 tests covering ordering, error handling, concurrency
- Tests use proper patterns (subtests, table-driven tests)
- No issues identified

### game/queuestats_test.go
**Quality**: Excellent
- 29 tests with thorough coverage
- 5 benchmarks for performance monitoring
- Covers edge cases, bounds, concurrent access
- Uses property-based testing patterns
- No issues identified

### storage/audit_test.go
**Quality**: Good
- 7 tests for audit logging functionality
- Tests concurrent access patterns
- No issues identified

### structs/structs_test.go
**Quality**: Good
- Tests description matching and skill mechanics
- Clear test structure
- No issues identified

### storage/dbm/dbm_test.go
**Quality**: Good
- Tests TypeHash, TypeTree, SubSets
- Covers creation, iteration, persistence
- No issues identified

### storage/storage_test.go
**Quality**: Good
- 1 test + 3 benchmarks
- Tests source validation and switching
- No issues identified

## Recommendations

### Immediate Actions (High Priority)

1. **Fix Test 28**: Replace `#lookroom` with captured room ID
2. **Update documentation**: Remove claims about non-existent group commands from `docs/integration-test-coverage.md`

### Short-term Improvements (Medium Priority)

3. **Add unit tests for game package**: ~~The `game/game_test.go` file only has benchmarks.~~ - PARTIALLY ADDRESSED
   - Added: Username validation, password hashing, switchboard, rate limiter tests
   - Remaining: Command parsing/routing, event handling, state transitions (complex, depend on storage/JS)

4. **Replace fixed sleeps**: Convert `time.Sleep` calls to polling patterns in Test 20 and any other tests using fixed delays

5. **Add output verification**: Tests that run commands should verify the output matches expectations

### Long-term Improvements (Low Priority)

6. **Improve test documentation**: Add more detailed comments explaining what each test section verifies
7. **Add negative test cases**: More tests for error conditions and edge cases
8. **Consider test isolation**: Review if any other tests could benefit from isolated rooms (like Tests 26 and 30)

## Test Coverage Summary

| Package | Test Files | Tests | Benchmarks | Quality |
|---------|------------|-------|------------|---------|
| integration_test | 2 | 31 sections | 0 | Good |
| js | 1 | 3 | 2 | Good |
| game | 5 | 49 | 9 | Good (improved) |
| storage | 3 | 12 | 3 | Good |
| storage/queue | 1 | 4 | 0 | Good |
| storage/dbm | 1 | 6 | 0 | Good |
| structs | 1 | 5 | 0 | Good |

## Revision History

- **December 2025**: Initial review completed
- **December 2025**: Fixed Test 12 - replaced Go API usage with SSH-based `createObject` helper
- **December 2025**: Added game package unit tests - validation, password, switchboard, rate limiter (20 new tests, 49 total)
- **December 2025**: Fixed race mode timing issues - increased timeout, added buffer drains, use waitForLookMatch pattern
