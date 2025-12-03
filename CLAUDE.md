# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

JuiceMUD is a MUD (Multi-User Dungeon) game server written in Go. It features:
- SSH-based player connections using the gliderlabs/ssh library
- JavaScript-based object scripting using V8 (rogchap.com/v8go)
- WebDAV interface for file management with HTTP digest authentication
- Persistent storage using tkrzw (hash/tree databases) and SQLite

## Build and Run Commands

```bash
# Build the server
go build -o juicemud ./server

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

**Skill/Challenge System**: Descriptions and exits can have challenge requirements. Objects have skills with theoretical/practical levels, recharge times, and forgetting mechanics.

**File System**: JavaScript sources stored in virtual filesystem with read/write group permissions. Files tracked in SQLite, content in tkrzw hash.

**Wizard Commands**: Users in "wizards" group get access to `/create`, `/inspect`, `/move`, `/remove`, `/enter`, `/exit`, `/debug`, `/ls`, `/chread`, `/chwrite`, `/groups`.

### Dependencies

- `tkrzw-go`: Requires tkrzw C++ library installed
- `v8go`: Requires V8 headers/libraries
- `bencgen`: Code generator for binary serialization (install separately for schema changes)
