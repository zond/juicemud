# Test Suite Review

This document summarizes findings from a comprehensive review of the test suite conducted December 2025.

## Overview

The codebase contains:
- **Integration tests**: 31 test sections in `integration_test/run_all.go`
- **Unit tests**: 9 test files across `js`, `game`, `storage`, and `structs` packages

## Integration Test Issues

### High Priority

#### Test 28: Invalid Object Reference
**Location**: `integration_test/run_all.go` Test 28

The test uses `#lookroom` as an object ID, but the codebase doesn't support `#lookroom` as a valid ID reference. The test should capture the room ID when it's created and use that instead.

```go
// Current (broken):
if err := ts.wizard.ensureSetLocation(ctx, "#look", "#lookroom"); err != nil {

// Should be:
roomID := ts.wizard.sendAndCapture(ctx, `/inspect genesis`, `"id": "(.*)"`)
if err := ts.wizard.ensureSetLocation(ctx, "#look", roomID); err != nil {
```

#### Documentation Claims Non-Existent Features
**Location**: `docs/integration-test-coverage.md` claims Test 13 tests group commands

The documentation claims that Test 13 tests `/mkgroup`, `/lsgroup`, `/rmgroup`, `/addgroup`, `/rmfromgroup`, and `/lsmember` commands. However, these commands do not exist in the codebase. The documentation should be corrected to reflect what Test 13 actually tests (wizard help commands).

### Medium Priority

#### Test 3: Unverified Output
**Location**: `integration_test/run_all.go` Test 3

The test runs `/ls` but doesn't verify the output contains expected content.

```go
// Current:
if err := ts.wizard.send(ctx, `/ls`); err != nil {
    return err
}
// Output is not verified

// Recommended: Add verification
output := ts.wizard.captureOutput(ctx, `/ls`)
if !strings.Contains(output, "expected_file.js") {
    return fmt.Errorf("expected file not in ls output")
}
```

#### Test 20: Fixed Sleep Instead of Polling
**Location**: `integration_test/run_all.go` Test 20

Uses `time.Sleep(300 * time.Millisecond)` instead of polling-based verification. Per project guidelines, tests should avoid fixed sleeps.

```go
// Current:
time.Sleep(300 * time.Millisecond)

// Recommended: Use polling pattern
if err := ts.waitForCondition(ctx, func() bool {
    // check condition
}); err != nil {
    return err
}
```

#### Test 11: Missing Error Message Verification
**Location**: `integration_test/run_all.go` Test 11

The test for `/rm` self-removal relies on checking `err != nil` but doesn't verify the specific error message "Can't remove yourself".

```go
// Current:
if err := ts.wizard.send(ctx, `/rm `+ts.wizard.id); err == nil {
    return fmt.Errorf("removing self should fail")
}

// Recommended: Verify error message
output, err := ts.wizard.sendAndCapture(ctx, `/rm `+ts.wizard.id)
if !strings.Contains(output, "Can't remove yourself") {
    return fmt.Errorf("expected 'Can't remove yourself' error")
}
```

#### Test 12: Unnecessary Go API Usage
**Location**: `integration_test/run_all.go` Test 12

Uses `ts.waitForSourceObject` for visible objects that could be verified through SSH commands. Per project guidelines, integration tests should prefer SSH/WebDAV interfaces.

```go
// Current:
obj, err := ts.waitForSourceObject(ctx, lookID, "look.js")

// Could use SSH instead:
output := ts.wizard.sendAndCapture(ctx, `/inspect `+lookID)
// Parse output to verify source path
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
**Quality**: Gap identified
- Contains only benchmarks (`BenchmarkGameProcess`, `BenchmarkV8JSON`)
- **No unit tests for game logic**
- This is a significant gap given the complexity of the game package

### storage/queue/queue_test.go
**Quality**: Good
- 4 tests covering ordering, error handling, concurrency
- Tests use proper patterns (subtests, table-driven tests)
- No issues identified

### game/queuestats_test.go
**Quality**: Excellent
- 25+ tests with thorough coverage
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

3. **Add unit tests for game package**: The `game/game_test.go` file only has benchmarks. Add unit tests for:
   - Command parsing and routing
   - Event handling logic
   - State transitions

4. **Replace fixed sleeps**: Convert `time.Sleep` calls to polling patterns in Test 20 and any other tests using fixed delays

5. **Add output verification**: Tests that run commands should verify the output matches expectations

6. **Reduce Go API usage**: Where practical, replace direct Go API calls with SSH command equivalents

### Long-term Improvements (Low Priority)

7. **Improve test documentation**: Add more detailed comments explaining what each test section verifies
8. **Add negative test cases**: More tests for error conditions and edge cases
9. **Consider test isolation**: Review if any other tests could benefit from isolated rooms (like Tests 26 and 30)

## Test Coverage Summary

| Package | Test Files | Tests | Benchmarks | Quality |
|---------|------------|-------|------------|---------|
| integration_test | 2 | 31 sections | 0 | Good (issues noted) |
| js | 1 | 3 | 2 | Good |
| game | 2 | 0 | 2 | **Gap: no unit tests** |
| storage | 3 | 12 | 3 | Good |
| storage/queue | 1 | 4 | 0 | Good |
| storage/dbm | 1 | 6 | 0 | Good |
| structs | 1 | 5 | 0 | Good |

## Revision History

- **December 2025**: Initial review completed
