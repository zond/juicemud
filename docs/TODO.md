# TODO

Known issues and tasks to address.

## Code Quality: Import Resolver Cache Double-Check

**Location:** `js/imports/imports.go:81-122`

**Issue:** The cache lookup uses RLock, then does expensive resolution, then caches with Lock. Two goroutines could both miss the cache and do redundant work. Not a correctness issue, but wasted computation.

**Fix:** Add double-check inside the Lock:
```go
r.mu.Lock()
if existing, ok := r.cache[sourcePath]; ok {
    r.mu.Unlock()
    return &ResolveResult{Source: existing.source, ...}, nil
}
r.cache[sourcePath] = &cacheEntry{...}
r.mu.Unlock()
```

**Priority:** Low - only affects concurrent first-access to same source.

**Date identified:** 2025-12-25

## Code Quality: Large Function addObjectCallbacks

**Location:** `game/processing.go:556-909`

**Issue:** `addObjectCallbacks` is over 350 lines with deeply nested callback definitions. Hard to test and maintain.

**Fix:** Extract each callback into its own method:
```go
func (g *Game) makeSetTimeoutCallback(ctx context.Context, object *structs.Object) func(...) *v8go.Value
func (g *Game) makeEmitCallback(ctx context.Context, object *structs.Object) func(...) *v8go.Value
// etc.
```

**Priority:** Low - purely organizational improvement.

**Date identified:** 2025-12-25

## Code Quality: Split Integration Tests

**Location:** `integration_test/run_all.go` (3000+ lines)

**Issue:** All integration tests in one file makes it hard to find specific tests.

**Fix:** Split into sub-files organized by feature:
- `auth_test.go` - login, user creation
- `movement_test.go` - exits, rooms
- `js_api_test.go` - emit, setTimeout, setInterval, createObject
- `wizard_commands_test.go` - /create, /remove, /inspect
- etc.

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