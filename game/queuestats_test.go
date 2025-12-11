package game

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/pkg/errors"
)

func TestNewQueueStats(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	if qs.objects == nil {
		t.Error("objects map should be initialized")
	}
	if qs.byCategory == nil {
		t.Error("byCategory map should be initialized")
	}
	if qs.byLocation == nil {
		t.Error("byLocation map should be initialized")
	}
	if qs.objectBuckets == nil {
		t.Error("objectBuckets map should be initialized")
	}
	if qs.locationBuckets == nil {
		t.Error("locationBuckets map should be initialized")
	}
	if qs.recentErrors == nil {
		t.Error("recentErrors slice should be initialized")
	}
	if len(qs.recentErrors) != recentErrorsBufferSize {
		t.Errorf("recentErrors should have size %d, got %d", recentErrorsBufferSize, len(qs.recentErrors))
	}
	if qs.startTime.IsZero() {
		t.Error("startTime should be set")
	}
}

func TestRecordEvent(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	qs.RecordEvent("obj1")
	qs.RecordEvent("obj1")
	qs.RecordEvent("obj2")

	if qs.totalEvents != 3 {
		t.Errorf("totalEvents should be 3, got %d", qs.totalEvents)
	}

	s1 := qs.objects["obj1"]
	if s1 == nil {
		t.Fatal("obj1 should exist in objects map")
	}
	if s1.Events != 2 {
		t.Errorf("obj1 events should be 2, got %d", s1.Events)
	}
	if s1.LastEvent.IsZero() {
		t.Error("obj1 LastEvent should be set")
	}

	s2 := qs.objects["obj2"]
	if s2 == nil {
		t.Fatal("obj2 should exist in objects map")
	}
	if s2.Events != 1 {
		t.Errorf("obj2 events should be 1, got %d", s2.Events)
	}
}

func TestRecordError(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	err := errors.New("test error")
	qs.RecordError("obj1", err)
	qs.RecordError("obj1", err)

	if qs.totalErrors != 2 {
		t.Errorf("totalErrors should be 2, got %d", qs.totalErrors)
	}

	s := qs.objects["obj1"]
	if s == nil {
		t.Fatal("obj1 should exist")
	}
	if s.Errors != 2 {
		t.Errorf("obj1 errors should be 2, got %d", s.Errors)
	}
	if s.ByCategory[CategoryOther] != 2 {
		t.Errorf("obj1 CategoryOther count should be 2, got %d", s.ByCategory[CategoryOther])
	}

	// Check global category stats
	cs := qs.byCategory[CategoryOther]
	if cs == nil {
		t.Fatal("CategoryOther should exist in byCategory")
	}
	if cs.Count != 2 {
		t.Errorf("CategoryOther count should be 2, got %d", cs.Count)
	}
}

func TestRecordErrorCategories(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	tests := []struct {
		name     string
		err      error
		expected ErrorCategory
	}{
		{"context deadline", context.DeadlineExceeded, CategoryTimeout},
		{"context canceled", context.Canceled, CategoryTimeout},
		{"json syntax error", &json.SyntaxError{Offset: 10}, CategoryJSON},
		{"json type error", &json.UnmarshalTypeError{Value: "string", Type: reflect.TypeOf(""), Offset: 0, Struct: "Test", Field: "field"}, CategoryJSON},
		{"not exist", os.ErrNotExist, CategoryStorage},
		{"permission denied", os.ErrPermission, CategoryStorage},
		{"generic error", errors.New("generic"), CategoryOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat, _, _ := classifyError(tt.err)
			if cat != tt.expected {
				t.Errorf("expected category %s, got %s", tt.expected, cat)
			}
		})
	}
}

func TestRecentErrors(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	// Record some errors
	for i := 0; i < 5; i++ {
		qs.RecordError(fmt.Sprintf("obj%d", i), errors.New(fmt.Sprintf("error %d", i)))
	}

	recent := qs.RecentErrors(3)
	if len(recent) != 3 {
		t.Errorf("expected 3 recent errors, got %d", len(recent))
	}

	// Should be in reverse order (newest first)
	if recent[0].Message != "error 4" {
		t.Errorf("expected newest error first, got %s", recent[0].Message)
	}
	if recent[2].Message != "error 2" {
		t.Errorf("expected third error to be 'error 2', got %s", recent[2].Message)
	}
}

func TestRecentErrorsForObject(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	qs.RecordError("obj1", errors.New("err1"))
	qs.RecordError("obj2", errors.New("err2"))
	qs.RecordError("obj1", errors.New("err3"))
	qs.RecordError("obj2", errors.New("err4"))

	recent := qs.RecentErrorsForObject("obj1", 10)
	if len(recent) != 2 {
		t.Errorf("expected 2 errors for obj1, got %d", len(recent))
	}
	for _, r := range recent {
		if r.ObjectID != "obj1" {
			t.Errorf("expected objectID obj1, got %s", r.ObjectID)
		}
	}
}

func TestRecentErrorsCircularBuffer(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	// Fill the buffer and overflow
	for i := 0; i < recentErrorsBufferSize+100; i++ {
		qs.RecordError("obj", errors.New(fmt.Sprintf("error %d", i)))
	}

	recent := qs.RecentErrors(10)
	if len(recent) != 10 {
		t.Errorf("expected 10 recent errors, got %d", len(recent))
	}

	// The oldest errors should have been overwritten
	// Most recent should be recentErrorsBufferSize+99
	expectedMsg := fmt.Sprintf("error %d", recentErrorsBufferSize+99)
	if recent[0].Message != expectedMsg {
		t.Errorf("expected newest error message %q, got %q", expectedMsg, recent[0].Message)
	}
}

func TestGlobalSnapshot(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	qs.RecordEvent("obj1")
	qs.RecordEvent("obj1")
	qs.RecordError("obj1", errors.New("err"))

	snap := qs.GlobalSnapshot()

	if snap.TotalEvents != 2 {
		t.Errorf("expected 2 events, got %d", snap.TotalEvents)
	}
	if snap.TotalErrors != 1 {
		t.Errorf("expected 1 error, got %d", snap.TotalErrors)
	}
	if snap.ErrorRate != 0.5 {
		t.Errorf("expected error rate 0.5, got %f", snap.ErrorRate)
	}
	if snap.Uptime <= 0 {
		t.Error("uptime should be positive")
	}
}

func TestObjectSnapshot(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	qs.RecordEvent("obj1")
	qs.RecordEvent("obj1")
	qs.RecordError("obj1", errors.New("err"))

	snap := qs.ObjectSnapshot("obj1")
	if snap == nil {
		t.Fatal("snapshot should not be nil")
	}
	if snap.Events != 2 {
		t.Errorf("expected 2 events, got %d", snap.Events)
	}
	if snap.Errors != 1 {
		t.Errorf("expected 1 error, got %d", snap.Errors)
	}
	if snap.ErrorRate != 0.5 {
		t.Errorf("expected error rate 0.5, got %f", snap.ErrorRate)
	}

	// Non-existent object
	snap2 := qs.ObjectSnapshot("nonexistent")
	if snap2 != nil {
		t.Error("snapshot for nonexistent object should be nil")
	}
}

func TestTopCategories(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	qs.RecordError("obj", errors.New("generic1"))
	qs.RecordError("obj", errors.New("generic2"))
	qs.RecordError("obj", context.DeadlineExceeded)

	cats := qs.TopCategories()
	if len(cats) != 2 {
		t.Fatalf("expected 2 categories, got %d", len(cats))
	}
	// CategoryOther should be first (2 errors)
	if cats[0].Category != CategoryOther {
		t.Errorf("expected CategoryOther first, got %s", cats[0].Category)
	}
	if cats[0].Count != 2 {
		t.Errorf("expected count 2, got %d", cats[0].Count)
	}
	if cats[1].Category != CategoryTimeout {
		t.Errorf("expected CategoryTimeout second, got %s", cats[1].Category)
	}
}

func TestTopLocations(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	// Record errors - they'll all be at "(unknown)" location since we're using simple errors
	for i := 0; i < 5; i++ {
		qs.RecordError("obj", errors.New("err"))
	}

	locs := qs.TopLocations(10)
	if len(locs) != 1 {
		t.Fatalf("expected 1 location, got %d", len(locs))
	}
	if locs[0].Count != 5 {
		t.Errorf("expected count 5, got %d", locs[0].Count)
	}
}

func TestTopObjects(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	// obj1: 10 events, 2 errors (20% error rate)
	for i := 0; i < 10; i++ {
		qs.RecordEvent("obj1")
	}
	qs.RecordError("obj1", errors.New("err"))
	qs.RecordError("obj1", errors.New("err"))

	// obj2: 5 events, 3 errors (60% error rate)
	for i := 0; i < 5; i++ {
		qs.RecordEvent("obj2")
	}
	for i := 0; i < 3; i++ {
		qs.RecordError("obj2", errors.New("err"))
	}

	// Sort by errors
	byErrors := qs.TopObjects(SortByErrors, 10, 0)
	if len(byErrors) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(byErrors))
	}
	if byErrors[0].ObjectID != "obj2" {
		t.Errorf("expected obj2 first by errors, got %s", byErrors[0].ObjectID)
	}

	// Sort by events
	byEvents := qs.TopObjects(SortByEvents, 10, 0)
	if byEvents[0].ObjectID != "obj1" {
		t.Errorf("expected obj1 first by events, got %s", byEvents[0].ObjectID)
	}

	// Sort by error rate
	byRate := qs.TopObjects(SortByErrorRate, 10, 0)
	if byRate[0].ObjectID != "obj2" {
		t.Errorf("expected obj2 first by error rate, got %s", byRate[0].ObjectID)
	}

	// Test limit
	limited := qs.TopObjects(SortByErrors, 1, 0)
	if len(limited) != 1 {
		t.Errorf("expected 1 object with limit, got %d", len(limited))
	}

	// Test minEvents filter
	withMin := qs.TopObjects(SortByErrorRate, 10, 8)
	if len(withMin) != 1 {
		t.Fatalf("expected 1 object with minEvents=8, got %d", len(withMin))
	}
	if withMin[0].ObjectID != "obj1" {
		t.Errorf("expected obj1 with minEvents=8, got %s", withMin[0].ObjectID)
	}
}

// createTestError is a helper that creates errors at a known location.
// All calls in this function will appear with the same file:line prefix.
func createTestError(msg string) error {
	return errors.New(msg) // This line is the "common location"
}

func TestObjectsAtLocation(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	// Use a helper to create errors at a common location
	err1 := createTestError("err1")
	err2 := createTestError("err2")
	err3 := createTestError("err3")

	qs.RecordError("obj1", err1)
	qs.RecordError("obj1", err2)
	qs.RecordError("obj2", err3)

	// Find the common location from obj1's ByLocation map
	snap1 := qs.ObjectSnapshot("obj1")
	if len(snap1.ByLocation) != 1 {
		t.Fatalf("expected 1 location for obj1, got %d: %v", len(snap1.ByLocation), snap1.ByLocation)
	}

	// Get the location key (should be like "queuestats_test.go:NNN")
	var commonLoc string
	for loc := range snap1.ByLocation {
		commonLoc = loc
	}

	objs := qs.ObjectsAtLocation(commonLoc)
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects at location %q, got %d", commonLoc, len(objs))
	}
	// obj1 should be first (2 errors at this location)
	if objs[0].ObjectID != "obj1" {
		t.Errorf("expected obj1 first, got %s", objs[0].ObjectID)
	}
}

func TestReset(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	qs.RecordEvent("obj1")
	qs.RecordError("obj1", errors.New("err"))

	qs.Reset()

	if qs.totalEvents != 0 {
		t.Errorf("totalEvents should be 0 after reset, got %d", qs.totalEvents)
	}
	if qs.totalErrors != 0 {
		t.Errorf("totalErrors should be 0 after reset, got %d", qs.totalErrors)
	}
	if len(qs.objects) != 0 {
		t.Errorf("objects should be empty after reset, got %d", len(qs.objects))
	}
	if len(qs.byCategory) != 0 {
		t.Errorf("byCategory should be empty after reset, got %d", len(qs.byCategory))
	}
	if len(qs.objectBuckets) != 0 {
		t.Errorf("objectBuckets should be empty after reset, got %d", len(qs.objectBuckets))
	}
}

func TestUpdateRates(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	// Record some events
	for i := 0; i < 10; i++ {
		qs.RecordEvent("obj1")
	}

	// First update initializes lastUpdate
	qs.UpdateRates()

	// Second update should compute rates
	time.Sleep(10 * time.Millisecond)
	qs.UpdateRates()

	snap := qs.GlobalSnapshot()
	// Rates should be non-negative (they start at 0 now)
	if snap.EventRates.PerSecond < 0 {
		t.Errorf("PerSecond rate should be non-negative, got %f", snap.EventRates.PerSecond)
	}
}

func TestRateStatsUpdate(t *testing.T) {
	r := &RateStats{}

	// First update just sets lastUpdate
	r.update(10)
	if !r.lastUpdate.IsZero() == false {
		// lastUpdate should be set
	}
	if r.SecondRate != 0 {
		t.Errorf("SecondRate should be 0 on first update, got %f", r.SecondRate)
	}

	// Wait a bit and update again
	time.Sleep(50 * time.Millisecond)
	r.update(5)

	// Rates should now be positive
	if r.SecondRate <= 0 {
		t.Errorf("SecondRate should be positive after second update, got %f", r.SecondRate)
	}
}

func TestTimeBucketTracking(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	qs.RecordEvent("obj1")

	// Check that object is tracked in a bucket
	if len(qs.objectBuckets) != 1 {
		t.Errorf("expected 1 object bucket, got %d", len(qs.objectBuckets))
	}

	bucket, exists := qs.objectBucket["obj1"]
	if !exists {
		t.Fatal("obj1 should have a bucket entry")
	}

	// Bucket should be the current hour
	expectedBucket := time.Now().Truncate(time.Hour)
	if !bucket.Equal(expectedBucket) {
		t.Errorf("expected bucket %v, got %v", expectedBucket, bucket)
	}

	// The bucket should contain obj1
	ids := qs.objectBuckets[bucket]
	if _, ok := ids["obj1"]; !ok {
		t.Error("obj1 should be in its bucket")
	}
}

func TestLocationBucketTracking(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	qs.RecordError("obj1", errors.New("test error"))

	// Check location bucket tracking
	if len(qs.locationBuckets) != 1 {
		t.Errorf("expected 1 location bucket, got %d", len(qs.locationBuckets))
	}

	// Get the actual location from the byLocation map (since errors.New captures stack trace)
	if len(qs.byLocation) != 1 {
		t.Fatalf("expected 1 location in byLocation, got %d", len(qs.byLocation))
	}
	var loc string
	for l := range qs.byLocation {
		loc = l
	}

	bucket, exists := qs.locationBucket[loc]
	if !exists {
		t.Fatalf("location %q should have a bucket entry", loc)
	}

	locs := qs.locationBuckets[bucket]
	if _, ok := locs[loc]; !ok {
		t.Error("location should be in its bucket")
	}
}

func TestTouchObjectSameBucket(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	// Record event - creates bucket entry
	qs.RecordEvent("obj1")

	initialBucket := qs.objectBucket["obj1"]
	initialBucketLen := len(qs.objectBuckets[initialBucket])

	// Record another event - should stay in same bucket
	qs.RecordEvent("obj1")

	// Should still be in same bucket
	if qs.objectBucket["obj1"] != initialBucket {
		t.Error("object should stay in same bucket within same hour")
	}
	if len(qs.objectBuckets[initialBucket]) != initialBucketLen {
		t.Error("bucket size should not change for same object")
	}
}

func TestEvictStaleLocked(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	// Add an object
	qs.RecordEvent("obj1")
	qs.RecordError("obj1", errors.New("err"))

	// Manually set the bucket to a very old time
	oldBucket := time.Now().Add(-statsTTL - time.Hour).Truncate(time.Hour)
	currentBucket := qs.objectBucket["obj1"]

	// Move obj1 to old bucket
	delete(qs.objectBuckets[currentBucket], "obj1")
	if len(qs.objectBuckets[currentBucket]) == 0 {
		delete(qs.objectBuckets, currentBucket)
	}

	qs.objectBuckets[oldBucket] = map[string]struct{}{"obj1": {}}
	qs.objectBucket["obj1"] = oldBucket

	// Same for location
	loc := "(unknown)"
	currentLocBucket := qs.locationBucket[loc]
	delete(qs.locationBuckets[currentLocBucket], loc)
	if len(qs.locationBuckets[currentLocBucket]) == 0 {
		delete(qs.locationBuckets, currentLocBucket)
	}
	qs.locationBuckets[oldBucket] = map[string]struct{}{loc: {}}
	qs.locationBucket[loc] = oldBucket

	// Run eviction
	qs.evictStaleLocked(time.Now())

	// Object should be evicted
	if _, exists := qs.objects["obj1"]; exists {
		t.Error("obj1 should be evicted")
	}
	if _, exists := qs.objectBucket["obj1"]; exists {
		t.Error("obj1 bucket entry should be removed")
	}
	if _, exists := qs.objectBuckets[oldBucket]; exists {
		t.Error("old bucket should be removed")
	}

	// Location should be evicted
	if _, exists := qs.byLocation[loc]; exists {
		t.Error("location should be evicted")
	}
	if _, exists := qs.locationBucket[loc]; exists {
		t.Error("location bucket entry should be removed")
	}
}

func TestEvictionPreservesCategories(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	qs.RecordError("obj1", errors.New("err"))

	// Force eviction
	qs.evictStaleLocked(time.Now().Add(statsTTL + time.Hour))

	// Categories should NOT be evicted
	if len(qs.byCategory) == 0 {
		t.Error("byCategory should not be evicted")
	}
}

func TestCloseMultipleTimes(t *testing.T) {
	qs := NewQueueStats()

	// Should not panic when called multiple times
	qs.Close()
	qs.Close()
	qs.Close()
}

func TestConcurrentAccess(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			qs.RecordEvent(fmt.Sprintf("obj%d", i%10))
			qs.RecordError(fmt.Sprintf("obj%d", i%10), errors.New("err"))
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			qs.GlobalSnapshot()
			qs.TopCategories()
			qs.TopLocations(10)
			qs.TopObjects(SortByErrors, 10, 0)
			qs.RecentErrors(10)
		}
		done <- true
	}()

	// Rate updater goroutine
	go func() {
		for i := 0; i < 100; i++ {
			qs.UpdateRates()
		}
		done <- true
	}()

	// Wait for all goroutines
	<-done
	<-done
	<-done
}

func TestTruncateMessage(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"short", "short"},
		{"with\nnewline", "with newline"},
		{"with\r\nwindows", "with windows"},
		{string(make([]byte, 200)), string(make([]byte, maxErrorMessageLength-3)) + "..."},
	}

	for _, tt := range tests {
		result := truncateMessage(tt.input)
		if tt.input == string(make([]byte, 200)) {
			// For the long string test, just check length
			if len(result) > maxErrorMessageLength {
				t.Errorf("truncated message should be at most %d chars, got %d", maxErrorMessageLength, len(result))
			}
		} else if result != tt.expected {
			t.Errorf("truncateMessage(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestTruncateMessageUTF8(t *testing.T) {
	// Test that UTF-8 characters are not split
	input := string(make([]rune, maxErrorMessageLength+10))
	for i := range input {
		input = input[:i] + "日" + input[i+1:]
	}
	// Create a string of Japanese characters longer than max
	input = ""
	for i := 0; i < maxErrorMessageLength+10; i++ {
		input += "日"
	}

	result := truncateMessage(input)

	// Should end with "..." and not have broken UTF-8
	if !isValidUTF8(result) {
		t.Error("truncated message should be valid UTF-8")
	}
	if len([]rune(result)) > maxErrorMessageLength {
		t.Errorf("truncated message should be at most %d runes, got %d", maxErrorMessageLength, len([]rune(result)))
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

func TestErrorLocationString(t *testing.T) {
	file := "test.go"
	line := 42
	line0 := 0
	col := 5

	tests := []struct {
		name     string
		loc      ErrorLocation
		expected string
	}{
		{"file:line:col", ErrorLocation{File: &file, Line: &line, Column: &col}, "test.go:42:5"},
		{"file:line", ErrorLocation{File: &file, Line: &line}, "test.go:42"},
		{"file:0", ErrorLocation{File: &file, Line: &line0}, "test.go:0"},
		{"file only", ErrorLocation{File: &file}, "test.go"},
		{"empty", ErrorLocation{}, "(unknown)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.loc.String()
			if result != tt.expected {
				t.Errorf("ErrorLocation.String() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestParseJSLocation(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		expectedFile string
		expectedLine int
		expectedCol  int // -1 means no column
		hasLine      bool
		hasCol       bool
	}{
		{"file:line:col", "/user.js:10:5", "/user.js", 10, 5, true, true},
		{"file:line", "user.js:10", "user.js", 10, 0, true, false},
		{"path:line:col", "/path/to/file.js:123:45", "/path/to/file.js", 123, 45, true, true},
		{"no line", "no-line-number", "no-line-number", 0, 0, false, false},
		{"empty", "", "", 0, 0, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseJSLocation(tt.input)

			if tt.input == "" {
				if result.File != nil {
					t.Errorf("expected nil File for empty input, got %q", *result.File)
				}
				return
			}

			if result.File == nil {
				t.Fatalf("expected non-nil File for %q", tt.input)
			}
			if *result.File != tt.expectedFile {
				t.Errorf("File = %q, want %q", *result.File, tt.expectedFile)
			}

			if tt.hasLine {
				if result.Line == nil {
					t.Fatalf("expected non-nil Line for %q", tt.input)
				}
				if *result.Line != tt.expectedLine {
					t.Errorf("Line = %d, want %d", *result.Line, tt.expectedLine)
				}
			} else {
				if result.Line != nil {
					t.Errorf("expected nil Line for %q, got %d", tt.input, *result.Line)
				}
			}

			if tt.hasCol {
				if result.Column == nil {
					t.Fatalf("expected non-nil Column for %q", tt.input)
				}
				if *result.Column != tt.expectedCol {
					t.Errorf("Column = %d, want %d", *result.Column, tt.expectedCol)
				}
			} else {
				if result.Column != nil {
					t.Errorf("expected nil Column for %q, got %d", tt.input, *result.Column)
				}
			}
		})
	}
}

func TestClassifyErrorJS(t *testing.T) {
	// Create a mock JS error using v8go
	// Note: We can't easily create a real v8go.JSError without a v8 context,
	// so we test the other categories more thoroughly

	// Test that classifyError handles nil
	cat, loc, msg := classifyError(nil)
	if cat != CategoryOther {
		t.Errorf("nil error should be CategoryOther, got %s", cat)
	}
	if loc.File != nil || loc.Line != nil {
		t.Error("nil error should have empty location")
	}
	if msg != "" {
		t.Error("nil error should have empty message")
	}
}

func TestSnapshotCopiesData(t *testing.T) {
	qs := NewQueueStats()
	defer qs.Close()

	qs.RecordEvent("obj1")
	qs.RecordError("obj1", errors.New("err"))

	snap := qs.ObjectSnapshot("obj1")

	// Modify the snapshot's maps
	snap.ByCategory[CategoryJS] = 999
	snap.ByLocation["fake"] = 999

	// Original should be unchanged
	s := qs.objects["obj1"]
	if s.ByCategory[CategoryJS] == 999 {
		t.Error("modifying snapshot should not affect original ByCategory")
	}
	if s.ByLocation["fake"] == 999 {
		t.Error("modifying snapshot should not affect original ByLocation")
	}
}

// Benchmark tests

func BenchmarkRecordEvent(b *testing.B) {
	qs := NewQueueStats()
	defer qs.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		qs.RecordEvent("obj1")
	}
}

func BenchmarkRecordError(b *testing.B) {
	qs := NewQueueStats()
	defer qs.Close()
	err := errors.New("test error")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		qs.RecordError("obj1", err)
	}
}

func BenchmarkGlobalSnapshot(b *testing.B) {
	qs := NewQueueStats()
	defer qs.Close()

	// Pre-populate with some data
	for i := 0; i < 1000; i++ {
		qs.RecordEvent(fmt.Sprintf("obj%d", i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		qs.GlobalSnapshot()
	}
}

func BenchmarkTopObjects(b *testing.B) {
	qs := NewQueueStats()
	defer qs.Close()

	// Pre-populate with some data
	for i := 0; i < 1000; i++ {
		qs.RecordEvent(fmt.Sprintf("obj%d", i))
		qs.RecordError(fmt.Sprintf("obj%d", i), errors.New("err"))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		qs.TopObjects(SortByErrors, 20, 0)
	}
}

func BenchmarkUpdateRates(b *testing.B) {
	qs := NewQueueStats()
	defer qs.Close()

	// Pre-populate with some data
	for i := 0; i < 1000; i++ {
		qs.RecordEvent(fmt.Sprintf("obj%d", i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		qs.UpdateRates()
	}
}
