# TODO: Orphaned Object Prevention via Source Root Switching

## Problem

Objects store a `SourcePath` field that references a JavaScript source file. If the source file is deleted without updating the objects that reference it, those objects become "orphaned" - they will fail at runtime when the game tries to load their source.

With filesystem-native source storage, standard tools (Git, editors, etc.) can modify sources directly, making accidental deletion possible.

## Solution: Source Root Switching with Validation

Instead of trying to prevent or detect orphans at runtime, we validate source files exist **before** switching to a new source root.

### Workflow

**Development:** Edit files directly in current source root → changes take effect immediately

**Production deployment:**
```bash
git clone/pull main /data/src-v2/
ssh game "/switchroot /data/src-v2"  # validates, then switches
```

Since production files aren't edited directly, accidental deletion isn't a concern.

### Validation Logic

Before switching roots (or on startup), the server:
1. Queries all objects and their source paths
2. Checks each source path exists in the target root
3. If any missing: reject with detailed error listing missing files and affected objects
4. If all exist: switch root atomically

### Architecture Change: SQLite for Object Metadata

**Current (problematic):**
- tkrzw hash: full object data (ID → state, source path, location, etc.)
- tkrzw tree: sourceObjects index (source path → object IDs)
- Must keep both in sync → race conditions (TOCTOU bugs in cleanup)

**Proposed (simple, correct):**
- SQLite `objects` table: `id`, `source_path` (indexed on both) - source of truth for object existence
- tkrzw hash: just runtime state (lazy-loaded, periodically flushed)

**Why this works:**

1. **SQLite is ACID** - object create/delete is transactional, no race conditions
2. **Built-in indexing** - `SELECT id FROM objects WHERE source_path = ?` just works
3. **tkrzw stays fast** - still handles the hot path (state load/save during execution)
4. **Orphan state is harmless** - if tkrzw has stale state for a deleted object, it's just wasted space

**Validation query:**
```sql
SELECT DISTINCT source_path FROM objects
```
Then check each path exists in new root. Simple, correct, no races.

### Object Lifecycle

**Create:**
1. Validate source file exists in current root (reject if missing)
2. `INSERT INTO objects (id, source_path)` - atomic
3. Write empty state to tkrzw (ensures we overwrite any orphaned state from previous object with same ID)

**Delete:**
1. `DELETE FROM objects WHERE id = ?` - atomic
2. Try to delete state from tkrzw (if fails, orphan state is harmless)

**Change Source:**
1. Validate new source file exists in current root (reject if missing)
2. `UPDATE objects SET source_path = ? WHERE id = ?` - atomic

**Run:**
1. Load state from tkrzw (create empty if missing)
2. Execute JavaScript
3. Flush state to tkrzw periodically

**Bootstrap (UNSAFEEnsureObject):**
1. `INSERT OR IGNORE INTO objects (id, source_path) VALUES (?, ?)`
2. `SetIfMissing` on tkrzw state

### What Stays in tkrzw

Runtime state that changes frequently during execution:
- Object state (JSON blob from JavaScript)
- Location
- Content (child object IDs)
- Skills
- Descriptions
- Exits
- Callbacks
- Source mod time (for reload detection)

### Schema

```sql
-- Add to existing SQLite database
CREATE TABLE objects (
    id TEXT PRIMARY KEY,
    source_path TEXT NOT NULL
);
CREATE INDEX idx_objects_source_path ON objects(source_path);
```

### New Commands

- `/switchroot <path>` - validate and switch source root (wizard only)

### Startup Behavior

On startup:
1. Validate all objects (from SQLite) have source files in configured root
2. If any sources missing: refuse to start with clear error listing missing files

Note: Orphan tkrzw state (state for objects not in SQLite) is harmless - just wasted space. An optional `/vacuum` command could clean these up, but it's not critical.

### Implementation

**Object struct** (modeled after User pattern):
```go
type Object struct {
    Id         string `sqly:"pkey"`
    SourcePath string `sqly:"index"`
}
```

Table created via `CreateTableIfNotExists` like User table.

**Migration:**
1. On first startup with new code, iterate existing tkrzw objects and populate SQLite
2. Remove `sourceObjects` tree (no longer needed)

The migration is one-time and automatic - existing objects get registered in SQLite on first access or via a startup scan.

## Benefits

- **No race conditions** - SQLite handles consistency
- **Simple validation** - single SQL query for all source paths
- **Atomic deploys** - `/switchroot` is all-or-nothing
- **Easy rollback** - switch back to previous root
- **tkrzw does what it's best at** - fast state persistence for the hot path
