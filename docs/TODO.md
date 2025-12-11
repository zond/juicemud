# TODO - Code Improvements

## Critical

### 1. Race Condition in Fanout.Write
**File:** `game/fanout.go:26-46`
**Issue:** Modifying map while iterating, plus no synchronization for concurrent access (multiple connections can add/remove consoles while writes happen).
**Fix:** Converted Fanout to a struct with a mutex. Copy terminals before iterating, collect failed terminals for removal after iteration.
**Status:** Fixed

## High

### 3. Potential Deadlock in LiveTypeHash
**File:** `storage/dbm/dbm.go:122-143`
**Issue:** Lock ordering between `updatesMutex` and `stageMutex` may be inconsistent across methods.
**Analysis:** After thorough review, no deadlock is possible:
1. `Flush()` acquires `updatesMutex`, releases it, then acquires `stageMutex` - sequential, not nested.
2. All other methods use only one of the two mutexes.
3. The `updated()` callback (which acquires `updatesMutex`) is called from `PostUnlock` which is invoked from user code *after* the object's own mutex is released, completely outside any LiveTypeHash lock scope.
**Fix:** Added clarifying comments to the type definition, `Flush()`, and `updated()` methods.
**Status:** Won't fix - no deadlock risk, added documentation

### 4. Error Swallowing in Queue Event Handler
**File:** `game/game.go:152-157`
**Issue:** Failed events are logged but lost forever. No retry or dead-letter mechanism.
**Options to consider:**
- Retry with exponential backoff for transient errors
- Dead-letter queue for persistent failures
- At minimum, metrics/alerting on failed events
**Fix:** Implemented comprehensive in-memory queue statistics tracking:
- Created `QueueStats` in `game/queuestats.go` with per-object and global event/error tracking
- Error classification by category (js, storage, timeout, json, other) and location (file:line)
- Exponential Moving Average (EMA) rate calculations over second/minute/hour/day windows
- Circular buffer of 10k recent errors for debugging
- Added `/queuestats` wizard command with subcommands:
  - `summary`: global stats and rates
  - `categories`: errors by category
  - `locations [n]`: top error locations
  - `objects [sort] [n]`: top objects by errors/events/rate
  - `object <id>`: per-object details
  - `recent [n]`: recent error messages
  - `reset`: clear statistics
**Status:** Fixed

### 5. Unbounded V8 Memory
**File:** `js/js.go:47-61`
**Issue:** V8 memory is unconstrained during execution. Malicious script could exhaust server memory before timeout fires.
**Note:** v8go doesn't expose V8's ResourceConstraints API.
**Status:** Open - no easy fix available

## Medium

### 6. Missing Username Validation
**File:** `game/connection.go:1109-1130`
**Issue:** No validation on username format, length, or characters.
**Fix:** Added `juicemud.ValidateName()` function with shared regex for usernames and group names.
**Status:** Fixed

### 8. N+1 Query in UserGroups
**File:** `storage/storage.go:1047-1065`
**Issue:** Separate query per group membership.
**Fix:** Use JOIN query to load all groups in one query.
**Status:** Fixed

### 9. Inconsistent Fanout.Write Return Value
**File:** `game/fanout.go:26-46`
**Issue:** Returns max bytes written to any single terminal, which violates `io.Writer` contract (should return len(b) on success).
**Fix:** Now returns `len(b)` on success since all terminals receive the same data.
**Status:** Fixed

### 9b. Memory Leak in Fanout Cleanup
**File:** `game/connection.go:124-132`
**Issue:** Empty Fanout objects were not cleaned up from `consoleByObjectID` when the last terminal was removed.
**Fix:** Added `Len()` method to Fanout and cleanup logic in `delConsole()` to delete empty Fanout objects. Also added `onEmpty` callback to handle cleanup when Write() removes failed terminals.
**Status:** Fixed

### 10. Missing Context Cancellation in Queue
**File:** `storage/queue/queue.go:109-144`
**Issue:** Queue's Start loop doesn't check for context cancellation.
**Fix:** Added context cancellation checking in Start(). A goroutine broadcasts on ctx.Done() to wake the loop, and the loop checks ctx.Err() before waiting.
**Status:** Fixed

### 11. Lock Token Validation Pattern
**File:** `dav/dav.go:195-209`
**Issue:** Lock is looked up and mutex released before token validation. Pattern is: Lock -> read -> Unlock -> validate.
**Analysis:** This is actually fine because:
1. The lock data (including token) is read while holding mutex
2. Validation happens on the read copy
3. Even if lock is deleted between read and validation, we just reject the request (safe)
**Status:** Won't fix - pattern is safe, though could use defer for clarity

### 14. Missing nil Check on Exit.Name()
**File:** `structs/structs.go:81-86`
**Issue:** No nil check on receiver `e`. If `e` is nil, this will panic.
**Analysis:** This follows Go convention - methods on nil receivers are typically a programming error. The caller should ensure Exit is not nil. Adding nil check would hide bugs.
**Status:** Won't fix - follows Go convention

## Low

### 12. Typo in Error Message
**File:** `game/processing.go:307`
**Issue:** `setSkillConfigss` has extra 's'.
**Status:** Fixed

### 13. String Comparison for sql.ErrNoRows
**File:** `storage/storage.go:115-123`
**Issue:** Using string comparison `err.Error() == "sql: no rows in result set"` instead of `errors.Is(err, sql.ErrNoRows)`.
**Status:** Fixed - now uses `errors.Is(err, sql.ErrNoRows)`

### 16. Float32 Precision in Skill Calculation
**File:** `structs/structs.go:713-714`
**Issue:** float32 has limited precision, could accumulate errors with many skill uses.
**Analysis:** float32 has ~7 significant digits. Skill levels are typically small numbers (0-100 range). Would need millions of operations to see noticeable drift. Not a practical concern.
**Status:** Won't fix - not a practical issue

## Code TODOs in Source

### 17a. Container Events
**File:** `game/processing.go:407`
```
// TODO: Consider adding events for container objects when content changes:
// - "received": notify container when it gains content
// - "transmitted": notify container when it loses content
```
**Status:** Open - should be implemented

### 17b. Rename Function
**File:** `storage/storage.go:411`
**Issue:** Function needed renaming to AccessObject.
**Status:** Fixed - function already renamed, removed stale TODO comment

## New Findings (Code Review 2025-12)

### 18. Queue Redesign Needed
**Files:** `storage/queue/queue.go:153-159`, `game/game.go:155-162`
**Issues:**
1. Each sleep spawns a goroutine that isn't tracked or cancelled. High event churn creates thousands of sleeping goroutines.
2. Each queue event spawns a goroutine without limit or backpressure. Event storms could spawn millions of goroutines.
**Fix:** Redesign queue to use `time.AfterFunc` with cancellation for sleeps, and a worker pool with semaphore for event handling.
**Status:** Open

### 19. Login Attempt Map Unbounded
**File:** `game/connection.go:41-112`
**Issue:** `loginRateLimiter.attempts` map has no maximum size. An attacker could exhaust memory by attempting logins with unique usernames.
**Options:**
- Add a maximum size with LRU eviction
- Use a probabilistic data structure (bloom filter)
- Rate limit by IP address instead/additionally
**Status:** Open - needs discussion

### 20. WebDAV Lock Map Unbounded
**File:** `dav/dav.go:491-539`
**Issue:** Lock map has no cap. Rapid LOCK requests on unique paths could exhaust memory.
**Options:**
- Add a maximum lock count
- Rate limit LOCK requests per user
- Require authentication for LOCK (already required?)
**Status:** Open - needs discussion

### 21. Race Condition in Console Fanout onEmpty Callback
**File:** `game/connection.go:114-136` (old), now `game/switchboard.go`
**Issue:** The `onEmpty` callback in `NewFanout` runs outside the lock when `Write()` removes failed terminals, potentially causing concurrent modifications to `consoleByObjectID`.
**Fix:** Replaced complex Fanout + consoleByObjectID design with a simpler Switchboard struct. The Switchboard manages all console connections in a single map protected by a single RWMutex. Writers hold the lock while cleaning up failed terminals, eliminating the race.
**Status:** Fixed

### 22. SkillConfigs Race Condition
**File:** `game/processing.go:304-341`
**Issue:** `structs.SkillConfigs` is a global `SyncMap` that can be modified by any JavaScript code via `setSkillConfigs`. The get-modify-set pattern isn't atomic, so concurrent modifications could lose updates.
**Options:**
- Add a `CompareAndSwap` or merge operation
- Make skill configs per-object rather than global
- Document that skill configs should only be set during initialization
**Status:** Open - needs discussion

### 23. Missing Context Cancellation in Sync Loop
**File:** `storage/storage.go:498-517`
**Issue:** The `sync` function loops through FileSync entries without checking for context cancellation.
**Fix:** Add `select { case <-ctx.Done(): return ctx.Err() default: }` in the loop.
**Status:** Open

### 24. Audit Log Panic on Encode Failure
**File:** `storage/audit.go:225`
**Issue:** The audit logger panics if JSON encoding fails.
**Analysis:** This is intentional and documented in the code comment. Audit logging is critical for security compliance - if we can't audit, we shouldn't continue operating. The panic ensures the issue is noticed immediately rather than silently losing audit records. Encoding failures should be extremely rare (only possible with custom types that fail to marshal, which we don't use).
**Status:** Won't fix - intentional design for security compliance
