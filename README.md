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

**Script Execution Model**: Understanding how scripts run is crucial for writing correct object code:

1. The **entire script** runs every time any callback is invoked (not just the callback function)
2. Top-level code runs first, then the specific callback is invoked
3. The `state` object persists between executions; local variables do not

This means initialization code at the top level will run repeatedly. Use the `created` event for one-time initialization:

```javascript
// CORRECT: Use 'created' event for one-time initialization
addCallback('created', ['emit'], (msg) => {
    state.counter = 0;
    state.intervalId = setInterval(5000, 'tick', {});
    setDescriptions([{Short: 'my object (initialized)'}]);
});

addCallback('tick', ['emit'], (msg) => {
    state.counter++;
    setDescriptions([{Short: 'my object (' + state.counter + ' ticks)'}]);
});
```

The `created` event is sent once when an object is first created via `/create`. If you can't use the `created` event (e.g., for objects that predate your code), guard your initialization:

```javascript
// ALTERNATIVE: Guard initialization with state check
if (state.initialized === undefined) {
    state.initialized = true;
    state.counter = 0;
    state.intervalId = setInterval(5000, 'tick', {});
    setDescriptions([{Short: 'my object (initialized)'}]);
}

addCallback('tick', ['emit'], (msg) => {
    state.counter++;
    setDescriptions([{Short: 'my object (' + state.counter + ' ticks)'}]);
});
```

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

**Special Event Types**: Some event names have built-in behavior:
- `message`: When received by a player object, prints the `Text` field to their terminal. Useful for NPC dialogue, announcements, etc:
```javascript
// Send a message to a player
emit(playerId, 'message', {Text: 'The wizard nods at you.'});
```

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

**Object Manipulation**: Use `moveObject(objectId, destinationId)` to programmatically move objects:
```javascript
// Move an NPC to a new room
moveObject(npcId, targetRoomId);

// Get the current location of the running object
var currentRoom = getLocation();

// Get the content (child objects) of the running object
var inventory = getContent();
```

Note: `moveObject()` validates containment rules and prevents cycles (an object cannot contain itself directly or indirectly).

**Object Creation and Removal**: Use `createObject(sourcePath, locationId)` and `removeObject(objectId)` to dynamically spawn and despawn objects:
```javascript
// Spawner creates a coin in the current room
addCallback('spawn', ['action'], (msg) => {
    var coinId = createObject('/items/coin.js', getLocation());
    state.spawnedCoins.push(coinId);
});

// Cleanup spawned coins
addCallback('cleanup', ['action'], (msg) => {
    for (var id of state.spawnedCoins) {
        removeObject(id);
    }
    state.spawnedCoins = [];
});

// Self-removing object (e.g., temporary effect)
addCallback('expire', ['emit'], (msg) => {
    removeObject(getId());  // Object removes itself
});

// New objects receive a 'created' event
addCallback('created', ['emit'], (msg) => {
    state.creatorId = msg.creatorId;
    setDescriptions([{Short: 'newly spawned item'}]);
});
```

Notes:
- `createObject()` is rate-limited to 10 creations per minute per object to prevent abuse
- `removeObject()` cannot remove non-empty containers (must remove contents first)
- `removeObject()` cannot remove the calling object's current location
- Objects can remove themselves (useful for expiring effects, consumed items, etc.)

**Movement and Container Events**: When objects move between locations, two types of events are generated:

1. **`movement`** events are sent to objects that successfully *detect* the moving object. These are subject to skill challenges - only objects passing perception checks receive them. Use for game/roleplay purposes where detection abilities matter:
```javascript
addCallback('movement', ['emit'], (msg) => {
    // msg.Object: the object that moved (as perceived by receiver)
    // msg.Source: old location ID (or null if created)
    // msg.Destination: new location ID (or null if removed)
    if (msg.Object && msg.Destination) {
        log('Detected arrival:', msg.Object.Id);
    }
});
```

2. **`received`** and **`transmitted`** events are sent directly to containers when their content changes. These are hardwired notifications, NOT subject to skill challenges - containers always receive them regardless of detection abilities. Use for programmatic bookkeeping:
```javascript
// Sent to container when it gains content
addCallback('received', ['emit'], (msg) => {
    log('Container received:', msg.Object.Id);
});

// Sent to container when it loses content
addCallback('transmitted', ['emit'], (msg) => {
    log('Container lost:', msg.Object.Id);
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

**Movement Rendering**: When players observe objects moving, they see descriptive messages like "A rat scurries south" or "A ghost drifts in from north". This rendering is controlled by the `Movement` field on objects:

```javascript
// Simple case: use default rendering with custom verb
setMovement({Active: true, Verb: 'scurries'});  // "A rat scurries south"
setMovement({Active: true, Verb: 'slithers'});  // "A snake slithers in from east"

// Default for new objects: {Active: true, Verb: 'moves'}
```

For advanced cases, set `Active: false` and handle the `renderMovement` event yourself. The event includes the observer's perspective on the movement:
```javascript
setMovement({Active: false, Verb: ''});

addCallback('renderMovement', ['emit'], (msg) => {
    // msg.Observer: ID of the player observing this movement
    // msg.Source: {Here: true} if observer's room, {Exit: 'north'} if visible neighbor, or null
    // msg.Destination: same format as Source

    var text;
    if (msg.Source && msg.Source.Here && msg.Destination && msg.Destination.Exit) {
        text = 'The ghost fades away ' + msg.Destination.Exit + '...';
    } else if (msg.Destination && msg.Destination.Here && msg.Source && msg.Source.Exit) {
        text = 'A chill runs down your spine as a ghost drifts in from ' + msg.Source.Exit + '.';
    } else if (msg.Destination && msg.Destination.Here) {
        text = 'A ghost materializes before you!';
    } else {
        text = 'You sense a ghostly presence nearby.';
    }

    // Send the rendered message back to the observer
    emit(msg.Observer, 'movementRendered', {Message: text});
});
```

**Object Identification**: Commands that target objects (like `look`, action commands) use pattern matching against Short descriptions:
- **Word matching**: `tome` matches "dusty tome", `torch` matches "burning torch"
- **Glob patterns**: `dust*` matches "dusty", `*orch` matches "torch"
- **Full description**: `"dusty tome"` matches exactly (use quotes for multi-word patterns)
- **Indexed selection**: When multiple objects match, use `0.torch`, `1.torch` to select which one
- **Wizard ID syntax**: Wizards can use `#objectid` to target by internal ID

**Delayed Execution**: Use `setTimeout(ms, eventName, message)` to schedule a one-time event to be delivered later:
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

**Recurring Execution**: Use `setInterval(ms, eventName, message)` for persistent recurring events that survive server restarts:
```javascript
// Start a heartbeat every 5 seconds (returns interval ID)
var heartbeatId = setInterval(5000, 'heartbeat', {});

// Handle the recurring event
addCallback('heartbeat', ['emit'], (msg) => {
    log('Heartbeat at ' + new Date().toISOString());
});

// Stop the interval when no longer needed
clearInterval(heartbeatId);
```

Key differences from `setTimeout`:
- Intervals persist to storage and survive server restarts
- Minimum interval is 5000ms (5 seconds)
- Maximum 10 intervals per object
- Use `/intervals` wizard command to view active intervals
- Interval events wrap your data: access via `msg.Data` (your original message) and `msg.Interval.ID`, `msg.Interval.Missed` (missed count from server downtime)

**Debugging**: Wizards can attach to an object's console with `/debug #objectid` to see `log()` output, and detach with `/undebug #objectid`. When you attach, the last 64 log messages (up to 10 minutes old) are displayed first, so you can see what happened before you connected.

**Direct Output**: Use `print(message)` to write directly to a player's terminal (only works for user objects with connections):
```javascript
addCallback('greet', ['command'], (msg) => {
    print('Hello, adventurer!');  // Printed directly to the player's terminal
});
```

Note: `print()` outputs immediately without formatting, while `log()` goes to the debug console. Use `print()` for player-facing messages and `log()` for debugging.

**Skill/Challenge System**: Objects have skills with theoretical/practical levels, recharge times, and forgetting mechanics. Descriptions and exits can require skill challenges to perceive or use. See `docs/skill-system.md` for the underlying math.

**Description Challenges**: Each description can have skill challenges that must be passed to perceive it:

```javascript
setDescriptions([
    {
        Short: 'ordinary rock',
        Long: 'A plain gray rock.',
        // No challenges - always visible
    },
    {
        Short: 'hidden gem',
        Long: 'A sparkling gem concealed in the shadows.',
        Challenges: [{Skill: 'perception', Level: 50}],
    },
    {
        Short: 'ancient runes',
        Long: 'Faint magical runes are etched into the surface.',
        Challenges: [{Skill: 'perception', Level: 30}, {Skill: 'arcana', Level: 20}],
    },
]);
```

How description visibility works:
- Descriptions are visible if they have no challenges OR if the sum of challenge results is positive
- At display time, the first *visible* description's `Short` is used as the object's name
- All visible `Long` texts are concatenated when examining
- Multiple challenges on one description are summed (must pass overall)
- Note: `Object.Name()` internally returns the first description unconditionally; challenge filtering only happens when rendering views

Use cases:
- **Hidden objects**: Require Perception to notice at all
- **Disguises**: First description is the disguise, second requires Insight to see through
- **Graduated detail**: Basic description always visible, expert analysis requires skill

**Exit Challenges**: Exits have two types of challenges:

```javascript
setExits([
    {
        Descriptions: [{Short: 'north'}],
        Destination: 'room-abc123',
        // No challenges - anyone can use and see through
    },
    {
        Descriptions: [{Short: 'locked door'}],
        Destination: 'room-def456',
        // UseChallenges: must pass to traverse the exit
        UseChallenges: [{Skill: 'strength', Level: 50, Message: 'The door is too heavy to open.'}],
    },
    {
        Descriptions: [{Short: 'foggy passage'}],
        Destination: 'room-ghi789',
        // TransmitChallenges: added difficulty to perceive things through this exit
        TransmitChallenges: [{Skill: 'perception', Level: 30}],
    },
]);
```

- **`UseChallenges`**: Checked when a player tries to move through the exit. On failure, the `Message` is printed and an `exitFailed` event is sent to the room.
- **`TransmitChallenges`**: Added to description challenges when viewing neighboring rooms via `scan` or detecting movement through the exit. Makes distant objects harder to perceive through foggy/dark/narrow passages.

Exit descriptions can also have challenges (for secret doors):
```javascript
setExits([{
    Descriptions: [{
        Short: 'hidden passage',
        Challenges: [{Skill: 'perception', Level: 80}],
    }],
    Destination: 'secret-room',
}]);
```

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
- State management: `/setstate` (modify object state JSON), `/skills` (view/update skills)
- Events: `/emit` (send events to objects)
- Debugging: `/debug`, `/undebug`
- Source files: `/ls`
- Monitoring: `/stats`, `/intervals`, `/flushstats`
- Admin: `/addwiz`, `/delwiz`

**Target Keywords**: Many wizard commands accept a target. Special keywords:
- `self` - targets your own object (equivalent to `/inspect self` instead of finding your ID)
- `#<id>` - targets by object ID directly

**Inspecting Objects**:
- `/inspect [target] [PATH]` - View object data, optionally drilling into a specific path

Examples:
```
/inspect                       # Show your own object
/inspect #abc123               # Show entire object
/inspect #abc123 State         # Show just the State field
/inspect #abc123 Skills.combat # Drill into nested data
/setstate #abc123 Foo.Bar 42   # Set nested state value
/setstate #abc123 Name "test"  # Set string state value
```

**Server Configuration**: The root object (ID `""`) stores server-wide configuration in its state. To configure the spawn location for new users:

```
/inspect # State               # View current server config
/setstate # Spawn.Container genesis  # Set spawn to genesis room
/setstate # Spawn.Container <room-id>  # Set spawn to a specific room ID
```

If the configured spawn location doesn't exist, new users fall back to genesis.

**Managing Skills**:
- `/skills [target]` - View all skills for an object
- `/skills [target] <skillname> <theoretical> <practical>` - Set skill levels

Examples:
```
/skills                           # Show your own skills
/skills #abc123                   # Show skills for object
/skills #abc123 perception 50 30  # Set perception: theoretical=50, practical=30
/skills self stealth 100 80       # Set your own stealth skill
```

**Emitting Events**:
- `/emit <target> <eventName> <tag> <message>` - Send an event to an object

Parameters:
- `target`: Object ID (e.g., `#abc123` or `self`)
- `eventName`: Event type (e.g., `tick`, `message`, `customEvent`)
- `tag`: One of `emit`, `command`, or `action`
- `message`: JSON content

Examples:
```
/emit #abc123 tick emit {}                    # Trigger a tick event
/emit #abc123 message emit {"Text":"Hello!"}  # Send a message to player
/emit self customEvent emit {"foo":"bar"}     # Emit to yourself
```

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
