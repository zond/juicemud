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

**Event System**: Objects receive events with types and tags:
- `emit` tag: Infrastructure events (connected, movement, created)
- `command` tag: Player commands to their object
- `action` tag: Actions from other objects

**Skill/Challenge System**: Descriptions and exits can have challenge requirements. Objects have skills with theoretical/practical levels, recharge times, and forgetting mechanics. See `docs/skill-system.md` for details.

**File System**: JavaScript sources stored in virtual filesystem with read/write group permissions. Files tracked in SQLite, content in tkrzw hash.

**Wizard Commands**: Users in "wizards" group get access to `/create`, `/inspect`, `/move`, `/remove`, `/enter`, `/exit`, `/debug`, `/ls`, `/chread`, `/chwrite`, `/groups`.

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
