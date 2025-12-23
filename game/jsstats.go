package game

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cache "github.com/go-pkgz/expirable-cache/v3"
	"github.com/pkg/errors"
	"github.com/zond/juicemud/js"
	"github.com/zond/juicemud/js/imports"
	"rogchap.com/v8go"
)

const (
	// slowExecutionThreshold defines executions considered "slow".
	slowExecutionThreshold = 50 * time.Millisecond
	// recentBufferSize is the maximum number of recent notable executions (errors + slow) to keep.
	recentBufferSize = 10000
	// jsStatsTTL is how long to keep stats for inactive scripts/objects before eviction.
	jsStatsTTL = 7 * 24 * time.Hour
	// maxScripts is the maximum number of scripts to track.
	maxScripts = 1000
	// maxObjects is the maximum number of objects to track.
	maxObjects = 50000
	// maxIntervals is the maximum number of intervals to track.
	maxIntervals = 10000
	// maxLocations is the maximum number of error locations to track.
	maxLocations = 5000
	// maxLocationsPerEntity is the maximum error locations tracked per script/object/interval.
	maxLocationsPerEntity = 100
	// maxErrorMessageLength is the maximum length of error messages stored.
	maxErrorMessageLength = 128
	// expiredCleanupInterval is how often to run DeleteExpired on caches.
	// With a 7-day TTL, hourly cleanup is more than sufficient.
	expiredCleanupInterval = time.Hour
)

// ErrorCategory classifies the source of an error.
type ErrorCategory string

const (
	CategoryJS      ErrorCategory = "js"
	CategoryStorage ErrorCategory = "storage"
	CategoryTimeout ErrorCategory = "timeout"
	CategoryJSON    ErrorCategory = "json"
	CategoryOther   ErrorCategory = "other"
)

// ErrorLocation represents where an error occurred (file:line:column).
// All fields are optional - nil/empty means not available.
type ErrorLocation struct {
	File   *string
	Line   *int
	Column *int
}

func (l ErrorLocation) String() string {
	if l.File == nil {
		return "(unknown)"
	}
	if l.Line == nil {
		return *l.File
	}
	if l.Column == nil {
		return fmt.Sprintf("%s:%d", *l.File, *l.Line)
	}
	return fmt.Sprintf("%s:%d:%d", *l.File, *l.Line, *l.Column)
}

// ExecutionRecord captures a notable execution (error or slow) for debugging.
type ExecutionRecord struct {
	Timestamp   time.Time
	ObjectID    string
	SourcePath  string
	IntervalID  string        // Empty if not an interval event
	Duration    time.Duration // 0 for non-execution errors (boot, load)
	IsError     bool
	Category    ErrorCategory // Only set if IsError
	Location    ErrorLocation // Only set if IsError
	Message     string        // Only set if IsError (truncated)
	ImportChain []string      // For slow executions
}

// RateStats tracks EMA of event counts (events per second).
type RateStats struct {
	SecondRate float64   // EMA over ~1 second
	MinuteRate float64   // EMA over ~1 minute
	HourRate   float64   // EMA over ~1 hour
	DayRate    float64   // EMA over ~1 day
	lastUpdate time.Time // last time rates were updated
}

// update applies a new observation to the EMA rates.
// count is the number of events since last update.
// Rates are stored as events-per-second, smoothed over different time windows.
func (r *RateStats) update(count uint64) {
	now := time.Now()
	if r.lastUpdate.IsZero() {
		// First update: just record the time, rates start at 0 and will
		// naturally build up. This avoids inflated initial rates.
		r.lastUpdate = now
		return
	}

	elapsed := now.Sub(r.lastUpdate).Seconds()
	if elapsed <= 0 {
		return
	}

	// Calculate instantaneous rate (events per second)
	instantRate := float64(count) / elapsed

	// EMA alpha values: alpha = 1 - exp(-elapsed/window)
	// This properly handles variable time intervals
	alphaSecond := 1 - math.Exp(-elapsed/1.0)
	alphaMinute := 1 - math.Exp(-elapsed/60.0)
	alphaHour := 1 - math.Exp(-elapsed/3600.0)
	alphaDay := 1 - math.Exp(-elapsed/86400.0)

	r.SecondRate = alphaSecond*instantRate + (1-alphaSecond)*r.SecondRate
	r.MinuteRate = alphaMinute*instantRate + (1-alphaMinute)*r.MinuteRate
	r.HourRate = alphaHour*instantRate + (1-alphaHour)*r.HourRate
	r.DayRate = alphaDay*instantRate + (1-alphaDay)*r.DayRate

	r.lastUpdate = now
}

// TimeRateStats tracks EMA of execution time (seconds of JS per second of wall time).
// This is distinct from RateStats which tracks event counts.
type TimeRateStats struct {
	SecondRate  float64   // Seconds of JS per second of wall time
	MinuteRate  float64   // Seconds of JS per minute of wall time
	HourRate    float64   // Seconds of JS per hour of wall time
	DayRate     float64   // Seconds of JS per day of wall time
	lastUpdate  time.Time // Last time rates were updated
	prevTotalNs uint64    // Previous total for delta calculation
}

// update applies a new observation to the time rate EMAs.
// currentTotalNs is the cumulative total execution time in nanoseconds.
func (r *TimeRateStats) update(currentTotalNs uint64) {
	now := time.Now()
	if r.lastUpdate.IsZero() {
		r.lastUpdate = now
		r.prevTotalNs = currentTotalNs
		return
	}

	elapsed := now.Sub(r.lastUpdate).Seconds()
	if elapsed <= 0 {
		return
	}

	// Guard against underflow (shouldn't happen but protects against bugs)
	if currentTotalNs < r.prevTotalNs {
		r.prevTotalNs = currentTotalNs
		return
	}

	// Calculate delta time in seconds
	deltaNs := currentTotalNs - r.prevTotalNs
	deltaSec := float64(deltaNs) / 1e9

	// Calculate instantaneous rate (seconds of JS per second of wall time)
	instantRate := deltaSec / elapsed

	// EMA alpha values: alpha = 1 - exp(-elapsed/window)
	alphaSecond := 1 - math.Exp(-elapsed/1.0)
	alphaMinute := 1 - math.Exp(-elapsed/60.0)
	alphaHour := 1 - math.Exp(-elapsed/3600.0)
	alphaDay := 1 - math.Exp(-elapsed/86400.0)

	r.SecondRate = alphaSecond*instantRate + (1-alphaSecond)*r.SecondRate
	r.MinuteRate = alphaMinute*instantRate + (1-alphaMinute)*r.MinuteRate
	r.HourRate = alphaHour*instantRate + (1-alphaHour)*r.HourRate
	r.DayRate = alphaDay*instantRate + (1-alphaDay)*r.DayRate

	r.lastUpdate = now
	r.prevTotalNs = currentTotalNs
}

// ScriptStats tracks per-script (source path) execution and error statistics.
type ScriptStats struct {
	// Execution stats
	Executions    uint64    // Total execution count
	TotalTimeNs   uint64    // Total execution time in nanoseconds
	MinTimeNs     uint64    // Minimum execution time (valid only if Executions > 0)
	MaxTimeNs     uint64    // Maximum execution time
	SlowCount     uint64    // Executions exceeding threshold
	LastExecution time.Time // Last execution timestamp

	// Error stats
	Errors     uint64    // Total error count
	LastError  time.Time // Last error timestamp
	ByCategory map[ErrorCategory]uint64
	ByLocation map[string]uint64 // error location string -> count

	// Rate tracking
	execRate  RateStats     // Executions per second
	timeRate  TimeRateStats // Seconds of JS per second of wall time
	errorRate RateStats     // Errors per second

	prevExecs  uint64
	prevErrors uint64
}

func newScriptStats() *ScriptStats {
	return &ScriptStats{
		ByCategory: make(map[ErrorCategory]uint64),
		ByLocation: make(map[string]uint64),
	}
}

// ObjectExecStats tracks per-object execution and error statistics.
type ObjectExecStats struct {
	// Execution stats
	Executions    uint64    // Total execution count
	TotalTimeNs   uint64    // Total execution time in nanoseconds
	MinTimeNs     uint64    // Minimum execution time (valid only if Executions > 0)
	MaxTimeNs     uint64    // Maximum execution time
	SlowCount     uint64    // Executions exceeding threshold
	LastExecution time.Time // Last execution timestamp
	SourcePath    string    // Current source path for this object

	// Error stats
	Errors     uint64    // Total error count
	LastError  time.Time // Last error timestamp
	ByCategory map[ErrorCategory]uint64
	ByLocation map[string]uint64 // error location string -> count
	ByScript   map[string]uint64 // script path -> error count

	// Rate tracking
	execRate  RateStats
	timeRate  TimeRateStats
	errorRate RateStats

	prevExecs  uint64
	prevErrors uint64
}

func newObjectExecStats() *ObjectExecStats {
	return &ObjectExecStats{
		ByCategory: make(map[ErrorCategory]uint64),
		ByLocation: make(map[string]uint64),
		ByScript:   make(map[string]uint64),
	}
}

// IntervalExecStats tracks per-interval execution and error statistics.
type IntervalExecStats struct {
	// Execution stats
	Executions    uint64    // Total execution count
	TotalTimeNs   uint64    // Total execution time in nanoseconds
	MinTimeNs     uint64    // Minimum execution time (valid only if Executions > 0)
	MaxTimeNs     uint64    // Maximum execution time
	SlowCount     uint64    // Executions exceeding threshold
	LastExecution time.Time // Last execution timestamp
	ObjectID      string    // Owner object ID
	EventName     string    // Event name for this interval

	// Error stats
	Errors     uint64    // Total error count
	LastError  time.Time // Last error timestamp
	ByCategory map[ErrorCategory]uint64
	ByLocation map[string]uint64 // error location string -> count

	// Rate tracking
	execRate  RateStats
	timeRate  TimeRateStats
	errorRate RateStats

	prevExecs  uint64
	prevErrors uint64
}

func newIntervalExecStats() *IntervalExecStats {
	return &IntervalExecStats{
		ByCategory: make(map[ErrorCategory]uint64),
		ByLocation: make(map[string]uint64),
	}
}

// limitCountMap ensures a count map doesn't exceed maxSize by evicting the lowest-count entry.
// This keeps the most significant (highest count) entries.
func limitCountMap(m map[string]uint64, maxSize int) {
	if len(m) < maxSize {
		return
	}
	var minKey string
	var minCount uint64 = math.MaxUint64
	for k, v := range m {
		if v < minCount {
			minKey, minCount = k, v
		}
	}
	if minKey != "" {
		delete(m, minKey)
	}
}

// JSStats tracks JavaScript execution and error statistics.
// It monitors execution times, errors per-script/object/interval, identifies slow executions,
// and provides EMA-based rate tracking.
//
// Thread safety: While the underlying caches are internally thread-safe for individual
// operations (Get, Set, etc.), we use an external mutex (mu) to ensure atomicity of
// read-modify-write sequences. Without it, concurrent goroutines could both Get() returning
// nil, both create new stat objects, and one would overwrite the other's modifications.
// The external RLock is used for read-only snapshot operations with Peek().
type JSStats struct {
	mu sync.RWMutex

	// Per-script (source path) statistics - LRU cache with TTL
	scripts cache.Cache[string, *ScriptStats]

	// Per-object statistics - LRU cache with TTL
	objects cache.Cache[string, *ObjectExecStats]

	// Per-interval statistics - LRU cache with TTL
	intervals cache.Cache[string, *IntervalExecStats]

	// Per-location error statistics - LRU cache with TTL
	locations cache.Cache[string, *LocationStats]

	// Recent notable executions circular buffer (errors + slow)
	recentRecords []ExecutionRecord
	recordIndex   int

	// Global execution counters
	totalExecs  uint64
	totalTimeNs uint64
	totalSlow   uint64
	startTime   time.Time

	// Global error counters
	totalErrors uint64
	byCategory  map[ErrorCategory]uint64

	// Global rates
	execRate   RateStats
	timeRate   TimeRateStats
	errorRate  RateStats
	prevExecs  uint64
	prevErrors uint64

	// Reference to imports resolver for import chains
	resolver *imports.Resolver

	// Last time DeleteExpired was called on caches
	lastExpiredCleanup time.Time
}

// LocationStats tracks error statistics for a code location (file:line:col).
type LocationStats struct {
	Location  string    // The location string (e.g., "/user.js:42:15")
	Count     uint64    // Total error count at this location
	FirstSeen time.Time // When this location was first seen
	LastSeen  time.Time // Most recent error at this location
}

// newStatsCache creates a new LRU cache with TTL for stats tracking.
func newStatsCache[V any](maxKeys int, ttl time.Duration) cache.Cache[string, V] {
	return cache.NewCache[string, V]().WithMaxKeys(maxKeys).WithTTL(ttl).WithLRU()
}

// NewJSStats creates a new JSStats tracker and starts the periodic
// rate update loop. The loop runs until the context is cancelled.
func NewJSStats(ctx context.Context, resolver *imports.Resolver) *JSStats {
	return NewJSStatsWithTTL(ctx, resolver, jsStatsTTL)
}

// NewJSStatsWithTTL creates a new JSStats tracker with a custom TTL.
// This is useful for testing with shorter TTL values.
func NewJSStatsWithTTL(ctx context.Context, resolver *imports.Resolver, ttl time.Duration) *JSStats {
	s := &JSStats{
		scripts:       newStatsCache[*ScriptStats](maxScripts, ttl),
		objects:       newStatsCache[*ObjectExecStats](maxObjects, ttl),
		intervals:     newStatsCache[*IntervalExecStats](maxIntervals, ttl),
		locations:     newStatsCache[*LocationStats](maxLocations, ttl),
		recentRecords: make([]ExecutionRecord, recentBufferSize),
		byCategory:    make(map[ErrorCategory]uint64),
		startTime:     time.Now(),
		resolver:      resolver,
	}
	go s.runUpdateLoop(ctx)
	return s
}

// runUpdateLoop runs the periodic rate update loop until context is cancelled.
func (s *JSStats) runUpdateLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.UpdateRates()
		}
	}
}

// IntervalExecInfo contains interval metadata for execution recording.
type IntervalExecInfo struct {
	IntervalID string
	EventName  string
}

// RecordExecution records a JavaScript execution with its duration.
// sourcePath is the source file path (e.g., "/user.js").
// objectID is the object that executed the script.
// duration is the execution time.
// intervalInfo is optional interval metadata (nil for non-interval events).
func (s *JSStats) RecordExecution(sourcePath, objectID string, duration time.Duration, intervalInfo *IntervalExecInfo) {
	// Handle edge cases
	if sourcePath == "" {
		sourcePath = "(no source)"
	}
	if duration <= 0 {
		duration = time.Nanosecond // Minimum 1ns for accounting
	}

	durationNs := uint64(duration.Nanoseconds())
	isSlow := duration >= slowExecutionThreshold

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// Update global counters
	s.totalExecs++
	s.totalTimeNs += durationNs
	if isSlow {
		s.totalSlow++
	}

	// Update per-script stats
	script, _ := s.scripts.Get(sourcePath)
	if script == nil {
		script = newScriptStats()
	}
	script.Executions++
	script.TotalTimeNs += durationNs
	script.LastExecution = now
	if script.Executions == 1 || durationNs < script.MinTimeNs {
		script.MinTimeNs = durationNs
	}
	if durationNs > script.MaxTimeNs {
		script.MaxTimeNs = durationNs
	}
	if isSlow {
		script.SlowCount++
	}
	s.scripts.Set(sourcePath, script, 0) // 0 = use cache default TTL

	// Update per-object stats
	obj, _ := s.objects.Get(objectID)
	if obj == nil {
		obj = newObjectExecStats()
	}
	obj.Executions++
	obj.TotalTimeNs += durationNs
	obj.LastExecution = now
	obj.SourcePath = sourcePath
	if obj.Executions == 1 || durationNs < obj.MinTimeNs {
		obj.MinTimeNs = durationNs
	}
	if durationNs > obj.MaxTimeNs {
		obj.MaxTimeNs = durationNs
	}
	if isSlow {
		obj.SlowCount++
	}
	s.objects.Set(objectID, obj, 0)

	// Update per-interval stats if this is an interval execution
	if intervalInfo != nil && intervalInfo.IntervalID != "" {
		intervalID := intervalInfo.IntervalID
		interval, _ := s.intervals.Get(intervalID)
		if interval == nil {
			interval = newIntervalExecStats()
		}
		interval.Executions++
		interval.TotalTimeNs += durationNs
		interval.LastExecution = now
		interval.ObjectID = objectID
		interval.EventName = intervalInfo.EventName
		if interval.Executions == 1 || durationNs < interval.MinTimeNs {
			interval.MinTimeNs = durationNs
		}
		if durationNs > interval.MaxTimeNs {
			interval.MaxTimeNs = durationNs
		}
		if isSlow {
			interval.SlowCount++
		}
		s.intervals.Set(intervalID, interval, 0)
	}

	// Record slow execution with import chain
	if isSlow {
		// Default to just the source path if resolver is nil or not cached
		importChain := []string{sourcePath}
		if s.resolver != nil {
			// Copy the slice to avoid referencing cache internals
			cached := s.resolver.GetCachedDeps(sourcePath)
			importChain = make([]string, len(cached))
			copy(importChain, cached)
		}
		var intervalID string
		if intervalInfo != nil {
			intervalID = intervalInfo.IntervalID
		}
		s.recentRecords[s.recordIndex] = ExecutionRecord{
			Timestamp:   now,
			ObjectID:    objectID,
			SourcePath:  sourcePath,
			IntervalID:  intervalID,
			Duration:    duration,
			IsError:     false,
			ImportChain: importChain,
		}
		s.recordIndex = (s.recordIndex + 1) % recentBufferSize
	}
}

// RecordError records a JavaScript execution error with its context.
// This should be called from run() when JS execution fails.
// sourcePath is the source file path (e.g., "/user.js").
// objectID is the object that executed the script.
// err is the error that occurred.
// duration is the execution time up until the error (0 if unknown, may be partial).
// intervalInfo is optional interval metadata (nil for non-interval events).
func (s *JSStats) RecordError(sourcePath, objectID string, err error, duration time.Duration, intervalInfo *IntervalExecInfo) {
	if err == nil {
		return
	}

	// Handle edge cases
	if sourcePath == "" {
		sourcePath = "(no source)"
	}

	// Classify the error
	category, location, message := classifyError(err)
	locStr := location.String()
	truncMsg := truncateMessage(message)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// Update global counters
	s.totalErrors++
	s.byCategory[category]++

	// Update per-script stats
	script, _ := s.scripts.Get(sourcePath)
	if script == nil {
		script = newScriptStats()
	}
	script.Errors++
	script.LastError = now
	script.ByCategory[category]++
	script.ByLocation[locStr]++
	limitCountMap(script.ByLocation, maxLocationsPerEntity)
	s.scripts.Set(sourcePath, script, 0)

	// Update per-object stats
	obj, _ := s.objects.Get(objectID)
	if obj == nil {
		obj = newObjectExecStats()
	}
	obj.Errors++
	obj.LastError = now
	obj.ByCategory[category]++
	obj.ByLocation[locStr]++
	limitCountMap(obj.ByLocation, maxLocationsPerEntity)
	obj.ByScript[sourcePath]++
	limitCountMap(obj.ByScript, maxLocationsPerEntity)
	obj.SourcePath = sourcePath // Update current source
	s.objects.Set(objectID, obj, 0)

	// Update per-interval stats if this is an interval execution
	var intervalID string
	if intervalInfo != nil && intervalInfo.IntervalID != "" {
		intervalID = intervalInfo.IntervalID
		interval, _ := s.intervals.Get(intervalID)
		if interval == nil {
			interval = newIntervalExecStats()
		}
		interval.Errors++
		interval.LastError = now
		interval.ByCategory[category]++
		interval.ByLocation[locStr]++
		limitCountMap(interval.ByLocation, maxLocationsPerEntity)
		interval.ObjectID = objectID
		interval.EventName = intervalInfo.EventName
		s.intervals.Set(intervalID, interval, 0)
	}

	// Update per-location stats
	loc, _ := s.locations.Get(locStr)
	if loc == nil {
		loc = &LocationStats{
			Location:  locStr,
			FirstSeen: now,
		}
	}
	loc.Count++
	loc.LastSeen = now
	s.locations.Set(locStr, loc, 0)

	// Add to recent records buffer
	s.recentRecords[s.recordIndex] = ExecutionRecord{
		Timestamp:  now,
		ObjectID:   objectID,
		SourcePath: sourcePath,
		IntervalID: intervalID,
		Duration:   duration,
		IsError:    true,
		Category:   category,
		Location:   location,
		Message:    truncMsg,
	}
	s.recordIndex = (s.recordIndex + 1) % recentBufferSize
}

// RecordLoadError records a pre-run loading error (e.g., AccessObject failure).
// This is called from loadRun() when the object cannot be loaded before JS execution.
// objectID is the object that failed to load.
// err is the error that occurred.
func (s *JSStats) RecordLoadError(objectID string, err error) {
	if err == nil {
		return
	}

	// Classify the error
	category, location, message := classifyError(err)
	locStr := location.String()
	truncMsg := truncateMessage(message)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// Update global counters
	s.totalErrors++
	s.byCategory[category]++

	// Update per-object stats (no script path for load errors)
	obj, _ := s.objects.Get(objectID)
	if obj == nil {
		obj = newObjectExecStats()
	}
	obj.Errors++
	obj.LastError = now
	obj.ByCategory[category]++
	limitCountMap(obj.ByLocation, maxLocationsPerEntity)
	obj.ByLocation[locStr]++
	s.objects.Set(objectID, obj, 0)

	// Update per-location stats
	loc, _ := s.locations.Get(locStr)
	if loc == nil {
		loc = &LocationStats{
			Location:  locStr,
			FirstSeen: now,
		}
	}
	loc.Count++
	loc.LastSeen = now
	s.locations.Set(locStr, loc, 0)

	// Add to recent records buffer
	s.recentRecords[s.recordIndex] = ExecutionRecord{
		Timestamp:  now,
		ObjectID:   objectID,
		SourcePath: "(load error)",
		Duration:   0,
		IsError:    true,
		Category:   category,
		Location:   location,
		Message:    truncMsg,
	}
	s.recordIndex = (s.recordIndex + 1) % recentBufferSize
}

// RecordBootError records a boot.js execution error.
// Boot JS doesn't go through run(), so it needs separate handling.
// err is the error that occurred during boot.js execution.
func (s *JSStats) RecordBootError(err error) {
	if err == nil {
		return
	}

	const bootSourcePath = "(boot.js)"
	const bootObjectID = "(boot)"

	// Classify the error
	category, location, message := classifyError(err)
	locStr := location.String()
	truncMsg := truncateMessage(message)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// Update global counters
	s.totalErrors++
	s.byCategory[category]++

	// Update per-script stats for boot
	script, _ := s.scripts.Get(bootSourcePath)
	if script == nil {
		script = newScriptStats()
	}
	script.Errors++
	script.LastError = now
	script.ByCategory[category]++
	limitCountMap(script.ByLocation, maxLocationsPerEntity)
	script.ByLocation[locStr]++
	s.scripts.Set(bootSourcePath, script, 0)

	// Update per-location stats
	loc, _ := s.locations.Get(locStr)
	if loc == nil {
		loc = &LocationStats{
			Location:  locStr,
			FirstSeen: now,
		}
	}
	loc.Count++
	loc.LastSeen = now
	s.locations.Set(locStr, loc, 0)

	// Add to recent records buffer
	s.recentRecords[s.recordIndex] = ExecutionRecord{
		Timestamp:  now,
		ObjectID:   bootObjectID,
		SourcePath: bootSourcePath,
		Duration:   0,
		IsError:    true,
		Category:   category,
		Location:   location,
		Message:    truncMsg,
	}
	s.recordIndex = (s.recordIndex + 1) % recentBufferSize
}

// RecordRecoveryError records an interval recovery or re-enqueue error.
// These are operational errors, not JS execution errors.
// objectID is the object the interval belongs to.
// intervalID is the interval that failed recovery.
// err is the error that occurred.
func (s *JSStats) RecordRecoveryError(objectID, intervalID string, err error) {
	if err == nil {
		return
	}

	const recoverySourcePath = "(recovery)"

	// Classify the error
	category, location, message := classifyError(err)
	locStr := location.String()
	truncMsg := truncateMessage(message)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// Update global counters
	s.totalErrors++
	s.byCategory[category]++

	// Update per-object stats
	obj, _ := s.objects.Get(objectID)
	if obj == nil {
		obj = newObjectExecStats()
	}
	obj.Errors++
	obj.LastError = now
	obj.ByCategory[category]++
	limitCountMap(obj.ByLocation, maxLocationsPerEntity)
	obj.ByLocation[locStr]++
	s.objects.Set(objectID, obj, 0)

	// Update per-interval stats
	if intervalID != "" {
		interval, _ := s.intervals.Get(intervalID)
		if interval == nil {
			interval = newIntervalExecStats()
		}
		interval.Errors++
		interval.LastError = now
		interval.ByCategory[category]++
		interval.ByLocation[locStr]++
		limitCountMap(interval.ByLocation, maxLocationsPerEntity)
		interval.ObjectID = objectID
		s.intervals.Set(intervalID, interval, 0)
	}

	// Update per-location stats
	loc, _ := s.locations.Get(locStr)
	if loc == nil {
		loc = &LocationStats{
			Location:  locStr,
			FirstSeen: now,
		}
	}
	loc.Count++
	loc.LastSeen = now
	s.locations.Set(locStr, loc, 0)

	// Add to recent records buffer
	s.recentRecords[s.recordIndex] = ExecutionRecord{
		Timestamp:  now,
		ObjectID:   objectID,
		SourcePath: recoverySourcePath,
		IntervalID: intervalID,
		Duration:   0,
		IsError:    true,
		Category:   category,
		Location:   location,
		Message:    truncMsg,
	}
	s.recordIndex = (s.recordIndex + 1) % recentBufferSize
}

// UpdateRates should be called periodically to update EMA rate calculations.
// It also triggers cleanup of expired cache entries.
func (s *JSStats) UpdateRates() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Update global rates
	execDelta := s.totalExecs - s.prevExecs
	s.execRate.update(execDelta)
	s.timeRate.update(s.totalTimeNs)
	errorDelta := s.totalErrors - s.prevErrors
	s.errorRate.update(errorDelta)
	s.prevExecs = s.totalExecs
	s.prevErrors = s.totalErrors

	// Update per-script rates
	for _, script := range s.scripts.Values() {
		execDelta := script.Executions - script.prevExecs
		script.execRate.update(execDelta)
		script.timeRate.update(script.TotalTimeNs)
		errorDelta := script.Errors - script.prevErrors
		script.errorRate.update(errorDelta)
		script.prevExecs = script.Executions
		script.prevErrors = script.Errors
	}

	// Update per-object rates
	for _, obj := range s.objects.Values() {
		execDelta := obj.Executions - obj.prevExecs
		obj.execRate.update(execDelta)
		obj.timeRate.update(obj.TotalTimeNs)
		errorDelta := obj.Errors - obj.prevErrors
		obj.errorRate.update(errorDelta)
		obj.prevExecs = obj.Executions
		obj.prevErrors = obj.Errors
	}

	// Update per-interval rates
	for _, interval := range s.intervals.Values() {
		execDelta := interval.Executions - interval.prevExecs
		interval.execRate.update(execDelta)
		interval.timeRate.update(interval.TotalTimeNs)
		errorDelta := interval.Errors - interval.prevErrors
		interval.errorRate.update(errorDelta)
		interval.prevExecs = interval.Executions
		interval.prevErrors = interval.Errors
	}

	// Periodically clean up expired cache entries
	if time.Since(s.lastExpiredCleanup) >= expiredCleanupInterval {
		s.scripts.DeleteExpired()
		s.objects.DeleteExpired()
		s.intervals.DeleteExpired()
		s.locations.DeleteExpired()
		s.lastExpiredCleanup = time.Now()
	}
}

// Snapshot types for query results

// GlobalJSSnapshot contains overall JS execution and error statistics.
type GlobalJSSnapshot struct {
	// Execution stats
	TotalExecs  uint64
	TotalTimeMs float64 // Total milliseconds of JS execution
	AvgTimeMs   float64
	TotalSlow   uint64
	SlowPercent float64
	Uptime      time.Duration
	ExecRates   RateSnapshot
	TimeRates   TimeRateSnapshot

	// Error stats
	TotalErrors  uint64
	ErrorPercent float64 // errors/executions ratio
	ByCategory   map[ErrorCategory]uint64
	ErrorRates   RateSnapshot
}

// RateSnapshot contains EMA rates for display.
// All rates are events-per-second, smoothed over different time windows.
// PerSecond is reactive (1s window), PerMinute is smoother (1m window), etc.
type RateSnapshot struct {
	PerSecond float64 // Events/second, 1s EMA window
	PerMinute float64 // Events/second, 1m EMA window
	PerHour   float64 // Events/second, 1h EMA window
	PerDay    float64 // Events/second, 24h EMA window
}

// TimeRateSnapshot contains EMA of execution time rates.
// All rates are JS-seconds per wall-second, smoothed over different time windows.
type TimeRateSnapshot struct {
	PerSecond float64 // JS sec/wall sec, 1s EMA window
	PerMinute float64 // JS sec/wall sec, 1m EMA window
	PerHour   float64 // JS sec/wall sec, 1h EMA window
	PerDay    float64 // JS sec/wall sec, 24h EMA window
}

// ScriptSnapshot contains stats for one script path.
type ScriptSnapshot struct {
	// Identity
	SourcePath string

	// Execution stats
	Executions    uint64
	AvgTimeMs     float64
	MinTimeMs     float64
	MaxTimeMs     float64
	SlowCount     uint64
	SlowPercent   float64
	LastExecution time.Time
	ImportChain   []string
	ExecRates     RateSnapshot
	TimeRates     TimeRateSnapshot

	// Error stats
	Errors       uint64
	ErrorPercent float64 // errors/executions ratio
	LastError    time.Time
	ByCategory   map[ErrorCategory]uint64
	ByLocation   map[string]uint64
	ErrorRates   RateSnapshot
}

// ObjectExecSnapshot contains execution and error stats for one object.
type ObjectExecSnapshot struct {
	// Identity
	ObjectID   string
	SourcePath string

	// Execution stats
	Executions    uint64
	AvgTimeMs     float64
	MinTimeMs     float64
	MaxTimeMs     float64
	SlowCount     uint64
	SlowPercent   float64
	LastExecution time.Time
	ExecRates     RateSnapshot
	TimeRates     TimeRateSnapshot

	// Error stats
	Errors       uint64
	ErrorPercent float64 // errors/executions ratio
	LastError    time.Time
	ByCategory   map[ErrorCategory]uint64
	ByLocation   map[string]uint64
	ByScript     map[string]uint64
	ErrorRates   RateSnapshot
}

// IntervalExecSnapshot contains execution and error stats for one interval.
type IntervalExecSnapshot struct {
	// Identity
	IntervalID string
	ObjectID   string
	EventName  string

	// Execution stats
	Executions    uint64
	AvgTimeMs     float64
	MinTimeMs     float64
	MaxTimeMs     float64
	SlowCount     uint64
	SlowPercent   float64
	LastExecution time.Time
	ExecRates     RateSnapshot
	TimeRates     TimeRateSnapshot

	// Error stats
	Errors       uint64
	ErrorPercent float64 // errors/executions ratio
	LastError    time.Time
	ByCategory   map[ErrorCategory]uint64
	ByLocation   map[string]uint64
	ErrorRates   RateSnapshot
}

// LocationSnapshot contains error stats for one code location.
type LocationSnapshot struct {
	Location  string
	Count     uint64
	FirstSeen time.Time
	LastSeen  time.Time
}

// GlobalSnapshot returns overall JS execution and error statistics.
func (s *JSStats) GlobalSnapshot() GlobalJSSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var avgTimeMs, slowPercent, errorPercent float64
	totalTimeMs := float64(s.totalTimeNs) / 1e6
	if s.totalExecs > 0 {
		avgTimeMs = totalTimeMs / float64(s.totalExecs)
		slowPercent = float64(s.totalSlow) / float64(s.totalExecs) * 100
		errorPercent = float64(s.totalErrors) / float64(s.totalExecs) * 100
	}

	// Copy category map
	byCategory := make(map[ErrorCategory]uint64, len(s.byCategory))
	for cat, count := range s.byCategory {
		byCategory[cat] = count
	}

	return GlobalJSSnapshot{
		TotalExecs:  s.totalExecs,
		TotalTimeMs: totalTimeMs,
		AvgTimeMs:   avgTimeMs,
		TotalSlow:   s.totalSlow,
		SlowPercent: slowPercent,
		Uptime:      time.Since(s.startTime),
		ExecRates: RateSnapshot{
			PerSecond: s.execRate.SecondRate,
			PerMinute: s.execRate.MinuteRate,
			PerHour:   s.execRate.HourRate,
			PerDay:    s.execRate.DayRate,
		},
		TimeRates: TimeRateSnapshot{
			PerSecond: s.timeRate.SecondRate,
			PerMinute: s.timeRate.MinuteRate,
			PerHour:   s.timeRate.HourRate,
			PerDay:    s.timeRate.DayRate,
		},
		TotalErrors:  s.totalErrors,
		ErrorPercent: errorPercent,
		ByCategory:   byCategory,
		ErrorRates: RateSnapshot{
			PerSecond: s.errorRate.SecondRate,
			PerMinute: s.errorRate.MinuteRate,
			PerHour:   s.errorRate.HourRate,
			PerDay:    s.errorRate.DayRate,
		},
	}
}

// ScriptSortField specifies how to sort script results.
type ScriptSortField int

const (
	SortScriptByTime      ScriptSortField = iota // Total execution time
	SortScriptByExecs                            // Execution count
	SortScriptBySlow                             // Slow count
	SortScriptByErrors                           // Error count
	SortScriptByErrorRate                        // Error percentage
)

// TopScripts returns the top n scripts sorted by the specified field.
func (s *JSStats) TopScripts(by ScriptSortField, n int) []ScriptSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := s.scripts.Keys()
	result := make([]ScriptSnapshot, 0, len(keys))
	for _, path := range keys {
		script, ok := s.scripts.Peek(path) // Peek to avoid updating LRU order
		if !ok {
			continue
		}
		result = append(result, s.scriptSnapshotLocked(path, script))
	}

	switch by {
	case SortScriptByTime:
		sort.Slice(result, func(i, j int) bool {
			return result[i].AvgTimeMs*float64(result[i].Executions) >
				result[j].AvgTimeMs*float64(result[j].Executions)
		})
	case SortScriptByExecs:
		sort.Slice(result, func(i, j int) bool {
			return result[i].Executions > result[j].Executions
		})
	case SortScriptBySlow:
		sort.Slice(result, func(i, j int) bool {
			return result[i].SlowCount > result[j].SlowCount
		})
	case SortScriptByErrors:
		sort.Slice(result, func(i, j int) bool {
			return result[i].Errors > result[j].Errors
		})
	case SortScriptByErrorRate:
		sort.Slice(result, func(i, j int) bool {
			return result[i].ErrorPercent > result[j].ErrorPercent
		})
	}

	if n > 0 && len(result) > n {
		result = result[:n]
	}
	return result
}

func (s *JSStats) scriptSnapshotLocked(path string, script *ScriptStats) ScriptSnapshot {
	var avgTimeMs, minTimeMs, slowPercent, errorPercent float64
	if script.Executions > 0 {
		avgTimeMs = float64(script.TotalTimeNs) / float64(script.Executions) / 1e6
		minTimeMs = float64(script.MinTimeNs) / 1e6
		slowPercent = float64(script.SlowCount) / float64(script.Executions) * 100
		errorPercent = float64(script.Errors) / float64(script.Executions) * 100
	}

	var importChain []string
	if s.resolver != nil {
		// Copy the slice to avoid referencing cache internals
		cached := s.resolver.GetCachedDeps(path)
		importChain = make([]string, len(cached))
		copy(importChain, cached)
	}

	// Copy error maps
	byCategory := make(map[ErrorCategory]uint64, len(script.ByCategory))
	for cat, count := range script.ByCategory {
		byCategory[cat] = count
	}
	byLocation := make(map[string]uint64, len(script.ByLocation))
	for loc, count := range script.ByLocation {
		byLocation[loc] = count
	}

	return ScriptSnapshot{
		SourcePath:    path,
		Executions:    script.Executions,
		AvgTimeMs:     avgTimeMs,
		MinTimeMs:     minTimeMs,
		MaxTimeMs:     float64(script.MaxTimeNs) / 1e6,
		SlowCount:     script.SlowCount,
		SlowPercent:   slowPercent,
		LastExecution: script.LastExecution,
		ImportChain:   importChain,
		ExecRates: RateSnapshot{
			PerSecond: script.execRate.SecondRate,
			PerMinute: script.execRate.MinuteRate,
			PerHour:   script.execRate.HourRate,
			PerDay:    script.execRate.DayRate,
		},
		TimeRates: TimeRateSnapshot{
			PerSecond: script.timeRate.SecondRate,
			PerMinute: script.timeRate.MinuteRate,
			PerHour:   script.timeRate.HourRate,
			PerDay:    script.timeRate.DayRate,
		},
		Errors:       script.Errors,
		ErrorPercent: errorPercent,
		LastError:    script.LastError,
		ByCategory:   byCategory,
		ByLocation:   byLocation,
		ErrorRates: RateSnapshot{
			PerSecond: script.errorRate.SecondRate,
			PerMinute: script.errorRate.MinuteRate,
			PerHour:   script.errorRate.HourRate,
			PerDay:    script.errorRate.DayRate,
		},
	}
}

// ScriptSnapshot returns stats for a specific script, or nil if not found.
func (s *JSStats) ScriptSnapshot(sourcePath string) *ScriptSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	script, ok := s.scripts.Peek(sourcePath)
	if !ok {
		return nil
	}
	snap := s.scriptSnapshotLocked(sourcePath, script)
	return &snap
}

// ObjectSortField specifies how to sort object results.
type ObjectSortField int

const (
	SortObjectByTime      ObjectSortField = iota // Total execution time
	SortObjectByExecs                            // Execution count
	SortObjectBySlow                             // Slow count
	SortObjectByErrors                           // Error count
	SortObjectByErrorRate                        // Error percentage
)

// TopObjects returns the top n objects sorted by the specified field.
func (s *JSStats) TopObjects(by ObjectSortField, n int) []ObjectExecSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := s.objects.Keys()
	result := make([]ObjectExecSnapshot, 0, len(keys))
	for _, id := range keys {
		obj, ok := s.objects.Peek(id)
		if !ok {
			continue
		}
		result = append(result, s.objectSnapshotLocked(id, obj))
	}

	switch by {
	case SortObjectByTime:
		sort.Slice(result, func(i, j int) bool {
			return result[i].AvgTimeMs*float64(result[i].Executions) >
				result[j].AvgTimeMs*float64(result[j].Executions)
		})
	case SortObjectByExecs:
		sort.Slice(result, func(i, j int) bool {
			return result[i].Executions > result[j].Executions
		})
	case SortObjectBySlow:
		sort.Slice(result, func(i, j int) bool {
			return result[i].SlowCount > result[j].SlowCount
		})
	case SortObjectByErrors:
		sort.Slice(result, func(i, j int) bool {
			return result[i].Errors > result[j].Errors
		})
	case SortObjectByErrorRate:
		sort.Slice(result, func(i, j int) bool {
			return result[i].ErrorPercent > result[j].ErrorPercent
		})
	}

	if n > 0 && len(result) > n {
		result = result[:n]
	}
	return result
}

func (s *JSStats) objectSnapshotLocked(id string, obj *ObjectExecStats) ObjectExecSnapshot {
	var avgTimeMs, minTimeMs, slowPercent, errorPercent float64
	if obj.Executions > 0 {
		avgTimeMs = float64(obj.TotalTimeNs) / float64(obj.Executions) / 1e6
		minTimeMs = float64(obj.MinTimeNs) / 1e6
		slowPercent = float64(obj.SlowCount) / float64(obj.Executions) * 100
		errorPercent = float64(obj.Errors) / float64(obj.Executions) * 100
	}

	// Copy error maps
	byCategory := make(map[ErrorCategory]uint64, len(obj.ByCategory))
	for cat, count := range obj.ByCategory {
		byCategory[cat] = count
	}
	byLocation := make(map[string]uint64, len(obj.ByLocation))
	for loc, count := range obj.ByLocation {
		byLocation[loc] = count
	}
	byScript := make(map[string]uint64, len(obj.ByScript))
	for script, count := range obj.ByScript {
		byScript[script] = count
	}

	return ObjectExecSnapshot{
		ObjectID:      id,
		SourcePath:    obj.SourcePath,
		Executions:    obj.Executions,
		AvgTimeMs:     avgTimeMs,
		MinTimeMs:     minTimeMs,
		MaxTimeMs:     float64(obj.MaxTimeNs) / 1e6,
		SlowCount:     obj.SlowCount,
		SlowPercent:   slowPercent,
		LastExecution: obj.LastExecution,
		ExecRates: RateSnapshot{
			PerSecond: obj.execRate.SecondRate,
			PerMinute: obj.execRate.MinuteRate,
			PerHour:   obj.execRate.HourRate,
			PerDay:    obj.execRate.DayRate,
		},
		TimeRates: TimeRateSnapshot{
			PerSecond: obj.timeRate.SecondRate,
			PerMinute: obj.timeRate.MinuteRate,
			PerHour:   obj.timeRate.HourRate,
			PerDay:    obj.timeRate.DayRate,
		},
		Errors:       obj.Errors,
		ErrorPercent: errorPercent,
		LastError:    obj.LastError,
		ByCategory:   byCategory,
		ByLocation:   byLocation,
		ByScript:     byScript,
		ErrorRates: RateSnapshot{
			PerSecond: obj.errorRate.SecondRate,
			PerMinute: obj.errorRate.MinuteRate,
			PerHour:   obj.errorRate.HourRate,
			PerDay:    obj.errorRate.DayRate,
		},
	}
}

// ObjectSnapshot returns stats for a specific object, or nil if not found.
func (s *JSStats) ObjectExecSnapshot(objectID string) *ObjectExecSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	obj, ok := s.objects.Peek(objectID)
	if !ok {
		return nil
	}
	snap := s.objectSnapshotLocked(objectID, obj)
	return &snap
}

// IntervalSortField specifies how to sort interval results.
type IntervalSortField int

const (
	SortIntervalByTime      IntervalSortField = iota // Total execution time
	SortIntervalByExecs                              // Execution count
	SortIntervalBySlow                               // Slow count
	SortIntervalByErrors                             // Error count
	SortIntervalByErrorRate                          // Error percentage
)

// TopIntervals returns the top n intervals sorted by the specified field.
func (s *JSStats) TopIntervals(by IntervalSortField, n int) []IntervalExecSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := s.intervals.Keys()
	result := make([]IntervalExecSnapshot, 0, len(keys))
	for _, id := range keys {
		interval, ok := s.intervals.Peek(id)
		if !ok {
			continue
		}
		result = append(result, s.intervalSnapshotLocked(id, interval))
	}

	switch by {
	case SortIntervalByTime:
		sort.Slice(result, func(i, j int) bool {
			return result[i].AvgTimeMs*float64(result[i].Executions) >
				result[j].AvgTimeMs*float64(result[j].Executions)
		})
	case SortIntervalByExecs:
		sort.Slice(result, func(i, j int) bool {
			return result[i].Executions > result[j].Executions
		})
	case SortIntervalBySlow:
		sort.Slice(result, func(i, j int) bool {
			return result[i].SlowCount > result[j].SlowCount
		})
	case SortIntervalByErrors:
		sort.Slice(result, func(i, j int) bool {
			return result[i].Errors > result[j].Errors
		})
	case SortIntervalByErrorRate:
		sort.Slice(result, func(i, j int) bool {
			return result[i].ErrorPercent > result[j].ErrorPercent
		})
	}

	if n > 0 && len(result) > n {
		result = result[:n]
	}
	return result
}

func (s *JSStats) intervalSnapshotLocked(id string, interval *IntervalExecStats) IntervalExecSnapshot {
	var avgTimeMs, minTimeMs, slowPercent, errorPercent float64
	if interval.Executions > 0 {
		avgTimeMs = float64(interval.TotalTimeNs) / float64(interval.Executions) / 1e6
		minTimeMs = float64(interval.MinTimeNs) / 1e6
		slowPercent = float64(interval.SlowCount) / float64(interval.Executions) * 100
		errorPercent = float64(interval.Errors) / float64(interval.Executions) * 100
	}

	// Copy error maps
	byCategory := make(map[ErrorCategory]uint64, len(interval.ByCategory))
	for cat, count := range interval.ByCategory {
		byCategory[cat] = count
	}
	byLocation := make(map[string]uint64, len(interval.ByLocation))
	for loc, count := range interval.ByLocation {
		byLocation[loc] = count
	}

	return IntervalExecSnapshot{
		IntervalID:    id,
		ObjectID:      interval.ObjectID,
		EventName:     interval.EventName,
		Executions:    interval.Executions,
		AvgTimeMs:     avgTimeMs,
		MinTimeMs:     minTimeMs,
		MaxTimeMs:     float64(interval.MaxTimeNs) / 1e6,
		SlowCount:     interval.SlowCount,
		SlowPercent:   slowPercent,
		LastExecution: interval.LastExecution,
		ExecRates: RateSnapshot{
			PerSecond: interval.execRate.SecondRate,
			PerMinute: interval.execRate.MinuteRate,
			PerHour:   interval.execRate.HourRate,
			PerDay:    interval.execRate.DayRate,
		},
		TimeRates: TimeRateSnapshot{
			PerSecond: interval.timeRate.SecondRate,
			PerMinute: interval.timeRate.MinuteRate,
			PerHour:   interval.timeRate.HourRate,
			PerDay:    interval.timeRate.DayRate,
		},
		Errors:       interval.Errors,
		ErrorPercent: errorPercent,
		LastError:    interval.LastError,
		ByCategory:   byCategory,
		ByLocation:   byLocation,
		ErrorRates: RateSnapshot{
			PerSecond: interval.errorRate.SecondRate,
			PerMinute: interval.errorRate.MinuteRate,
			PerHour:   interval.errorRate.HourRate,
			PerDay:    interval.errorRate.DayRate,
		},
	}
}

// TopLocations returns the top n error locations by count.
func (s *JSStats) TopLocations(n int) []LocationSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := s.locations.Keys()
	result := make([]LocationSnapshot, 0, len(keys))
	for _, key := range keys {
		loc, ok := s.locations.Peek(key)
		if !ok {
			continue
		}
		result = append(result, LocationSnapshot{
			Location:  loc.Location,
			Count:     loc.Count,
			FirstSeen: loc.FirstSeen,
			LastSeen:  loc.LastSeen,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})

	if n > 0 && len(result) > n {
		result = result[:n]
	}
	return result
}

// RecentRecords returns the n most recent notable executions (errors + slow), newest first.
// Use filter to select specific record types: nil for all, or a function like func(r *ExecutionRecord) bool { return r.IsError }.
func (s *JSStats) RecentRecords(n int, filter func(*ExecutionRecord) bool) []ExecutionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]ExecutionRecord, 0, n)
	for i := 0; i < recentBufferSize && len(result) < n; i++ {
		idx := (s.recordIndex - 1 - i + recentBufferSize) % recentBufferSize
		rec := s.recentRecords[idx]
		if rec.Timestamp.IsZero() {
			break // Empty slot, buffer not yet full
		}
		if filter == nil || filter(&rec) {
			result = append(result, rec)
		}
	}
	return result
}

// RecentSlowExecutions returns the n most recent slow executions (non-errors), newest first.
func (s *JSStats) RecentSlowExecutions(n int) []ExecutionRecord {
	return s.RecentRecords(n, func(r *ExecutionRecord) bool { return !r.IsError })
}

// RecentErrors returns the n most recent error executions, newest first.
func (s *JSStats) RecentErrors(n int) []ExecutionRecord {
	return s.RecentRecords(n, func(r *ExecutionRecord) bool { return r.IsError })
}

// Reset clears all statistics.
func (s *JSStats) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.scripts.Purge()
	s.objects.Purge()
	s.intervals.Purge()
	s.locations.Purge()
	s.recentRecords = make([]ExecutionRecord, recentBufferSize)
	s.recordIndex = 0
	s.totalExecs = 0
	s.totalTimeNs = 0
	s.totalSlow = 0
	s.totalErrors = 0
	s.byCategory = make(map[ErrorCategory]uint64)
	s.startTime = now
	s.execRate = RateStats{}
	s.timeRate = TimeRateStats{}
	s.errorRate = RateStats{}
	s.prevExecs = 0
	s.prevErrors = 0
}

// Error classification

// stackTracer is implemented by errors with stack traces (from pkg/errors).
type stackTracer interface {
	StackTrace() errors.StackTrace
}

// classifyError extracts category, location, and message from an error.
func classifyError(err error) (ErrorCategory, ErrorLocation, string) {
	if err == nil {
		return CategoryOther, ErrorLocation{}, ""
	}

	// Check for v8go.JSError
	var jsErr *v8go.JSError
	if errors.As(err, &jsErr) {
		loc := parseJSLocation(jsErr.Location)
		return CategoryJS, loc, jsErr.Message
	}

	// Check for js.ErrTimeout
	if errors.Is(err, js.ErrTimeout) {
		return CategoryTimeout, extractGoLocation(err), "JS execution timeout"
	}

	// Check for timeout/cancellation
	if errors.Is(err, context.DeadlineExceeded) {
		return CategoryTimeout, extractGoLocation(err), "execution timeout"
	}
	if errors.Is(err, context.Canceled) {
		return CategoryTimeout, extractGoLocation(err), "context canceled"
	}

	// Check for JSON errors
	var jsonSyntaxErr *json.SyntaxError
	if errors.As(err, &jsonSyntaxErr) {
		return CategoryJSON, extractGoLocation(err), err.Error()
	}
	var jsonTypeErr *json.UnmarshalTypeError
	if errors.As(err, &jsonTypeErr) {
		return CategoryJSON, extractGoLocation(err), err.Error()
	}

	// Check for storage errors
	if errors.Is(err, os.ErrNotExist) {
		return CategoryStorage, extractGoLocation(err), "not found"
	}
	if errors.Is(err, os.ErrPermission) {
		return CategoryStorage, extractGoLocation(err), "permission denied"
	}

	// Default to other
	return CategoryOther, extractGoLocation(err), err.Error()
}

// parseJSLocation parses JS error locations like "/user.js:10:5", "user.js:10",
// or Windows paths like "C:\path\file.js:10:5". Parses from right to left to
// handle colons in Windows drive letters.
func parseJSLocation(loc string) ErrorLocation {
	if loc == "" {
		return ErrorLocation{}
	}

	// Parse from right: look for :col (optional) then :line
	// Format: file:line or file:line:col
	lastColon := strings.LastIndex(loc, ":")
	if lastColon == -1 {
		// No colon, just a file name
		return ErrorLocation{File: &loc}
	}

	// Check if the part after last colon is a number
	afterLast := loc[lastColon+1:]
	num1, err1 := strconv.Atoi(afterLast)
	if err1 != nil {
		// Not a number, treat whole thing as file (e.g., "C:" alone)
		return ErrorLocation{File: &loc}
	}

	// Look for second-to-last colon
	beforeLast := loc[:lastColon]
	secondColon := strings.LastIndex(beforeLast, ":")
	if secondColon == -1 {
		// Only one colon with number: file:line
		file := beforeLast
		return ErrorLocation{File: &file, Line: &num1}
	}

	// Check if part between colons is a number
	between := beforeLast[secondColon+1:]
	num2, err2 := strconv.Atoi(between)
	if err2 != nil {
		// Second part not a number: file:line (e.g., "C:\path:10")
		file := beforeLast
		return ErrorLocation{File: &file, Line: &num1}
	}

	// Both are numbers: file:line:col
	file := beforeLast[:secondColon]
	return ErrorLocation{File: &file, Line: &num2, Column: &num1}
}

// goLocationRE parses Go stack frame strings like "file.go:123"
var goLocationRE = regexp.MustCompile(`([^/\s]+\.go):(\d+)`)

func extractGoLocation(err error) ErrorLocation {
	st, ok := err.(stackTracer)
	if !ok {
		return ErrorLocation{}
	}

	frames := st.StackTrace()
	if len(frames) == 0 {
		return ErrorLocation{}
	}

	// Get the first frame (deepest in call stack, closest to error origin)
	frameStr := fmt.Sprintf("%+s:%d", frames[0], frames[0])

	if matches := goLocationRE.FindStringSubmatch(frameStr); matches != nil {
		file := matches[1]
		line, _ := strconv.Atoi(matches[2])
		return ErrorLocation{File: &file, Line: &line}
	}

	return ErrorLocation{}
}

func truncateMessage(msg string) string {
	// Replace newlines with spaces for single-line display
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", "")

	// Truncate by runes to avoid splitting UTF-8 characters
	runes := []rune(msg)
	if len(runes) > maxErrorMessageLength {
		return string(runes[:maxErrorMessageLength-3]) + "..."
	}
	return msg
}
