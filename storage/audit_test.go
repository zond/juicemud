package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// testStorageWithDir creates a temporary storage and returns the directory path.
func testStorageWithDir(t *testing.T) (*Storage, string, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "juicemud-audit-test-*")
	if err != nil {
		t.Fatal(err)
	}
	ctx := juicemud.MakeMainContext(context.Background())
	s, err := New(ctx, dir)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	cleanup := func() {
		s.Close()
		os.RemoveAll(dir)
	}
	return s, dir, cleanup
}

// === User Audit Tests ===

func TestAudit_UserCreate(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	ctx := juicemud.MakeMainContext(context.Background())
	user := &User{Name: "alice"}
	if err := s.StoreUser(ctx, user, false, "192.168.1.1:12345"); err != nil {
		t.Fatal(err)
	}

	// Close to flush audit log
	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "USER_CREATE")
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
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

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

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "USER_CREATE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 USER_CREATE entry (not logged on update), got %d", len(entries))
	}
}

// === Group Audit Tests ===

func TestAudit_GroupCreate(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	if err := s.CreateGroup(ctx, "wizards", "owner", true); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "GROUP_CREATE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 GROUP_CREATE entry, got %d", len(entries))
	}

	var data AuditGroupCreate
	parseAuditData(t, entries[0], &data)
	if data.Caller.Name != "owner" {
		t.Errorf("Caller.Name = %q, want %q", data.Caller.Name, "owner")
	}
	if data.Group.Name != "wizards" {
		t.Errorf("Group.Name = %q, want %q", data.Group.Name, "wizards")
	}
	if data.Owner.Name != "owner" {
		t.Errorf("Owner.Name = %q, want %q", data.Owner.Name, "owner")
	}
	if !data.Supergroup {
		t.Error("Supergroup should be true")
	}
}

func TestAudit_GroupDelete(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)

	if err := s.DeleteGroup(ctx, "wizards"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "GROUP_DELETE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 GROUP_DELETE entry, got %d", len(entries))
	}

	var data AuditGroupDelete
	parseAuditData(t, entries[0], &data)
	if data.Caller.Name != "owner" {
		t.Errorf("Caller.Name = %q, want %q", data.Caller.Name, "owner")
	}
	if data.Group.Name != "wizards" {
		t.Errorf("Group.Name = %q, want %q", data.Group.Name, "wizards")
	}
}

func TestAudit_GroupEditName(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)

	if err := s.EditGroupName(ctx, "wizards", "mages"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "GROUP_EDIT")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 GROUP_EDIT entry, got %d", len(entries))
	}

	var data AuditGroupEdit
	parseAuditData(t, entries[0], &data)
	if data.Caller.Name != "owner" {
		t.Errorf("Caller.Name = %q, want %q", data.Caller.Name, "owner")
	}
	if data.NameFrom != "wizards" {
		t.Errorf("NameFrom = %q, want %q", data.NameFrom, "wizards")
	}
	if data.NameTo != "mages" {
		t.Errorf("NameTo = %q, want %q", data.NameTo, "mages")
	}
}

func TestAudit_GroupEditOwner(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	admins := createTestGroup(t, s, "admins", 0, true)
	createTestGroup(t, s, "mods", 0, true)
	createTestGroup(t, s, "wizards", admins.Id, false)

	if err := s.EditGroupOwner(ctx, "wizards", "mods"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "GROUP_EDIT")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 GROUP_EDIT entry, got %d", len(entries))
	}

	var data AuditGroupEdit
	parseAuditData(t, entries[0], &data)
	if data.OwnerFrom == nil || data.OwnerFrom.Name != "admins" {
		t.Errorf("OwnerFrom.Name = %v, want %q", data.OwnerFrom, "admins")
	}
	if data.OwnerTo == nil || data.OwnerTo.Name != "mods" {
		t.Errorf("OwnerTo.Name = %v, want %q", data.OwnerTo, "mods")
	}
}

func TestAudit_GroupEditSupergroup(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)

	if err := s.EditGroupSupergroup(ctx, "wizards", true); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "GROUP_EDIT")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 GROUP_EDIT entry, got %d", len(entries))
	}

	var data AuditGroupEdit
	parseAuditData(t, entries[0], &data)
	if data.SupergroupFrom == nil || *data.SupergroupFrom != false {
		t.Errorf("SupergroupFrom = %v, want false", data.SupergroupFrom)
	}
	if data.SupergroupTo == nil || *data.SupergroupTo != true {
		t.Errorf("SupergroupTo = %v, want true", data.SupergroupTo)
	}
}

// === Membership Audit Tests ===

func TestAudit_MemberAdd(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	alice := createTestUser(t, s, "alice", false)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)

	if err := s.AddUserToGroup(ctx, alice, "wizards"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "MEMBER_ADD")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 MEMBER_ADD entry, got %d", len(entries))
	}

	var data AuditMemberAdd
	parseAuditData(t, entries[0], &data)
	if data.Caller.Name != "owner" {
		t.Errorf("Caller.Name = %q, want %q", data.Caller.Name, "owner")
	}
	if data.Added.Name != "alice" {
		t.Errorf("Added.Name = %q, want %q", data.Added.Name, "alice")
	}
	if data.Group.Name != "wizards" {
		t.Errorf("Group.Name = %q, want %q", data.Group.Name, "wizards")
	}
}

func TestAudit_MemberAdd_NotLoggedWhenAlreadyMember(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	alice := createTestUser(t, s, "alice", false)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)

	// Add alice first time
	if err := s.AddUserToGroup(ctx, alice, "wizards"); err != nil {
		t.Fatal(err)
	}
	// Add alice second time (should be no-op, no audit entry)
	if err := s.AddUserToGroup(ctx, alice, "wizards"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "MEMBER_ADD")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 MEMBER_ADD entry (not logged when already member), got %d", len(entries))
	}
}

func TestAudit_MemberRemove(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	alice := createTestUser(t, s, "alice", false)

	createTestGroup(t, s, "wizards", 0, false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "wizards"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(owner)
	if err := s.RemoveUserFromGroup(ctx, "alice", "wizards"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "MEMBER_REMOVE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 MEMBER_REMOVE entry, got %d", len(entries))
	}

	var data AuditMemberRemove
	parseAuditData(t, entries[0], &data)
	if data.Caller.Name != "owner" {
		t.Errorf("Caller.Name = %q, want %q", data.Caller.Name, "owner")
	}
	if data.Removed.Name != "alice" {
		t.Errorf("Removed.Name = %q, want %q", data.Removed.Name, "alice")
	}
	if data.Group.Name != "wizards" {
		t.Errorf("Group.Name = %q, want %q", data.Group.Name, "wizards")
	}
}

// === File Audit Tests ===

func TestAudit_FileCreate(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create root first
	if err := s.CreateDir(juicemud.MakeMainContext(context.Background()), "/"); err != nil {
		t.Fatal(err)
	}

	// Create a file
	if _, _, err := s.EnsureFile(ctx, "/test.js"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "FILE_CREATE")
	// Find the one for /test.js (not root)
	var found *auditEntry
	for _, e := range entries {
		var data AuditFileCreate
		parseAuditData(t, e, &data)
		if data.Path == "/test.js" {
			found = &e
			break
		}
	}
	if found == nil {
		t.Fatal("Expected FILE_CREATE entry for /test.js")
	}

	var data AuditFileCreate
	parseAuditData(t, *found, &data)
	if data.Caller.Name != "owner" {
		t.Errorf("Caller.Name = %q, want %q", data.Caller.Name, "owner")
	}
	if data.IsDir {
		t.Error("IsDir should be false for a file")
	}
}

func TestAudit_FileCreate_Directory(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create root first
	if err := s.CreateDir(juicemud.MakeMainContext(context.Background()), "/"); err != nil {
		t.Fatal(err)
	}

	// Create a directory
	if err := s.CreateDir(ctx, "/mydir"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "FILE_CREATE")
	var found *auditEntry
	for _, e := range entries {
		var data AuditFileCreate
		parseAuditData(t, e, &data)
		if data.Path == "/mydir" {
			found = &e
			break
		}
	}
	if found == nil {
		t.Fatal("Expected FILE_CREATE entry for /mydir")
	}

	var data AuditFileCreate
	parseAuditData(t, *found, &data)
	if !data.IsDir {
		t.Error("IsDir should be true for a directory")
	}
}

func TestAudit_FileUpdate(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create root and file first
	if err := s.CreateDir(juicemud.MakeMainContext(context.Background()), "/"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnsureFile(ctx, "/test.js"); err != nil {
		t.Fatal(err)
	}

	// Update the file
	if err := s.StoreSource(ctx, "/test.js", []byte("// updated content")); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "FILE_UPDATE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 FILE_UPDATE entry, got %d", len(entries))
	}

	var data AuditFileUpdate
	parseAuditData(t, entries[0], &data)
	if data.Caller.Name != "owner" {
		t.Errorf("Caller.Name = %q, want %q", data.Caller.Name, "owner")
	}
	if data.Path != "/test.js" {
		t.Errorf("Path = %q, want %q", data.Path, "/test.js")
	}
}

func TestAudit_FileDelete(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create root and file first
	if err := s.CreateDir(juicemud.MakeMainContext(context.Background()), "/"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnsureFile(ctx, "/test.js"); err != nil {
		t.Fatal(err)
	}

	// Delete the file
	if err := s.DelFile(ctx, "/test.js"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "FILE_DELETE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 FILE_DELETE entry, got %d", len(entries))
	}

	var data AuditFileDelete
	parseAuditData(t, entries[0], &data)
	if data.Caller.Name != "owner" {
		t.Errorf("Caller.Name = %q, want %q", data.Caller.Name, "owner")
	}
	if data.Path != "/test.js" {
		t.Errorf("Path = %q, want %q", data.Path, "/test.js")
	}
}

func TestAudit_FileMove(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create root and file first
	if err := s.CreateDir(juicemud.MakeMainContext(context.Background()), "/"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnsureFile(ctx, "/old.js"); err != nil {
		t.Fatal(err)
	}

	// Move/rename the file
	if err := s.MoveFile(ctx, "/old.js", "/new.js"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "FILE_MOVE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 FILE_MOVE entry, got %d", len(entries))
	}

	var data AuditFileMove
	parseAuditData(t, entries[0], &data)
	if data.Caller.Name != "owner" {
		t.Errorf("Caller.Name = %q, want %q", data.Caller.Name, "owner")
	}
	if data.OldPath != "/old.js" {
		t.Errorf("OldPath = %q, want %q", data.OldPath, "/old.js")
	}
	if data.NewPath != "/new.js" {
		t.Errorf("NewPath = %q, want %q", data.NewPath, "/new.js")
	}
}

func TestAudit_FileChmod(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create root, file, and a group
	if err := s.CreateDir(juicemud.MakeMainContext(context.Background()), "/"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnsureFile(ctx, "/test.js"); err != nil {
		t.Fatal(err)
	}
	createTestGroup(t, s, "wizards", 0, false)

	// Change read permission
	if err := s.ChreadFile(ctx, "/test.js", "wizards"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "FILE_CHMOD")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 FILE_CHMOD entry, got %d", len(entries))
	}

	var data AuditFileChmod
	parseAuditData(t, entries[0], &data)
	if data.Caller.Name != "owner" {
		t.Errorf("Caller.Name = %q, want %q", data.Caller.Name, "owner")
	}
	if data.Path != "/test.js" {
		t.Errorf("Path = %q, want %q", data.Path, "/test.js")
	}
	if data.Permission != "read" {
		t.Errorf("Permission = %q, want %q", data.Permission, "read")
	}
	if data.NewGroup == nil || data.NewGroup.Name != "wizards" {
		t.Errorf("NewGroup = %v, want wizards", data.NewGroup)
	}
}

func TestAudit_FileChmod_NotLoggedWhenUnchanged(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create root, file, and a group
	if err := s.CreateDir(juicemud.MakeMainContext(context.Background()), "/"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnsureFile(ctx, "/test.js"); err != nil {
		t.Fatal(err)
	}
	createTestGroup(t, s, "wizards", 0, false)

	// Change read permission
	if err := s.ChreadFile(ctx, "/test.js", "wizards"); err != nil {
		t.Fatal(err)
	}
	// Change to the same group again (should not log)
	if err := s.ChreadFile(ctx, "/test.js", "wizards"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "FILE_CHMOD")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 FILE_CHMOD entry (not logged when unchanged), got %d", len(entries))
	}
}

// === Session ID Tests ===

func TestAudit_SessionIDIncluded(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)
	ctx = SetSessionID(ctx, "test-session-123")

	createTestGroup(t, s, "wizards", 0, false)

	if err := s.DeleteGroup(ctx, "wizards"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "GROUP_DELETE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 GROUP_DELETE entry, got %d", len(entries))
	}

	if entries[0].SessionID != "test-session-123" {
		t.Errorf("SessionID = %q, want %q", entries[0].SessionID, "test-session-123")
	}
}

// === System Caller Tests ===

func TestAudit_SystemCallerWhenNoAuth(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	// Use main context (no authenticated user)
	ctx := juicemud.MakeMainContext(context.Background())

	// Create root directory (called by system)
	if err := s.CreateDir(ctx, "/"); err != nil {
		t.Fatal(err)
	}

	s.Close()

	entries := filterAuditByEvent(readAuditLog(t, dir), "FILE_CREATE")
	if len(entries) != 1 {
		t.Fatalf("Expected 1 FILE_CREATE entry, got %d", len(entries))
	}

	var data AuditFileCreate
	parseAuditData(t, entries[0], &data)
	if data.Caller.Name != "system" {
		t.Errorf("Caller.Name = %q, want %q", data.Caller.Name, "system")
	}
	if data.Caller.ID != nil {
		t.Errorf("Caller.ID = %v, want nil for system", data.Caller.ID)
	}
}

// === Audit Log Format Tests ===

func TestAudit_EntriesAreValidJSON(t *testing.T) {
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)
	if err := s.DeleteGroup(ctx, "wizards"); err != nil {
		t.Fatal(err)
	}

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
	s, dir, cleanup := testStorageWithDir(t)
	defer cleanup()

	createTestUser(t, s, "owner", true)
	createTestGroup(t, s, "wizards", 0, false)

	s.Close()

	entries := readAuditLog(t, dir)
	if len(entries) == 0 {
		t.Fatal("Expected at least one audit entry")
	}

	// Verify time format (should parse as RFC3339)
	for _, entry := range entries {
		if entry.Time == "" {
			t.Error("Time field is empty")
			continue
		}
		// RFC3339Nano should parse RFC3339 as well
		if !strings.Contains(entry.Time, "T") || !strings.Contains(entry.Time, "Z") {
			t.Errorf("Time %q doesn't look like RFC3339", entry.Time)
		}
	}
}
