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

### jsstats.go - JSStats
- **Before:** ~1655 lines with 4 duplicate Record* functions, 3 duplicate Top* functions
- **After:** ~1490 lines (-160 lines)
- **Changes:**
  - Extracted `recordErrorInternal` helper with `errorRecordParams` struct
  - Added `sortable` interface and generic `sortSnapshots[T]` helper
  - Added `calcExecMetrics` for shared metric calculations
  - Added `snapshot()` methods on RateStats/TimeRateStats
  - Bug fix: RecordLoadError limitCountMap ordering

### lang/lang.go - Article function
- **Before:** 20+ regex compilations inside function (recompiled every call)
- **After:** All 18 regexes pre-compiled at package level
- **Changes:**
  - Moved `regexp.MustCompile` calls to package-level `var` block
  - Significant performance improvement (compile once vs every call)

### game/processing.go - addObjectCallbacks
- **Before:** ~380-line monolithic function
- **After:** Organized into focused helpers
- **Changes:**
  - Extracted `addEventCallbacks` (emit, emitToLocation)
  - Extracted `addTimerCallbacks` (setTimeout, setInterval, clearInterval)
  - Extracted `addLifecycleCallbacks` (getNeighbourhood, getId, createObject, removeObject, print)
  - Pure refactoring, no behavioral changes

---

## Medium Priority

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
