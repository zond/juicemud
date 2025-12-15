package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zond/juicemud"
)

// auditEntry is a test-friendly version of AuditEntry that uses json.RawMessage for Data.
type auditEntry struct {
	Time      string          `json:"time"`
	SessionID string          `json:"session_id,omitempty"`
	Event     string          `json:"event"`
	Data      json.RawMessage `json:"data"`
}

// readAuditLog reads all audit log entries from the storage's audit log file.
func readAuditLog(t *testing.T, dir string) []auditEntry {
	t.Helper()
	path := filepath.Join(dir, "audit.log")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("Failed to open audit log: %v", err)
	}
	defer f.Close()

	var entries []auditEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry auditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("Failed to parse audit log line %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("Failed to read audit log: %v", err)
	}
	return entries
}

// filterAuditByEvent returns only entries with the given event type.
func filterAuditByEvent(entries []auditEntry, event string) []auditEntry {
	var result []auditEntry
	for _, e := range entries {
		if e.Event == event {
			result = append(result, e)
		}
	}
	return result
}

// parseAuditData parses the Data field of an audit entry into the given struct.
func parseAuditData(t *testing.T, entry auditEntry, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(entry.Data, v); err != nil {
		t.Fatalf("Failed to parse audit data for event %s: %v", entry.Event, err)
	}
}

// withTestStorage creates a temporary storage, passes it to f, then shuts down
// the storage and returns all audit log entries. The temp directory is cleaned up.
func withTestStorage(t *testing.T, f func(s *Storage)) []auditEntry {
	t.Helper()
	dir, err := os.MkdirTemp("", "juicemud-audit-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithCancel(context.Background())
	ctx = juicemud.MakeMainContext(ctx)
	s, err := New(ctx, dir)
	if err != nil {
		cancel()
		t.Fatal(err)
	}

	f(s)

	// Cancel context and close to flush audit log
	cancel()
	s.Close()

	return readAuditLog(t, dir)
}

// === User Audit Tests ===

func TestAudit_UserCreate(t *testing.T) {
	entries := withTestStorage(t, func(s *Storage) {
		ctx := juicemud.MakeMainContext(context.Background())
		user := &User{Name: "alice"}
		if err := s.StoreUser(ctx, user, false, "192.168.1.1:12345"); err != nil {
			t.Fatal(err)
		}
	})

	entries = filterAuditByEvent(entries, "USER_CREATE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 USER_CREATE entry, got %d", len(entries))
	}

	var data AuditUserCreate
	parseAuditData(t, entries[0], &data)
	if data.User.Name != "alice" {
		t.Errorf("User.Name = %q, want %q", data.User.Name, "alice")
	}
	if data.Remote != "192.168.1.1:12345" {
		t.Errorf("Remote = %q, want %q", data.Remote, "192.168.1.1:12345")
	}
}

func TestAudit_UserCreate_NotLoggedOnUpdate(t *testing.T) {
	entries := withTestStorage(t, func(s *Storage) {
		ctx := juicemud.MakeMainContext(context.Background())
		user := &User{Name: "alice"}
		if err := s.StoreUser(ctx, user, false, "192.168.1.1:12345"); err != nil {
			t.Fatal(err)
		}

		// Update the user (should not create another USER_CREATE entry)
		user.PasswordHash = "newhash"
		if err := s.StoreUser(ctx, user, true, "192.168.1.2:12345"); err != nil {
			t.Fatal(err)
		}
	})

	entries = filterAuditByEvent(entries, "USER_CREATE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 USER_CREATE entry (not logged on update), got %d", len(entries))
	}
}

// === Session ID Tests ===

func TestAudit_SessionIDIncluded(t *testing.T) {
	entries := withTestStorage(t, func(s *Storage) {
		ctx := juicemud.MakeMainContext(context.Background())
		ctx = SetSessionID(ctx, "test-session-123")

		user := &User{Name: "testuser"}
		if err := s.StoreUser(ctx, user, false, "192.168.1.1:12345"); err != nil {
			t.Fatal(err)
		}
	})

	entries = filterAuditByEvent(entries, "USER_CREATE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 USER_CREATE entry, got %d", len(entries))
	}

	if entries[0].SessionID != "test-session-123" {
		t.Errorf("SessionID = %q, want %q", entries[0].SessionID, "test-session-123")
	}
}

// === Audit Log Format Tests ===

func TestAudit_EntriesAreValidJSON(t *testing.T) {
	dir, err := os.MkdirTemp("", "juicemud-audit-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithCancel(context.Background())
	ctx = juicemud.MakeMainContext(ctx)
	s, err := New(ctx, dir)
	if err != nil {
		cancel()
		t.Fatal(err)
	}

	user := &User{Name: "testuser"}
	if err := s.StoreUser(ctx, user, false, "192.168.1.1:12345"); err != nil {
		t.Fatal(err)
	}

	cancel()
	s.Close()

	// Read raw file content
	path := filepath.Join(dir, "audit.log")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	for i, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("Line %d is not valid JSON: %v\nContent: %s", i+1, err, line)
		}
		// Verify required fields
		if _, ok := entry["time"]; !ok {
			t.Errorf("Line %d missing 'time' field", i+1)
		}
		if _, ok := entry["event"]; !ok {
			t.Errorf("Line %d missing 'event' field", i+1)
		}
		if _, ok := entry["data"]; !ok {
			t.Errorf("Line %d missing 'data' field", i+1)
		}
	}
}

func TestAudit_TimeIsRFC3339(t *testing.T) {
	entries := withTestStorage(t, func(s *Storage) {
		ctx := juicemud.MakeMainContext(context.Background())
		user := &User{Name: "testuser"}
		if err := s.StoreUser(ctx, user, false, "192.168.1.1:12345"); err != nil {
			t.Fatal(err)
		}
	})

	if len(entries) == 0 {
		t.Fatal("Expected at least one audit entry")
	}

	// Verify time format (should parse as RFC3339)
	for _, entry := range entries {
		if entry.Time == "" {
			t.Error("Time field is empty")
			continue
		}
		// Use proper RFC3339 parsing for validation
		if _, err := time.Parse(time.RFC3339Nano, entry.Time); err != nil {
			t.Errorf("Time %q is not valid RFC3339: %v", entry.Time, err)
		}
	}
}

// === Wizard Status Audit Tests ===

func TestAudit_WizardGrant(t *testing.T) {
	entries := withTestStorage(t, func(s *Storage) {
		ctx := juicemud.MakeMainContext(context.Background())

		// Create owner user who will grant wizard status
		owner := &User{Name: "owner", Owner: true, Wizard: true}
		if err := s.StoreUser(ctx, owner, false, "192.168.1.1:12345"); err != nil {
			t.Fatal(err)
		}

		// Create target user who will receive wizard status
		target := &User{Name: "target"}
		if err := s.StoreUser(ctx, target, false, "192.168.1.2:12345"); err != nil {
			t.Fatal(err)
		}

		// Set context as authenticated owner
		ctx = AuthenticateUser(ctx, owner)

		// Grant wizard status
		if err := s.SetUserWizard(ctx, "target", true); err != nil {
			t.Fatal(err)
		}
	})

	entries = filterAuditByEvent(entries, "WIZARD_GRANT")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 WIZARD_GRANT entry, got %d", len(entries))
	}

	var data AuditWizardGrant
	parseAuditData(t, entries[0], &data)
	if data.Target.Name != "target" {
		t.Errorf("Target.Name = %q, want %q", data.Target.Name, "target")
	}
	if data.GrantedBy.Name != "owner" {
		t.Errorf("GrantedBy.Name = %q, want %q", data.GrantedBy.Name, "owner")
	}
}

func TestAudit_WizardRevoke(t *testing.T) {
	entries := withTestStorage(t, func(s *Storage) {
		ctx := juicemud.MakeMainContext(context.Background())

		// Create owner user who will revoke wizard status
		owner := &User{Name: "owner", Owner: true, Wizard: true}
		if err := s.StoreUser(ctx, owner, false, "192.168.1.1:12345"); err != nil {
			t.Fatal(err)
		}

		// Create target user who will have wizard status revoked
		target := &User{Name: "target", Wizard: true}
		if err := s.StoreUser(ctx, target, false, "192.168.1.2:12345"); err != nil {
			t.Fatal(err)
		}

		// Set context as authenticated owner
		ctx = AuthenticateUser(ctx, owner)

		// Revoke wizard status
		if err := s.SetUserWizard(ctx, "target", false); err != nil {
			t.Fatal(err)
		}
	})

	entries = filterAuditByEvent(entries, "WIZARD_REVOKE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 WIZARD_REVOKE entry, got %d", len(entries))
	}

	var data AuditWizardRevoke
	parseAuditData(t, entries[0], &data)
	if data.Target.Name != "target" {
		t.Errorf("Target.Name = %q, want %q", data.Target.Name, "target")
	}
	if data.RevokedBy.Name != "owner" {
		t.Errorf("RevokedBy.Name = %q, want %q", data.RevokedBy.Name, "owner")
	}
}

func TestAudit_WizardGrant_RequiresOwner(t *testing.T) {
	withTestStorage(t, func(s *Storage) {
		ctx := juicemud.MakeMainContext(context.Background())

		// Create non-owner wizard user
		wizard := &User{Name: "wizard", Owner: false, Wizard: true}
		if err := s.StoreUser(ctx, wizard, false, "192.168.1.1:12345"); err != nil {
			t.Fatal(err)
		}

		// Create target user
		target := &User{Name: "target"}
		if err := s.StoreUser(ctx, target, false, "192.168.1.2:12345"); err != nil {
			t.Fatal(err)
		}

		// Set context as authenticated non-owner wizard
		ctx = AuthenticateUser(ctx, wizard)

		// Try to grant wizard status - should fail
		err := s.SetUserWizard(ctx, "target", true)
		if err == nil {
			t.Fatal("Expected error when non-owner tries to grant wizard")
		}
		if !strings.Contains(err.Error(), "only owners") {
			t.Errorf("Error should mention only owners: %v", err)
		}
	})
}

func TestAudit_WizardGrant_RequiresAuth(t *testing.T) {
	withTestStorage(t, func(s *Storage) {
		ctx := juicemud.MakeMainContext(context.Background())

		// Create target user
		target := &User{Name: "target"}
		if err := s.StoreUser(ctx, target, false, "192.168.1.2:12345"); err != nil {
			t.Fatal(err)
		}

		// Try to grant wizard status without authentication - should fail
		err := s.SetUserWizard(ctx, "target", true)
		if err == nil {
			t.Fatal("Expected error when unauthenticated user tries to grant wizard")
		}
		if !strings.Contains(err.Error(), "only owners") {
			t.Errorf("Error should mention only owners: %v", err)
		}
	})
}
