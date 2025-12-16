# TODO: Orphaned Object Prevention via Source Root Switching

## Problem

Objects store a `SourcePath` field that references a JavaScript source file. If the source file is deleted without updating the objects that reference it, those objects become "orphaned" - they will fail at runtime when the game tries to load their source.

With filesystem-native source storage, standard tools (Git, editors, etc.) can modify sources directly, making accidental deletion possible.

## Solution: RWMutex Protection + Source Root Validation

### Overview

The `sourceObjects` tkrzw tree maps source paths to object IDs. Currently, operations on this index can race with each other, leading to potential inconsistencies (e.g., TOCTOU bugs during cleanup).

The solution is to add an `RWMutex` to protect all `sourceObjects` operations, ensuring atomicity.

### Current Race Condition

In `EachSourceObject`, the cleanup logic has a TOCTOU race:
1. Thread A iterates sourceObjects, checks `Has(objId)` → false (object doesn't exist)
2. Thread B creates a new object with the same ID, adds it to sourceObjects
3. Thread A deletes the "stale" entry, which is now actually valid

### RWMutex Solution

Add a `sync.RWMutex` (`sourceObjectsMu`) to the Storage struct that protects all sourceObjects operations.

**Operations requiring write lock:**
- `CreateObject` - adds to sourceObjects
- `RemoveObject` - removes from sourceObjects
- `ChangeSource` - removes old, adds new mapping
- `UNSAFEEnsureObject` - adds to sourceObjects
- `EachSourceObject` - iterates and cleans up stale entries

**Why write lock for EachSourceObject:**
- Cleanup deletes stale entries during iteration
- Iteration and deletion are fast enough to hold the lock
- We don't iterate objects often (mainly for validation)
- tkrzw is fast, so this won't be a bottleneck

### Lock Hierarchy

The lock ordering MUST be followed to prevent deadlocks:
1. `sourceObjectsMu` (Storage-level, new)
2. Object locks (via `structs.WithLock`, sorted by ID)
3. `LiveTypeHash.stageMutex`
4. Tree.mutex / Hash.mutex (tkrzw internal)

All operations must acquire locks in this order. This is documented with a comment in storage.go.

### Implementation

**Storage struct change:**
```go
type Storage struct {
    queue           *queue.Queue
    sql             *sqly.DB
    sourcesDir      string
    sourceObjects   *dbm.Tree
    sourceObjectsMu sync.RWMutex  // Protects sourceObjects operations
    objects         *dbm.LiveTypeHash[structs.Object, *structs.Object]
    audit           *AuditLogger
}
```

**CreateObject:**

The lock must be acquired at the outermost level, before object locks:

```go
func (s *Storage) CreateObject(ctx context.Context, obj *structs.Object) error {
    if obj.PostUnlock != nil {
        return errors.Errorf("can't create object already known to storage: %+v", obj)
    }

    id := obj.GetId()
    locID := obj.GetLocation()

    loc, err := s.objects.Get(locID)
    if err != nil {
        return juicemud.WithStack(err)
    }

    s.sourceObjectsMu.Lock()
    defer s.sourceObjectsMu.Unlock()

    if err := s.sourceObjects.SubSet(obj.Unsafe.SourcePath, obj.Unsafe.Id, nil); err != nil {
        return juicemud.WithStack(err)
    }

    if err := structs.WithLock(func() error {
        if obj.Unsafe.Location != locID {
            return errors.Errorf("%q no longer located in %q", id, locID)
        }
        if _, found := loc.Unsafe.Content[id]; found {
            return errors.Errorf("%q already contains %q", locID, id)
        }
        return juicemud.WithStack(s.objects.Proc([]dbm.LProc[structs.Object, *structs.Object]{
            s.objects.LProc(id, func(_ string, _ *structs.Object) (*structs.Object, error) {
                return obj, nil
            }),
            s.objects.LProc(locID, func(_ string, loc *structs.Object) (*structs.Object, error) {
                loc.Unsafe.Content[id] = true
                return loc, nil
            }),
        }))
    }, obj, loc); err != nil {
        // Rollback: remove from sourceObjects on failure
        // This is safe even if another thread added the same entry because
        // object IDs are unique and an object can only be created once
        if delerr := s.sourceObjects.SubDel(obj.Unsafe.SourcePath, obj.Unsafe.Id); !errors.Is(delerr, os.ErrNotExist) && delerr != nil {
            return fmt.Errorf("trying to remove source mapping when handling %w: %w", err, delerr)
        }
        return juicemud.WithStack(err)
    }
    return nil
}
```

**RemoveObject:**

Critical: The lock must be acquired BEFORE removing from objects, not after. This ensures `EachSourceObject` sees a consistent state.

```go
func (s *Storage) RemoveObject(ctx context.Context, obj *structs.Object) error {
    if obj.PostUnlock == nil {
        return errors.Errorf("can't remove object not known to storage: %v", obj)
    }

    id := obj.GetId()
    locID := obj.GetLocation()
    sourcePath := obj.GetSourcePath()

    loc, err := s.objects.Get(locID)
    if err != nil {
        return juicemud.WithStack(err)
    }

    s.sourceObjectsMu.Lock()
    defer s.sourceObjectsMu.Unlock()

    if err := structs.WithLock(func() error {
        if obj.Unsafe.Location != locID {
            return errors.Errorf("%q no longer located in %q", id, locID)
        }
        if _, found := loc.Unsafe.Content[id]; !found {
            return errors.Errorf("%q doesn't contain %q", locID, id)
        }
        if len(obj.Unsafe.Content) > 0 {
            return errors.Errorf("%q isn't empty", id)
        }
        if err := s.objects.Proc([]dbm.LProc[structs.Object, *structs.Object]{
            s.objects.LProc(id, func(_ string, _ *structs.Object) (*structs.Object, error) {
                return nil, nil
            }),
            s.objects.LProc(locID, func(_ string, loc *structs.Object) (*structs.Object, error) {
                delete(loc.Unsafe.Content, id)
                return loc, nil
            }),
        }); err != nil {
            return juicemud.WithStack(err)
        }
        return nil
    }, obj, loc); err != nil {
        return juicemud.WithStack(err)
    }

    return juicemud.WithStack(s.sourceObjects.SubDel(sourcePath, id))
}
```

**ChangeSource:**

The current implementation doesn't hold object locks when modifying the object. This is a pre-existing issue. The caller is expected to have exclusive access to the object. Document this precondition.

```go
// ChangeSource updates the source path for an object.
// PRECONDITION: Caller must have exclusive access to the object (typically via holding its lock
// or being the only goroutine accessing it).
func (s *Storage) ChangeSource(ctx context.Context, obj *structs.Object, newSourcePath string) error {
    if obj.PostUnlock == nil {
        return errors.Errorf("can't set source of an object unknown to storage: %+v", obj)
    }

    oldSourcePath := obj.GetSourcePath()
    if oldSourcePath == "" {
        return errors.Errorf("can't change the source of an object that doesn't have a source: %+v", obj)
    }

    s.sourceObjectsMu.Lock()
    defer s.sourceObjectsMu.Unlock()

    if err := s.sourceObjects.SubSet(newSourcePath, obj.Unsafe.Id, nil); err != nil {
        return juicemud.WithStack(err)
    }

    obj.SetSourcePath(newSourcePath)
    obj.SetSourceModTime(0)

    if err := s.objects.Flush(); err != nil {
        return juicemud.WithStack(err)
    }

    return juicemud.WithStack(s.sourceObjects.SubDel(oldSourcePath, obj.Unsafe.Id))
}
```

**EachSourceObject:**

Fixed: Check yield return value in cleanup loop.

```go
func (s *Storage) EachSourceObject(ctx context.Context, path string) iter.Seq2[string, error] {
    return func(yield func(string, error) bool) {
        s.sourceObjectsMu.Lock()
        defer s.sourceObjectsMu.Unlock()

        var staleIDs []string
        for entry, err := range s.sourceObjects.SubEach(path) {
            if err != nil {
                if !yield("", juicemud.WithStack(err)) {
                    return
                }
                continue
            }
            if s.objects.Has(entry.K) {
                if !yield(entry.K, nil) {
                    return
                }
            } else {
                staleIDs = append(staleIDs, entry.K)
            }
        }
        // Clean up stale entries while still holding lock
        for _, id := range staleIDs {
            if err := s.sourceObjects.SubDel(path, id); err != nil {
                if !yield("", juicemud.WithStack(err)) {
                    return
                }
            }
        }
    }
}
```

**UNSAFEEnsureObject:**
```go
func (s *Storage) UNSAFEEnsureObject(ctx context.Context, obj *structs.Object) error {
    s.sourceObjectsMu.Lock()
    defer s.sourceObjectsMu.Unlock()

    if err := s.sourceObjects.SubSet(obj.GetSourcePath(), obj.GetId(), nil); err != nil {
        return juicemud.WithStack(err)
    }
    return juicemud.WithStack(s.objects.SetIfMissing(obj))
}
```

**Close:**

Document that callers must ensure no operations are in progress before calling Close. In practice, the Game shutdown path cancels contexts and waits for goroutines before closing storage.

### Source Root Switching Workflow

**Development:** Edit files directly in current source root → changes take effect immediately

**Production deployment:**
```bash
git clone/pull main /data/src-v2/
ssh game "/switchroot /data/src-v2"  # validates, then switches
```

Since production files aren't edited directly, accidental deletion isn't a concern.

### Validation Logic

Before switching roots (or on startup), the server:
1. Iterates all source paths via sourceObjects (protected by mutex)
2. Checks each source path exists in the target root
3. If any missing: reject with detailed error listing missing files and affected objects
4. If all exist: switch root atomically

### New Commands

- `/switchroot <path>` - validate and switch source root (wizard only)

### Startup Behavior

On startup:
1. Validate all distinct source paths have source files in configured root
2. If any sources missing: refuse to start with clear error listing missing files

### Benefits

- **No race conditions** - RWMutex ensures atomicity of sourceObjects operations
- **Simple solution** - no new dependencies or data stores
- **tkrzw stays fast** - object state operations (hot path) are unaffected
- **Minimal code changes** - just add mutex around existing operations
- **Easy validation** - iterate sourceObjects under lock for `/switchroot`

### What Stays in tkrzw

Everything stays in tkrzw:
- Object state (JSON blob from JavaScript)
- Location
- Content (child object IDs)
- Skills
- Descriptions
- Exits
- Callbacks
- Source path and mod time
- sourceObjects index (source path → object IDs)

### Trade-offs

- Write operations on sourceObjects are serialized, but this is acceptable because:
  - Object creation/deletion is rare compared to object state changes
  - tkrzw operations are fast (microseconds)
  - The mutex protects only sourceObjects, not the object state hash

### Notes on Orphan State

If `CreateObject` fails after adding to sourceObjects and the rollback also fails, there may be an orphan entry in sourceObjects. This is harmless because:
1. `EachSourceObject` checks `objects.Has()` and cleans up stale entries
2. The entry will be removed on the next iteration over that source path
