# Security and Correctness Review: js/ Package

**Date:** 2025-12-06
**Reviewer:** Claude Code
**Package:** `github.com/zond/juicemud/js`

## Overview

The `js/` package provides a V8 JavaScript runtime for executing game object scripts in JuiceMUD. It pools V8 isolates, handles state persistence, and bridges Go callbacks to JavaScript.

## Critical Issues

### 1. V8 Context Reuse Without Cleanup

**Location:** `js/js.go:271-273`

```go
m := <-machines
defer func() { machines <- m }()
```

The same V8 context (`m.vctx`) is reused across different script executions without cleanup. This causes:

- **Global state pollution**: Variables/functions defined by one script persist for subsequent scripts
- **Information disclosure**: Script A can read globals left by Script B
- **Callback pollution**: Old callbacks remain registered unless explicitly overwritten

**Example attack scenario:**
```javascript
// Object A's script runs first
globalThis.secret = "password123";

// Object B's script runs later on same machine
log(globalThis.secret); // Leaks "password123"
```

**Recommendation:** Either create a fresh context per execution, or explicitly clear the global object between runs. The v8go library supports `ctx.Close()` and creating new contexts from an existing isolate.

### 2. Race Condition in Timeout Mechanism

**Location:** `js/js.go:250-267`

```go
func (rc *RunContext) withTimeout(_ context.Context, f func() (*v8go.Value, error), timeoutNanos *int64) (*v8go.Value, error) {
    results := make(chan result, 1)
    thisTimeout := atomic.LoadInt64(timeoutNanos)
    go func() {
        t := time.Now()
        val, err := f()
        atomic.AddInt64(timeoutNanos, -int64(time.Since(t)))
        results <- result{value: val, err: err}
    }()
    // ...
}
```

After `TerminateExecution()` is called on timeout, the goroutine may still send to the channel. The buffered channel prevents a goroutine leak, but:

- The V8 value (`val`) returned after termination could be in an undefined state
- The goroutine continues to reference `timeoutNanos` after the function returns

**Recommendation:** Consider adding synchronization to ensure clean shutdown, or document that post-termination values are undefined.

### 3. Missing Context Cancellation Handling

**Location:** `js/js.go:250`

```go
func (rc *RunContext) withTimeout(_ context.Context, f func() (*v8go.Value, error), ...)
```

The `context.Context` parameter is unused (named `_`). Script execution cannot be cancelled via context cancellation - only via timeout. If the parent context is cancelled (e.g., server shutdown, HTTP request cancelled), scripts continue running until timeout.

**Recommendation:** Add a select case for `ctx.Done()` alongside the timeout case:

```go
select {
case res := <-results:
    return res.value, juicemud.WithStack(res.err)
case <-ctx.Done():
    rc.m.iso.TerminateExecution()
    return nil, juicemud.WithStack(ctx.Err())
case <-time.After(time.Duration(thisTimeout)):
    rc.m.iso.TerminateExecution()
    return nil, juicemud.WithStack(ErrTimeout)
}
```

## Medium Issues

### 4. No Memory Limits on V8 Isolates

**Location:** `js/js.go:44-56`

V8 isolates are created without memory limits:

```go
func newMachine() (*machine, error) {
    m := &machine{
        iso: v8go.NewIsolate(),
    }
```

A malicious script could allocate large amounts of memory:
```javascript
let a = [];
while(true) a.push(new Array(1000000));
```

While timeout would eventually kill it, memory exhaustion could occur first, potentially crashing the server.

**Recommendation:** Use `v8go.NewIsolate(v8go.WithMaxHeapSize(bytes))` to limit memory per isolate.

### 5. State Can Grow Unbounded

Scripts can store arbitrary data in `state`, which is persisted to the database. There's no size limit on state, allowing denial of service via storage exhaustion.

**Recommendation:** Add a maximum state size check in `collectResult()`:

```go
if len(rc.r.State) > maxStateSize {
    return nil, fmt.Errorf("state exceeds maximum size of %d bytes", maxStateSize)
}
```

### 6. Test Bug: Incorrect Assertion Message

**Location:** `js/js_test.go:111-113`

```go
if result != "20" {
    t.Errorf("got %q, want 45", result)  // Comment says "want 45" but compares to "20"
}
```

The test checks that `result != "20"` but the error message says "want 45". The test logic appears correct (verifying callback with wrong tag isn't invoked), but the error message is misleading.

## Minor Issues

### 7. Unused Error Check in newMachine

**Location:** `js/js.go:49-51`

```go
if m.vctx = v8go.NewContext(m.iso); err != nil {
    return nil, juicemud.WithStack(err)
}
```

`err` is always `nil` here because `v8go.NewContext` doesn't return an error, and `err` was declared but not assigned in this statement. This is a no-op check.

### 8. logFunc Creates New Logger Per Call

**Location:** `js/js.go:170`

```go
log.New(w, "", 0).Println(anyArgs...)
```

A new `log.Logger` is created for every `log()` call from JavaScript. This is inefficient but functionally correct.

## Security Strengths

1. **V8 Sandboxing**: Scripts run in isolated V8 contexts with no file system or network access by default
2. **JSON Serialization Boundary**: All data crossing Go/JS boundary goes through JSON, preventing prototype pollution attacks
3. **Timeout Enforcement**: Scripts cannot run indefinitely (200ms default in game/processing.go)
4. **Explicit Callback Whitelisting**: Only explicitly registered Go functions are callable from JavaScript
5. **Proper Error Propagation**: Errors are wrapped with stack traces for debugging

## Test Coverage Assessment

### Covered

- Basic script execution
- State persistence between calls
- Callback registration with tags
- Message passing to callbacks
- V8 array manipulation

### Missing Coverage

- Timeout behavior (script exceeding timeout)
- Error handling paths (malformed scripts, callback errors)
- Context reuse contamination between different objects
- Memory exhaustion scenarios
- Malformed JSON state handling
- Concurrent execution safety (multiple goroutines using the pool)

## Recommendations Summary

| Priority | Issue | Recommendation | Status |
|----------|-------|----------------|--------|
| High | Context reuse without cleanup | Create fresh context per execution or clear globals | Accepted risk (semi-trusted admins) |
| High | Unused context cancellation | Respect Go context.Context for cancellation | **Fixed** |
| Medium | No V8 memory limits | Add `WithMaxHeapSize` to isolate creation | Deferred (v8go 0.9.0 doesn't support it) |
| Medium | Unbounded state size | Add maximum state size validation | **Fixed** (1MB limit) |
| Low | Test error message | Fix misleading error message in TestBasics | **Fixed** |
| Low | Missing test coverage | Add timeout and error handling tests | Pending |
| Low | Unused error check in newMachine | Remove no-op error check | **Fixed** |

## Files Reviewed

- `js/js.go` - Main implementation (360 lines)
- `js/js_test.go` - Tests and benchmarks (201 lines)
- `game/processing.go` - Usage context (lines 1-470)
