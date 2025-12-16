# TODO: Orphaned Object Prevention via Source Validation

## Problem

Objects store a `SourcePath` field that references a JavaScript source file. If the source file is deleted without updating the objects that reference it, those objects become "orphaned" - they will fail at runtime when the game tries to load their source.

With filesystem-native source storage, standard tools (Git, editors, etc.) can modify sources directly, making accidental deletion possible.

## Solution: RWMutex Protection + Source Validation

### Part 1: RWMutex Protection (DONE)

The `sourceObjects` tkrzw tree maps source paths to object IDs. A `sync.RWMutex` (`sourceObjectsMu`) protects all operations to prevent race conditions.

**Implementation complete in storage/storage.go:**
- Lock hierarchy documented in Storage struct comment
- CreateObject, RemoveObject, ChangeSource, EachSourceObject, UNSAFEEnsureObject all protected

### Part 2: Source Directory Management

#### Directory Layout

```
~/.juicemud/
  src/
    current -> v2/     # symlink to active version
    v1/                # source versions
    v2/
    v3/
```

#### Symlink Resolution

The server always resolves symlinks to find the actual source directory:
- Config specifies `src/current` (default) or explicit path
- On startup: resolve symlinks → validate → use real path
- This means `current` points TO the source root, it's not the root itself
- Benefit: failed switch doesn't corrupt state (symlink still points to old valid root)

#### Startup Validation

On startup:
1. Resolve symlinks in configured source path to get real directory
2. Iterate all objects, collect distinct source paths
3. Check each source path exists in the resolved directory
4. If any missing: refuse to start with clear error listing missing files and affected objects
5. If all exist: proceed with startup

#### Source Switching via Control Socket

**Why not an SSH command:**
- Anyone who can run the admin binary already has filesystem access
- Separate binary is cleaner for automation/deployment scripts
- Control socket provides simple, secure local-only communication

**Control socket:**
```
~/.juicemud/control.sock    # Unix domain socket
```

**Protocol:** Simple line-based text
```
# Client sends:
SWITCH_SOURCES <path>\n

# Server responds:
OK\n
# or
ERROR: <message>\n
```

**Switch flow:**
1. Admin updates symlink manually: `ln -sf v3 ~/.juicemud/src/current`
2. Admin runs: `juicemud-admin switch-sources` (defaults to "current") or `juicemud-admin switch-sources v3`
3. Server receives command via control socket
4. Server resolves symlinks in target path to get real directory
5. Server validates all object source paths exist in new root
6. If valid:
   - Switch internal `sourcesDir` atomically
   - Respond `OK`
7. If invalid:
   - Respond `ERROR: missing sources: path1 (N objects), path2 (M objects), ...`
   - Nothing changes

**Note:** The server never modifies the symlink - that's the admin's responsibility. The server only reads and resolves it.

**juicemud-admin binary:**
```bash
# Default: switch to whatever 'current' symlink points to
juicemud-admin switch-sources

# Explicit: switch to specific version
juicemud-admin switch-sources v3

# With custom socket path
juicemud-admin --socket /path/to/control.sock switch-sources v3
```

### Implementation Tasks

- [x] Add RWMutex protection to sourceObjects operations
- [x] Add `ValidateSources(ctx, rootDir)` method to Storage
- [x] Add symlink resolution in Storage.New() and source path handling
- [x] Add startup validation in Game.New()
- [x] Add control socket listener to Server
- [x] Add SWITCH_SOURCES command handler
- [x] Create `bin/admin/main.go` for juicemud-admin binary
- [ ] Update README with new deployment workflow

### Deployment Workflow

**Initial setup:**
```bash
mkdir -p ~/.juicemud/src
git clone repo ~/.juicemud/src/v1
ln -s v1 ~/.juicemud/src/current
juicemud-server  # starts with src/current -> v1
```

**Deploy new version:**
```bash
git clone repo ~/.juicemud/src/v2
ln -sf v2 ~/.juicemud/src/current      # update symlink
juicemud-admin switch-sources          # validates current, switches if valid
# Server now uses v2
```

**Rollback:**
```bash
ln -sf v1 ~/.juicemud/src/current      # update symlink
juicemud-admin switch-sources          # validates current, switches if valid
```

**Restart after switch:**
```bash
# Server reads src/current -> v2, resolves, validates, starts with v2
juicemud-server
```
