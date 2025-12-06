// Binary integration_test runs integration tests and leaves the server running
// for manual testing via SSH.
//
// Usage:
//
//	go run ./bin/integration_test
package main

import (
	"fmt"
	"os"

	"github.com/zond/juicemud/integration_test"
)

func main() {
	ts, err := integration_test.NewTestServer()
	if err != nil {
		fmt.Printf("Failed to create server: %v\n", err)
		os.Exit(1)
	}
	defer ts.Close()

	fmt.Println("Running integration tests...")
	fmt.Println()

	if err := integration_test.RunAll(ts); err != nil {
		fmt.Printf("\nFAILED: %v\n", err)
	} else {
		fmt.Println("\nAll tests PASSED")
	}

	fmt.Println()
	fmt.Println("Server running for manual testing...")
	fmt.Println()
	fmt.Println("To connect:")
	fmt.Printf("  ssh -o StrictHostKeyChecking=no any-username@localhost -p %s\n", getPort(ts.SSHAddr()))
	fmt.Println()
	fmt.Println("Existing test user: testuser / testpass123 (wizard)")
	fmt.Println()
	fmt.Println("Press Ctrl+C to stop.")

	<-make(chan struct{})
}

func getPort(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return addr
}
