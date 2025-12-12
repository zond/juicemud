# Queue TODO - Issues from Code Review

## Completed

### 1. Push() After Context Cancellation (Documentation Only)
**Status:** Done - documented in queue.go comments

### 2. Handler Drops Events on Context Cancellation
**Status:** Fixed - redesigned with worker pool pattern
- Handler sends to unbuffered channel (blocks until worker receives)
- Handler returns error if context cancelled before handoff
- Start() only deletes event if handler returns nil
- Events not handed off stay in B-tree for next startup

### 3. Remove Close() - Use Context-Only Shutdown
**Status:** Fixed - Queue.Close() removed
- Start() returns when context is cancelled
- Owner (Game) creates child context, cancels it to shut down
- Game waits for workers via WaitGroup after cancelling

### 4. Unused ctx Parameter in Push()
**Status:** Fixed - renamed to `_`

### 5. Missing Tests
**Status:** Fixed - added new tests
- TestQueueHandlerError - verifies events persist on handler error
- TestQueueConcurrentPush - verifies concurrent push safety

---

## Remaining

### 6. Replace Close() Pattern Across Codebase

**Status:** Needs investigation and implementation

**Issue:** Other background tasks in the codebase use the Close() + context dual pattern.

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
- `game/connection.go` - loginRateLimiter.Close()
- `game/queuestats.go` - QueueStats.Close()
- Any other background goroutines with Close() methods
