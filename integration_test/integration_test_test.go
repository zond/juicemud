package integration_test

import (
	"context"
	"testing"
	"time"
)

func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Set a 60-second timeout for the entire integration test
	// (increased from 30s to accommodate -race mode which is ~3-4x slower)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		ts, err := NewTestServer()
		if err != nil {
			done <- err
			return
		}
		defer ts.Close()
		done <- RunAll(ts)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("integration test timed out after 30 seconds")
	}
}
