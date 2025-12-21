# JuiceMUD

A MUD (Multi-User Dungeon) game server written in Go, featuring JavaScript-based object scripting and SSH-based player connections.

## Features

- SSH-based player connections using [gliderlabs/ssh](https://github.com/gliderlabs/ssh)
- JavaScript object scripting using V8 via [rogchap.com/v8go](https://rogchap.com/v8go)
- Filesystem-native source management (edit sources directly in `<dir>/src/`)
- Persistent storage using [tkrzw](https://github.com/estraier/tkrzw-go) (hash/tree databases) and SQLite
- Sophisticated skill system with forgetting mechanics and challenge-based access control
- Argon2id password hashing for secure credential storage

## Build and Run

```bash
# Build the server
go build -o juicemud ./bin/server

# Build the admin CLI
go build -o juicemud-admin ./bin/admin

# Run the server (default port: SSH 15000)
./juicemud

# Run all tests
go test ./...

# Run a single test
go test -v ./structs -run TestLevel

# Run tests in a specific package
go test -v ./storage/dbm

# Generate code from schema (after modifying structs/schema.benc)
go generate ./structs
```

## Architecture

```
                                    ┌─────────────────┐
                                    │   bin/server    │
                                    │    main.go      │
                                    └────────┬────────┘
                                             │
                                    ┌────────▼────────┐
                                    │     server      │
                                    │   Server.New()  │
                                    └────────┬────────┘
                                             │
                                    ┌────────▼────────┐
                                    │   SSH Server    │
                                    │   (port 15000)  │
                                    └────────┬────────┘
                                             │
                                    ┌────────▼────────┐
                                    │      game       │
                                    │ HandleSession() │
                                    └────────┬────────┘
                                             │
                                    ┌────────▼────────┐
                                    │   Connection    │
                                    │  (player I/O)   │
                                    └────────┬────────┘
                                             │
                                    ┌────────▼────────┐
                                    │     storage     │
                                    │   Storage{}     │
                                    └────────┬────────┘
                                             │
         ┌───────────────────┬───────────────┴───────────────┐
         │                   │                               │
┌────────▼────────┐ ┌────────▼────────┐             ┌────────▼────────┐
│   SQLite (db)   │ │  tkrzw (hash)   │             │  tkrzw (tree)   │
│     users       │ │    objects      │             │     events      │
└─────────────────┘ └─────────────────┘             │    (queue)      │
                                                    └─────────────────┘
                    ┌─────────────────┐
                    │   Filesystem    │
                    │   <dir>/src/    │
                    │  (JS sources)   │
                    └─────────────────┘
```

### Core Components

**Server Entry Point** (`server/server.go`): Starts the SSH server for player connections.

**Game Engine** (`game/`):
- `game.go` - Initializes the game, handles SSH sessions, sets up initial world objects
- `connection.go` - Player connection handling, command processing, wizard commands (prefixed with `/`)
- `processing.go` - Object execution, JavaScript callback registration, movement detection

**Storage Layer** (`storage/`):
- `storage.go` - Main storage interface, SQLite for users, filesystem for JavaScript sources
- `dbm/dbm.go` - Wrapper around tkrzw for key-value storage with typed generics
- `queue/queue.go` - Event queue for scheduled object callbacks

**Object System** (`structs/`):
- Objects are defined in `schema.benc` (generates `schema.go` and `decorated.go` via bencgen)
- Objects have: id, state (JSON), location, content (child objects), skills, descriptions, exits, callbacks
- `structs.go` - Object methods, skill system, challenge checks, location/neighbourhood types

**JavaScript Runtime** (`js/js.go`):
- Pool of V8 isolates (one per CPU)
- Objects run JavaScript source files that register callbacks via `addCallback(eventType, tags, handler)`
- State persists between executions as JSON
- 200ms timeout per execution
- Supports imports via `// @import` directive (see below)

### Key Concepts

**JavaScript Imports**: Source files can import other source files using the `// @import` directive:

```javascript
// /lib/util.js - A shared library
var util = util || {};
util.greet = function(name) { return 'Hello, ' + name + '!'; };
```

```javascript
// /mobs/dog.js - Imports the utility library
// @import /lib/util.js
// @import ./base.js      // Relative import from same directory
// @import ../lib/math.js // Relative import from parent directory

addCallback('bark', ['action'], (msg) => {
    log(util.greet('World'));
});
```

Import behavior:
- Imports are resolved at source load time (build-time concatenation)
- Dependencies are included in topological order (imported code comes first)
- Circular imports are detected and produce an error
- Diamond dependencies are handled correctly (each file included once)
- Modifying any file in the import chain triggers a refresh

**Event System**: Objects communicate through events. Objects register callbacks with `addCallback(eventType, tags, handler)`:

```javascript
// Register a callback for the "greet" event with the "command" tag
addCallback('greet', ['command'], (msg) => {
    // Update the object's description to show what happened
    setDescriptions([{Short: 'greeter (just greeted ' + msg.line + ')'}]);
});
```

**Event Tags** determine how events are routed:
- `command`: Commands from a player to their own object. Content: `{name: "...", line: "..."}`
- `action`: Actions directed at sibling objects (other objects in the same location). Content: `{name: "...", line: "..."}`
- `emit`: System infrastructure events with arbitrary JSON content depending on the source

**Inter-Object Communication**: Objects can communicate using `emit()` and `emitToLocation()`:

```javascript
// Emit to a specific object by ID
emit(targetId, 'ping', {message: 'hello'});

// Emit with skill challenge - target only receives if they pass
emit(targetId, 'whisper', {secret: 'hidden'}, [
    {Skill: 'perception', Level: 50}
]);

// Broadcast to all objects at a location
emitToLocation(getLocation(), 'announcement', {msg: 'Hello everyone!'});

// Broadcast with challenge - only skilled objects receive
emitToLocation(getLocation(), 'telepathy', {thought: 'secret'}, [
    {Skill: 'psychic', Level: 100}
]);

// Receive emitted events
addCallback('ping', ['emit'], (msg) => {
    log('Received ping:', msg.message);
});
```

Challenge format uses PascalCase to match Go structs: `{Skill: string, Level: number, Message?: string}`. Challenge checks have side effects - recipient skills may improve or decay.

**Movement and Container Events**: When objects move between locations, two types of events are generated:

1. **`movement`** events are sent to objects that successfully *detect* the moving object. These are subject to skill challenges - only objects passing perception checks receive them. Use for game/roleplay purposes where detection abilities matter:
```javascript
addCallback('movement', ['emit'], (msg) => {
    // msg.Object: the object that moved (as perceived by receiver)
    // msg.Source: old location ID (or null if created)
    // msg.Destination: new location ID (or null if removed)
    if (msg.Object && msg.Destination) {
        log('Detected arrival:', msg.Object.Unsafe.Name);
    }
});
```

2. **`received`** and **`transmitted`** events are sent directly to containers when their content changes. These are hardwired notifications, NOT subject to skill challenges - containers always receive them regardless of detection abilities. Use for programmatic bookkeeping:
```javascript
// Sent to container when it gains content
addCallback('received', ['emit'], (msg) => {
    log('Container received:', msg.Object.Unsafe.Id);
});

// Sent to container when it loses content
addCallback('transmitted', ['emit'], (msg) => {
    log('Container lost:', msg.Object.Unsafe.Id);
});
```

3. **`exitFailed`** events are sent to the room (container) when someone fails a skill challenge on an exit. The challenge's `Message` field (if set) is automatically printed to the user who failed. Use the event to announce the failure to others in the room:
```javascript
addCallback('exitFailed', ['emit'], (msg) => {
    // msg.subject: the object that failed the exit challenge
    // msg.exit: the exit that was attempted
    // msg.score: the total challenge score (negative = failed)
    // msg.primaryFailure: the challenge that failed worst (has Skill, Level, Message)
    var name = msg.subject.Descriptions[0].Short;
    var exitName = msg.exit.Descriptions[0].Short;
    emitToLocation(getLocation(), 'announce', {
        message: name + ' tries to go ' + exitName + ', but fails miserably.'
    });
});
```

**Object Identification**: Commands that target objects (like `look`, action commands) use pattern matching against Short descriptions:
- **Word matching**: `tome` matches "dusty tome", `torch` matches "burning torch"
- **Glob patterns**: `dust*` matches "dusty", `*orch` matches "torch"
- **Full description**: `"dusty tome"` matches exactly (use quotes for multi-word patterns)
- **Indexed selection**: When multiple objects match, use `0.torch`, `1.torch` to select which one
- **Wizard ID syntax**: Wizards can use `#objectid` to target by internal ID

**Delayed Execution**: Use `setTimeout(ms, eventName, message)` to schedule an event to be delivered later:
```javascript
// Schedule a "tick" event to be delivered in 1000ms
setTimeout(1000, 'tick', {count: 1});

// Handle the delayed event
addCallback('tick', ['emit'], (msg) => {
    setDescriptions([{Short: 'timer (count: ' + msg.count + ')'}]);
    // Schedule the next tick
    setTimeout(1000, 'tick', {count: msg.count + 1});
});
```

**Debugging**: Wizards can attach to an object's console with `/debug #objectid` to see `log()` output, and detach with `/undebug #objectid`.

**Skill/Challenge System**: Descriptions and exits can have challenge requirements. Objects have skills with theoretical/practical levels, recharge times, and forgetting mechanics. See `docs/skill-system.md` for details.

**File System**: JavaScript sources are stored directly on the filesystem in `<dir>/src/`. Wizards can browse and edit these files using standard text editors or IDEs.

**Player Commands**: All users have access to:
- `look` or `l`: View current room, or `look <target>` to examine a specific object
- `scan`: View current room and neighboring rooms via exits
- Direction shortcuts: `n`, `s`, `e`, `w`, `ne`, `nw`, `se`, `sw`, `u`, `d` expand to `north`, `south`, etc. when matching exits

**Command Resolution**: When a player types a command, it's processed in this order (stopping at the first handler that returns a truthy value):
1. **Player's object** receives it as a `command` event (if registered via `addCallback('name', ['command'], ...)`)
2. **Current room** receives it as an `action` event (if registered via `addCallback('name', ['action'], ...)`)
3. **Exits** are checked - if an exit's name matches, the player moves through it
4. **Sibling objects** in the room receive it as an `action` event (checked in order)

Objects only receive events they've registered callbacks for with the matching tag. For example, `trigger logger` only invokes the logger's callback if it registered `addCallback('trigger', ['action'], ...)`.

**Wizard Commands**: Wizard users (User.Wizard = true) get additional `/`-prefixed commands:
- Object management: `/create`, `/inspect`, `/move`, `/remove`, `/enter`, `/exit`
- State management: `/getstate`, `/setstate` (view/modify object state JSON)
- Debugging: `/debug`, `/undebug`
- Source files: `/ls`
- Monitoring: `/queuestats`, `/jsstats`, `/flushstats`
- Admin: `/addwiz`, `/delwiz`

**State Management Commands**:
- `/getstate #objectID [PATH]` - View object state (or specific nested value)
- `/setstate #objectID PATH VALUE` - Set a value at a dot-separated path

Examples:
```
/getstate #abc123              # Show entire state
/getstate #abc123 Spawn        # Show just the Spawn field
/setstate #abc123 Foo.Bar 42   # Set nested value
/setstate #abc123 Name "test"  # Set string value
```

**Server Configuration**: The root object (ID `""`) stores server-wide configuration in its state. To configure the spawn location for new users:

```
/getstate #                    # View current server config
/setstate # Spawn.Container genesis  # Set spawn to genesis room
/setstate # Spawn.Container <room-id>  # Set spawn to a specific room ID
```

If the configured spawn location doesn't exist, new users fall back to genesis.

## Dependencies

**Required system libraries:**
- tkrzw C++ library (for tkrzw-go)
- V8 headers and libraries (for v8go)

**Code generation:**
- bencgen: Code generator for binary serialization (install separately for schema changes)

## Administration

The server exposes a Unix domain socket for runtime administration at `<dir>/control.sock`. The `juicemud-admin` CLI tool communicates with this socket.

### Source Versioning

JavaScript sources can be organized into version directories for zero-downtime updates:

```
<dir>/src/
├── current -> v2/     # Symlink to active version
├── v1/                # Previous version
└── v2/                # Current version
```

The server follows the `src/current` symlink at startup. To deploy a new version:

1. Create a new version directory (e.g., `v3/`) with updated sources
2. Update the symlink: `ln -sfn v3 src/current`
3. Validate and switch atomically:
   ```bash
   juicemud-admin switch-sources
   ```

The `switch-sources` command resolves symlinks, validates all source files referenced by existing objects exist in the new directory, and only switches if validation passes. If sources are missing, it reports them:

```
Error: missing source files:
  /room.js (3 objects)
  /player.js (1 objects)
```

Options:
- `juicemud-admin switch-sources src/v3` - switch to a specific path
- `juicemud-admin -socket /path/to/control.sock switch-sources` - use a different socket

## Project Structure

```
juicemud/
├── bin/
│   ├── admin/               # Admin CLI for runtime management
│   └── server/              # Main server binary
├── crypto/                  # SSH key generation
├── decorator/               # Object decoration utilities
├── docs/                    # Documentation
├── game/                    # Game engine
├── integration_test/        # Integration tests
├── js/                      # JavaScript runtime
├── lang/                    # Natural language utilities
├── loader/                  # JavaScript source loading
├── server/                  # Server initialization
├── storage/
│   ├── dbm/                 # tkrzw database wrappers
│   ├── queue/               # Event queue
│   └── storage.go           # Main storage interface
├── structs/                 # Object definitions
└── juicemud.go              # Shared utilities
```
