# Queue TODO - Issues from Code Review

## 1. Push() After Context Cancellation (Documentation Only)

**Status:** Document only - behavior is acceptable

**Issue:** Events pushed after context cancellation starts but before it completes may be persisted but not processed in the current session.

**Decision:** This is acceptable because:
- After context is cancelled, the queue is supposed to be shut down
- Persisted events will be processed on next startup
- This matches the existing behavior where future events persist

**Action:** Add comment documenting this behavior.

## 2. Handler Drops Events on Context Cancellation (Critical)

**Status:** Needs fix - redesign with worker pool

**File:** `game/game.go`, lines 176-191

**Problem:** If context is cancelled while a handler goroutine is waiting for a semaphore slot, the event is silently dropped. The event has already been deleted from the B-tree at this point (in `Start()`), so it is lost forever.

**Solution:** Redesign with worker pool pattern:
1. Handler sends to unbuffered channel (blocks until worker receives)
2. Pool of N workers read from channel and process events
3. Handler returns error if context cancelled before handoff
4. Start() only deletes event if handler returns nil
5. Events not handed off stay in B-tree for next startup

**Changes required:**
- Change `EventHandler` signature to return `error`
- Update `Start()` to check handler error, skip deletion on error
- Update `game.go` to use worker pool with unbuffered channel
- Workers wait via WaitGroup to ensure in-flight events complete

## 3. Remove Close() - Use Context-Only Shutdown

**Status:** Needs implementation

**Issue:** Queue has both `Close()` and context cancellation, which is redundant and harder to coordinate.

**Solution:** Remove `Close()` entirely, use context-only shutdown:
- `Start()` returns when context is cancelled
- Owner creates child context, cancels it to shut down
- Owner waits for workers via WaitGroup after Start() returns
- Remove `closed`, `started`, `done`, `mu` fields from Queue struct

**Benefits:**
- Single shutdown mechanism
- Automatic propagation (parent cancel → child cancel)
- Idiomatic Go pattern
- Simpler code

## 4. Unused ctx Parameter in Push()

**Status:** Minor cleanup

**File:** `storage/queue/queue.go`, line 98

**Problem:** The `ctx` parameter is never used.

**Fix:** Rename to `_` to signal it's unused but kept for API consistency.

## 5. Missing Tests

**Status:** Needs tests

Update tests after redesign:
- Concurrent Push() operations
- Push() during/after context cancellation
- Context cancellation scenarios
- Handler errors / handoff failures
- Worker pool draining on shutdown

## 6. Replace Close() Pattern Across Codebase

**Status:** Needs investigation and implementation

**Issue:** Other background tasks in the codebase may use the Close() + context dual pattern.

**Action:**
1. Search codebase for background tasks with Close() functions
2. Replace with context-only shutdown pattern where appropriate
3. Owner creates child context, passes to background task
4. To shut down: cancel context and wait for task to complete

**Pattern to follow:**
```go
// Owner creates child context
taskCtx, taskCancel := context.WithCancel(parentCtx)

// Start background task
var wg sync.WaitGroup
wg.Add(1)
go func() {
    defer wg.Done()
    backgroundTask(taskCtx)
}()

// To shut down:
taskCancel()
wg.Wait()  // Wait for task to complete
```

**Files to investigate:**
- `game/game.go` - Game.Close(), loginRateLimiter.Close(), queueStats.Close()
- `storage/queue/queue.go` - Queue.Close()
- Any other background goroutines with Close() methods
