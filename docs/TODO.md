# TODO

Known issues and tasks to address.

## Code Quality: Split Integration Tests

**Location:** `integration_test/run_all.go` (2400+ lines)

**Issue:** All integration tests in one file makes it hard to find specific tests.

**Progress:** Infrastructure is in place (TestMain handles setup, package variables for shared state, `uniqueSourcePath` helper for test isolation). The following tests have been extracted from `RunAll` into individual `TestXxx` functions in `integration_test_test.go`:
- `TestSetTimeout` - setTimeout() delayed events
- `TestStatsCommand` - /stats wizard command
- `TestStatePersistence` - Object state persistence
- `TestCreatedEvent` - 'created' event with creator info
- `TestLookTarget` - look command with target
- `TestRemoveCommand` - /remove wizard command
- `TestEmitInterObject` - emit() inter-object communication
- `TestCircularContainerPrevention` - circular container prevention
- `TestExitAtUniverseRoot` - /exit at genesis edge case
- `TestRemoveCurrentLocation` - /remove current location edge case
- `TestJavaScriptImports` - @import directive for JS modules
- `TestExitFailedEvent` - exitFailed event on challenge failure
- `TestSetInterval` - setInterval()/clearInterval() periodic events
- `TestIntervalsCommand` - /intervals wizard command
- `TestCreateRemoveObject` - createObject()/removeObject() JS APIs
- `TestRemoveCallback` - removeCallback() JS API
- `TestGetSetSourcePath` - getSourcePath()/setSourcePath() JS APIs
- `TestGetSetLearning` - getLearning()/setLearning() JS APIs
- `TestSkillConfig` - getSkillConfig()/casSkillConfig() JS APIs

**Remaining work:** Continue extracting test sections one at a time into individual `TestXxx` functions. ~13 test sections remain in `RunAll`.

**Priority:** Low - purely organizational improvement.

**Date identified:** 2025-12-25

## Code Quality: Inconsistent Error Wrapping

**Location:** Multiple files

**Issue:** Some places use `juicemud.WithStack(err)`, others use `errors.Wrapf`, some return raw errors.

**Recommendation:** Standardize on:
- `juicemud.WithStack(err)` for stack traces at error origin
- `errors.Wrap(err, "context")` for adding context when propagating
- Never return unwrapped errors from public functions

**Priority:** Low - doesn't affect functionality.

**Date identified:** 2025-12-25

## Code Quality: Loader Creates Full Game Instance

**Location:** `loader/loader.go:43-46`

**Issue:** The loader creates a full `game.New()` instance just to do backup/restore, which starts event workers and runs boot.js unnecessarily.

**Fix options:**
1. Add a "storage-only" mode to Game initialization
2. Don't require Game initialization for loader operations
3. Make backup/restore work directly on storage without Game

**Priority:** Low - only affects the loader tool startup time.

**Date identified:** 2025-12-25