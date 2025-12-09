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
| `/inspect` | Test 11: Verifies object existence before/after removal |
| `/ls` | Called but output not verified |
| `/enter` | Moving into rooms |
| `/exit` | Moving out of rooms |
| `/move` | Moving objects between locations |
| `/remove` | Test 11: Deletes objects, verifies self-removal fails gracefully |
| `setDescriptions()` | Used in all JS sources, updated dynamically in emit/timer/movement tests |
| `setExits()` | Used in lookroom.js |
| `addCallback('connected')` | Implicitly via user.js |
| `addCallback([action])` | Test 9-10: Custom action handlers trigger emit/setTimeout |
| `scan` | Verified in scan command test |
| Challenge system | Hidden gem with perception challenge, skill-gated exit |
| `setSkills()` / `getSkills()` | Used in challenge test via "train" command |
| `setLearning()` / `getLearning()` | Enabled in challenge test |
| Custom command handlers | "train" command registered via `addCallback` |
| `emit()` | Test 9: Sender emits to receiver using ID from msg.line, descriptions update |
| `setTimeout()` | Test 10: Timer schedules delayed event, description changes on fire |
| `movement` event | Test 12: Observer description updates when objects move |

## Not Tested

### Player Commands

| Command | Description |
|---------|-------------|
| `look [target]` | Look at specific object (not just room) |

### Wizard Commands

| Command | Description |
|---------|-------------|
| `/debug` / `/undebug` | Attach to object console |
| `/groups` | Show user groups |
| `/chread` / `/chwrite` | File permissions |

### JavaScript API Functions

| Function | Description |
|----------|-------------|
| `getNeighbourhood()` | Get surrounding rooms/objects |
| `removeCallback()` | Unregister callback |
| `log()` | Console output |
| `getSkillConfig()` / `setSkillConfig()` | Skill system configuration |

### Events

| Event | Description |
|-------|-------------|
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
| Can't exit universe | `/exit` at top level fails |
| WebDAV unauthorized access | Non-owner can't access WebDAV |

## Suggested Priority for New Tests

### High Priority (core gameplay)

1. ~~**`emit()` / `setTimeout()`**~~ ✓ Tested in Tests 9-10
2. ~~**`/remove` command**~~ ✓ Tested in Test 11

### Medium Priority (administrative/debugging)

3. **`/debug` and `log()`** - Essential for game development
4. **`/groups`, `/chread`, `/chwrite`** - Permission system
5. ~~**Movement events**~~ ✓ Tested in Test 12

### Lower Priority (advanced features)

6. **Skill decay** - Skills decay over time without use
7. **Edge case error handling** - Defensive tests
8. **`created` event** - Verify objects receive creation notification
