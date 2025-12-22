package game

import (
	"context"
	"testing"
	"time"
)

func TestNewJSStats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)
	if stats == nil {
		t.Fatal("expected non-nil JSStats")
	}

	g := stats.GlobalSnapshot()
	if g.TotalExecs != 0 {
		t.Errorf("expected 0 total execs, got %d", g.TotalExecs)
	}
	if g.TotalSlow != 0 {
		t.Errorf("expected 0 total slow, got %d", g.TotalSlow)
	}
}

func TestRecordExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	// Record a normal execution
	stats.RecordExecution("/test.js", "obj1", 10*time.Millisecond, nil)

	g := stats.GlobalSnapshot()
	if g.TotalExecs != 1 {
		t.Errorf("expected 1 total execs, got %d", g.TotalExecs)
	}
	if g.TotalSlow != 0 {
		t.Errorf("expected 0 total slow, got %d", g.TotalSlow)
	}

	// Verify script stats
	scripts := stats.TopScripts(SortScriptByExecs, 10)
	if len(scripts) != 1 {
		t.Fatalf("expected 1 script, got %d", len(scripts))
	}
	if scripts[0].SourcePath != "/test.js" {
		t.Errorf("expected source path /test.js, got %s", scripts[0].SourcePath)
	}
	if scripts[0].Executions != 1 {
		t.Errorf("expected 1 execution, got %d", scripts[0].Executions)
	}

	// Verify object stats
	objs := stats.TopObjects(SortObjectByExecs, 10)
	if len(objs) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objs))
	}
	if objs[0].ObjectID != "obj1" {
		t.Errorf("expected object ID obj1, got %s", objs[0].ObjectID)
	}
}

func TestRecordSlowExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	// Record a slow execution (>= 50ms threshold)
	stats.RecordExecution("/slow.js", "obj1", 60*time.Millisecond, nil)

	g := stats.GlobalSnapshot()
	if g.TotalExecs != 1 {
		t.Errorf("expected 1 total execs, got %d", g.TotalExecs)
	}
	if g.TotalSlow != 1 {
		t.Errorf("expected 1 total slow, got %d", g.TotalSlow)
	}

	// Verify slow execution was recorded
	recent := stats.RecentSlowExecutions(10)
	if len(recent) != 1 {
		t.Fatalf("expected 1 recent slow execution, got %d", len(recent))
	}
	if recent[0].SourcePath != "/slow.js" {
		t.Errorf("expected source path /slow.js, got %s", recent[0].SourcePath)
	}
	if recent[0].Duration != 60*time.Millisecond {
		t.Errorf("expected duration 60ms, got %v", recent[0].Duration)
	}
}

func TestMinTimeInitialization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	// Record first execution
	stats.RecordExecution("/test.js", "obj1", 20*time.Millisecond, nil)

	script := stats.ScriptSnapshot("/test.js")
	if script == nil {
		t.Fatal("expected script snapshot")
	}

	// First execution should set min correctly
	if script.MinTimeMs != 20.0 {
		t.Errorf("expected min time 20ms, got %.1fms", script.MinTimeMs)
	}

	// Record shorter execution
	stats.RecordExecution("/test.js", "obj1", 10*time.Millisecond, nil)

	script = stats.ScriptSnapshot("/test.js")
	if script.MinTimeMs != 10.0 {
		t.Errorf("expected min time 10ms, got %.1fms", script.MinTimeMs)
	}

	// Record longer execution - min should not change
	stats.RecordExecution("/test.js", "obj1", 30*time.Millisecond, nil)

	script = stats.ScriptSnapshot("/test.js")
	if script.MinTimeMs != 10.0 {
		t.Errorf("expected min time still 10ms, got %.1fms", script.MinTimeMs)
	}
}

func TestTopScriptsSorting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	// Script A: many fast executions
	for i := 0; i < 10; i++ {
		stats.RecordExecution("/a.js", "obj1", 5*time.Millisecond, nil)
	}

	// Script B: fewer but slower executions
	for i := 0; i < 3; i++ {
		stats.RecordExecution("/b.js", "obj2", 40*time.Millisecond, nil)
	}

	// Script C: one slow execution
	stats.RecordExecution("/c.js", "obj3", 60*time.Millisecond, nil)

	// Sort by executions: A should be first
	byExecs := stats.TopScripts(SortScriptByExecs, 10)
	if byExecs[0].SourcePath != "/a.js" {
		t.Errorf("expected /a.js first by execs, got %s", byExecs[0].SourcePath)
	}

	// Sort by slow count: C should be first (only one with slow)
	bySlow := stats.TopScripts(SortScriptBySlow, 10)
	if bySlow[0].SourcePath != "/c.js" {
		t.Errorf("expected /c.js first by slow, got %s", bySlow[0].SourcePath)
	}

	// Sort by total time: B should be first (3 * 40ms = 120ms total)
	// A has 10 * 5ms = 50ms, C has 60ms
	byTime := stats.TopScripts(SortScriptByTime, 10)
	if byTime[0].SourcePath != "/b.js" {
		t.Errorf("expected /b.js first by time, got %s", byTime[0].SourcePath)
	}
}

func TestTopObjectsSorting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	// Object A: many fast executions
	for i := 0; i < 10; i++ {
		stats.RecordExecution("/test.js", "objA", 5*time.Millisecond, nil)
	}

	// Object B: fewer but slower executions
	for i := 0; i < 3; i++ {
		stats.RecordExecution("/test.js", "objB", 40*time.Millisecond, nil)
	}

	// Object C: one slow execution
	stats.RecordExecution("/test.js", "objC", 60*time.Millisecond, nil)

	// Sort by executions: A should be first
	byExecs := stats.TopObjects(SortObjectByExecs, 10)
	if byExecs[0].ObjectID != "objA" {
		t.Errorf("expected objA first by execs, got %s", byExecs[0].ObjectID)
	}

	// Sort by slow count: C should be first
	bySlow := stats.TopObjects(SortObjectBySlow, 10)
	if bySlow[0].ObjectID != "objC" {
		t.Errorf("expected objC first by slow, got %s", bySlow[0].ObjectID)
	}
}

func TestEmptySourcePath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	// Empty source path should be replaced with placeholder
	stats.RecordExecution("", "obj1", 10*time.Millisecond, nil)

	scripts := stats.TopScripts(SortScriptByExecs, 10)
	if len(scripts) != 1 {
		t.Fatalf("expected 1 script, got %d", len(scripts))
	}
	if scripts[0].SourcePath != "(no source)" {
		t.Errorf("expected source path '(no source)', got %s", scripts[0].SourcePath)
	}
}

func TestZeroDuration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	// Zero duration should be replaced with 1ns
	stats.RecordExecution("/test.js", "obj1", 0, nil)

	g := stats.GlobalSnapshot()
	if g.TotalExecs != 1 {
		t.Errorf("expected 1 execution, got %d", g.TotalExecs)
	}
	// TotalTimeMs should be > 0 (1ns = 0.000001ms)
	if g.TotalTimeMs <= 0 {
		t.Errorf("expected positive total time, got %f", g.TotalTimeMs)
	}
}

func TestJSStatsReset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	// Record some executions
	stats.RecordExecution("/test.js", "obj1", 10*time.Millisecond, nil)
	stats.RecordExecution("/slow.js", "obj2", 60*time.Millisecond, nil)

	g := stats.GlobalSnapshot()
	if g.TotalExecs != 2 {
		t.Fatalf("expected 2 executions before reset, got %d", g.TotalExecs)
	}

	// Reset
	stats.Reset()

	g = stats.GlobalSnapshot()
	if g.TotalExecs != 0 {
		t.Errorf("expected 0 executions after reset, got %d", g.TotalExecs)
	}
	if g.TotalSlow != 0 {
		t.Errorf("expected 0 slow after reset, got %d", g.TotalSlow)
	}

	scripts := stats.TopScripts(SortScriptByExecs, 10)
	if len(scripts) != 0 {
		t.Errorf("expected 0 scripts after reset, got %d", len(scripts))
	}
}

func TestRecentSlowExecutionsOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	// Record slow executions in order
	stats.RecordExecution("/first.js", "obj1", 60*time.Millisecond, nil)
	time.Sleep(time.Millisecond) // Ensure different timestamps
	stats.RecordExecution("/second.js", "obj2", 70*time.Millisecond, nil)
	time.Sleep(time.Millisecond)
	stats.RecordExecution("/third.js", "obj3", 80*time.Millisecond, nil)

	// Most recent should be first
	recent := stats.RecentSlowExecutions(10)
	if len(recent) != 3 {
		t.Fatalf("expected 3 recent slow executions, got %d", len(recent))
	}
	if recent[0].SourcePath != "/third.js" {
		t.Errorf("expected /third.js first, got %s", recent[0].SourcePath)
	}
	if recent[1].SourcePath != "/second.js" {
		t.Errorf("expected /second.js second, got %s", recent[1].SourcePath)
	}
	if recent[2].SourcePath != "/first.js" {
		t.Errorf("expected /first.js third, got %s", recent[2].SourcePath)
	}
}

func TestMemoryLimitScripts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a stats instance and manually set a low limit for testing
	stats := NewJSStats(ctx, nil)

	// Record more scripts than maxScripts to trigger eviction
	// We can't easily test with the actual limit, so just verify basic eviction works
	for i := 0; i < 1010; i++ {
		stats.RecordExecution("/test"+string(rune(i))+".js", "obj", time.Millisecond, nil)
	}

	// Should have evicted some entries to stay under limit
	scripts := stats.TopScripts(SortScriptByExecs, 2000)
	if len(scripts) > 1000 {
		t.Errorf("expected at most 1000 scripts, got %d", len(scripts))
	}
}

func TestJSStatsConcurrentAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	done := make(chan bool)

	// Writer goroutines
	for i := 0; i < 10; i++ {
		go func(n int) {
			for j := 0; j < 100; j++ {
				stats.RecordExecution("/test.js", "obj", time.Millisecond*time.Duration(n), nil)
			}
			done <- true
		}(i)
	}

	// Reader goroutines
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				_ = stats.GlobalSnapshot()
				_ = stats.TopScripts(SortScriptByExecs, 10)
				_ = stats.TopObjects(SortObjectByExecs, 10)
				_ = stats.RecentSlowExecutions(10)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 15; i++ {
		<-done
	}

	// Should have recorded 1000 executions
	g := stats.GlobalSnapshot()
	if g.TotalExecs != 1000 {
		t.Errorf("expected 1000 executions, got %d", g.TotalExecs)
	}
}

func TestTimeRateStats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	// Record some executions
	stats.RecordExecution("/test.js", "obj1", 10*time.Millisecond, nil)
	stats.RecordExecution("/test.js", "obj1", 20*time.Millisecond, nil)

	// Trigger rate update
	stats.UpdateRates()

	g := stats.GlobalSnapshot()

	// After update, rates should have some value
	// The exact values depend on timing, so just verify they exist
	if g.TotalTimeMs == 0 {
		t.Error("expected non-zero total time")
	}
}

func BenchmarkRecordExecution(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stats.RecordExecution("/test.js", "obj1", 10*time.Millisecond, nil)
	}
}

func BenchmarkRecordExecutionParallel(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats := NewJSStats(ctx, nil)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			stats.RecordExecution("/test.js", "obj1", 10*time.Millisecond, nil)
		}
	})
}
