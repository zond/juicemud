# Storage Subsystem Security and Correctness Review

**Date**: 2025-12-06
**Reviewer**: Claude Code
**Scope**: `/storage/` directory

## Overview

The storage subsystem is a hybrid storage layer using:
- **SQLite** for relational metadata (users, groups, files)
- **Tkrzw** key-value stores for object and source data
- **In-memory caching** (`LiveTypeHash`) for frequently accessed objects

---

## Security Findings

### 1. SQL Injection - LOW RISK

All SQL queries use parameterized queries (`?` placeholders):
- `storage.go:468`: `"SELECT * FROM FileSync ORDER BY Id ASC LIMIT 1"`
- `storage.go:648`: `"SELECT * FROM File WHERE Path = ?"`
- `storage.go:799`: `"SELECT * FROM \`Group\` WHERE Name = ?"`

**Assessment**: Properly protected against SQL injection.

### 2. Access Control - GOOD with one concern

The access control model is well-designed:
- `CallerAccessToGroupID` checks user membership in groups
- `CheckCallerAccessToGroupID` enforces permissions on file operations
- Owner users (`user.Owner == true`) bypass all checks
- Main context (`IsMainContext`) bypasses checks (for server initialization)

**Concern at `storage.go:521-539` (`ChreadFile`)**:

```go
func (s *Storage) ChreadFile(ctx context.Context, path string, reader string) error {
    // ...
    if err := s.CheckCallerAccessToGroupID(ctx, file.WriteGroup); err != nil {
        return juicemud.WithStack(err)
    }
    rg, err := s.loadGroupByName(ctx, tx, reader)
    // Missing check: does caller have access to the new reader group?
    file.ReadGroup = rg.Id
```

Unlike `ChwriteFile` which validates the caller has access to the new group (line 510-511), `ChreadFile` doesn't verify the caller can assign the `reader` group. This could allow privilege escalation - a user could set a file's ReadGroup to a group they don't belong to.

**Severity**: Medium

### 3. Path Traversal - LOW RISK

File paths are stored in a database table (`File`) with unique constraints. The code uses `filepath.Dir()` and `filepath.Base()` for path manipulation but doesn't appear to allow arbitrary filesystem access - all paths are validated through the database.

### 4. Race Conditions - Properly Handled

The code uses multiple locking strategies:
- `sync.RWMutex` on all DBM types (`dbm.go:20-21`)
- Object-level locks via `structs.WithLock` which sorts by ID to prevent deadlocks (`structs.go:793-823`)
- `LiveTypeHash` uses separate mutexes for stage and updates

The double-checked locking pattern in `LiveTypeHash.Get` is correctly implemented.

### 5. Data Integrity - GOOD

- FileSync provides crash recovery via a write-ahead log pattern (`storage.go:464-485`)
- Tkrzw is configured with `"restore_mode": "RESTORE_SYNC|RESTORE_NO_SHORTCUTS|RESTORE_WITH_HARDSYNC"` for durability
- Atomic operations via `Proc` method using tkrzw's `ProcessMulti`

### 6. Password Storage - ACCEPTABLE

`storage.go:842-844`: Passwords stored as `PasswordHash` - the actual hashing is done externally (likely in authentication layer).

### 7. Authentication Context - GOOD

`storage.go:854-877`: Authentication uses context values. The `AuthenticateUser` function properly handles both regular contexts and custom `settableContext` types.

---

## Correctness Findings

### 1. Iterator Lock Released Early (HIGH)

**Location**: `dbm.go:35-64` (`Hash.Each`) and `dbm.go:581-615` (`Tree.SubEach`)

```go
func (h *Hash) Each() iter.Seq2[BEntry, error] {
    h.mutex.RLock()
    defer h.mutex.RUnlock()
    return func(yield func(BEntry, error) bool) {
        // ... iteration happens here
    }
}
```

The read lock is acquired before returning the iterator function but released immediately due to `defer`. The actual iteration happens inside the returned function, which executes after the defer runs. This means **iteration happens without the lock held**, causing potential data races with concurrent writes.

**Fix**: Move the lock inside the iterator function:
```go
func (h *Hash) Each() iter.Seq2[BEntry, error] {
    return func(yield func(BEntry, error) bool) {
        h.mutex.RLock()
        defer h.mutex.RUnlock()
        // ... iteration
    }
}
```

### 2. Error Handling Bug in sync() (HIGH)

**Location**: `storage.go:480-481`

```go
if _, err := s.sql.ExecContext(ctx, "DELETE FROM FileSync WHERE Id = ?", oldestSync.Id); err != nil && errors.Is(err, os.ErrNotExist) {
    return juicemud.WithStack(err)
}
```

This condition is inverted. It should return an error when `err != nil && !errors.Is(err, os.ErrNotExist)`. Currently, it only returns an error if both `err != nil` AND the error is `os.ErrNotExist`, which is the opposite of the intended behavior. This could silently drop database errors.

**Fix**:
```go
if _, err := s.sql.ExecContext(ctx, "DELETE FROM FileSync WHERE Id = ?", oldestSync.Id); err != nil && !errors.Is(err, os.ErrNotExist) {
    return juicemud.WithStack(err)
}
```

### 3. CreateDir Missing Parent Field (HIGH)

**Location**: `storage.go:760-768`

```go
file := &File{
    Path:       path,
    Name:       filepath.Base(path),
    Dir:        true,
    ReadGroup:  parent.ReadGroup,
    WriteGroup: parent.WriteGroup,
}
```

The `Parent` field is not set (defaults to 0). This breaks the hierarchical file structure for non-root directories. Child lookups via `getChildren()` will fail to find directories created this way.

**Fix**:
```go
file := &File{
    Parent:     parent.Id,
    Path:       path,
    Name:       filepath.Base(path),
    Dir:        true,
    ReadGroup:  parent.ReadGroup,
    WriteGroup: parent.WriteGroup,
}
```

### 4. ChreadFile Missing Group Access Check (MEDIUM)

**Location**: `storage.go:534`

As noted in Security Finding #2, `ChreadFile` should verify the caller has access to the new reader group before assigning it.

**Fix**: Add access check after loading the group:
```go
rg, err := s.loadGroupByName(ctx, tx, reader)
if err != nil {
    return juicemud.WithStack(err)
}
if err := s.CheckCallerAccessToGroupID(ctx, rg.Id); err != nil {
    return juicemud.WithStack(err)
}
```

### 5. LiveTypeHash Unbounded Memory (MEDIUM)

**Location**: `dbm.go:103-112`

```go
type LiveTypeHash[T any, S structs.Snapshottable[T]] struct {
    stage        map[string]*T
    // ...
}
```

Objects are added to `stage` when loaded but never evicted. For long-running servers with many objects, this could cause memory issues.

**Recommendation**: Consider implementing an LRU eviction policy or periodic cleanup of objects that haven't been accessed recently.

### 6. Queue Close() Potential Deadlock (MEDIUM)

**Location**: `queue/queue.go:59-66`

```go
func (q *Queue) Close() error {
    q.cond.L.Lock()
    defer q.cond.L.Unlock()
    q.closed = true
    q.cond.Broadcast()
    q.cond.Wait()  // Waits while holding the lock
    return nil
}
```

After closing, it broadcasts to wake `Start()`, but then waits on the condition variable while still holding the lock. The synchronization between `Close()` and `Start()` is fragile and could deadlock under specific timing conditions.

**Recommendation**: Review the shutdown sequence to ensure `Start()` can always complete and broadcast back to `Close()`.

### 7. Hash.Each Yields Error with Potentially Invalid Data (LOW)

**Location**: `dbm.go:46-52`

```go
} else if !status.IsOK() {
    if !yield(BEntry{
        K: string(key),
        V: value,
    }, juicemud.WithStack(status)) {
```

When there's an error (`!status.IsOK()`), the code still tries to use `key` and `value`, which may be invalid or contain garbage data.

**Fix**: Yield an empty entry with the error:
```go
} else if !status.IsOK() {
    if !yield(BEntry{}, juicemud.WithStack(status)) {
```

---

## Summary of Issues

| Issue | Severity | Location | Status |
|-------|----------|----------|--------|
| Iterator lock released early | High | `dbm.go:35-64`, `dbm.go:581-615` | Open |
| Error condition inverted in sync() | High | `storage.go:480-481` | Open |
| CreateDir missing Parent field | High | `storage.go:760-768` | Open |
| ChreadFile missing group access check | Medium | `storage.go:534` | Open |
| LiveTypeHash unbounded memory | Medium | `dbm.go:107` | Open |
| Queue Close potential deadlock | Medium | `queue/queue.go:59-66` | Open |
| Hash.Each yields error with invalid data | Low | `dbm.go:46-52` | Open |

---

## Test Coverage Gaps

The existing tests cover basic functionality but don't test:
- Concurrent access patterns (race condition detection)
- Error recovery scenarios (crash during sync)
- Edge cases in access control (permission boundary testing)
- Memory behavior under load (for LiveTypeHash)

---

## Recommendations

1. **Immediate**: Fix the three HIGH severity bugs (iterator locking, sync error handling, CreateDir parent)
2. **Short-term**: Add the missing access check in ChreadFile
3. **Medium-term**: Implement memory management for LiveTypeHash
4. **Long-term**: Add concurrent access tests with race detector enabled
