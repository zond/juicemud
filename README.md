# JuiceMUD

A MUD (Multi-User Dungeon) game server written in Go, featuring JavaScript-based object scripting and SSH-based player connections.

## Features

- SSH-based player connections using [gliderlabs/ssh](https://github.com/gliderlabs/ssh)
- JavaScript object scripting using V8 via [rogchap.com/v8go](https://rogchap.com/v8go)
- Filesystem-native source management (edit sources directly in `<dir>/src/`)
- Persistent storage using [tkrzw](https://github.com/estraier/tkrzw-go) (hash/tree databases) and SQLite
- Sophisticated skill system with forgetting mechanics and challenge-based access control
- Argon2id password hashing for secure credential storage

## Quick Start

```bash
# Build the server
go build -o juicemud ./bin/server

# Build the admin CLI
go build -o juicemud-admin ./bin/admin

# Run the server (default port: SSH 15000)
./juicemud

# Run all tests
go test ./...
```

---

## Architecture & Persistence

### System Overview

```
                        ┌─────────────────────────────────┐
                        │       Players (SSH :15000)      │
                        └───────────────┬─────────────────┘
                                        │
                        ┌───────────────▼─────────────────┐
                        │          Game Engine            │
                        │                                 │
                        │  • Player sessions & commands   │
                        │  • Object execution & events    │
                        │  • Movement & skill checks      │
                        └───────────────┬─────────────────┘
                                        │
                        ┌───────────────▼─────────────────┐
                        │          JS Runtime             │
                        │         (V8 pool)               │
                        │                                 │
                        │  • Executes object callbacks    │
                        │  • 200ms timeout per execution  │
                        │  • State persists as JSON       │
                        └───────────────┬─────────────────┘
                                        │
┌───────────────────────────────────────┴───────────────────────────────────────┐
│                              Storage Layer                                    │
│                                                                               │
│  ┌───────────────┐  ┌───────────────┐  ┌───────────────┐  ┌───────────────┐  │
│  │    SQLite     │  │  tkrzw hash   │  │  tkrzw tree   │  │  Filesystem   │  │
│  │               │  │               │  │               │  │               │  │
│  │  • Users      │  │  • Objects    │  │  • Events     │  │  • JS sources │  │
│  │  • Audit logs │  │    (by ID)    │  │    (by time)  │  │    (src/)     │  │
│  └───────────────┘  └───────────────┘  └───────────────┘  └───────────────┘  │
│                                                                               │
└───────────────────────────────────────────────────────────────────────────────┘
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
- Supports imports via `// @import` directive

### Persistence Layer

JuiceMUD uses three different storage backends, each chosen for its specific strengths:

#### SQLite (`storage.db`)
Relational storage for structured data with query requirements:
- **Users**: Authentication credentials, roles (owner/wizard), associated object IDs, last login timestamps
- **Audit logs**: Timestamped records of significant events (logins, object changes, admin actions)

SQLite provides ACID transactions and SQL queries for user management operations like listing users by role or finding inactive accounts.

#### tkrzw Hash Database (`objects.tkh`)
High-performance key-value storage for objects:
- **Key**: Object ID (base64-encoded unique identifier)
- **Value**: Binary-serialized object data (benc format)

Hash database provides O(1) lookups ideal for the primary access pattern: loading an object by ID. Objects are serialized using benc (binary encoding) for compact storage and fast deserialization.

#### tkrzw Tree Database (`events.tkt`)
Ordered key-value storage for the event queue:
- **Key**: Timestamp + event ID (ensures chronological ordering)
- **Value**: Scheduled callback data (target object, event type, payload)

Tree database maintains sorted order, enabling efficient "get all events before time X" queries for the scheduler. Used for `setTimeout()` and `setInterval()` callbacks.

#### Filesystem (`src/`)
JavaScript source files stored as regular files:
- Direct editing with any text editor or IDE
- Version control friendly (git, etc.)
- Supports versioned directories with symlinks for atomic deployments

Source files are loaded on demand and cached. The loader resolves `// @import` directives and concatenates dependencies in topological order.

### Data Flow

1. **Player connects** via SSH, authenticates against SQLite user table
2. **Player's object** is loaded from tkrzw hash DB
3. **Command received** triggers JavaScript execution:
   - Source loaded from filesystem (with imports resolved)
   - Object state deserialized from stored JSON
   - V8 executes the script, callback runs
   - Modified state serialized back to object
   - Object persisted to tkrzw hash DB
4. **Scheduled events** are polled from tkrzw tree DB and dispatched

### Project Structure

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

---

## Running the Server

### Dependencies

**Required system libraries:**
- tkrzw C++ library (for tkrzw-go)
- V8 headers and libraries (for v8go)

**Code generation:**
- bencgen: Code generator for binary serialization (install separately for schema changes)

### Server Configuration

The root object (ID `""`) stores server-wide configuration in its state:

```
/inspect # State               # View current server config
/setstate # Spawn.Container genesis  # Set spawn location for new users
```

**Spawn Location**: Where new users appear. Falls back to "genesis" if not set or invalid.

**Skill Configs**: Game-wide skill parameters. See the JavaScript API section for `getSkillConfig()` and `casSkillConfig()`.

### Administration

The server exposes a Unix domain socket for runtime administration at `<dir>/control.sock`. Use the `juicemud-admin` CLI:

```bash
# Switch to new source version (validates first)
juicemud-admin switch-sources

# Switch to specific path
juicemud-admin switch-sources src/v3

# Use different socket
juicemud-admin -socket /path/to/control.sock switch-sources
```

### Source Versioning & Development Workflow

#### Recommended Setup

For a production MUD, sources should be version-controlled (e.g., on GitHub). Wizards develop locally using their own checkouts and private test worlds, then submit pull requests that propagate to production:

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        Development Workflow                             │
│                                                                         │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐                 │
│  │  Wizard A   │    │  Wizard B   │    │  Wizard C   │                 │
│  │             │    │             │    │             │                 │
│  │ Local test  │    │ Local test  │    │ Local test  │                 │
│  │ world + src │    │ world + src │    │ world + src │                 │
│  └──────┬──────┘    └──────┬──────┘    └──────┬──────┘                 │
│         │                  │                  │                        │
│         └────────┬─────────┴─────────┬────────┘                        │
│                  │   Pull Requests   │                                 │
│                  ▼                   ▼                                 │
│         ┌─────────────────────────────────────┐                        │
│         │           GitHub Repo               │                        │
│         │         (main branch)               │                        │
│         └──────────────────┬──────────────────┘                        │
│                            │ git pull / deploy script                  │
│                            ▼                                           │
│         ┌─────────────────────────────────────┐                        │
│         │       Production Server             │                        │
│         │                                     │                        │
│         │  src/v1/  src/v2/  src/v3/ ...     │                        │
│         │              ▲                      │                        │
│         │              └── current symlink    │                        │
│         └─────────────────────────────────────┘                        │
└─────────────────────────────────────────────────────────────────────────┘
```

#### Local Development

Each wizard runs their own JuiceMUD instance with a private test world:

```bash
# Clone the source repo
git clone git@github.com:yourmud/world-source.git

# Run a local test server (uses local src/ directory)
./juicemud -dir ./testworld -src ./world-source

# Develop, test, iterate...
# Changes to world-source/*.js take effect on next object execution
```

Wizards can experiment freely without affecting production or other developers.

#### Version Directories

Production servers organize sources into version directories for atomic updates:

```
<dir>/src/
├── current -> v42/    # Symlink to active version
├── v41/               # Previous version (rollback target)
└── v42/               # Current version
```

The server follows the `src/current` symlink at startup.

#### Deployment Process

When new code is merged to main:

```bash
# 1. Pull latest sources into a new version directory
cd /var/juicemud/src
git clone --depth 1 git@github.com:yourmud/world-source.git v43

# 2. Update the symlink
ln -sfn v43 current

# 3. Tell the running server to switch (validates first)
juicemud-admin switch-sources
```

The `switch-sources` command:
1. **Validates** all source files referenced by existing objects exist in the new directory
2. **Switches** the active source directory if validation passes
3. **Reloads lazily** - objects get their new code when next executed, not all at once

If sources are missing, the switch is rejected:

```
Error: missing source files:
  /room.js (3 objects)
  /player.js (1 objects)
```

This prevents deploying broken code that would leave objects without their scripts.

#### Rollback

If issues are discovered after deployment:

```bash
# Point symlink back to previous version
ln -sfn v41 current

# Tell server to switch back
juicemud-admin switch-sources
```

Objects will lazily reload their previous code on next execution.

---

## Wizard Commands Reference

Wizard users (User.Wizard = true) get additional `/`-prefixed commands. Regular players have access to basic commands like `look`, `scan`, and movement.

### Target Syntax

Many commands accept a target argument:
- **Pattern matching**: `torch` matches objects with "torch" in their Short description
- **Glob patterns**: `dust*` matches "dusty", `*orch` matches "torch"
- **Quoted phrases**: `"dusty tome"` matches the full phrase
- **Indexed selection**: `0.torch`, `1.torch` when multiple objects match
- **Object ID**: `#abc123` targets by internal ID
- **Self**: `self` targets your own object

### Object Management

```
/create <sourcePath>           Create object from source file at current location
/inspect [target] [PATH]       View object data, optionally drill into a path
/move <target> <destination>   Move object to new location
/remove <target>               Delete an object (must be empty)
/enter <target>                Move yourself into an object
/exit                          Move yourself to parent container
```

**Inspect examples:**
```
/inspect                       # Show your own object
/inspect #abc123               # Show entire object
/inspect #abc123 State         # Show just the State field
/inspect #abc123 Skills.combat # Drill into nested data
```

### State Management

```
/setstate <target> <path> <value>   Set a value in object state
/skills [target]                    View all skills for an object
/skills [target] <skill> <th> <pr>  Set skill levels (theoretical, practical)
```

**Examples:**
```
/setstate #abc123 Foo.Bar 42        # Set nested state value
/setstate #abc123 Name "test"       # Set string value (use quotes)
/skills                             # Show your own skills
/skills #abc123 perception 50 30    # Set perception: theoretical=50, practical=30
```

### Events

```
/emit <target> <event> <tag> <json>   Send an event to an object
```

**Parameters:**
- `target`: Object ID (`#abc123` or `self`)
- `event`: Event type (e.g., `tick`, `message`)
- `tag`: One of `emit`, `command`, or `action`
- `json`: Message content as JSON

**Examples:**
```
/emit #abc123 tick emit {}                    # Trigger a tick event
/emit #abc123 message emit {"Text":"Hello!"}  # Send message to player
/emit self customEvent emit {"foo":"bar"}     # Emit to yourself
```

### Debugging

```
/debug <target>      Attach to object's debug console (see log() output)
/undebug <target>    Detach from debug console
```

When you attach, the last 64 log messages (up to 10 minutes old) are displayed.

### Source Files

```
/ls [path]              List source files in directory (tree-style)
/ls -r [depth] [path]   Recursive listing (default depth: 10)
```

**Output format:**
- Directories shown with `/` suffix
- Files show object count in parentheses: `file.js (3)`
- Single file shows objects using it

### Object Hierarchy

```
/tree [target]            Show contents of object (default: current location)
/tree -r [depth] [target] Recursive tree view (default depth: 5)
```

**Output format:**
- Shows `#id  name` for each object
- Uses tree characters for hierarchy (`├──`, `└──`, `│`)

### Monitoring

```
/stats                              Show server statistics summary (alias: dashboard)
/stats errors [sub]                 Error stats (sub: summary|categories|locations|recent)
/stats perf [sub]                   Performance stats (sub: summary|scripts|slow)
/stats scripts [sort] [n]           Top n scripts (sort: time|execs|slow|errors|errorrate)
/stats script <path>                Detailed stats for specific script
/stats objects [sort] [n]           Top n objects (sort: time|execs|slow|errors|errorrate)
/stats object <id>                  Detailed stats for specific object
/stats intervals [sort] [n]         Top n intervals (sort: time|execs|slow|errors|errorrate)
/stats users [filter] [sort] [n]    List users
/stats flush                        Show database flush health status
/stats reset                        Clear all statistics
/intervals [target]                 View active intervals for object
```

**User listing options:**
- Filters: `all` (default), `owners`, `wizards`, `players`
- Sort: `name` (default), `id`, `login` (most recent), `stale` (least recent)

### Administration

```
/addwiz <username>    Grant wizard privileges
/delwiz <username>    Revoke wizard privileges
/deluser <username>   Delete a user account (removes user and their object)
```

### Player Commands

All users (not just wizards) have access to:

```
look [target]         View current room, or examine specific object (alias: l)
scan                  View current room and neighboring rooms
<direction>           Move through exit (n, s, e, w, ne, nw, se, sw, u, d)
```

Direction shortcuts expand automatically: `n` becomes `north`, etc.

---

## JavaScript API Reference

Objects are scripted in JavaScript. Each object has a source file that registers callbacks to handle events.

### Core Functions

#### `addCallback(eventType, tags, handler)`
Register a callback for an event type.

```javascript
addCallback('greet', ['command'], (msg) => {
    print('Hello!');
});
```

**Parameters:**
- `eventType`: String - event name to handle
- `tags`: Array - routing tags: `['command']`, `['action']`, or `['emit']`
- `handler`: Function - receives message object

#### `removeCallback(eventType)`
Unregister a callback for an event type.

```javascript
removeCallback('greet');  // Stop handling 'greet' events
```

#### `log(...args)`
Write to the debug console (viewable with `/debug`).

```javascript
log('Debug info:', someValue);
```

#### `print(message)`
Write directly to player's terminal. Only works for objects with connected players.

```javascript
print('You feel a chill...');
```

### Object Properties

Most object properties have both getter and setter functions (e.g., `getDescriptions()` / `setDescriptions()`). The setters modify the object; getters return the current value.

#### `state`
Persistent object state. Survives between callback invocations and server restarts.

```javascript
state.counter = (state.counter || 0) + 1;
```

#### `getId()`
Returns the current object's ID.

#### `getLocation()`
Returns the ID of the object's container (location).

#### `getContent()`
Returns array of IDs of objects contained by this object.

#### `getNeighbourhood()`
Returns the object's neighbourhood: its location, neighbouring locations (via exits), and content. Useful for AI/NPC logic that needs spatial awareness.

```javascript
var hood = getNeighbourhood();
// hood.Location - ID of container
// hood.Neighbours - array of {Exit, Location} for adjacent rooms
// hood.Content - array of object IDs inside this object
```

#### `getDescriptions()` / `setDescriptions(descriptions)`
Get or set the object's descriptions.

```javascript
setDescriptions([
    {
        Short: 'rusty sword',
        Long: 'A sword covered in rust.',
        Challenges: [{Skill: 'perception', Level: 30}]  // Optional
    }
]);
```

#### `getExits()` / `setExits(exits)`
Get or set the object's exits (for rooms).

```javascript
setExits([
    {
        Descriptions: [{Short: 'north'}],
        Destination: 'room-id-here',
        UseChallenges: [{Skill: 'strength', Level: 50, Message: 'Too heavy!'}],
        TransmitChallenges: [{Skill: 'perception', Level: 20}]
    }
]);
```

#### `getMovement()` / `setMovement(movement)`
Get or set how movement is rendered when this object moves.

```javascript
setMovement({Active: true, Verb: 'scurries'});  // "A rat scurries south"
setMovement({Active: false, Verb: ''});          // Handle renderMovement yourself
```

#### `getSkills()` / `setSkills(skills)`
Get or set the object's skills. Skills are a map of skill name to skill data.

```javascript
var skills = getSkills();
// skills['perception'] = {Theoretical: 50, Practical: 30, ...}
```

#### `getLearning()` / `setLearning(enabled)`
Get or set whether the object learns from skill checks (improves/decays skills).

```javascript
setLearning(false);  // Disable skill improvement for this object
```

#### `getSourcePath()` / `setSourcePath(path)`
Get or set the object's JavaScript source file path.

```javascript
var path = getSourcePath();  // e.g., "/mobs/rat.js"
```

### Communication

#### `emit(targetId, eventType, message, [challenges])`
Send an event to a specific object.

```javascript
emit(targetId, 'ping', {data: 'hello'});

// With skill challenge - target only receives if they pass
emit(targetId, 'whisper', {secret: 'hidden'}, [
    {Skill: 'perception', Level: 50}
]);
```

#### `emitToLocation(locationId, eventType, message, [challenges])`
Broadcast an event to all objects at a location.

```javascript
emitToLocation(getLocation(), 'announcement', {text: 'Hello everyone!'});
```

### Object Lifecycle

#### `createObject(sourcePath, locationId)`
Create a new object. Returns the new object's ID. Rate-limited to 10/minute per object.

```javascript
var coinId = createObject('/items/coin.js', getLocation());
```

#### `removeObject(objectId)`
Remove an object. Cannot remove non-empty containers or the calling object's location.

```javascript
removeObject(coinId);
removeObject(getId());  // Object removes itself
```

#### `moveObject(objectId, destinationId)`
Move an object to a new location. Validates containment rules.

```javascript
moveObject(npcId, targetRoomId);
```

### Timing

#### `setTimeout(ms, eventType, message)`
Schedule a one-time event.

```javascript
setTimeout(5000, 'delayed', {info: 'data'});
```

#### `setInterval(ms, eventType, message)`
Schedule a recurring event. Returns interval ID. Minimum 5000ms, max 10 per object.

```javascript
var id = setInterval(5000, 'heartbeat', {});
```

Interval events wrap your data:
- `msg.Data` - your original message
- `msg.Interval.ID` - interval identifier
- `msg.Interval.Missed` - count of missed executions (server downtime)

#### `clearInterval(intervalId)`
Stop a recurring interval.

```javascript
clearInterval(state.heartbeatId);
```

### Skill Configuration

#### `getSkillConfig(skillName)`
Get configuration for a skill. Returns null if not configured.

```javascript
var config = getSkillConfig('perception');
// config.Forget - seconds until decay
// config.Recharge - seconds for full XP gain
// config.Duration - seconds for deterministic results
```

#### `casSkillConfig(skillName, oldConfig, newConfig)`
Compare-and-swap skill configuration. Returns true if successful.

```javascript
// Create new config (oldConfig = null)
casSkillConfig('stealth', null, {Forget: 3600, Recharge: 1000, Duration: 60});

// Update existing config
var old = getSkillConfig('stealth');
casSkillConfig('stealth', old, {Forget: 7200, Recharge: old.Recharge, Duration: old.Duration});

// Delete config (newConfig = null)
casSkillConfig('stealth', old, null);
```

### Data Structures

#### Challenge
```javascript
{Skill: 'perception', Level: 50, Message: 'You fail to notice.'}
```
- `Skill`: String - skill name
- `Level`: Number - difficulty (0-100+)
- `Message`: String (optional) - shown on failure

#### Description
```javascript
{Short: 'golden key', Long: 'A small golden key.', Challenges: [...]}
```
- `Short`: String - brief name (shown in lists)
- `Long`: String - detailed description (shown on examine)
- `Challenges`: Array (optional) - skill checks to perceive

#### Exit
```javascript
{
    Descriptions: [{Short: 'north', Long: 'A dark passage.'}],
    Destination: 'room-id',
    UseChallenges: [...],      // Must pass to traverse
    TransmitChallenges: [...]  // Added when viewing through exit
}
```

---

## MUD Scripting Guide

This section covers best practices and patterns for writing effective object scripts.

### Script Execution Model

**Critical concept**: The *entire script* runs every time any callback is invoked, not just the callback function.

1. Top-level code executes first
2. Then the specific callback is invoked
3. The `state` object persists; local variables do not

**Best Practice**: Put ALL code inside callback handlers. Top-level statements should only be `addCallback()` registrations.

```javascript
// CORRECT: Only addCallback() at top level
addCallback('created', ['emit'], (msg) => {
    state.counter = 0;
    state.intervalId = setInterval(5000, 'tick', {});
    setDescriptions([{Short: 'my object'}]);
});

addCallback('tick', ['emit'], (msg) => {
    state.counter++;
    setDescriptions([{Short: 'my object (' + state.counter + ' ticks)'}]);
});
```

```javascript
// WRONG: Top-level code runs on EVERY callback!
setDescriptions([{Short: 'my object'}]);  // Resets every time!
state.intervalId = setInterval(5000, 'tick', {});  // Creates infinite intervals!

addCallback('tick', ['emit'], (msg) => {
    state.counter = (state.counter || 0) + 1;
    setDescriptions([{Short: 'my object (' + state.counter + ' ticks)'}]);
});
// Result: Descriptions reset before tick runs, intervals pile up forever
```

**For pre-existing objects** that can't use the `created` event, guard initialization:

```javascript
if (state.initialized === undefined) {
    state.initialized = true;
    state.counter = 0;
    state.intervalId = setInterval(5000, 'tick', {});
}
```

### Event System

Events are how objects communicate. Register handlers with `addCallback(eventType, tags, handler)`.

#### Event Tags

Tags determine how events are routed:

| Tag | Source | Use Case |
|-----|--------|----------|
| `command` | Player typing a command | Player abilities, inventory actions |
| `action` | Sibling objects in same location | Interactive objects, NPCs |
| `emit` | System or other objects via `emit()` | Timers, inter-object messaging |

#### Command Resolution Order

When a player types a command:

1. **Player's object** receives it as `command` event
2. **Current room** receives it as `action` event
3. **Exits** are checked for matching names
4. **Sibling objects** receive it as `action` event (in order)

First handler returning truthy stops the chain.

#### Special Events

| Event | When Sent | Content |
|-------|-----------|---------|
| `created` | Object first created via `/create` or `createObject()` | `{creatorId: '...'}` |
| `connected` | Player logs in or is created | `{remote, username, object, cause}` (cause: "login" or "create") |
| `message` | Sent to player object | `{Text: '...'}` prints to terminal |
| `movement` | Object detected moving (skill-checked) | `{Object, Source, Destination}` |
| `received` | Container gains content (guaranteed) | `{Object}` |
| `transmitted` | Container loses content (guaranteed) | `{Object}` |
| `exitFailed` | Someone fails exit challenge | `{subject, exit, score, primaryFailure}` |
| `renderMovement` | Custom movement rendering | `{Observer, Source, Destination}` |

### JavaScript Imports

Source files can import other files:

```javascript
// @import /lib/util.js
// @import ./local.js        // Relative to current file
// @import ../shared/lib.js  // Parent directory

addCallback('test', ['command'], (msg) => {
    util.doSomething();  // Function from imported file
});
```

**Import behavior:**
- Resolved at load time (concatenation)
- Topological order (dependencies first)
- Circular imports cause errors
- Diamond dependencies handled (each file included once)

**Library pattern:**
```javascript
// /lib/util.js
var util = util || {};
util.greet = function(name) { return 'Hello, ' + name; };
```

### Skill Challenges

#### Description Challenges

Control what players can perceive:

```javascript
setDescriptions([
    {Short: 'ordinary rock', Long: 'A plain gray rock.'},
    {
        Short: 'hidden gem',
        Long: 'A sparkling gem concealed within.',
        Challenges: [{Skill: 'perception', Level: 50}]
    }
]);
```

- No challenges = always visible
- Multiple challenges are summed (must pass overall)
- First visible description's Short is used as the object's name

#### Exit Challenges

```javascript
setExits([{
    Descriptions: [{Short: 'heavy door'}],
    Destination: 'room-id',
    UseChallenges: [{Skill: 'strength', Level: 60, Message: 'Too heavy!'}],
    TransmitChallenges: [{Skill: 'perception', Level: 30}]
}]);
```

- `UseChallenges`: Must pass to traverse; Message shown on failure
- `TransmitChallenges`: Added difficulty when viewing through exit

#### Secret Exits

```javascript
setExits([{
    Descriptions: [{
        Short: 'hidden passage',
        Challenges: [{Skill: 'perception', Level: 80}]
    }],
    Destination: 'secret-room'
}]);
```

### Common Patterns

#### Spawner

```javascript
addCallback('created', ['emit'], (msg) => {
    state.spawned = [];
});

addCallback('spawn', ['action'], (msg) => {
    if (state.spawned.length < 5) {
        var id = createObject('/mobs/rat.js', getLocation());
        state.spawned.push(id);
    }
});

addCallback('cleanup', ['action'], (msg) => {
    for (var id of state.spawned) {
        removeObject(id);
    }
    state.spawned = [];
});
```

#### NPC with Dialogue

```javascript
addCallback('talk', ['action'], (msg) => {
    emit(msg.source, 'message', {Text: 'The merchant nods at you.'});
});

addCallback('movement', ['emit'], (msg) => {
    if (msg.Destination && msg.Object) {
        emit(msg.Object.Id, 'message', {Text: 'Welcome to my shop!'});
    }
});
```

#### Self-Removing Effect

```javascript
addCallback('created', ['emit'], (msg) => {
    state.targetId = msg.Data.targetId;
    setTimeout(30000, 'expire', {});
});

addCallback('expire', ['emit'], (msg) => {
    emit(state.targetId, 'message', {Text: 'The effect wears off.'});
    removeObject(getId());
});
```

#### Room with Exit Failure Announcement

```javascript
addCallback('exitFailed', ['emit'], (msg) => {
    var name = msg.subject.Descriptions[0].Short;
    var exitName = msg.exit.Descriptions[0].Short;
    emitToLocation(getId(), 'message', {
        Text: name + ' struggles with the ' + exitName + ' but fails.'
    });
});
```

#### Custom Movement Rendering

```javascript
setMovement({Active: false, Verb: ''});

addCallback('renderMovement', ['emit'], (msg) => {
    var text;
    if (msg.Source && msg.Source.Here) {
        text = 'The ghost fades into the ' + msg.Destination.Exit + '...';
    } else if (msg.Destination && msg.Destination.Here) {
        text = 'A chill runs down your spine as a ghost materializes.';
    }
    emit(msg.Observer, 'movementRendered', {Message: text});
});
```

---

## Developing JuiceMUD

Information for contributors working on the Go codebase.

### Running Tests

```bash
# All tests
go test ./...

# Single test
go test -v ./structs -run TestLevel

# Specific package
go test -v ./storage/dbm

# Integration tests only
go test -v ./integration_test
```

### Code Generation

Object schemas are defined in `structs/schema.benc` and compiled with bencgen:

```bash
go generate ./structs
```

This generates `schema.go` (binary serialization) and `decorated.go` (helper methods).

### Project Conventions

See `CLAUDE.md` for detailed coding conventions, including:
- JSON struct tags (no field renames, only `omitempty`)
- Integration test guidelines (use SSH interfaces)
- Documentation requirements

### Key Internal Types

- `structs.Object` - Core object type with state, descriptions, exits, skills
- `storage.Storage` - Main storage interface
- `game.Connection` - Player session handler
- `js.Pool` - V8 isolate pool for JavaScript execution
