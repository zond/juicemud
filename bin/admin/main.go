// juicemud-admin is the administration tool for JuiceMUD servers.
// It communicates with a running server via Unix domain socket.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	// Default socket path
	homeDir, _ := os.UserHomeDir()
	defaultSocket := filepath.Join(homeDir, ".juicemud", "control.sock")

	socketPath := flag.String("socket", defaultSocket, "Path to control socket")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <command> [args...]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  switch-sources [path]  Switch to a new sources directory\n")
		fmt.Fprintf(os.Stderr, "                         Default path: 'src/current' (resolved from symlink)\n")
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	}

	command := args[0]
	switch command {
	case "switch-sources":
		var targetPath string
		if len(args) > 1 {
			targetPath = args[1]
		}
		if err := switchSources(*socketPath, targetPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		flag.Usage()
		os.Exit(1)
	}
}

func switchSources(socketPath, targetPath string) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect to control socket %s: %w", socketPath, err)
	}
	defer conn.Close()

	// Send command
	var cmd string
	if targetPath != "" {
		cmd = fmt.Sprintf("SWITCH_SOURCES %s\n", targetPath)
	} else {
		cmd = "SWITCH_SOURCES\n"
	}
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}

	// Read response
	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	response = strings.TrimSpace(response)
	if response == "OK" {
		fmt.Println("Sources switched successfully")
		return nil
	}

	// Read multi-line error response
	if strings.HasPrefix(response, "ERROR:") {
		errMsg := strings.TrimPrefix(response, "ERROR: ")
		// Check if there are more lines (for missing files list)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			errMsg += line
		}
		return fmt.Errorf("%s", strings.TrimSpace(errMsg))
	}

	return fmt.Errorf("unexpected response: %s", response)
}
