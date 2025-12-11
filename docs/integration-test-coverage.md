# Integration Test Coverage Analysis

This document tracks what functionality is covered by the integration tests and what remains untested.

## Testing Philosophy

Integration tests use SSH and WebDAV interfaces for all interactions, matching how users and wizards interact with the game in production. Direct storage access is only used for:

- **Setup operations** (e.g., `makeUserWizard` to grant wizard privileges)
- **Verifying persistence** (e.g., `LoadUser` in Test 1 to confirm user was saved)
- **Hidden objects** (e.g., objects with perception challenges that users can't `/inspect`)

For verifying object state and waiting for object creation, tests prefer:
1. `tc.waitForObject(pattern)` - polls via `/inspect` to find objects by description pattern
2. `tc.waitForLocation(target, location)` - polls via `/inspect` to check object location
3. `tc.inspect(target)` - runs `/inspect` and parses the JSON result

## Currently Tested

| Feature | Test | How it's tested |
|---------|------|-----------------|
| User creation | 1 | `createUser()` via SSH prompts |
| User login/reconnect | 1 | `loginUser()` via SSH prompts |
| User persistence | 1 | `LoadUser` after creation (storage access justified for verification) |
| `look` command | 1, 5, 6 | Verify room descriptions, exits, and contents |
| `look [target]` | - | Not tested - looking at specific objects |
| Exit-based movement | 5, 6 | `south`, `north` commands via exits |
| WebDAV GET | 2 | Reading `/user.js` |
| WebDAV PUT | 2 | Creating `/testroom.js`, `/box.js`, etc. |
| `/create` | 3 | Creating objects from source files |
| `/inspect` | 3, 11 | Verifying object properties, existence before/after removal |
| `/ls` | 3 | Called but output not verified |
| `/enter` | 4 | Moving into rooms as wizard |
| `/exit` | 4 | Moving out of rooms as wizard |
| `/move` | 5, 12 | Moving objects between locations |
| `/remove` | 11 | Deletes objects, verifies self-removal fails |
| `setDescriptions()` | All | Used in all JS sources, updated dynamically |
| `setExits()` | 5 | Creating exits between rooms |
| `addCallback('connected', ['emit'])` | 1 | Implicitly via user.js |
| `addCallback('x', ['command'])` | 8 | "train" command handler |
| `addCallback('x', ['action'])` | 9, 10 | ping/start handlers for emit/setTimeout |
| `addCallback('x', ['emit'])` | 9, 10, 12 | Receiving emit/setTimeout/movement events |
| `scan` | 7 | Shows current room and neighboring rooms via exits |
| Challenge system (description) | 8 | Hidden gem with perception challenge |
| Challenge system (exit) | 8 | Skill-gated exit requiring strength |
| `setSkills()` | 8 | Granting perception/strength via train command |
| `emit()` | 9 | Sender emits to receiver, descriptions update |
| `setTimeout()` | 10 | Timer schedules delayed event |
| `movement` event | 12 | Observer sees object move out of room |
| `/mkgroup` | 13 | Create groups with owner and supergroup flags |
| `/rmgroup` | 13 | Delete empty groups |
| `/adduser` | 13 | Add user to group |
| `/rmuser` | 13 | Remove user from group |
| `/editgroup` | 13 | Edit group name, owner, supergroup flag |
| `/listgroups` | 13 | List all groups with properties |
| `/members` | 13 | List members of a group |
| `/groups` | 13 | Show user's group memberships |
| `/debug` | 14 | Attach to object console |
| `/undebug` | 14 | Detach from object console |
| `log()` | 14 | Console output appears when debug attached |
| `created` event | 15 | Object receives creator info on `/create` |

## Not Tested

### Player Commands

| Command | Description | Priority |
|---------|-------------|----------|
| `look [target]` | Look at specific object (not just room) | Medium |

### Wizard Commands

| Command | Description | Priority |
|---------|-------------|----------|
| `/chread` / `/chwrite` | File read/write permissions | Medium |
| `/checkperm` | Dry-run permission check (not implemented) | Low |

### JavaScript API Functions

| Function | Description | Priority |
|----------|-------------|----------|
| `getNeighbourhood()` | Get surrounding rooms/objects | Medium |
| `removeCallback()` | Unregister a callback | Low |
| `getSkillConfig()` / `setSkillConfig()` | Individual skill configuration | Low |
| `getSkillConfigs()` / `setSkillConfigs()` | Bulk skill configuration | Low |
| `setInterval()` | Repeating timed events (currently no-op) | Low |
| `getLocation()` / `setLocation()` | Direct location manipulation | Low |
| `getContent()` / `setContent()` | Direct content manipulation | Low |
| `getSourcePath()` / `setSourcePath()` | Source path access | Low |
| `getLearning()` / `setLearning()` | Learning mode toggle | Low |

### Events

| Event | Description | Priority |
|-------|-------------|----------|
| Room/sibling `action` | Action handlers on location/siblings | Medium |

### Edge Cases

| Case | Description | Priority |
|------|-------------|----------|
| Circular container prevention | `/move` should fail if dest is inside obj | Medium |
| Can't exit universe | `/exit` at top level should fail | Low |
| WebDAV unauthorized access | Non-owner/non-wizard can't access WebDAV | Medium |
| `/remove` current location | Should fail with error message | Low |
| State persistence | `state` object survives across runs | Medium |

## Suggested Next Tests

### 1. `look [target]` (Medium Priority)
Test looking at specific objects:
- Create an object with a long description
- Use `look <object>` to examine it
- Verify shows object name and long description

### 2. Room/sibling action handlers (Medium Priority)
Test that actions can be handled by room or siblings:
- Create room with action handler
- Enter room and issue action command
- Verify room's handler is invoked

### 3. State persistence (Medium Priority)
Test that JS `state` object persists:
- Create object that stores counter in `state`
- Increment counter via command
- Verify counter persists across multiple commands

### 4. Edge cases (Lower Priority)
- `/exit` at genesis should fail gracefully
- `/move #obj #obj` should fail (circular)
- WebDAV access without wizard privileges
