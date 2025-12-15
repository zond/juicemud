# Implementation Plan: Filesystem-Native Source Storage

## Overview

Replace the database-backed source storage with direct filesystem access. This enables:
- Standard tooling (any editor, IDE, agentic tools like Claude Code)
- Git version control and GitHub integration
- Simpler architecture (no WebDAV, no custom ACLs)
- Local development with personal server instances

## Critical Changes Beyond Source Storage

### 1. Password Hashing: MD5 → Argon2id

**Current:** MD5-based HA1 hash (was required by HTTP Digest Auth for WebDAV)
```go
// Weak - MD5(username:realm:password)
ha1 := digest.ComputeHA1(user.Name, juicemud.DAVAuthRealm, password)
```

**New:** Argon2id (Password Hashing Competition winner, OWASP recommended)
```go
import "golang.org/x/crypto/argon2"

// Parameters per RFC 9106 & OWASP:
// time=1, memory=64MB, threads=4, keyLen=32
func HashPassword(password string) (string, error) {
    salt := make([]byte, 16)
    rand.Read(salt)
    hash := argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
    // Encode as: $argon2id$v=19$m=65536,t=1,p=4$<base64-salt>$<base64-hash>
    return encodeArgon2Hash(salt, hash), nil
}

func VerifyPassword(password, encodedHash string) bool {
    salt, expectedHash, params := decodeArgon2Hash(encodedHash)
    hash := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, params.keyLen)
    return subtle.ConstantTimeCompare(hash, expectedHash) == 1
}
```

**Files to modify:**
- `game/connection.go` - Update password verification (line 538) and creation (line 623)
- `storage/storage.go` - Update `User.PasswordHash` field documentation
- Create new `auth/password.go` package for Argon2 functions
- Remove `digest/` package entirely
- Remove `juicemud.DAVAuthRealm` constant

### 2. Path Traversal Protection

The current `fs/pathify()` prevents `..` attacks. New filesystem code needs equivalent:
```go
func (s *Storage) safePath(path string) (string, error) {
    // Prevent null byte injection
    if strings.ContainsRune(path, 0) {
        return "", errors.New("invalid path: contains null byte")
    }

    cleanPath := filepath.Clean(path)
    fullPath := filepath.Join(s.sourcesDir, cleanPath)

    absSourcesDir, _ := filepath.Abs(s.sourcesDir)
    absFullPath, _ := filepath.Abs(fullPath)

    if !strings.HasPrefix(absFullPath, absSourcesDir+string(filepath.Separator)) &&
       absFullPath != absSourcesDir {
        return "", errors.New("path traversal detected")
    }
    return fullPath, nil
}
```

### 3. Wizard Commands

**Remove:**
- `/chread` - file permission management no longer exists
- `/chwrite` - file permission management no longer exists
- `/mkgroup` - group system no longer exists
- `/rmgroup` - group system no longer exists
- `/adduser` - group system no longer exists
- `/rmuser` - group system no longer exists
- `/editgroup` - group system no longer exists
- `/listgroups` - group system no longer exists
- `/members` - group system no longer exists
- `/groups` - group system no longer exists

**Add:**
- `/addwiz <username>` - grant wizard privileges (set Wizard=true)
- `/delwiz <username>` - revoke wizard privileges (set Wizard=false)

**Convert to filesystem:**
- `/ls` - reimplement to walk filesystem directories, remove permission columns

## Architecture Change

### Before (Current)
```
Sources in tkrzw hash → WebDAV interface → Wizards edit via HTTP
```

### After (Proposed)
```
Sources as .js files → Any editor/tool → Git for versioning
```

## Components to Modify

### 1. Storage Layer (`storage/storage.go`)

**Remove:**
- `sources *dbm.Hash` - source content storage
- `modTimes *dbm.Hash` - modification time storage
- `FileSync` table and related sync logic
- `StoreSource()` method (or repurpose for filesystem writes)
- `sync()`, `runSync()`, `logSync()` functions

**Modify:**
- `LoadSource(ctx, path)` → Read from filesystem instead of database
- `SourceModTime(ctx, path)` → Use `os.Stat().ModTime()` instead of database

**Add:**
- `sourcesDir string` field - computed as `filepath.Join(dir, "src")`
- No separate constructor parameter needed - derived from main dir

**Keep unchanged:**
- `sourceObjects *dbm.Tree` - still need to track which objects use which source
- `objects *dbm.LiveTypeHash` - object runtime state stays in database
- All object-related methods (CreateObject, AccessObject, etc.)

### 2. Server Configuration (`server/server.go`)

**Modify:**
- Remove WebDAV-related code entirely
- Sources dir is derived inside storage constructor as `filepath.Join(dir, "src")` - no config change needed

**Remove:**
- `dav.New()` call
- `fs.Fs` creation
- HTTP/HTTPS WebDAV handlers
- Digest authentication for WebDAV
- All WebDAV-related imports

### 3. Filesystem Package (`fs/`)

**Remove entirely** - no longer needed without WebDAV.

### 4. WebDAV Package (`dav/`)

**Remove entirely** - no longer needed.

### 5. Digest Auth Package (`digest/`)

**Remove entirely** - including `digest/tool/` subdirectory.

### 6. Crypto Package (`crypto/crypto.go`)

**Modify:**
- Remove `HTTPSCertPath string` field from `Crypto` struct
- Remove HTTPS certificate generation logic (no longer needed without HTTP server)

### 7. Password Hashing (`game/connection.go`)

**Add functions** for Argon2id password hashing:
- `hashPassword(password string) (string, error)` - create new hash
- `verifyPassword(password, hash string) (bool, error)` - verify password
- Use `golang.org/x/crypto/argon2` with IDKey variant
- Store as standard PHC string format: `$argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>`

### 8. File/Permission System (`storage/storage.go`)

**Remove File-related:**
- `File` struct and SQLite table
- `FileSync` struct and SQLite table
- `LoadFile()`, `EnsureFile()`, `DelFile()`, `CreateDir()`, `MoveFile()`
- `ChreadFile()`, `ChwriteFile()`, `FileGroups()`
- `GetHA1AndAuthContext()` method (implements digest.UserStore interface)
- `EachSource()` method (iterates tkrzw sources hash)
- All file permission checking logic

**Remove Group-related (entire group system):**
- `Group` struct and SQLite table
- `GroupMember` struct and SQLite table
- `Groups` type and its sort methods (`Len`, `Swap`, `Less`)
- `loadGroupByName()`, `loadGroupByID()`
- `EnsureGroup()`
- `UserGroups()` - returns groups a user is in
- `UserAccessToGroup()`, `UserAccessToGroupID()`, `userAccessToGroupIDTx()`
- `CheckCallerAccessToGroupID()`, `CallerAccessToGroupID()`
- `AddUserToGroup()`, `RemoveUserFromGroup()`
- `validateGroup()`, `detectCycle()`
- `CreateGroup()`, `DeleteGroup()`
- `editGroup()`, `logGroupEdit()`, `groupEditAudit` struct
- `EditGroupName()`, `EditGroupOwner()`, `EditGroupSupergroup()`
- `LoadGroup()`, `LoadGroupByID()`, `ListGroups()`, `GroupMembers()`

**Modify `New()` constructor:**
- Remove `sources` and `modTimes` hash initialization
- Update table initialization loop: `[]any{File{}, FileSync{}, Group{}, User{}, GroupMember{}}` → `[]any{User{}}`
- Add `sourcesDir` field computed as `filepath.Join(dir, "src")`

**Modify `Close()` method:**
- Remove `s.sources.Close()` call
- Remove `s.modTimes.Close()` call

**Add to User struct:**
```go
type User struct {
    Id           int64  `sqly:"pkey"`
    Name         string `sqly:"unique"`
    PasswordHash string
    Object       string `sqly:"unique"`
    Owner        bool   // Existing field
    Wizard       bool   // NEW: replaces "wizards" group membership
}
```

**Add methods:**
- `SetUserWizard(ctx, username string, wizard bool) error` - set wizard flag

**Rationale:** With file permissions removed, the ONLY use of groups is checking if a user is in the "wizards" group to enable wizard commands (`game/connection.go:445`). A `Wizard bool` field is simpler.

### 9. Audit System (`storage/audit.go`)

**Remove group/file audit types:**
- `AuditGroupCreate`, `AuditGroupDelete`, `AuditGroupEdit`
- `AuditMemberAdd`, `AuditMemberRemove`
- `AuditFileCreate`, `AuditFileUpdate`, `AuditFileDelete`, `AuditFileMove`, `AuditFileChmod`
- Group-related fields from any remaining audit types (`OldGroup`, `NewGroup`, etc.)

### 10. Documentation

**Delete:**
- `docs/group-system.md` - documents the removed group system

### 11. Test Files

**Delete entirely:**
- `storage/group_test.go` - all group-related tests
  - **Note:** First move `testStorage()` and `createTestUser()` helpers to `storage/storage_test.go` or a new `storage/test_helpers_test.go`

**Modify:**
- `storage/storage_test.go` - remove File operation tests (`TestLoadSource_FileWithoutContent`, `TestDelFile_FileWithoutContent`, `TestMoveFile_FileWithoutContent`)
- `storage/audit_test.go` - remove all group/file audit tests
- `integration_test/run_all.go` - remove entire "Test 13: Group management commands" section (lines ~1073-1497)
- `integration_test/run_all.go` - remove `/adduser testuser wizards` setup and related wizard group checks
- `integration_test/server.go` - expose `SourcesDir()` method for tests to write source files directly

### 12. Integration Tests (`integration_test/`)

**Modify:**
- Remove WebDAV test setup (DAV client creation)
- Change source file creation from `dav.Put()` to `os.WriteFile()`
- Update test helpers to write to filesystem instead of WebDAV
- Remove `webDAVClient` type
- Update wizard setup to use new `/addwiz` command instead of group membership

### 13. Main Binary (`bin/server/`)

**Modify:**
- Remove all HTTP/HTTPS-related flags (`--http-addr`, `--https-addr`, etc.)
- Remove HTTP server startup code
- Sources dir is automatically `<dir>/src` - no new flag needed

### 14. Game Initialization (`game/game.go`)

**Remove:**
- `wizardsGroup` constant
- `initialGroups` slice
- Loop that calls `s.EnsureGroup()` for initial groups

**Modify `New()` function:**
- Replace `s.CreateDir()` calls with `os.MkdirAll()` on sourcesDir subdirectories
- Replace `s.EnsureFile()` + `s.StoreSource()` with filesystem operations:
  - Check if file exists with `os.Stat()`
  - Write initial sources with `os.WriteFile()` if missing
- Remove dependency on `File` table for initialization

### 15. Wizard Commands (`game/wizcommands.go`)

**Remove:**
- `/chread` command handler
- `/chwrite` command handler
- `/groups` command handler
- `/mkgroup` command handler
- `/rmgroup` command handler
- `/editgroup` command handler
- `/adduser` command handler
- `/rmuser` command handler
- `/members` command handler
- `/listgroups` command handler

**Add:**
- `/addwiz <username>` - call `storage.SetUserWizard(ctx, username, true)`
- `/delwiz <username>` - call `storage.SetUserWizard(ctx, username, false)`

**Modify:**
- `/ls` command - reimplement to:
  - Use `os.ReadDir()` to list filesystem directories
  - Remove Read/Write group columns from output
  - Keep object count ("Used by N objects") using `sourceObjects` index
- `/create` command - replace `storage.FileExists()` with `os.Stat()` check on source path

### 16. Wizard Check (`game/connection.go`)

**Modify line 445:**
```go
// Before:
if has, err := c.game.storage.UserAccessToGroup(c.ctx, c.user, wizardsGroup); err != nil {
    return juicemud.WithStack(err)
} else if has {
    c.wiz = true
}

// After:
c.wiz = c.user.Wizard
```

### 17. Loader (`loader/loader.go`)

**Modify backup:**
- Remove `EachSource()` iteration - sources are now on filesystem (Git-versioned)
- Remove `Sources` field from backup `data` struct
- Backup only includes objects (tkrzw runtime state)

**Modify restore:**
- Remove `EnsureFile()` and `StoreSource()` calls
- Sources don't need restore - they're in Git
- Only restore objects

### 18. Misc Cleanups (`juicemud.go`)

**Modify:**
- Remove `DAVAuthRealm` constant
- Update `validNameRE` comment to remove "groups" reference (now just "users")

## Detailed Implementation

### Step 1: Modify Storage Constructor

```go
// storage/storage.go

type Storage struct {
    sourcesDir    string              // NEW: derived as filepath.Join(dir, "src")
    sql           *sqlx.DB
    sourceObjects *dbm.Tree           // Keep: path → object ID mapping
    objects       *dbm.LiveTypeHash   // Keep: object runtime state
    queue         *queue.Queue        // Keep: event queue
    // REMOVE: sources, modTimes
}

func New(ctx context.Context, dir string) (*Storage, error) {
    sourcesDir := filepath.Join(dir, "src")
    if err := os.MkdirAll(sourcesDir, 0755); err != nil {
        return nil, err
    }
    // ... existing setup ...
    s := &Storage{
        sourcesDir: sourcesDir,
        // ... other fields ...
    }
    // REMOVE: sources and modTimes hash initialization
    return s, nil
}
```

### Step 2: Implement Filesystem-Based Source Loading

```go
// storage/storage.go

func (s *Storage) LoadSource(ctx context.Context, path string) ([]byte, int64, error) {
    fullPath := filepath.Join(s.sourcesDir, path)

    info, err := os.Stat(fullPath)
    if err != nil {
        return nil, 0, juicemud.WithStack(err)
    }

    content, err := os.ReadFile(fullPath)
    if err != nil {
        return nil, 0, juicemud.WithStack(err)
    }

    return content, info.ModTime().UnixNano(), nil
}

func (s *Storage) SourceModTime(ctx context.Context, path string) (int64, error) {
    fullPath := filepath.Join(s.sourcesDir, path)

    info, err := os.Stat(fullPath)
    if err != nil {
        return 0, juicemud.WithStack(err)
    }

    return info.ModTime().UnixNano(), nil
}
```

### Step 3: Update Server Configuration

```go
// server/server.go

type Config struct {
    SSHAddr    string
    SourcesDir string  // NEW: path to source files directory
    Dir        string  // database and settings directory
    // REMOVE: HTTPSAddr, HTTPAddr, EnableHTTP, Hostname
}

func DefaultConfig() Config {
    return Config{
        SSHAddr:    "127.0.0.1:15000",
        SourcesDir: "./sources",  // Default to ./sources subdirectory
        Dir:        filepath.Join(os.Getenv("HOME"), ".juicemud"),
    }
}
```

### Step 4: Simplify Server Startup

```go
// server/server.go

func (s *Server) Start(ctx context.Context) error {
    sshLn, err := net.Listen("tcp", s.config.SSHAddr)
    if err != nil {
        return juicemud.WithStack(err)
    }
    defer sshLn.Close()

    return s.startWithListeners(ctx, sshLn)
}

func (s *Server) startWithListeners(ctx context.Context, sshLn net.Listener) error {
    ctx, cancel := context.WithCancel(ctx)
    defer cancel()

    // Initialize storage with sources directory
    store, err := storage.New(ctx, s.config.Dir, s.config.SourcesDir)
    if err != nil {
        return juicemud.WithStack(err)
    }
    defer store.Close()

    // Initialize game
    g, err := game.New(ctx, store)
    if err != nil {
        return juicemud.WithStack(err)
    }
    defer g.Wait()

    // REMOVED: All WebDAV/HTTP setup

    // Create SSH server
    sshServer := &ssh.Server{
        Addr:    s.config.SSHAddr,
        Handler: g.HandleSession,
    }
    sshServer.AddHostKey(s.signer)

    log.Printf("Serving SSH on %q", sshLn.Addr())

    errCh := make(chan error, 1)
    go func() {
        errCh <- sshServer.Serve(sshLn)
    }()

    // Wait for context cancellation or server error
    select {
    case err := <-errCh:
        return err
    case <-ctx.Done():
    }

    // Graceful shutdown
    shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer shutdownCancel()
    return sshServer.Shutdown(shutdownCtx)
}
```

### Step 5: Update Integration Tests

```go
// integration_test/run_all.go

// Instead of:
// dav.Put("/room.js", sourceCode)

// Use:
func writeSource(sourcesDir, path, content string) error {
    fullPath := filepath.Join(sourcesDir, path)
    if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
        return err
    }
    return os.WriteFile(fullPath, []byte(content), 0644)
}

// In tests:
writeSource(tc.sourcesDir, "/room.js", roomSource)
```

### Step 6: Remove Deleted Packages

Delete entire directories:
- `/Users/zond/projects/juicemud/fs/`
- `/Users/zond/projects/juicemud/dav/`
- `/Users/zond/projects/juicemud/digest/`

### Step 7: Clean Up Imports

Remove unused imports from all files that referenced:
- `github.com/zond/juicemud/fs`
- `github.com/zond/juicemud/dav`
- `github.com/zond/juicemud/digest`

## File Changes Summary

| File | Action |
|------|--------|
| `storage/storage.go` | Major modification - remove DB sources, add filesystem, remove File/Group/GroupMember tables, add Wizard field to User |
| `storage/audit.go` | Modify - remove group/file audit types |
| `storage/group_test.go` | **DELETE** (move helpers to storage_test.go first) |
| `storage/storage_test.go` | Modify - remove File tests, add helpers from group_test.go |
| `storage/audit_test.go` | Modify - remove group/file audit tests |
| `server/server.go` | Major modification - remove WebDAV/HTTP, simplify to SSH-only |
| `crypto/crypto.go` | Modify - remove HTTPSCertPath and certificate generation |
| `game/game.go` | Modify - remove wizardsGroup/initialGroups, change initialization to use filesystem |
| `game/connection.go` | Modify - add hashPassword/verifyPassword, use `c.user.Wizard` |
| `game/wizcommands.go` | Modify - remove 12 group/file commands, add /addwiz /delwiz, convert /ls, fix /create |
| `bin/server/main.go` | Minor modification - remove HTTP/HTTPS flags |
| `fs/` | **DELETE entire directory** |
| `dav/` | **DELETE entire directory** |
| `digest/` | **DELETE entire directory** (including tool/ subdir) |
| `docs/group-system.md` | **DELETE** - documents removed group system |
| `juicemud.go` | Remove `DAVAuthRealm`, update validNameRE comment |
| `loader/loader.go` | Modify - remove source backup/restore, keep only objects |
| `integration_test/run_all.go` | Modify - remove WebDAV, remove group tests, use filesystem |
| `integration_test/server.go` | Modify - expose SourcesDir() for tests |
| `integration_test/helpers.go` | Modify - remove DAV client, add writeSource helper |
| `README.md` | Update documentation - remove WebDAV references, add Git workflow |
| `go.mod` | Add `golang.org/x/crypto` dependency for argon2 |

## New Development Workflow

### Local Development
```bash
# Start game server to create data directory
juicemud --dir ~/.juicemud-dev

# Clone world repo into src/ subdirectory
cd ~/.juicemud-dev
git clone https://github.com/you/myworld.git src

# Edit sources with any tool
code src/lib/human.js
claude  # Claude Code works directly on filesystem

# Changes take effect immediately (on next object access)
```

### Production Deployment
```bash
# On production server, set up GitHub webhook or action:
# 1. On push to main: cd <dir>/src && git pull
# 2. Server automatically picks up changes (file modtimes changed)

# Or manually:
ssh prod-server
cd /opt/juicemud-data/src
git pull
# Server sees new modtimes, reloads sources on next object access
```

## Testing Strategy

1. **Unit tests**: Test new `LoadSource()` and `SourceModTime()` with temp directories
2. **Integration tests**: Modify existing tests to use filesystem instead of WebDAV
3. **Manual testing**: Verify edit → save → test cycle works smoothly

## Rollback Plan

If issues arise, the old code remains in Git history. The main risk is data - but since there are no deployed games, this isn't a concern.

## Future Considerations

1. **File watching**: Could add `fsnotify` to proactively reload objects when sources change
2. **Source validation**: Could add JS syntax checking on load
3. **Hot reload command**: Wizard command to force-reload all objects from a source path
