# TODO

Known issues and tasks to address.

## Flaky Test: TestCheckWithDetails

**Location:** `structs/structs_test.go`

**Test:** `TestCheckWithDetails/primary_failure_is_the_worst_scoring_challenge`

**Symptom:** The test intermittently fails with "expected non-nil failure" (approximately 40% failure rate when run in a loop).

**To reproduce:**
```bash
for i in 1 2 3 4 5; do go test -count=1 -run TestCheckWithDetails ./structs/ 2>&1 | tail -2; done
```

**Likely cause:** The test involves skill challenges which may have non-deterministic elements (random rolls, time-based decay, etc.). The test assumes a specific outcome but the challenge system may produce different results on different runs.

**Priority:** Medium - does not affect production, but causes CI flakiness.

**Date identified:** 2025-12-23

## Missing Feature: JS createObject() API

**Current state:** Objects can only be created via the wizard `/create` command. There is no `createObject()` function available to JavaScript code.

**Desired behavior:** JS code should be able to programmatically create new objects, for example:
```javascript
// Create a new object from a source file
var newObjId = createObject('/items/coin.js', {
    location: getLocation(),  // Where to place the new object
    state: {value: 100}       // Optional initial state
});
```

**Use cases:**
- NPCs spawning items (loot drops, crafting)
- Rooms generating content dynamically
- Factory/spawner objects creating instances

**Considerations:**
- Rate limiting to prevent abuse
- Permission checks (should any object be able to create, or only certain ones?)
- Source file validation
- Whether to send `created` event to new object

**Priority:** Medium - enables more dynamic game content.

**Date identified:** 2025-12-23
