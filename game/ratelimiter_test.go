package game

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestLoginRateLimiterRecordClear(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := newLoginRateLimiter(ctx)

	// Initially no entries
	l.mu.RLock()
	_, exists := l.attempts["testuser"]
	l.mu.RUnlock()
	if exists {
		t.Error("testuser should not have an entry initially")
	}

	// Record failure
	l.recordFailure("testuser")
	l.mu.RLock()
	_, exists = l.attempts["testuser"]
	l.mu.RUnlock()
	if !exists {
		t.Error("testuser should have an entry after recordFailure")
	}

	// Clear failure
	l.clearFailure("testuser")
	l.mu.RLock()
	_, exists = l.attempts["testuser"]
	l.mu.RUnlock()
	if exists {
		t.Error("testuser should not have an entry after clearFailure")
	}
}

func TestLoginRateLimiterMultipleUsers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := newLoginRateLimiter(ctx)

	// Record failures for multiple users
	l.recordFailure("user1")
	l.recordFailure("user2")
	l.recordFailure("user3")

	l.mu.RLock()
	count := len(l.attempts)
	l.mu.RUnlock()

	if count != 3 {
		t.Errorf("expected 3 entries, got %d", count)
	}

	// Clear one user
	l.clearFailure("user2")

	l.mu.RLock()
	count = len(l.attempts)
	_, user1Exists := l.attempts["user1"]
	_, user2Exists := l.attempts["user2"]
	_, user3Exists := l.attempts["user3"]
	l.mu.RUnlock()

	if count != 2 {
		t.Errorf("expected 2 entries after clear, got %d", count)
	}
	if !user1Exists {
		t.Error("user1 should still exist")
	}
	if user2Exists {
		t.Error("user2 should not exist")
	}
	if !user3Exists {
		t.Error("user3 should still exist")
	}
}

func TestLoginRateLimiterClearNonexistent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := newLoginRateLimiter(ctx)

	// Clear nonexistent should not panic
	l.clearFailure("nonexistent")

	l.mu.RLock()
	count := len(l.attempts)
	l.mu.RUnlock()

	if count != 0 {
		t.Errorf("expected 0 entries, got %d", count)
	}
}

func TestLoginRateLimiterRecordUpdatesTime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := newLoginRateLimiter(ctx)

	// Record first failure
	l.recordFailure("testuser")
	l.mu.RLock()
	firstTime := l.attempts["testuser"]
	l.mu.RUnlock()

	// Small delay to ensure time difference
	time.Sleep(10 * time.Millisecond)

	// Record again - should update time
	l.recordFailure("testuser")
	l.mu.RLock()
	secondTime := l.attempts["testuser"]
	l.mu.RUnlock()

	if !secondTime.After(firstTime) {
		t.Error("second recordFailure should update timestamp")
	}
}

func TestLoginRateLimiterConcurrentAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := newLoginRateLimiter(ctx)

	var wg sync.WaitGroup
	const goroutines = 10
	const iterations = 100

	// Concurrent record/clear operations
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			username := "user"
			for j := 0; j < iterations; j++ {
				l.recordFailure(username)
			}
		}(i)

		go func(id int) {
			defer wg.Done()
			username := "user"
			for j := 0; j < iterations; j++ {
				l.clearFailure(username)
			}
		}(i)
	}

	wg.Wait()
	// Test passes if no race conditions or panics occurred
}

func TestLoginRateLimiterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	l := newLoginRateLimiter(ctx)

	// Record a failure
	l.recordFailure("testuser")

	// Cancel context - cleanup loop should stop
	cancel()

	// Give cleanup loop time to exit
	time.Sleep(50 * time.Millisecond)

	// Rate limiter should still function for record/clear
	// (cleanup loop stopping shouldn't break the limiter)
	l.recordFailure("anotheruser")
	l.mu.RLock()
	_, exists := l.attempts["anotheruser"]
	l.mu.RUnlock()
	if !exists {
		t.Error("should still be able to record after context cancellation")
	}
}
