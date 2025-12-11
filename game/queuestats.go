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

	"github.com/pkg/errors"
	"rogchap.com/v8go"
)

const (
	// recentErrorsBufferSize is the maximum number of recent errors to keep for debugging.
	recentErrorsBufferSize = 10000
	// maxErrorMessageLength is the maximum length of error messages stored in recent errors.
	maxErrorMessageLength = 128
	// statsTTL is how long to keep stats for inactive objects/locations before eviction.
	statsTTL = 7 * 24 * time.Hour // 7 days
	// evictionInterval is how often to run the eviction check.
	evictionInterval = time.Hour
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

// ErrorRecord captures a single error occurrence with full context.
type ErrorRecord struct {
	Timestamp time.Time
	ObjectID  string
	Category  ErrorCategory
	Location  ErrorLocation
	Message   string // Truncated error message
}

// ErrorStats holds aggregate statistics for a particular dimension.
type ErrorStats struct {
	Count     uint64
	FirstSeen time.Time
	LastSeen  time.Time
}

// RateStats tracks exponential moving averages for error/event rates.
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

// ObjectStats tracks per-object event and error statistics.
// "Events" counts all event executions (attempts), while "Errors" counts failed executions.
// Successful executions = Events - Errors.
type ObjectStats struct {
	Events     uint64    // Total event execution attempts
	Errors     uint64    // Failed executions
	LastEvent  time.Time // Last event attempt (used for TTL eviction)
	LastError  time.Time
	ByCategory map[ErrorCategory]uint64
	ByLocation map[string]uint64 // location string -> count

	eventRate RateStats
	errorRate RateStats
	// Track previous counts for rate calculation
	prevEvents uint64
	prevErrors uint64
}

func newObjectStats() *ObjectStats {
	return &ObjectStats{
		ByCategory: make(map[ErrorCategory]uint64),
		ByLocation: make(map[string]uint64),
	}
}

// QueueStats tracks in-memory statistics for queue event processing.
// It tracks both event execution attempts and errors, with per-object granularity.
// Stale entries (no activity for statsTTL) are automatically evicted.
type QueueStats struct {
	mu sync.RWMutex

	// Per-object statistics
	objects map[string]*ObjectStats

	// Global histograms
	byCategory map[ErrorCategory]*ErrorStats
	byLocation map[string]*ErrorStats // location string -> stats

	// Time-bucketed tracking for efficient eviction (hour -> set of keys).
	// When an entry is updated, it's moved to the current hour's bucket.
	// Eviction deletes all entries in buckets older than statsTTL.
	objectBuckets   map[time.Time]map[string]struct{}
	locationBuckets map[time.Time]map[string]struct{}

	// Track which bucket each entry is currently in (for efficient moves)
	objectBucket   map[string]time.Time
	locationBucket map[string]time.Time

	// Recent errors circular buffer for debugging
	recentErrors []ErrorRecord
	recentIndex  int

	// Global counters (these are cumulative and never reset by eviction)
	totalEvents uint64
	totalErrors uint64
	startTime   time.Time

	// Global rates
	eventRate  RateStats
	errorRate  RateStats
	prevEvents uint64
	prevErrors uint64

	// Eviction tracking
	lastEviction time.Time

	// Lifecycle
	closeCh   chan struct{}
	closeOnce sync.Once
}

// NewQueueStats creates a new QueueStats tracker.
func NewQueueStats() *QueueStats {
	now := time.Now()
	return &QueueStats{
		objects:         make(map[string]*ObjectStats),
		byCategory:      make(map[ErrorCategory]*ErrorStats),
		byLocation:      make(map[string]*ErrorStats),
		objectBuckets:   make(map[time.Time]map[string]struct{}),
		locationBuckets: make(map[time.Time]map[string]struct{}),
		objectBucket:    make(map[string]time.Time),
		locationBucket:  make(map[string]time.Time),
		recentErrors:    make([]ErrorRecord, recentErrorsBufferSize),
		startTime:       now,
		lastEviction:    now,
		closeCh:         make(chan struct{}),
	}
}

// Start begins the periodic rate update loop. Should be called in a goroutine.
func (q *QueueStats) Start() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-q.closeCh:
			return
		case <-ticker.C:
			q.UpdateRates()
		}
	}
}

// Close stops the periodic rate update loop. Safe to call multiple times.
func (q *QueueStats) Close() {
	q.closeOnce.Do(func() {
		close(q.closeCh)
	})
}

// RecordEvent records an event execution attempt for an object.
// This should be called for every event processed, regardless of success or failure.
// The error rate is calculated as Errors/Events, representing the failure percentage.
func (q *QueueStats) RecordEvent(objectID string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.totalEvents++
	now := time.Now()

	s := q.objects[objectID]
	if s == nil {
		s = newObjectStats()
		q.objects[objectID] = s
	}
	s.Events++
	s.LastEvent = now

	// Update time bucket for efficient eviction
	q.touchObjectLocked(objectID, now)
}

// touchObjectLocked updates the time bucket for an object. Must be called with q.mu held.
func (q *QueueStats) touchObjectLocked(objectID string, now time.Time) {
	bucket := now.Truncate(time.Hour)
	oldBucket, exists := q.objectBucket[objectID]

	// If already in the current bucket, nothing to do
	if exists && oldBucket == bucket {
		return
	}

	// Remove from old bucket if exists
	if exists {
		if oldSet := q.objectBuckets[oldBucket]; oldSet != nil {
			delete(oldSet, objectID)
			if len(oldSet) == 0 {
				delete(q.objectBuckets, oldBucket)
			}
		}
	}

	// Add to new bucket
	newSet := q.objectBuckets[bucket]
	if newSet == nil {
		newSet = make(map[string]struct{})
		q.objectBuckets[bucket] = newSet
	}
	newSet[objectID] = struct{}{}
	q.objectBucket[objectID] = bucket
}

// touchLocationLocked updates the time bucket for a location. Must be called with q.mu held.
func (q *QueueStats) touchLocationLocked(location string, now time.Time) {
	bucket := now.Truncate(time.Hour)
	oldBucket, exists := q.locationBucket[location]

	// If already in the current bucket, nothing to do
	if exists && oldBucket == bucket {
		return
	}

	// Remove from old bucket if exists
	if exists {
		if oldSet := q.locationBuckets[oldBucket]; oldSet != nil {
			delete(oldSet, location)
			if len(oldSet) == 0 {
				delete(q.locationBuckets, oldBucket)
			}
		}
	}

	// Add to new bucket
	newSet := q.locationBuckets[bucket]
	if newSet == nil {
		newSet = make(map[string]struct{})
		q.locationBuckets[bucket] = newSet
	}
	newSet[location] = struct{}{}
	q.locationBucket[location] = bucket
}

// RecordError records a failed event execution for an object.
// This should be called when an event execution fails, after RecordEvent was called.
func (q *QueueStats) RecordError(objectID string, err error) {
	category, location, message := classifyError(err)

	q.mu.Lock()
	defer q.mu.Unlock()

	q.totalErrors++
	now := time.Now()
	locStr := location.String()

	// Update per-object stats
	s := q.objects[objectID]
	if s == nil {
		s = newObjectStats()
		q.objects[objectID] = s
	}
	s.Errors++
	s.LastError = now
	s.ByCategory[category]++
	s.ByLocation[locStr]++

	// Update time buckets for efficient eviction
	q.touchObjectLocked(objectID, now)
	q.touchLocationLocked(locStr, now)

	// Update global category stats
	cs := q.byCategory[category]
	if cs == nil {
		cs = &ErrorStats{FirstSeen: now}
		q.byCategory[category] = cs
	}
	cs.Count++
	cs.LastSeen = now

	// Update global location stats
	ls := q.byLocation[locStr]
	if ls == nil {
		ls = &ErrorStats{FirstSeen: now}
		q.byLocation[locStr] = ls
	}
	ls.Count++
	ls.LastSeen = now

	// Add to recent errors buffer
	q.recentErrors[q.recentIndex] = ErrorRecord{
		Timestamp: now,
		ObjectID:  objectID,
		Category:  category,
		Location:  location,
		Message:   truncateMessage(message),
	}
	q.recentIndex = (q.recentIndex + 1) % recentErrorsBufferSize
}

// UpdateRates should be called periodically to update EMA rate calculations.
// It also triggers eviction of stale entries if enough time has passed.
func (q *QueueStats) UpdateRates() {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()

	// Update global rates
	eventDelta := q.totalEvents - q.prevEvents
	errorDelta := q.totalErrors - q.prevErrors
	q.eventRate.update(eventDelta)
	q.errorRate.update(errorDelta)
	q.prevEvents = q.totalEvents
	q.prevErrors = q.totalErrors

	// Update per-object rates
	for _, s := range q.objects {
		eventDelta := s.Events - s.prevEvents
		errorDelta := s.Errors - s.prevErrors
		s.eventRate.update(eventDelta)
		s.errorRate.update(errorDelta)
		s.prevEvents = s.Events
		s.prevErrors = s.Errors
	}

	// Run eviction periodically
	if now.Sub(q.lastEviction) >= evictionInterval {
		q.evictStaleLocked(now)
		q.lastEviction = now
	}
}

// evictStaleLocked removes entries that haven't been updated within statsTTL.
// Uses time-bucketed tracking for O(expired buckets) complexity instead of O(total entries).
// Must be called with q.mu held.
func (q *QueueStats) evictStaleLocked(now time.Time) {
	cutoff := now.Add(-statsTTL).Truncate(time.Hour)

	// Evict stale object buckets
	for bucket, ids := range q.objectBuckets {
		if bucket.Before(cutoff) {
			for id := range ids {
				delete(q.objects, id)
				delete(q.objectBucket, id)
			}
			delete(q.objectBuckets, bucket)
		}
	}

	// Evict stale location buckets
	for bucket, locs := range q.locationBuckets {
		if bucket.Before(cutoff) {
			for loc := range locs {
				delete(q.byLocation, loc)
				delete(q.locationBucket, loc)
			}
			delete(q.locationBuckets, bucket)
		}
	}

	// Note: byCategory is not evicted since there are only 5 categories
	// and they represent useful historical data
}

// Snapshot types for query results

// GlobalSnapshot contains overall queue statistics.
type GlobalSnapshot struct {
	TotalEvents uint64
	TotalErrors uint64
	ErrorRate   float64 // errors/events ratio
	Uptime      time.Duration
	EventRates  RateSnapshot
	ErrorRates  RateSnapshot
}

// RateSnapshot contains EMA rates at different time windows.
type RateSnapshot struct {
	PerSecond float64
	PerMinute float64
	PerHour   float64
	PerDay    float64
}

// CategorySnapshot contains stats for one error category.
type CategorySnapshot struct {
	Category  ErrorCategory
	Count     uint64
	FirstSeen time.Time
	LastSeen  time.Time
}

// LocationSnapshot contains stats for one error location.
type LocationSnapshot struct {
	Location  string
	Count     uint64
	FirstSeen time.Time
	LastSeen  time.Time
}

// ObjectSnapshot contains stats for one object.
type ObjectSnapshot struct {
	ObjectID   string
	Events     uint64
	Errors     uint64
	ErrorRate  float64 // errors/events ratio
	LastEvent  time.Time
	LastError  time.Time
	ByCategory map[ErrorCategory]uint64
	ByLocation map[string]uint64
	EventRates RateSnapshot
	ErrorRates RateSnapshot
}

// GlobalSnapshot returns overall queue statistics.
func (q *QueueStats) GlobalSnapshot() GlobalSnapshot {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var errorRate float64
	if q.totalEvents > 0 {
		errorRate = float64(q.totalErrors) / float64(q.totalEvents)
	}

	return GlobalSnapshot{
		TotalEvents: q.totalEvents,
		TotalErrors: q.totalErrors,
		ErrorRate:   errorRate,
		Uptime:      time.Since(q.startTime),
		EventRates: RateSnapshot{
			PerSecond: q.eventRate.SecondRate,
			PerMinute: q.eventRate.MinuteRate * 60,
			PerHour:   q.eventRate.HourRate * 3600,
			PerDay:    q.eventRate.DayRate * 86400,
		},
		ErrorRates: RateSnapshot{
			PerSecond: q.errorRate.SecondRate,
			PerMinute: q.errorRate.MinuteRate * 60,
			PerHour:   q.errorRate.HourRate * 3600,
			PerDay:    q.errorRate.DayRate * 86400,
		},
	}
}

// TopCategories returns all categories sorted by error count descending.
func (q *QueueStats) TopCategories() []CategorySnapshot {
	q.mu.RLock()
	defer q.mu.RUnlock()

	result := make([]CategorySnapshot, 0, len(q.byCategory))
	for cat, stats := range q.byCategory {
		result = append(result, CategorySnapshot{
			Category:  cat,
			Count:     stats.Count,
			FirstSeen: stats.FirstSeen,
			LastSeen:  stats.LastSeen,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})
	return result
}

// TopLocations returns the top n error locations by count.
func (q *QueueStats) TopLocations(n int) []LocationSnapshot {
	q.mu.RLock()
	defer q.mu.RUnlock()

	result := make([]LocationSnapshot, 0, len(q.byLocation))
	for loc, stats := range q.byLocation {
		result = append(result, LocationSnapshot{
			Location:  loc,
			Count:     stats.Count,
			FirstSeen: stats.FirstSeen,
			LastSeen:  stats.LastSeen,
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

// ObjectSnapshot returns stats for a specific object, or nil if not found.
func (q *QueueStats) ObjectSnapshot(objectID string) *ObjectSnapshot {
	q.mu.RLock()
	defer q.mu.RUnlock()

	s := q.objects[objectID]
	if s == nil {
		return nil
	}
	return q.objectSnapshotLocked(objectID, s)
}

func (q *QueueStats) objectSnapshotLocked(objectID string, s *ObjectStats) *ObjectSnapshot {
	var errorRate float64
	if s.Events > 0 {
		errorRate = float64(s.Errors) / float64(s.Events)
	}

	byCategory := make(map[ErrorCategory]uint64, len(s.ByCategory))
	for k, v := range s.ByCategory {
		byCategory[k] = v
	}
	byLocation := make(map[string]uint64, len(s.ByLocation))
	for k, v := range s.ByLocation {
		byLocation[k] = v
	}

	return &ObjectSnapshot{
		ObjectID:   objectID,
		Events:     s.Events,
		Errors:     s.Errors,
		ErrorRate:  errorRate,
		LastEvent:  s.LastEvent,
		LastError:  s.LastError,
		ByCategory: byCategory,
		ByLocation: byLocation,
		EventRates: RateSnapshot{
			PerSecond: s.eventRate.SecondRate,
			PerMinute: s.eventRate.MinuteRate * 60,
			PerHour:   s.eventRate.HourRate * 3600,
			PerDay:    s.eventRate.DayRate * 86400,
		},
		ErrorRates: RateSnapshot{
			PerSecond: s.errorRate.SecondRate,
			PerMinute: s.errorRate.MinuteRate * 60,
			PerHour:   s.errorRate.HourRate * 3600,
			PerDay:    s.errorRate.DayRate * 86400,
		},
	}
}

// SortField specifies how to sort object results.
type SortField int

const (
	SortByErrors SortField = iota
	SortByEvents
	SortByErrorRate
)

// TopObjects returns the top n objects sorted by the specified field.
// For SortByErrorRate, only objects with at least minEvents are considered.
func (q *QueueStats) TopObjects(by SortField, n int, minEvents uint64) []ObjectSnapshot {
	q.mu.RLock()
	defer q.mu.RUnlock()

	result := make([]ObjectSnapshot, 0, len(q.objects))
	for id, s := range q.objects {
		if by == SortByErrorRate && s.Events < minEvents {
			continue
		}
		result = append(result, *q.objectSnapshotLocked(id, s))
	}

	switch by {
	case SortByErrors:
		sort.Slice(result, func(i, j int) bool {
			return result[i].Errors > result[j].Errors
		})
	case SortByEvents:
		sort.Slice(result, func(i, j int) bool {
			return result[i].Events > result[j].Events
		})
	case SortByErrorRate:
		sort.Slice(result, func(i, j int) bool {
			return result[i].ErrorRate > result[j].ErrorRate
		})
	}

	if n > 0 && len(result) > n {
		result = result[:n]
	}
	return result
}

// RecentErrors returns the n most recent errors, newest first.
func (q *QueueStats) RecentErrors(n int) []ErrorRecord {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return q.recentErrorsLocked(n, "")
}

// RecentErrorsForObject returns the n most recent errors for a specific object.
func (q *QueueStats) RecentErrorsForObject(objectID string, n int) []ErrorRecord {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return q.recentErrorsLocked(n, objectID)
}

func (q *QueueStats) recentErrorsLocked(n int, filterObjectID string) []ErrorRecord {
	result := make([]ErrorRecord, 0, n)

	// Walk backwards from the most recent entry
	for i := 0; i < recentErrorsBufferSize && len(result) < n; i++ {
		idx := (q.recentIndex - 1 - i + recentErrorsBufferSize) % recentErrorsBufferSize
		rec := q.recentErrors[idx]
		if rec.Timestamp.IsZero() {
			// Empty slot, buffer not yet full
			break
		}
		if filterObjectID == "" || rec.ObjectID == filterObjectID {
			result = append(result, rec)
		}
	}
	return result
}

// ObjectsAtLocation returns objects that have errors at the specified location.
func (q *QueueStats) ObjectsAtLocation(location string) []ObjectSnapshot {
	q.mu.RLock()
	defer q.mu.RUnlock()

	result := make([]ObjectSnapshot, 0)
	for id, s := range q.objects {
		if count := s.ByLocation[location]; count > 0 {
			result = append(result, *q.objectSnapshotLocked(id, s))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ByLocation[location] > result[j].ByLocation[location]
	})
	return result
}

// Reset clears all statistics.
func (q *QueueStats) Reset() {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()
	q.objects = make(map[string]*ObjectStats)
	q.byCategory = make(map[ErrorCategory]*ErrorStats)
	q.byLocation = make(map[string]*ErrorStats)
	q.objectBuckets = make(map[time.Time]map[string]struct{})
	q.locationBuckets = make(map[time.Time]map[string]struct{})
	q.objectBucket = make(map[string]time.Time)
	q.locationBucket = make(map[string]time.Time)
	q.recentErrors = make([]ErrorRecord, recentErrorsBufferSize)
	q.recentIndex = 0
	q.totalEvents = 0
	q.totalErrors = 0
	q.startTime = now
	q.lastEviction = now
	q.eventRate = RateStats{}
	q.errorRate = RateStats{}
	q.prevEvents = 0
	q.prevErrors = 0
}

// Error classification

// stackTracer is implemented by errors with stack traces (from pkg/errors).
type stackTracer interface {
	StackTrace() errors.StackTrace
}

// jsLocationRE parses JS error locations like "/user.js:10:5" or "user.js:10"
var jsLocationRE = regexp.MustCompile(`^(.+?):(\d+)(?::(\d+))?$`)

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

func parseJSLocation(loc string) ErrorLocation {
	if matches := jsLocationRE.FindStringSubmatch(loc); matches != nil {
		file := matches[1]
		line, _ := strconv.Atoi(matches[2])
		result := ErrorLocation{File: &file, Line: &line}
		if matches[3] != "" {
			col, _ := strconv.Atoi(matches[3])
			result.Column = &col
		}
		return result
	}
	if loc != "" {
		return ErrorLocation{File: &loc}
	}
	return ErrorLocation{}
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
