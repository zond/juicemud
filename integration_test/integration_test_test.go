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

	// Set a 30-second timeout for the entire integration test
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
