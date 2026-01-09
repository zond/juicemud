# Code Simplification TODO

Last updated: 2026-01-09

## Completed

### /stats wizard command (wizcommands.go → stats_commands.go)
- **Before:** 636 lines with 5+ levels of nested switch statements
- **After:** Table-driven dispatch with individual handler functions
- **Changes:**
  - Extracted to separate `stats_commands.go` file
  - Generic `sortedKeys` function for deterministic map iteration
  - Generic `parseSortArg` to consolidate 4 identical functions
  - Removed unused `help` field from dispatch map

---

## High Priority

### 1. jsstats.go - JSStats (~1655 lines)
**Priority: HIGHEST** - Most significant opportunity for reduction

**Issues:**
- 4 nearly identical `Record*` functions (~280 lines total):
  - `RecordError` (~95 lines)
  - `RecordLoadError` (~55 lines)
  - `RecordBootError` (~59 lines)
  - `RecordRecoveryError` (~74 lines)
- 3 identical `Top*` functions with same switch statements (~135 lines):
  - `TopScripts`
  - `TopObjects`
  - `TopIntervals`
- 3 identical `*SnapshotLocked` helpers (~165 lines):
  - `scriptSnapshotLocked`
  - `objectSnapshotLocked`
  - `intervalSnapshotLocked`

**Suggested fix:**
- Extract generic `recordErrorInternal(source, objectID, intervalID string, err error, duration time.Duration)`
- Create single generic `topEntities[T any]` with sort function parameter
- Use interfaces or generics for snapshot creation

**Estimated reduction:** ~300-400 lines

---

### 2. lang/lang.go - Article function (~125 lines)
**Priority: HIGH** - Easy win

**Issues:**
- 20+ `regexp.MustCompile` calls inside the function body
- Regexes recompiled on every call
- Performance impact on frequently-called function

**Suggested fix:**
- Move all regex compilations to package-level `var` block
- Consider combining related patterns with alternation

**Estimated reduction:** Minimal lines but significant performance improvement

---

### 3. game/processing.go - addObjectCallbacks (~380 lines)
**Priority: HIGH**

**Issues:**
- Single massive function registering 15+ JavaScript callbacks
- Each callback has its own validation and error handling
- Deeply nested closures capturing multiple variables
- Mixed concerns: getters/setters, timers, events, lifecycle

**Suggested fix:**
- Group callbacks by category into separate functions:
  - `addPropertyCallbacks()`
  - `addTimerCallbacks()`
  - `addEventCallbacks()`
  - `addLifecycleCallbacks()`
- Extract common validation into helpers
- Consider table-driven approach for simple getters/setters

**Estimated reduction:** ~50-100 lines, major readability improvement

---

### 4. structs/combat.go - TakeDamage (~150 lines)
**Priority: MEDIUM**

**Issues:**
- Many sequential calculations (damage, overkill, severing, bleeding)
- Wound level calculation uses cascading if-else with magic numbers
- Multiple result conditions mixed together

**Suggested fix:**
- Extract `calculateWoundLevel(damageFraction float64) int32`
- Extract `handleSeverance(...)` method
- Consider result builder pattern

**Estimated reduction:** ~20-30 lines, better testability

---

### 5. game/connection.go - objectAttempter.attempt (~135 lines)
**Priority: MEDIUM**

**Issues:**
- Multiple fallthrough paths for command handling
- Direction alias expansion mixed with exit finding
- Complex exit challenge handling with JS callbacks

**Suggested fix:**
- Extract `findExitByDirection()` method
- Extract `handleExitChallenge()` method
- Use early returns to reduce nesting

**Estimated reduction:** ~20 lines, better readability

---

### 6. game/processing.go - emitMovement (~115 lines)
**Priority: MEDIUM**

**Issues:**
- Complex observer detection from two neighborhoods
- Set operations for finding who sees what
- Three types of events emitted

**Suggested fix:**
- Extract `findMovementObservers()` helper
- Consider builder pattern for movement events

**Estimated reduction:** ~20 lines

---

## Medium Priority

### 7. storage/dbm/dbm.go - LiveTypeHash (~325 lines)
**Priority: MEDIUM** - Complex but necessary

**Issues:**
- Dual mutex system with documented lock ordering
- `getNOLOCK` name is misleading (it does acquire a lock)
- Complex two-phase approach in `Proc` method

**Suggested fix:**
- Rename `getNOLOCK` to something clearer
- Add prominent lock ordering documentation
- Consider simplifying to single lock with finer sections

---

### 8. structs/structs.go - Challenge.Check (~150 lines)
**Priority: MEDIUM**

**Issues:**
- Complex skill calculation with EMA, decay, recharge
- Side effects during checks (updates LastUsedAt, applies learning)
- `CheckWithDetails` duplicates logic from `Check`

**Suggested fix:**
- Extract `SkillCalculator` type for formulas
- Separate "compute" vs "apply" phases
- Document mathematical model

---

### 9. game/wizcommands.go - remaining commands (~450 lines after stats extraction)
**Priority: LOW**

**Issues:**
- `/intervals` handler alone is ~120 lines
- `/setstate` has complex path navigation
- Inline handler functions could be extracted

**Suggested fix:**
- Extract complex handlers to named methods
- Use consistent argument parsing pattern

---

## Low Priority

### 10. game/connection.go - parseShellTokens (~60 lines)
**Issues:** Complex state machine, already using `shellwords` elsewhere
**Suggested fix:** Replace with `shellwords` library or document edge cases

### 11. storage/storage.go - MoveObject (~100 lines)
**Issues:** TOCTOU prevention pattern, cycle detection
**Suggested fix:** Document pattern, possibly extract helpers

### 12. game/game.go - New constructor (~180 lines)
**Issues:** First startup vs normal startup branching, goroutine setup
**Suggested fix:** Extract worker setup, separate initialization paths

### 13. js/js.go - Target.Run (~70 lines)
**Issues:** V8 pool management, callback invocation
**Suggested fix:** Extract callback logic, document timeout pattern

### 14. server/server.go - startWithListener (~115 lines)
**Issues:** Complex startup sequence, directory creation
**Suggested fix:** Extract source resolution, symlink creation

---

## Notes

- Line counts are approximate
- Priority based on: impact × ease of fix
- Some "simplifications" are really about improving testability/readability
- Always run code-simplifier agent, then code-excellence-reviewer before committing
