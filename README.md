# JuiceMUD

A MUD (Multi-User Dungeon) game server written in Go, featuring JavaScript-based object scripting, SSH-based player connections, and WebDAV file management.

## Features

- SSH-based player connections using [gliderlabs/ssh](https://github.com/gliderlabs/ssh)
- JavaScript object scripting using V8 via [rogchap.com/v8go](https://rogchap.com/v8go)
- WebDAV interface for file management with HTTP Digest authentication
- Persistent storage using [tkrzw](https://github.com/estraier/tkrzw-go) (hash/tree databases) and SQLite
- Sophisticated skill system with forgetting mechanics and challenge-based access control

## Build and Run

```bash
# Build the server
go build -o juicemud ./bin/server

# Run the server (default ports: SSH 15000, HTTPS 8081, HTTP 8080)
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
              ┌──────────────────────────────┼──────────────────────────────┐
              │                              │                              │
     ┌────────▼────────┐            ┌────────▼────────┐            ┌────────▼────────┐
     │   SSH Server    │            │  HTTPS Server   │            │   HTTP Server   │
     │   (port 15000)  │            │   (port 8081)   │            │   (port 8080)   │
     └────────┬────────┘            └────────┬────────┘            └────────┬────────┘
              │                              │                              │
     ┌────────▼────────┐            ┌────────▼────────┐                     │
     │      game       │            │   digest.Wrap   │◄────────────────────┘
     │ HandleSession() │            │  (HTTP Digest)  │
     └────────┬────────┘            └────────┬────────┘
              │                              │
     ┌────────▼────────┐            ┌────────▼────────┐
     │   Connection    │            │   dav.Handler   │
     │  (player I/O)   │            │   (WebDAV ops)  │
     └────────┬────────┘            └────────┬────────┘
              │                              │
              └──────────────┬───────────────┘
                             │
                    ┌────────▼────────┐
                    │     storage     │
                    │   Storage{}     │
                    └────────┬────────┘
                             │
         ┌───────────────────┼───────────────────┐
         │                   │                   │
┌────────▼────────┐ ┌────────▼────────┐ ┌────────▼────────┐
│   SQLite (db)   │ │  tkrzw (hash)   │ │  tkrzw (tree)   │
│  users, files,  │ │    objects,     │ │     events      │
│     groups      │ │   file content  │ │    (queue)      │
└─────────────────┘ └─────────────────┘ └─────────────────┘
```

### Core Components

**Server Entry Point** (`server/server.go`): Starts three services:
- SSH server for player connections
- HTTPS/HTTP servers for WebDAV file access

**Game Engine** (`game/`):
- `game.go` - Initializes the game, handles SSH sessions, sets up initial world objects
- `connection.go` - Player connection handling, command processing, wizard commands (prefixed with `/`)
- `processing.go` - Object execution, JavaScript callback registration, movement detection

**Storage Layer** (`storage/`):
- `storage.go` - Main storage interface, SQLite for users/files/groups, file access control
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

### Key Concepts

**Event System**: Objects communicate through events. Objects register callbacks with `addCallback(eventType, tags, handler)`:

```javascript
// Register a callback for the "greet" event with the "command" tag
addCallback('greet', ['command'], (msg) => {
    send(msg.name + ' greets everyone warmly.');
});
```

**Event Tags** determine how events are routed:
- `command`: Commands from a player to their own object. Content: `{name: "...", line: "..."}`
- `action`: Actions directed at sibling objects (other objects in the same location). Content: `{name: "...", line: "..."}`
- `emit`: System infrastructure events with arbitrary JSON content depending on the source

**Inter-Object Communication**: Objects can communicate with siblings using `emit()`:

```javascript
// Emit an event to all siblings in the current location
emit('ping', {message: 'hello from sender'});

// Receive emitted events
addCallback('ping', ['emit'], (msg) => {
    log('Received ping:', msg.message);
});
```

The `emit()` function sends events to all objects in the same location.

**Movement and Container Events**: When objects move between locations, two types of events are generated:

1. **`movement`** events are sent to objects that successfully *detect* the moving object. These are subject to skill challenges - only objects passing perception checks receive them. Use for game/roleplay purposes where detection abilities matter:
```javascript
addCallback('movement', ['emit'], (msg) => {
    // msg.Object: the object that moved (as perceived by receiver)
    // msg.Source: old location ID (or null if created)
    // msg.Destination: new location ID (or null if removed)
    if (msg.Object && msg.Destination) {
        send('You notice ' + msg.Object.Unsafe.Name + ' arriving.');
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

**Calling Other Objects**: Use `call(target, eventType, line)` to trigger actions on other objects:
```javascript
// Trigger the "trigger" event on an object matching "logger"
call('logger', 'trigger', 'with some extra args');
```

**Object Identification**: Commands that target objects (like `look`, `call`, action commands) use pattern matching against Short descriptions:
- **Word matching**: `tome` matches "dusty tome", `torch` matches "burning torch"
- **Glob patterns**: `dust*` matches "dusty", `*orch` matches "torch"
- **Full description**: `"dusty tome"` matches exactly (use quotes for multi-word patterns)
- **Indexed selection**: When multiple objects match, use `0.torch`, `1.torch` to select which one
- **Wizard ID syntax**: Wizards can use `#objectid` to target by internal ID

**Delayed Execution**: Use `setTimeout(handler, ms)` to schedule code to run later:
```javascript
setTimeout(() => {
    setDescriptions([{Short: 'timer (activated)'}]);
}, 1000);
```

**Debugging**: Wizards can attach to an object's console with `/debug #objectid` to see `log()` output, and detach with `/undebug #objectid`.

**Skill/Challenge System**: Descriptions and exits can have challenge requirements. Objects have skills with theoretical/practical levels, recharge times, and forgetting mechanics. See `docs/skill-system.md` for details.

**File System**: JavaScript sources stored in virtual filesystem with read/write group permissions. Files tracked in SQLite, content in tkrzw hash.

**Player Commands**: All users have access to:
- `look` or `l`: View current room, or `look <target>` to examine a specific object
- `scan`: View current room and neighboring rooms via exits

**Command Resolution**: When a player types a command, it's processed in this order (stopping at the first handler that returns a truthy value):
1. **Player's object** receives it as a `command` event (if registered via `addCallback('name', ['command'], ...)`)
2. **Current room** receives it as an `action` event (if registered via `addCallback('name', ['action'], ...)`)
3. **Exits** are checked - if an exit's name matches, the player moves through it
4. **Sibling objects** in the room receive it as an `action` event (checked in order)

Objects only receive events they've registered callbacks for with the matching tag. For example, `trigger logger` only invokes the logger's callback if it registered `addCallback('trigger', ['action'], ...)`.

**Wizard Commands**: Users in "wizards" group get additional `/`-prefixed commands:
- Object management: `/create`, `/inspect`, `/move`, `/remove`, `/enter`, `/exit`
- Debugging: `/debug`, `/undebug`
- File permissions: `/ls`, `/chread`, `/chwrite`
- Group management: `/groups`, `/mkgroup`, `/rmgroup`, `/editgroup`, `/adduser`, `/rmuser`, `/members`, `/listgroups`

## Dependencies

**Required system libraries:**
- tkrzw C++ library (for tkrzw-go)
- V8 headers and libraries (for v8go)

**Code generation:**
- bencgen: Code generator for binary serialization (install separately for schema changes)

## Project Structure

```
juicemud/
├── bin/
│   ├── server/              # Main server binary
│   └── integration_test/    # Standalone test runner
├── dav/                     # WebDAV handler
├── digest/                  # HTTP Digest authentication
├── docs/                    # Documentation
├── fs/                      # Virtual filesystem for WebDAV
├── game/                    # Game engine
├── integration_test/        # Integration tests
├── js/                      # JavaScript runtime
├── lang/                    # Natural language utilities
├── server/                  # Server initialization
├── storage/
│   ├── dbm/                 # tkrzw database wrappers
│   ├── queue/               # Event queue
│   └── storage.go           # Main storage interface
├── structs/                 # Object definitions
└── juicemud.go              # Shared utilities
```
