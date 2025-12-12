package storage

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zond/juicemud"
)

// testStorage creates a temporary storage for testing.
func testStorage(t *testing.T) (*Storage, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "juicemud-test-*")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = juicemud.MakeMainContext(ctx)
	s, err := New(ctx, dir)
	if err != nil {
		cancel()
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	cleanup := func() {
		cancel() // Cancel context first to stop goroutines
		s.Close()
		os.RemoveAll(dir)
	}
	return s, cleanup
}

// createTestUser creates a user for testing.
func createTestUser(t *testing.T, s *Storage, name string, owner bool) *User {
	t.Helper()
	ctx := juicemud.MakeMainContext(context.Background())
	user := &User{Name: name, Owner: owner}
	if err := s.StoreUser(ctx, user, false, "test"); err != nil {
		t.Fatal(err)
	}
	// Reload to get ID
	user, err := s.LoadUser(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

// createTestGroup creates a group directly in the database for testing.
func createTestGroup(t *testing.T, s *Storage, name string, ownerGroup int64, supergroup bool) *Group {
	t.Helper()
	ctx := juicemud.MakeMainContext(context.Background())
	group := &Group{Name: name, OwnerGroup: ownerGroup, Supergroup: supergroup}
	if _, err := s.EnsureGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	// Reload to get ID
	g, err := s.LoadGroup(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// userContext creates a context authenticated as the given user.
func userContext(user *User) context.Context {
	return AuthenticateUser(context.Background(), user)
}

// === Name Validation Tests ===

func TestValidateName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"a", true},
		{"wizards", true},
		{"Admins", true},
		{"guild-foo", true},
		{"test_group", true},
		{"A1B2C3", true},
		{"a-b_c-d", true},
		{"abcdefghijklmnop", true}, // 16 chars - max allowed

		{"", false},                  // empty
		{"1wizards", false},          // starts with digit
		{"-wizards", false},          // starts with hyphen
		{"_wizards", false},          // starts with underscore
		{"wiz@rds", false},           // invalid char
		{"wiz ards", false},          // space
		{"abcdefghijklmnopq", false}, // 17 chars - too long
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := juicemud.ValidateName(tt.name, "test name")
			got := err == nil
			if got != tt.valid {
				t.Errorf("ValidateName(%q) = %v (err=%v), want valid=%v", tt.name, got, err, tt.valid)
			}
		})
	}
}

// === Group Creation Tests ===

func TestCreateGroup_OwnerCanCreateWithOwnerGroup0(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	err := s.CreateGroup(ctx, "admins", "owner", false)
	if err != nil {
		t.Fatalf("Owner should be able to create group with OwnerGroup=0: %v", err)
	}

	g, err := s.LoadGroup(ctx, "admins")
	if err != nil {
		t.Fatalf("Failed to load created group: %v", err)
	}
	if g.OwnerGroup != 0 {
		t.Errorf("OwnerGroup = %d, want 0", g.OwnerGroup)
	}
}

func TestCreateGroup_OwnerCanCreateWithAnySupergroup(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create a supergroup first
	admins := createTestGroup(t, s, "admins", 0, true)

	err := s.CreateGroup(ctx, "wizards", "admins", false)
	if err != nil {
		t.Fatalf("Owner should be able to create group owned by any supergroup: %v", err)
	}

	g, err := s.LoadGroup(ctx, "wizards")
	if err != nil {
		t.Fatalf("Failed to load created group: %v", err)
	}
	if g.OwnerGroup != admins.Id {
		t.Errorf("OwnerGroup = %d, want %d", g.OwnerGroup, admins.Id)
	}
}

func TestCreateGroup_NonOwnerInSupergroupCanCreate(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// Setup: create supergroup owned by owner
	admins := createTestGroup(t, s, "admins", 0, true)

	// Create non-owner user and add to supergroup
	alice := createTestUser(t, s, "alice", false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "admins"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)

	err := s.CreateGroup(ctx, "wizards", "admins", false)
	if err != nil {
		t.Fatalf("Non-owner in Supergroup should be able to create group: %v", err)
	}

	g, err := s.LoadGroup(ctx, "wizards")
	if err != nil {
		t.Fatalf("Failed to load created group: %v", err)
	}
	if g.OwnerGroup != admins.Id {
		t.Errorf("OwnerGroup = %d, want %d", g.OwnerGroup, admins.Id)
	}
}

func TestCreateGroup_NonOwnerInNonSupergroupCannotCreate(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// Setup: create non-supergroup
	createTestGroup(t, s, "wizards", 0, false)

	// Create non-owner user and add to non-supergroup
	alice := createTestUser(t, s, "alice", false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "wizards"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)

	err := s.CreateGroup(ctx, "builders", "wizards", false)
	if err == nil {
		t.Fatal("Non-owner in non-Supergroup should NOT be able to create group")
	}
}

func TestCreateGroup_NonOwnerCannotCreateWithOwnerGroup0(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	alice := createTestUser(t, s, "alice", false)
	ctx := userContext(alice)

	err := s.CreateGroup(ctx, "admins", "owner", false)
	if err == nil {
		t.Fatal("Non-owner should NOT be able to create group with OwnerGroup=0")
	}
}

func TestCreateGroup_NonOwnerCannotCreateOwnedBySupergroupNotIn(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// Setup: create two supergroups
	createTestGroup(t, s, "admins", 0, true)
	createTestGroup(t, s, "mods", 0, true)

	// Alice is only in admins
	alice := createTestUser(t, s, "alice", false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "admins"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)

	// Try to create group owned by mods (not in)
	err := s.CreateGroup(ctx, "builders", "mods", false)
	if err == nil {
		t.Fatal("Non-owner should NOT be able to create group owned by Supergroup they're not in")
	}
}

func TestCreateGroup_InvalidNameRejected(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	err := s.CreateGroup(ctx, "1invalid", "owner", false)
	if err == nil {
		t.Fatal("Invalid group name should be rejected")
	}
}

func TestCreateGroup_ReservedNameRejected(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	err := s.CreateGroup(ctx, "owner", "owner", false)
	if err == nil {
		t.Fatal("Reserved group name 'owner' should be rejected")
	}
}

func TestCreateGroup_DuplicateNameRejected(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "admins", 0, true)

	err := s.CreateGroup(ctx, "admins", "owner", false)
	if err == nil {
		t.Fatal("Duplicate group name should be rejected")
	}
}

// === Group Deletion Tests ===

func TestDeleteGroup_OwnerCanDeleteEmptyGroup(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "empty", 0, false)

	err := s.DeleteGroup(ctx, "empty")
	if err != nil {
		t.Fatalf("Owner should be able to delete empty group: %v", err)
	}

	_, err = s.LoadGroup(ctx, "empty")
	if err == nil {
		t.Fatal("Group should not exist after deletion")
	}
}

func TestDeleteGroup_SupergroupMemberCanDelete(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// Setup: admins (supergroup) owns builders
	admins := createTestGroup(t, s, "admins", 0, true)
	createTestGroup(t, s, "builders", admins.Id, false)

	alice := createTestUser(t, s, "alice", false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "admins"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)

	err := s.DeleteGroup(ctx, "builders")
	if err != nil {
		t.Fatalf("Supergroup member should be able to delete owned group: %v", err)
	}
}

func TestDeleteGroup_CannotDeleteGroupWithMembers(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	alice := createTestUser(t, s, "alice", false)

	createTestGroup(t, s, "wizards", 0, false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "wizards"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(owner)

	err := s.DeleteGroup(ctx, "wizards")
	if err == nil {
		t.Fatal("Should NOT be able to delete group with members")
	}
}

func TestDeleteGroup_CannotDeleteGroupReferencedAsOwnerGroup(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	admins := createTestGroup(t, s, "admins", 0, true)
	createTestGroup(t, s, "wizards", admins.Id, false)

	err := s.DeleteGroup(ctx, "admins")
	if err == nil {
		t.Fatal("Should NOT be able to delete group referenced as OwnerGroup")
	}
}

func TestDeleteGroup_NonSupergroupMemberCannotDelete(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// Setup: wizards (non-supergroup) owns builders
	wizards := createTestGroup(t, s, "wizards", 0, false)
	createTestGroup(t, s, "builders", wizards.Id, false)

	alice := createTestUser(t, s, "alice", false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "wizards"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)

	err := s.DeleteGroup(ctx, "builders")
	if err == nil {
		t.Fatal("Non-supergroup member should NOT be able to delete groups")
	}
}

// === Membership Tests ===

func TestRemoveUserFromGroup_OwnerCanRemove(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	alice := createTestUser(t, s, "alice", false)

	createTestGroup(t, s, "wizards", 0, false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "wizards"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(owner)
	err := s.RemoveUserFromGroup(ctx, "alice", "wizards")
	if err != nil {
		t.Fatalf("Owner should be able to remove user from group: %v", err)
	}

	members, err := s.GroupMembers(ctx, "wizards")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if m.Name == "alice" {
			t.Fatal("Alice should have been removed from wizards")
		}
	}
}

func TestRemoveUserFromGroup_OwnerGroupMemberCanRemove(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// admins owns wizards
	admins := createTestGroup(t, s, "admins", 0, false)
	createTestGroup(t, s, "wizards", admins.Id, false)

	alice := createTestUser(t, s, "alice", false)
	bob := createTestUser(t, s, "bob", false)

	// Alice is admin, Bob is wizard
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "admins"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), bob, "wizards"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)
	err := s.RemoveUserFromGroup(ctx, "bob", "wizards")
	if err != nil {
		t.Fatalf("OwnerGroup member should be able to remove user: %v", err)
	}
}

func TestRemoveUserFromGroup_NonOwnerGroupMemberCannotRemove(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	createTestGroup(t, s, "wizards", 0, false)

	alice := createTestUser(t, s, "alice", false)
	bob := createTestUser(t, s, "bob", false)

	// Both are wizards, but wizards is owner-only (OwnerGroup=0)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "wizards"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), bob, "wizards"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)
	err := s.RemoveUserFromGroup(ctx, "bob", "wizards")
	if err == nil {
		t.Fatal("Non-OwnerGroup member should NOT be able to remove users")
	}
}

// === AddUserToGroup Permission Tests ===

func TestAddUserToGroup_OwnerCanAdd(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	alice := createTestUser(t, s, "alice", false)

	createTestGroup(t, s, "wizards", 0, false)

	ctx := userContext(owner)
	err := s.AddUserToGroup(ctx, alice, "wizards")
	if err != nil {
		t.Fatalf("Owner should be able to add user to group: %v", err)
	}

	members, err := s.GroupMembers(ctx, "wizards")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range members {
		if m.Name == "alice" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Alice should be in wizards")
	}
}

func TestAddUserToGroup_OwnerGroupMemberCanAdd(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// admins owns wizards
	admins := createTestGroup(t, s, "admins", 0, false)
	createTestGroup(t, s, "wizards", admins.Id, false)

	alice := createTestUser(t, s, "alice", false)
	bob := createTestUser(t, s, "bob", false)

	// Alice is admin
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "admins"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)
	err := s.AddUserToGroup(ctx, bob, "wizards")
	if err != nil {
		t.Fatalf("OwnerGroup member should be able to add user: %v", err)
	}
}

func TestAddUserToGroup_NonOwnerGroupMemberCannotAdd(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	createTestGroup(t, s, "wizards", 0, false)

	alice := createTestUser(t, s, "alice", false)
	bob := createTestUser(t, s, "bob", false)

	// Alice is a wizard, but wizards has OwnerGroup=0 (Owner-only)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "wizards"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)
	err := s.AddUserToGroup(ctx, bob, "wizards")
	if err == nil {
		t.Fatal("Non-OwnerGroup member should NOT be able to add users")
	}
}

func TestAddUserToGroup_NotInOwnerGroupCannotAdd(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// admins owns wizards
	admins := createTestGroup(t, s, "admins", 0, false)
	createTestGroup(t, s, "wizards", admins.Id, false)

	alice := createTestUser(t, s, "alice", false)
	bob := createTestUser(t, s, "bob", false)

	// Alice is NOT in admins
	ctx := userContext(alice)
	err := s.AddUserToGroup(ctx, bob, "wizards")
	if err == nil {
		t.Fatal("User not in OwnerGroup should NOT be able to add users")
	}
}

// === Group Editing Tests ===

func TestEditGroupName_OwnerGroupMemberCanRename(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	admins := createTestGroup(t, s, "admins", 0, false)
	createTestGroup(t, s, "wizards", admins.Id, false)

	alice := createTestUser(t, s, "alice", false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "admins"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)
	err := s.EditGroupName(ctx, "wizards", "mages")
	if err != nil {
		t.Fatalf("OwnerGroup member should be able to rename: %v", err)
	}

	_, err = s.LoadGroup(ctx, "mages")
	if err != nil {
		t.Fatal("Group should exist with new name")
	}
}

func TestEditGroupOwner_RequiresSupergroup(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// admins (supergroup) owns wizards
	admins := createTestGroup(t, s, "admins", 0, true)
	mods := createTestGroup(t, s, "mods", 0, true)
	createTestGroup(t, s, "wizards", admins.Id, false)

	alice := createTestUser(t, s, "alice", false)
	// Alice is in both supergroups
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "admins"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "mods"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)
	err := s.EditGroupOwner(ctx, "wizards", "mods")
	if err != nil {
		t.Fatalf("Should be able to transfer to another Supergroup: %v", err)
	}

	g, err := s.LoadGroup(ctx, "wizards")
	if err != nil {
		t.Fatal(err)
	}
	if g.OwnerGroup != mods.Id {
		t.Errorf("OwnerGroup = %d, want %d", g.OwnerGroup, mods.Id)
	}
}

func TestEditGroupOwner_CannotSetToNonSupergroup(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	admins := createTestGroup(t, s, "admins", 0, true)
	createTestGroup(t, s, "builders", 0, false) // NOT a supergroup
	createTestGroup(t, s, "wizards", admins.Id, false)

	alice := createTestUser(t, s, "alice", false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "admins"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "builders"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)
	err := s.EditGroupOwner(ctx, "wizards", "builders")
	if err == nil {
		t.Fatal("Should NOT be able to transfer to non-Supergroup")
	}
}

func TestEditGroupOwner_SelfOwnershipRejected(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// wizards is a supergroup so it could theoretically own itself
	createTestGroup(t, s, "wizards", 0, true)

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	err := s.EditGroupOwner(ctx, "wizards", "wizards")
	if err == nil {
		t.Fatal("Self-ownership should be rejected")
	}
}

func TestEditGroupOwner_CycleRejected(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// Create: admins -> wizards -> builders (all supergroups for the test)
	admins := createTestGroup(t, s, "admins", 0, true)
	wizards := createTestGroup(t, s, "wizards", admins.Id, true)
	createTestGroup(t, s, "builders", wizards.Id, true)

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Try to make admins owned by builders (would create cycle)
	err := s.EditGroupOwner(ctx, "admins", "builders")
	if err == nil {
		t.Fatal("Cycle should be rejected")
	}
}

func TestEditGroupSupergroup_RequiresSupergroup(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	admins := createTestGroup(t, s, "admins", 0, true)
	createTestGroup(t, s, "wizards", admins.Id, false)

	alice := createTestUser(t, s, "alice", false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "admins"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)
	err := s.EditGroupSupergroup(ctx, "wizards", true)
	if err != nil {
		t.Fatalf("Supergroup member should be able to grant Supergroup: %v", err)
	}

	g, err := s.LoadGroup(ctx, "wizards")
	if err != nil {
		t.Fatal(err)
	}
	if !g.Supergroup {
		t.Error("Supergroup should be true")
	}
}

func TestEditGroupSupergroup_NonSupergroupCannotGrant(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// wizards (non-supergroup) owns builders
	wizards := createTestGroup(t, s, "wizards", 0, false)
	createTestGroup(t, s, "builders", wizards.Id, false)

	alice := createTestUser(t, s, "alice", false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "wizards"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)
	err := s.EditGroupSupergroup(ctx, "builders", true)
	if err == nil {
		t.Fatal("Non-supergroup member should NOT be able to grant Supergroup")
	}
}

// === Query Helper Tests ===

func TestListGroups(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	ctx := juicemud.MakeMainContext(context.Background())

	createTestGroup(t, s, "admins", 0, true)
	createTestGroup(t, s, "wizards", 0, false)
	createTestGroup(t, s, "builders", 0, false)

	groups, err := s.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(groups) != 3 {
		t.Errorf("Expected 3 groups, got %d", len(groups))
	}

	// Should be sorted by name
	names := make([]string, len(groups))
	for i, g := range groups {
		names[i] = g.Name
	}
	if names[0] != "admins" || names[1] != "builders" || names[2] != "wizards" {
		t.Errorf("Groups not sorted: %v", names)
	}
}

func TestGroupMembers(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	ctx := juicemud.MakeMainContext(context.Background())

	createTestGroup(t, s, "wizards", 0, false)
	alice := createTestUser(t, s, "alice", false)
	bob := createTestUser(t, s, "bob", false)

	if err := s.AddUserToGroup(ctx, alice, "wizards"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUserToGroup(ctx, bob, "wizards"); err != nil {
		t.Fatal(err)
	}

	members, err := s.GroupMembers(ctx, "wizards")
	if err != nil {
		t.Fatal(err)
	}

	if len(members) != 2 {
		t.Errorf("Expected 2 members, got %d", len(members))
	}
}

// === File Reference Tests ===

func TestDeleteGroup_CannotDeleteGroupReferencedByFiles(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	wizards := createTestGroup(t, s, "wizards", 0, false)

	// Create root directory first
	if err := s.CreateDir(juicemud.MakeMainContext(context.Background()), "/"); err != nil {
		t.Fatal(err)
	}

	// Create a file that references the group
	file := &File{
		Parent:     0,
		Path:       "/test.js",
		Name:       "test.js",
		Dir:        false,
		ReadGroup:  wizards.Id,
		WriteGroup: wizards.Id,
	}
	if err := s.sql.Upsert(ctx, file, false); err != nil {
		t.Fatal(err)
	}

	err := s.DeleteGroup(ctx, "wizards")
	if err == nil {
		t.Fatal("Should NOT be able to delete group referenced by files")
	}
}

// === Additional Tests from Review ===

func TestLoadGroup_EmptyNameReturnsError(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	ctx := juicemud.MakeMainContext(context.Background())

	_, err := s.LoadGroup(ctx, "")
	if err == nil {
		t.Fatal("LoadGroup with empty name should return error")
	}
}

func TestCreateGroup_NonExistentOwnerGroupRejected(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	err := s.CreateGroup(ctx, "wizards", "nonexistent", false)
	if err == nil {
		t.Fatal("Should reject non-existent OwnerGroup")
	}
}

func TestEditGroupOwner_NonExistentOwnerGroupRejected(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)

	err := s.EditGroupOwner(ctx, "wizards", "nonexistent")
	if err == nil {
		t.Fatal("Should reject non-existent OwnerGroup")
	}
}

func TestCreateGroup_NoAuthenticatedUserFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	ctx := context.Background() // No authenticated user

	err := s.CreateGroup(ctx, "admins", "owner", false)
	if err == nil {
		t.Fatal("Should fail without authenticated user")
	}
}

func TestDeleteGroup_NonExistentGroupFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	err := s.DeleteGroup(ctx, "nonexistent")
	if err == nil {
		t.Fatal("Deleting non-existent group should fail")
	}
}

func TestEditGroupName_InvalidNameRejected(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)

	err := s.EditGroupName(ctx, "wizards", "1invalid")
	if err == nil {
		t.Fatal("Invalid new name should be rejected")
	}
}

func TestEditGroupName_ReservedNameRejected(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)

	err := s.EditGroupName(ctx, "wizards", "owner")
	if err == nil {
		t.Fatal("Reserved name 'owner' should be rejected")
	}
}

func TestEditGroupName_DuplicateNameRejected(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "admins", 0, false)
	createTestGroup(t, s, "wizards", 0, false)

	err := s.EditGroupName(ctx, "wizards", "admins")
	if err == nil {
		t.Fatal("Renaming to existing name should be rejected")
	}
}

func TestEditGroupOwner_NonOwnerCannotSetToOwner(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	admins := createTestGroup(t, s, "admins", 0, true)
	createTestGroup(t, s, "wizards", admins.Id, false)

	alice := createTestUser(t, s, "alice", false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "admins"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)
	err := s.EditGroupOwner(ctx, "wizards", "owner")
	if err == nil {
		t.Fatal("Non-owner should NOT be able to set OwnerGroup to 0")
	}
}

func TestEditGroupOwner_OwnerCanSetToOwner(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	admins := createTestGroup(t, s, "admins", 0, true)
	createTestGroup(t, s, "wizards", admins.Id, false)

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	err := s.EditGroupOwner(ctx, "wizards", "owner")
	if err != nil {
		t.Fatalf("Owner should be able to set OwnerGroup to 0: %v", err)
	}

	g, err := s.LoadGroup(ctx, "wizards")
	if err != nil {
		t.Fatal(err)
	}
	if g.OwnerGroup != 0 {
		t.Errorf("OwnerGroup = %d, want 0", g.OwnerGroup)
	}
}

// === Transactional Integrity Tests ===

func TestCreateGroup_ConcurrentCreationOfSameName(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create a supergroup for ownership
	createTestGroup(t, s, "admins", 0, true)

	// Launch multiple goroutines trying to create the same group
	var wg sync.WaitGroup
	successCount := int32(0)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.CreateGroup(ctx, "newgroup", "admins", false)
			if err == nil {
				atomic.AddInt32(&successCount, 1)
			}
		}()
	}
	wg.Wait()

	if successCount != 1 {
		t.Errorf("Expected exactly 1 successful creation, got %d", successCount)
	}
}

// === Owner User Edit Operations Tests ===

func TestEditGroupName_OwnerCanRenameAnyGroup(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create group with OwnerGroup=0 (no one in OwnerGroup)
	createTestGroup(t, s, "wizards", 0, false)

	err := s.EditGroupName(ctx, "wizards", "mages")
	if err != nil {
		t.Fatalf("Owner should be able to rename any group: %v", err)
	}
}

func TestEditGroupSupergroup_OwnerCanModifyAnyGroup(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)

	err := s.EditGroupSupergroup(ctx, "wizards", true)
	if err != nil {
		t.Fatalf("Owner should be able to modify Supergroup on any group: %v", err)
	}

	g, err := s.LoadGroup(ctx, "wizards")
	if err != nil {
		t.Fatal(err)
	}
	if !g.Supergroup {
		t.Error("Supergroup should be true")
	}
}

// === Additional Permission Tests ===

func TestEditGroupName_NonOwnerGroupMemberCannotRename(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// admins owns wizards, alice is NOT in admins
	admins := createTestGroup(t, s, "admins", 0, false)
	createTestGroup(t, s, "wizards", admins.Id, false)

	alice := createTestUser(t, s, "alice", false)
	// Alice is in wizards but NOT in admins (the OwnerGroup)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "wizards"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)
	err := s.EditGroupName(ctx, "wizards", "mages")
	if err == nil {
		t.Fatal("Non-OwnerGroup member should NOT be able to rename group")
	}
}

func TestRemoveUserFromGroup_NotInOwnerGroupCannotRemove(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	// admins owns wizards
	admins := createTestGroup(t, s, "admins", 0, false)
	createTestGroup(t, s, "wizards", admins.Id, false)

	alice := createTestUser(t, s, "alice", false)
	bob := createTestUser(t, s, "bob", false)

	// Alice is NOT in admins, Bob is in wizards
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), bob, "wizards"); err != nil {
		t.Fatal(err)
	}

	ctx := userContext(alice)
	err := s.RemoveUserFromGroup(ctx, "bob", "wizards")
	if err == nil {
		t.Fatal("User not in OwnerGroup should NOT be able to remove users")
	}
}

// === Unauthenticated Context Tests ===

func TestDeleteGroup_NoAuthenticatedUserFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	createTestGroup(t, s, "wizards", 0, false)

	ctx := context.Background() // No authenticated user
	err := s.DeleteGroup(ctx, "wizards")
	if err == nil {
		t.Fatal("Should fail without authenticated user")
	}
}

func TestEditGroupName_NoAuthenticatedUserFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	createTestGroup(t, s, "wizards", 0, false)

	ctx := context.Background() // No authenticated user
	err := s.EditGroupName(ctx, "wizards", "mages")
	if err == nil {
		t.Fatal("Should fail without authenticated user")
	}
}

func TestEditGroupOwner_NoAuthenticatedUserFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	admins := createTestGroup(t, s, "admins", 0, true)
	createTestGroup(t, s, "wizards", admins.Id, false)

	ctx := context.Background() // No authenticated user
	err := s.EditGroupOwner(ctx, "wizards", "owner")
	if err == nil {
		t.Fatal("Should fail without authenticated user")
	}
}

func TestEditGroupSupergroup_NoAuthenticatedUserFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	admins := createTestGroup(t, s, "admins", 0, true)
	createTestGroup(t, s, "wizards", admins.Id, false)

	ctx := context.Background() // No authenticated user
	err := s.EditGroupSupergroup(ctx, "wizards", true)
	if err == nil {
		t.Fatal("Should fail without authenticated user")
	}
}

func TestAddUserToGroup_NoAuthenticatedUserFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	createTestGroup(t, s, "wizards", 0, false)
	alice := createTestUser(t, s, "alice", false)

	ctx := context.Background() // No authenticated user
	err := s.AddUserToGroup(ctx, alice, "wizards")
	if err == nil {
		t.Fatal("Should fail without authenticated user")
	}
}

func TestRemoveUserFromGroup_NoAuthenticatedUserFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	createTestGroup(t, s, "wizards", 0, false)
	alice := createTestUser(t, s, "alice", false)
	if err := s.AddUserToGroup(juicemud.MakeMainContext(context.Background()), alice, "wizards"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background() // No authenticated user
	err := s.RemoveUserFromGroup(ctx, "alice", "wizards")
	if err == nil {
		t.Fatal("Should fail without authenticated user")
	}
}

// === Non-Existent Group Tests ===

func TestEditGroupName_NonExistentGroupFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	err := s.EditGroupName(ctx, "nonexistent", "newname")
	if err == nil {
		t.Fatal("Editing non-existent group should fail")
	}
}

func TestEditGroupOwner_NonExistentGroupFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "admins", 0, true)

	err := s.EditGroupOwner(ctx, "nonexistent", "admins")
	if err == nil {
		t.Fatal("Editing non-existent group should fail")
	}
}

func TestEditGroupSupergroup_NonExistentGroupFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	err := s.EditGroupSupergroup(ctx, "nonexistent", true)
	if err == nil {
		t.Fatal("Editing non-existent group should fail")
	}
}

func TestAddUserToGroup_NonExistentGroupFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	alice := createTestUser(t, s, "alice", false)
	ctx := userContext(owner)

	err := s.AddUserToGroup(ctx, alice, "nonexistent")
	if err == nil {
		t.Fatal("Adding to non-existent group should fail")
	}
}

func TestRemoveUserFromGroup_NonExistentGroupFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	createTestUser(t, s, "alice", false)
	ctx := userContext(owner)

	err := s.RemoveUserFromGroup(ctx, "alice", "nonexistent")
	if err == nil {
		t.Fatal("Removing from non-existent group should fail")
	}
}

// === Membership Edge Cases ===

func TestRemoveUserFromGroup_UserNotMemberFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	createTestUser(t, s, "alice", false)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)
	// Alice is NOT a member of wizards

	err := s.RemoveUserFromGroup(ctx, "alice", "wizards")
	if err == nil {
		t.Fatal("Removing user who is not a member should fail")
	}
}

func TestRemoveUserFromGroup_NonExistentUserFails(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)

	err := s.RemoveUserFromGroup(ctx, "nonexistent", "wizards")
	if err == nil {
		t.Fatal("Removing non-existent user should fail")
	}
}

func TestAddUserToGroup_AlreadyMemberIsIdempotent(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	alice := createTestUser(t, s, "alice", false)
	ctx := userContext(owner)

	createTestGroup(t, s, "wizards", 0, false)

	// Add alice to wizards
	if err := s.AddUserToGroup(ctx, alice, "wizards"); err != nil {
		t.Fatalf("First add should succeed: %v", err)
	}

	// Adding again should be idempotent (no error)
	if err := s.AddUserToGroup(ctx, alice, "wizards"); err != nil {
		t.Fatalf("Adding already-member should be idempotent: %v", err)
	}

	// Verify alice is still a member (only once)
	members, err := s.GroupMembers(ctx, "wizards")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 {
		t.Fatalf("Expected 1 member, got %d", len(members))
	}
	if members[0].Name != "alice" {
		t.Fatalf("Expected alice, got %s", members[0].Name)
	}
}
