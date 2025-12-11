# Code Smells and Issues

This document tracks identified code quality issues, bugs, and areas for improvement.

## Critical

### 1. Array Out-of-Bounds in connection.go:892
**File**: `game/connection.go:892`
**Issue**: Accesses `descs[1].Short` without checking if `len(descs) > 1`
**Risk**: Panic/crash when object has only one description
**Status**: Fixed - removed the buggy else-if clause entirely

### 2. Logic Bug in connection.go:892-894
**File**: `game/connection.go:892-894`
**Issue**: The `else if descs[1].Short != ""` block prints `descs[0].Short` instead of `descs[1].Short`
**Risk**: Wrong description displayed to users
**Status**: Fixed - removed the buggy else-if clause entirely (same fix as #1)

## High Priority

### 3. N+1 Query in /listgroups
**File**: `game/connection.go` (listgroups handler)
**Issue**: Loads all groups, then queries members for each group in a loop
**Impact**: Performance degrades with number of groups
**Status**: Fixed - now builds a map from the initial ListGroups result

### 4. Memory Inefficiency in EachSourceObject
**File**: `storage/storage.go`
**Issue**: `EachSourceObject` loads all objects into a slice before iterating, rather than streaming
**Impact**: High memory usage with large object counts
**Status**: Fixed - refactored to collect only ID strings (not full entries), added doc comment explaining why buffering is necessary (SubEach holds read lock, can't delete during iteration)

### 5. Silent setInterval Failure
**File**: `game/processing.go`
**Issue**: `setInterval` was registered but did nothing (no-op), without warning users
**Impact**: Confusing behavior - code appeared to work but timer never fired
**Status**: Fixed - removed setInterval entirely; setTimeout with manual rescheduling is the preferred pattern

## Medium Priority

### 6. Hardcoded Timeouts
**File**: `game/processing.go`
**Issue**: 200ms JavaScript execution timeout was a magic number
**Impact**: Hard to find and adjust
**Status**: Fixed - extracted to named constant `jsExecutionTimeout`

### 7. Stale TODO Comment
**File**: `structs/structs.go`
**Issue**: TODO comment about skill system may be outdated
**Status**: N/A - no such TODO exists; this was a false positive from initial analysis

### 8. Panic in Background Goroutines
**File**: Various
**Issue**: Background goroutines may panic without recovery, crashing the server
**Impact**: Server instability
**Status**: Won't fix - goroutines handle errors properly; true panics indicate bugs that should crash. Adding recovery everywhere would hide bugs and add complexity.

## Low Priority

### 9. "Unsafe" Naming Confusion
**File**: `structs/schema.benc`, generated code
**Issue**: "Unsafe" in field names (e.g., `obj.Unsafe.ID`) doesn't clearly communicate what's unsafe about it
**Impact**: Code readability, learning curve for new developers
**Status**: Fixed - added doc comment in generated code explaining it permits unsynchronized access

### 10. Integration Test Timing
**File**: `integration_test/run_all.go`
**Issue**: Some tests may have timing-dependent failures in slow environments
**Impact**: Flaky tests
**Status**: Fixed - extracted `defaultWaitTimeout` constant (5s), applied consistently across all wait calls

### 11. Code Duplication in Test Sources
**File**: `integration_test/run_all.go`
**Issue**: Similar JavaScript source strings (e.g., `loggerSource` and `loggerResetSource`) with minor differences
**Impact**: Maintenance burden
**Status**: Fixed (commit 6eda7d7)
