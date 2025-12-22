package game

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/zond/juicemud/js/imports"
)

const (
	// slowExecutionThreshold defines executions considered "slow".
	slowExecutionThreshold = 50 * time.Millisecond
	// recentSlowBufferSize is the maximum number of recent slow executions to keep.
	recentSlowBufferSize = 10000
	// jsStatsTTL is how long to keep stats for inactive scripts/objects before eviction.
	jsStatsTTL = 7 * 24 * time.Hour
	// jsStatsEvictionInterval is how often to run the eviction check.
	jsStatsEvictionInterval = time.Hour
	// maxScripts is the maximum number of scripts to track.
	maxScripts = 1000
	// maxObjects is the maximum number of objects to track.
	maxObjects = 50000
	// maxIntervals is the maximum number of intervals to track.
	maxIntervals = 10000
)

// SlowExecutionRecord captures a single slow execution for debugging.
type SlowExecutionRecord struct {
	Timestamp   time.Time
	ObjectID    string
	SourcePath  string
	Duration    time.Duration
	ImportChain []string // Captured at record time from resolver
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

// ScriptStats tracks per-script (source path) execution statistics.
type ScriptStats struct {
	Executions    uint64    // Total execution count
	TotalTimeNs   uint64    // Total execution time in nanoseconds
	MinTimeNs     uint64    // Minimum execution time (valid only if Executions > 0)
	MaxTimeNs     uint64    // Maximum execution time
	SlowCount     uint64    // Executions exceeding threshold
	LastExecution time.Time // Last execution timestamp

	execRate RateStats     // Executions per second (reuses QueueStats type)
	timeRate TimeRateStats // Seconds of JS per second of wall time

	prevExecs   uint64
	prevTimeNs  uint64
}

func newScriptStats() *ScriptStats {
	return &ScriptStats{}
}

// ObjectExecStats tracks per-object execution statistics.
type ObjectExecStats struct {
	Executions    uint64    // Total execution count
	TotalTimeNs   uint64    // Total execution time in nanoseconds
	MinTimeNs     uint64    // Minimum execution time (valid only if Executions > 0)
	MaxTimeNs     uint64    // Maximum execution time
	SlowCount     uint64    // Executions exceeding threshold
	LastExecution time.Time // Last execution timestamp
	SourcePath    string    // Current source path for this object

	execRate RateStats
	timeRate TimeRateStats

	prevExecs  uint64
	prevTimeNs uint64
}

func newObjectExecStats() *ObjectExecStats {
	return &ObjectExecStats{}
}

// IntervalExecStats tracks per-interval execution statistics.
type IntervalExecStats struct {
	Executions    uint64    // Total execution count
	TotalTimeNs   uint64    // Total execution time in nanoseconds
	MinTimeNs     uint64    // Minimum execution time (valid only if Executions > 0)
	MaxTimeNs     uint64    // Maximum execution time
	SlowCount     uint64    // Executions exceeding threshold
	LastExecution time.Time // Last execution timestamp
	ObjectID      string    // Owner object ID
	EventName     string    // Event name for this interval

	execRate RateStats
	timeRate TimeRateStats

	prevExecs  uint64
	prevTimeNs uint64
}

func newIntervalExecStats() *IntervalExecStats {
	return &IntervalExecStats{}
}

// JSStats tracks JavaScript execution performance statistics.
// It monitors execution times per-script and per-object, identifies slow executions,
// and provides EMA-based rate tracking similar to QueueStats.
type JSStats struct {
	mu sync.RWMutex

	// Per-script (source path) statistics
	scripts map[string]*ScriptStats

	// Per-object statistics
	objects map[string]*ObjectExecStats

	// Per-interval statistics
	intervals map[string]*IntervalExecStats

	// Time-bucketed tracking for efficient eviction
	scriptBuckets   map[time.Time]map[string]struct{}
	objectBuckets   map[time.Time]map[string]struct{}
	intervalBuckets map[time.Time]map[string]struct{}
	scriptBucket    map[string]time.Time
	objectBucket    map[string]time.Time
	intervalBucket  map[string]time.Time

	// Recent slow executions circular buffer
	recentSlow []SlowExecutionRecord
	slowIndex  int

	// Global counters
	totalExecs  uint64
	totalTimeNs uint64
	totalSlow   uint64
	startTime   time.Time

	// Global rates
	execRate   RateStats
	timeRate   TimeRateStats
	prevExecs  uint64
	prevTimeNs uint64

	// Eviction tracking
	lastEviction time.Time

	// Reference to imports resolver for import chains
	resolver *imports.Resolver
}

// NewJSStats creates a new JSStats tracker and starts the periodic
// rate update loop. The loop runs until the context is cancelled.
func NewJSStats(ctx context.Context, resolver *imports.Resolver) *JSStats {
	now := time.Now()
	s := &JSStats{
		scripts:         make(map[string]*ScriptStats),
		objects:         make(map[string]*ObjectExecStats),
		intervals:       make(map[string]*IntervalExecStats),
		scriptBuckets:   make(map[time.Time]map[string]struct{}),
		objectBuckets:   make(map[time.Time]map[string]struct{}),
		intervalBuckets: make(map[time.Time]map[string]struct{}),
		scriptBucket:    make(map[string]time.Time),
		objectBucket:    make(map[string]time.Time),
		intervalBucket:  make(map[string]time.Time),
		recentSlow:      make([]SlowExecutionRecord, recentSlowBufferSize),
		startTime:       now,
		lastEviction:    now,
		resolver:        resolver,
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
	script := s.scripts[sourcePath]
	if script == nil {
		// Check memory limit
		if len(s.scripts) >= maxScripts {
			s.evictOldestScriptLocked()
		}
		script = newScriptStats()
		s.scripts[sourcePath] = script
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
	s.touchScriptLocked(sourcePath, now)

	// Update per-object stats
	obj := s.objects[objectID]
	if obj == nil {
		// Check memory limit
		if len(s.objects) >= maxObjects {
			s.evictOldestObjectLocked()
		}
		obj = newObjectExecStats()
		s.objects[objectID] = obj
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
	s.touchObjectLocked(objectID, now)

	// Update per-interval stats if this is an interval execution
	if intervalInfo != nil && intervalInfo.IntervalID != "" {
		intervalID := intervalInfo.IntervalID
		interval := s.intervals[intervalID]
		if interval == nil {
			// Check memory limit
			if len(s.intervals) >= maxIntervals {
				s.evictOldestIntervalLocked()
			}
			interval = newIntervalExecStats()
			s.intervals[intervalID] = interval
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
		s.touchIntervalLocked(intervalID, now)
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
		s.recentSlow[s.slowIndex] = SlowExecutionRecord{
			Timestamp:   now,
			ObjectID:    objectID,
			SourcePath:  sourcePath,
			Duration:    duration,
			ImportChain: importChain,
		}
		s.slowIndex = (s.slowIndex + 1) % recentSlowBufferSize
	}
}

// touchScriptLocked updates the time bucket for a script. Must be called with s.mu held.
func (s *JSStats) touchScriptLocked(sourcePath string, now time.Time) {
	bucket := now.Truncate(time.Hour)
	oldBucket, exists := s.scriptBucket[sourcePath]

	if exists && oldBucket == bucket {
		return
	}

	if exists {
		if oldSet := s.scriptBuckets[oldBucket]; oldSet != nil {
			delete(oldSet, sourcePath)
			if len(oldSet) == 0 {
				delete(s.scriptBuckets, oldBucket)
			}
		}
	}

	newSet := s.scriptBuckets[bucket]
	if newSet == nil {
		newSet = make(map[string]struct{})
		s.scriptBuckets[bucket] = newSet
	}
	newSet[sourcePath] = struct{}{}
	s.scriptBucket[sourcePath] = bucket
}

// touchObjectLocked updates the time bucket for an object. Must be called with s.mu held.
func (s *JSStats) touchObjectLocked(objectID string, now time.Time) {
	bucket := now.Truncate(time.Hour)
	oldBucket, exists := s.objectBucket[objectID]

	if exists && oldBucket == bucket {
		return
	}

	if exists {
		if oldSet := s.objectBuckets[oldBucket]; oldSet != nil {
			delete(oldSet, objectID)
			if len(oldSet) == 0 {
				delete(s.objectBuckets, oldBucket)
			}
		}
	}

	newSet := s.objectBuckets[bucket]
	if newSet == nil {
		newSet = make(map[string]struct{})
		s.objectBuckets[bucket] = newSet
	}
	newSet[objectID] = struct{}{}
	s.objectBucket[objectID] = bucket
}

// touchIntervalLocked updates the time bucket for an interval. Must be called with s.mu held.
func (s *JSStats) touchIntervalLocked(intervalID string, now time.Time) {
	bucket := now.Truncate(time.Hour)
	oldBucket, exists := s.intervalBucket[intervalID]

	if exists && oldBucket == bucket {
		return
	}

	if exists {
		if oldSet := s.intervalBuckets[oldBucket]; oldSet != nil {
			delete(oldSet, intervalID)
			if len(oldSet) == 0 {
				delete(s.intervalBuckets, oldBucket)
			}
		}
	}

	newSet := s.intervalBuckets[bucket]
	if newSet == nil {
		newSet = make(map[string]struct{})
		s.intervalBuckets[bucket] = newSet
	}
	newSet[intervalID] = struct{}{}
	s.intervalBucket[intervalID] = bucket
}

// evictOldestScriptLocked removes the oldest script entry. Must be called with s.mu held.
func (s *JSStats) evictOldestScriptLocked() {
	var oldestBucket time.Time
	for bucket := range s.scriptBuckets {
		if oldestBucket.IsZero() || bucket.Before(oldestBucket) {
			oldestBucket = bucket
		}
	}
	if !oldestBucket.IsZero() {
		if paths := s.scriptBuckets[oldestBucket]; paths != nil {
			for path := range paths {
				delete(s.scripts, path)
				delete(s.scriptBucket, path)
				delete(paths, path)
				if len(paths) == 0 {
					delete(s.scriptBuckets, oldestBucket)
				}
				break // Just remove one
			}
		}
	}
}

// evictOldestObjectLocked removes the oldest object entry. Must be called with s.mu held.
func (s *JSStats) evictOldestObjectLocked() {
	var oldestBucket time.Time
	for bucket := range s.objectBuckets {
		if oldestBucket.IsZero() || bucket.Before(oldestBucket) {
			oldestBucket = bucket
		}
	}
	if !oldestBucket.IsZero() {
		if ids := s.objectBuckets[oldestBucket]; ids != nil {
			for id := range ids {
				delete(s.objects, id)
				delete(s.objectBucket, id)
				delete(ids, id)
				if len(ids) == 0 {
					delete(s.objectBuckets, oldestBucket)
				}
				break // Just remove one
			}
		}
	}
}

// evictOldestIntervalLocked removes the oldest interval entry. Must be called with s.mu held.
func (s *JSStats) evictOldestIntervalLocked() {
	var oldestBucket time.Time
	for bucket := range s.intervalBuckets {
		if oldestBucket.IsZero() || bucket.Before(oldestBucket) {
			oldestBucket = bucket
		}
	}
	if !oldestBucket.IsZero() {
		if ids := s.intervalBuckets[oldestBucket]; ids != nil {
			for id := range ids {
				delete(s.intervals, id)
				delete(s.intervalBucket, id)
				delete(ids, id)
				if len(ids) == 0 {
					delete(s.intervalBuckets, oldestBucket)
				}
				break // Just remove one
			}
		}
	}
}

// UpdateRates should be called periodically to update EMA rate calculations.
// It also triggers eviction of stale entries if enough time has passed.
func (s *JSStats) UpdateRates() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// Update global rates
	execDelta := s.totalExecs - s.prevExecs
	s.execRate.update(execDelta)
	s.timeRate.update(s.totalTimeNs)
	s.prevExecs = s.totalExecs
	s.prevTimeNs = s.totalTimeNs

	// Update per-script rates
	for _, script := range s.scripts {
		execDelta := script.Executions - script.prevExecs
		script.execRate.update(execDelta)
		script.timeRate.update(script.TotalTimeNs)
		script.prevExecs = script.Executions
		script.prevTimeNs = script.TotalTimeNs
	}

	// Update per-object rates
	for _, obj := range s.objects {
		execDelta := obj.Executions - obj.prevExecs
		obj.execRate.update(execDelta)
		obj.timeRate.update(obj.TotalTimeNs)
		obj.prevExecs = obj.Executions
		obj.prevTimeNs = obj.TotalTimeNs
	}

	// Update per-interval rates
	for _, interval := range s.intervals {
		execDelta := interval.Executions - interval.prevExecs
		interval.execRate.update(execDelta)
		interval.timeRate.update(interval.TotalTimeNs)
		interval.prevExecs = interval.Executions
		interval.prevTimeNs = interval.TotalTimeNs
	}

	// Run eviction periodically
	if now.Sub(s.lastEviction) >= jsStatsEvictionInterval {
		s.evictStaleLocked(now)
		s.lastEviction = now
	}
}

// evictStaleLocked removes entries that haven't been updated within jsStatsTTL.
// Must be called with s.mu held.
func (s *JSStats) evictStaleLocked(now time.Time) {
	cutoff := now.Add(-jsStatsTTL).Truncate(time.Hour)

	// Evict stale script buckets
	for bucket, paths := range s.scriptBuckets {
		if bucket.Before(cutoff) {
			for path := range paths {
				delete(s.scripts, path)
				delete(s.scriptBucket, path)
			}
			delete(s.scriptBuckets, bucket)
		}
	}

	// Evict stale object buckets
	for bucket, ids := range s.objectBuckets {
		if bucket.Before(cutoff) {
			for id := range ids {
				delete(s.objects, id)
				delete(s.objectBucket, id)
			}
			delete(s.objectBuckets, bucket)
		}
	}

	// Evict stale interval buckets
	for bucket, ids := range s.intervalBuckets {
		if bucket.Before(cutoff) {
			for id := range ids {
				delete(s.intervals, id)
				delete(s.intervalBucket, id)
			}
			delete(s.intervalBuckets, bucket)
		}
	}
}

// Snapshot types for query results

// GlobalJSSnapshot contains overall JS execution statistics.
type GlobalJSSnapshot struct {
	TotalExecs  uint64
	TotalTimeMs float64 // Total milliseconds of JS execution
	AvgTimeMs   float64
	TotalSlow   uint64
	SlowPercent float64
	Uptime      time.Duration
	ExecRates   RateSnapshot
	TimeRates   TimeRateSnapshot
}

// TimeRateSnapshot contains EMA of execution time rates.
type TimeRateSnapshot struct {
	PerSecond float64 // Seconds of JS per second of wall time
	PerMinute float64 // Seconds of JS per minute of wall time
	PerHour   float64 // Seconds of JS per hour of wall time
	PerDay    float64 // Seconds of JS per day of wall time
}

// ScriptSnapshot contains stats for one script path.
type ScriptSnapshot struct {
	SourcePath    string
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
}

// ObjectExecSnapshot contains execution stats for one object.
type ObjectExecSnapshot struct {
	ObjectID      string
	SourcePath    string
	Executions    uint64
	AvgTimeMs     float64
	MinTimeMs     float64
	MaxTimeMs     float64
	SlowCount     uint64
	SlowPercent   float64
	LastExecution time.Time
	ExecRates     RateSnapshot
	TimeRates     TimeRateSnapshot
}

// IntervalExecSnapshot contains execution stats for one interval.
type IntervalExecSnapshot struct {
	IntervalID    string
	ObjectID      string
	EventName     string
	Executions    uint64
	AvgTimeMs     float64
	MinTimeMs     float64
	MaxTimeMs     float64
	SlowCount     uint64
	SlowPercent   float64
	LastExecution time.Time
	ExecRates     RateSnapshot
	TimeRates     TimeRateSnapshot
}

// GlobalSnapshot returns overall JS execution statistics.
func (s *JSStats) GlobalSnapshot() GlobalJSSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var avgTimeMs, slowPercent float64
	totalTimeMs := float64(s.totalTimeNs) / 1e6
	if s.totalExecs > 0 {
		avgTimeMs = totalTimeMs / float64(s.totalExecs)
		slowPercent = float64(s.totalSlow) / float64(s.totalExecs) * 100
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
			PerMinute: s.execRate.MinuteRate * 60,
			PerHour:   s.execRate.HourRate * 3600,
			PerDay:    s.execRate.DayRate * 86400,
		},
		TimeRates: TimeRateSnapshot{
			PerSecond: s.timeRate.SecondRate,
			PerMinute: s.timeRate.MinuteRate * 60,
			PerHour:   s.timeRate.HourRate * 3600,
			PerDay:    s.timeRate.DayRate * 86400,
		},
	}
}

// ScriptSortField specifies how to sort script results.
type ScriptSortField int

const (
	SortScriptByTime ScriptSortField = iota
	SortScriptByExecs
	SortScriptBySlow
)

// TopScripts returns the top n scripts sorted by the specified field.
func (s *JSStats) TopScripts(by ScriptSortField, n int) []ScriptSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]ScriptSnapshot, 0, len(s.scripts))
	for path, script := range s.scripts {
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
	}

	if n > 0 && len(result) > n {
		result = result[:n]
	}
	return result
}

func (s *JSStats) scriptSnapshotLocked(path string, script *ScriptStats) ScriptSnapshot {
	var avgTimeMs, minTimeMs, slowPercent float64
	if script.Executions > 0 {
		avgTimeMs = float64(script.TotalTimeNs) / float64(script.Executions) / 1e6
		minTimeMs = float64(script.MinTimeNs) / 1e6
		slowPercent = float64(script.SlowCount) / float64(script.Executions) * 100
	}

	var importChain []string
	if s.resolver != nil {
		// Copy the slice to avoid referencing cache internals
		cached := s.resolver.GetCachedDeps(path)
		importChain = make([]string, len(cached))
		copy(importChain, cached)
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
			PerMinute: script.execRate.MinuteRate * 60,
			PerHour:   script.execRate.HourRate * 3600,
			PerDay:    script.execRate.DayRate * 86400,
		},
		TimeRates: TimeRateSnapshot{
			PerSecond: script.timeRate.SecondRate,
			PerMinute: script.timeRate.MinuteRate * 60,
			PerHour:   script.timeRate.HourRate * 3600,
			PerDay:    script.timeRate.DayRate * 86400,
		},
	}
}

// ScriptSnapshot returns stats for a specific script, or nil if not found.
func (s *JSStats) ScriptSnapshot(sourcePath string) *ScriptSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	script := s.scripts[sourcePath]
	if script == nil {
		return nil
	}
	snap := s.scriptSnapshotLocked(sourcePath, script)
	return &snap
}

// ObjectSortField specifies how to sort object results.
type ObjectSortField int

const (
	SortObjectByTime ObjectSortField = iota
	SortObjectByExecs
	SortObjectBySlow
)

// TopObjects returns the top n objects sorted by the specified field.
func (s *JSStats) TopObjects(by ObjectSortField, n int) []ObjectExecSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]ObjectExecSnapshot, 0, len(s.objects))
	for id, obj := range s.objects {
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
	}

	if n > 0 && len(result) > n {
		result = result[:n]
	}
	return result
}

func (s *JSStats) objectSnapshotLocked(id string, obj *ObjectExecStats) ObjectExecSnapshot {
	var avgTimeMs, minTimeMs, slowPercent float64
	if obj.Executions > 0 {
		avgTimeMs = float64(obj.TotalTimeNs) / float64(obj.Executions) / 1e6
		minTimeMs = float64(obj.MinTimeNs) / 1e6
		slowPercent = float64(obj.SlowCount) / float64(obj.Executions) * 100
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
			PerMinute: obj.execRate.MinuteRate * 60,
			PerHour:   obj.execRate.HourRate * 3600,
			PerDay:    obj.execRate.DayRate * 86400,
		},
		TimeRates: TimeRateSnapshot{
			PerSecond: obj.timeRate.SecondRate,
			PerMinute: obj.timeRate.MinuteRate * 60,
			PerHour:   obj.timeRate.HourRate * 3600,
			PerDay:    obj.timeRate.DayRate * 86400,
		},
	}
}

// ObjectSnapshot returns stats for a specific object, or nil if not found.
func (s *JSStats) ObjectExecSnapshot(objectID string) *ObjectExecSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	obj := s.objects[objectID]
	if obj == nil {
		return nil
	}
	snap := s.objectSnapshotLocked(objectID, obj)
	return &snap
}

// IntervalSortField specifies how to sort interval results.
type IntervalSortField int

const (
	SortIntervalByTime IntervalSortField = iota
	SortIntervalByExecs
	SortIntervalBySlow
)

// TopIntervals returns the top n intervals sorted by the specified field.
func (s *JSStats) TopIntervals(by IntervalSortField, n int) []IntervalExecSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]IntervalExecSnapshot, 0, len(s.intervals))
	for id, interval := range s.intervals {
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
	}

	if n > 0 && len(result) > n {
		result = result[:n]
	}
	return result
}

func (s *JSStats) intervalSnapshotLocked(id string, interval *IntervalExecStats) IntervalExecSnapshot {
	var avgTimeMs, minTimeMs, slowPercent float64
	if interval.Executions > 0 {
		avgTimeMs = float64(interval.TotalTimeNs) / float64(interval.Executions) / 1e6
		minTimeMs = float64(interval.MinTimeNs) / 1e6
		slowPercent = float64(interval.SlowCount) / float64(interval.Executions) * 100
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
			PerMinute: interval.execRate.MinuteRate * 60,
			PerHour:   interval.execRate.HourRate * 3600,
			PerDay:    interval.execRate.DayRate * 86400,
		},
		TimeRates: TimeRateSnapshot{
			PerSecond: interval.timeRate.SecondRate,
			PerMinute: interval.timeRate.MinuteRate * 60,
			PerHour:   interval.timeRate.HourRate * 3600,
			PerDay:    interval.timeRate.DayRate * 86400,
		},
	}
}

// RecentSlowExecutions returns the n most recent slow executions, newest first.
func (s *JSStats) RecentSlowExecutions(n int) []SlowExecutionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]SlowExecutionRecord, 0, n)
	for i := 0; i < recentSlowBufferSize && len(result) < n; i++ {
		idx := (s.slowIndex - 1 - i + recentSlowBufferSize) % recentSlowBufferSize
		rec := s.recentSlow[idx]
		if rec.Timestamp.IsZero() {
			break // Empty slot, buffer not yet full
		}
		result = append(result, rec)
	}
	return result
}

// Reset clears all statistics.
func (s *JSStats) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.scripts = make(map[string]*ScriptStats)
	s.objects = make(map[string]*ObjectExecStats)
	s.intervals = make(map[string]*IntervalExecStats)
	s.scriptBuckets = make(map[time.Time]map[string]struct{})
	s.objectBuckets = make(map[time.Time]map[string]struct{})
	s.intervalBuckets = make(map[time.Time]map[string]struct{})
	s.scriptBucket = make(map[string]time.Time)
	s.objectBucket = make(map[string]time.Time)
	s.intervalBucket = make(map[string]time.Time)
	s.recentSlow = make([]SlowExecutionRecord, recentSlowBufferSize)
	s.slowIndex = 0
	s.totalExecs = 0
	s.totalTimeNs = 0
	s.totalSlow = 0
	s.startTime = now
	s.lastEviction = now
	s.execRate = RateStats{}
	s.timeRate = TimeRateStats{}
	s.prevExecs = 0
	s.prevTimeNs = 0
}
