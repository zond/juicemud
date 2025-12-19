# Integration Test Coverage Analysis

This document tracks what functionality is covered by the integration tests and what remains untested.

## Testing Philosophy

Integration tests use SSH interfaces for all interactions, matching how users and wizards interact with the game in production. Source files are written directly via `TestServer.WriteSource()` since there is no WebDAV interface. Direct storage access is only used for:

- **Setup operations** (e.g., `makeUserWizard` to grant wizard privileges)
- **Verifying persistence** (e.g., `LoadUser` in Test 1 to confirm user was saved)
- **Hidden objects** (e.g., objects with perception challenges that users can't `/inspect`)

For verifying object state and waiting for object creation, tests prefer:
1. `tc.waitForObject(pattern)` - polls via `/inspect` to find objects by description pattern
2. `tc.waitForLocation(target, location)` - polls via `/inspect` to check object location
3. `tc.waitForSourcePath(target, path)` - polls via `/inspect` to check object source path
4. `tc.inspect(target)` - runs `/inspect` and parses the JSON result
5. `tc.enterIsolatedRoom(ts, name)` - creates a room and enters it to prevent action name collisions

## Currently Tested

| Feature | Test | How it's tested |
|---------|------|-----------------|
| User creation | 1 | `createUser()` via SSH prompts |
| User login/reconnect | 1 | `loginUser()` via SSH prompts |
| User persistence | 1 | `LoadUser` after creation (storage access justified for verification) |
| `look` command | 1, 5, 6 | Verify room descriptions, exits, and contents |
| `look [target]` | 16 | Look at specific object, shows name and Long description |
| Exit-based movement | 5, 6 | `south`, `north` commands via exits |
| Source file read | 2 | Reading `/user.js` via `TestServer.ReadSource()` |
| Source file write | 2 | Creating `/testroom.js`, `/box.js`, etc. via `TestServer.WriteSource()` |
| `/create` | 3 | Creating objects from source files |
| `/inspect` | 3, 11 | Verifying object properties, existence before/after removal |
| `/ls` | 3 | Lists files, verifies created file appears |
| `/enter` | 4 | Moving into rooms as wizard |
| `/exit` | 4 | Moving out of rooms as wizard |
| `/move` | 5, 12, 24 | Moving objects between locations |
| `/move` circular prevention | 24 | Rejects self-move, 2-level, and 3-level cycles |
| `/remove` | 11 | Deletes objects, verifies self-removal error message |
| `setDescriptions()` | All | Used in all JS sources, updated dynamically |
| `setExits()` | 5 | Creating exits between rooms |
| `addCallback('connected', ['emit'])` | 1 | Implicitly via user.js |
| `addCallback('x', ['command'])` | 8 | "train" command handler |
| `addCallback('x', ['action'])` | 9, 10, 18 | ping/start handlers for emit/setTimeout, room/sibling actions |
| `addCallback('x', ['emit'])` | 9, 10, 12 | Receiving emit/setTimeout/movement events |
| `scan` | 7 | Shows current room and neighboring rooms via exits |
| Challenge system (description) | 8 | Hidden gem with perception challenge |
| Challenge system (exit) | 8 | Skill-gated exit requiring strength |
| `setSkills()` | 8 | Granting perception/strength via train command |
| `emit()` | 9 | Sender emits to receiver, descriptions update |
| `emit()` with challenges | 20 | High-skill recipient receives, low-skill filtered out |
| `emitToLocation()` | 21 | Broadcasts to all objects in a location |
| `emitToLocation()` with challenges | 22 | Broadcasts with skill-based filtering |
| `getId()` | 20 | Used implicitly in emitToLocation (emitter ID for challenges) |
| `setTimeout()` | 10 | Timer schedules delayed event |
| `movement` event | 12 | Observer sees object move out of room |
| `/debug` | 13 | Attach to object console |
| `/undebug` | 13 | Detach from object console |
| `log()` | 13 | Console output appears when debug attached |
| `created` event | 14 | Object receives creator info on `/create` |
| Room `action` handler | 18 | Room receives action commands issued by player |
| Sibling `action` handler | 18 | Objects in same room receive action commands |
| `state` persistence | 19 | JS state object persists across multiple command invocations |
| `getNeighbourhood()` | 25 | Returns current location and neighboring rooms via exits |
| `removeCallback()` | 26 | Unregister a callback, verify action no longer triggers |
| `getSkillConfig()` / `casSkillConfig()` | 27 | Get and update skill configuration with CAS |
| `getLocation()` | 28 | Get object's current location ID |
| `moveObject()` | 28 | Move object to new location, replaces setLocation() |
| `getContent()` | 29 | Get IDs of objects contained within |
| `getSourcePath()` / `setSourcePath()` | 30 | Get and change object's source file path |
| `getLearning()` / `setLearning()` | 31 | Get and toggle learning mode |

## Not Tested

### Player Commands

(All player commands are now tested)

### Wizard Commands

(All wizard commands are now tested or removed)

### JavaScript API Functions

| Function | Description | Priority |
|----------|-------------|----------|
| `getSkillConfigs()` / `setSkillConfigs()` | Bulk skill configuration | Low |

Note: `setLocation()` and `setContent()` were removed from the API in favor of `moveObject()`.

### Events

(All priority events are now tested)

### Edge Cases

| Case | Description | Priority |
|------|-------------|----------|
| Can't exit universe | `/exit` at top level should fail | Low |
| `/remove` current location | Should fail with error message | Low |

## Suggested Next Tests

### 1. Edge cases (Low Priority)
- `/exit` at genesis should fail gracefully
