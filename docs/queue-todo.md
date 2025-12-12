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

### 6. Replace Close() Pattern Across Codebase
**Status:** Done - refactored to context-based lifecycle management

Components now follow these rules:
- Components with only goroutines (DigestAuth, QueueStats, loginRateLimiter) are controlled by context cancellation
- Components with resources (Storage) have Close() methods
- Components with goroutines that need waiting (LiveTypeHash, Game) have Close() or Wait() that blocks until goroutines exit
- Server has a blocking Start() that handles all lifecycle management internally

Changes:
- `game/connection.go` - loginRateLimiter now per-Game, context-controlled
- `game/queuestats.go` - QueueStats.Close() removed, context-controlled
- `digest/digest.go` - DigestAuth.Close() removed, context-controlled
- `game/game.go` - Game.Close() replaced with Wait()
- `server/server.go` - Server.Start() now blocking, manages its own context
