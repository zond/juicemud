# TODO: LiveTypeHash Flush Error Handling

## Problem

The `LiveTypeHash` in `storage/dbm/dbm.go` has a background flush loop that writes dirty
objects to disk every second. Currently, errors are logged but could accumulate silently
without operators or wizards being aware of data durability issues.

```go
// Current implementation (storage/dbm/dbm.go:180-195)
func (l *LiveTypeHash[T, S]) runFlushLoop(ctx context.Context) {
    // ...
    if err := l.Flush(); err != nil {
        log.Printf("LiveTypeHash flush error: %v", err)  // Silent accumulation
    }
}
```

## Suggested Solution

### 1. Exponential Backoff
On flush errors, increase the retry interval to avoid hammering a failing disk:
- Normal: 1 second
- After error: 2s → 4s → 8s → ... → max 30s
- Reset to 1s on successful flush

### 2. Track Flush State
Add fields to `LiveTypeHash` to track:
- `flushBroken bool` - whether flushing is currently failing
- `lastFlushAt time.Time` - when the last successful flush occurred
- `lastFlushErr error` - the most recent error
- `lastFlushErrAt time.Time` - when the error occurred

### 3. Expose to Game Layer
Add a method like `FlushHealth()` that returns the current flush state, accessible
from the game layer for monitoring.

### 4. Wizard Visibility
- **Login message**: When a wizard logs in, if flushing is broken, show a warning:
  ```
  WARNING: Database flush failing since 2024-01-15 10:30:00
  Error: disk full
  ```
- **Prompt indicator**: Add an indicator to wizard prompts when flush is broken,
  similar to how some systems show `[!]` or `[FLUSH ERR]`

### 5. Implementation Notes

The flush state should be accessible via:
1. `Storage.FlushHealth()` method
2. Passed to `Connection` during session setup
3. Checked periodically or on prompt render for wizards

Consider adding a `/flushstatus` wizard command to show detailed flush health.

## Files to Modify

- `storage/dbm/dbm.go` - Add state tracking and exponential backoff
- `storage/storage.go` - Expose flush health method
- `game/connection.go` - Show warning on wizard login
- `game/wizcommands.go` - Optional: add `/flushstatus` command

## Priority

Medium - Data durability issues should be visible, but the current logging
provides some visibility and the final `Close()` will surface persistent errors.
