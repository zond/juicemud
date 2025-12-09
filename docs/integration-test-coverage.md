# Integration Test Coverage Analysis

This document tracks what functionality is covered by the integration tests and what remains untested.

## Currently Tested

| Feature | How it's tested |
|---------|-----------------|
| User creation | `createUser()` in helpers.go |
| User login | `loginUser()` in helpers.go |
| User persistence | Verified via storage after creation |
| `look` command | Multiple tests verify room descriptions |
| Exit-based movement | `south`, `north` commands |
| WebDAV GET/PUT | Reading/writing source files |
| `/create` | Creating objects from source files |
| `/inspect` | Called but output not verified |
| `/ls` | Called but output not verified |
| `/enter` | Moving into rooms |
| `/exit` | Moving out of rooms |
| `/move` | Moving objects between locations |
| `setDescriptions()` | Used in all JS sources |
| `setExits()` | Used in lookroom.js |
| `addCallback('connected')` | Implicitly via user.js |
| `scan` | Verified in scan command test |
| Challenge system | Hidden gem with perception challenge, skill-gated exit |
| `setSkills()` / `getSkills()` | Used in challenge test via "train" command |
| `setLearning()` / `getLearning()` | Enabled in challenge test |
| Custom command handlers | "train" command registered via `addCallback` |

## Not Tested

### Player Commands

| Command | Description |
|---------|-------------|
| `look [target]` | Look at specific object (not just room) |

### Wizard Commands

| Command | Description |
|---------|-------------|
| `/remove` | Delete objects |
| `/debug` / `/undebug` | Attach to object console |
| `/groups` | Show user groups |
| `/chread` / `/chwrite` | File permissions |
| `/inspect` output | Currently called but output not verified |

### JavaScript API Functions

| Function | Description |
|----------|-------------|
| `emit()` | Send event to another object |
| `setTimeout()` | Delayed events |
| `getNeighbourhood()` | Get surrounding rooms/objects |
| `removeCallback()` | Unregister callback |
| `log()` | Console output |
| `getSkillConfig()` / `setSkillConfig()` | Skill system configuration |

### Events

| Event | Description |
|-------|-------------|
| `movement` | Notifies when objects move |
| `created` | Fires on `/create` |
| Custom actions | Room/sibling action handlers |

### Skill/Challenge System

| Feature | Description |
|---------|-------------|
| Skill decay/forgetting | Skills decay over time |

### Edge Cases

| Case | Description |
|------|-------------|
| Circular container prevention | Can't put object in itself |
| Can't remove self | `/remove` on self fails |
| Can't exit universe | `/exit` at top level fails |
| WebDAV unauthorized access | Non-owner can't access WebDAV |

## Suggested Priority for New Tests

### High Priority (core gameplay)

1. **`emit()` / `setTimeout()`** - Core JS inter-object communication
2. **`/remove` command** - Essential world management

### Medium Priority (administrative/debugging)

4. **`/debug` and `log()`** - Essential for game development
5. **`/groups`, `/chread`, `/chwrite`** - Permission system
6. **Movement events** - Notifying objects of movement

### Lower Priority (advanced features)

7. **Skill decay** - Skills decay over time without use
8. **`/inspect` output validation** - Nice to have
9. **Edge case error handling** - Defensive tests
