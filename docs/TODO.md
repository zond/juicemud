# TODO

Known issues and tasks to address.

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