# Group System

> **Design Document**: This describes the proposed group permission system. The current
> implementation only has basic group membership. The following features need to be added:
> - `Supergroup` field on the Group struct
> - Commands: `/mkgroup`, `/rmgroup`, `/adduser`, `/rmuser`, `/editgroup`, `/listgroups`, `/members`, `/checkperm`
> - `RemoveUserFromGroup` storage function
> - OwnerGroup-based permission checks
> - Orphan prevention (can't delete groups that are referenced)
> - OwnerGroup validation (must reference existing group or be 0)
> - Cycle prevention
> - Group name validation
> - Fix `loadGroupByName("")` to return error instead of zero-value Group

This document describes the group permission system used for access control in juicemud.

## Terminology

- **Owner user**: A user with `Owner=true` flag—a superuser who bypasses all permission checks
- **Wizard**: A user who is a member of the "wizards" group, granting access to `/`-commands

Note: Group names and user names are separate namespaces and do not conflict.

## Data Model

```go
type Group struct {
    Id         int64  `sqly:"pkey"`
    Name       string `sqly:"unique"`  // Max 16 ASCII characters
    OwnerGroup int64  // 0 = Owner-only, else = ID of group that manages this
    Supergroup bool   // Members have full admin rights over owned groups
}

type User struct {
    // ...
    Owner bool  // Superuser flag - bypasses all group checks
}
```

### Group Name Constraints

- Minimum 1 character, maximum 16 characters
- Must start with a letter (a-z, A-Z)
- Remaining characters: letters, digits (0-9), hyphen (-), underscore (_)
- Case-sensitive
- Must be unique
- Reserved names: `owner` (used as keyword in commands)

Validation regex: `^[a-zA-Z][a-zA-Z0-9_-]{0,15}$`

### OwnerGroup Constraints

- Must be 0 (Owner-only) or reference an existing group's ID
- Cannot reference the group itself (no self-ownership)

## Core Concepts

### Owner Users
Users with `Owner=true` are superusers who bypass all permission checks. They can:
- Manage any group's membership
- Create/delete any group
- Modify any group's properties
- Set `OwnerGroup=0` (which means "Owner-only")

The initial Owner user is created via direct database access or API calls during server setup.

### OwnerGroup
Each group has an `OwnerGroup` field that determines who can manage it:
- `OwnerGroup=0`: Only Owner users can manage this group
- `OwnerGroup=G`: Members of group G can manage this group

"Manage" means adding/removing members and editing properties (see Permission Matrix for specific constraints on each action).

### Supergroup
This flag grants members full administrative power over the group hierarchy:
- **Direct admin**: Full control over groups directly owned by this Supergroup (where `group.OwnerGroup == this_supergroup`)
- **Indirect admin**: Members can gain control of groups deeper in the hierarchy by first transferring them up. For example, if `admins` (Supergroup) owns `wizards`, and `wizards` owns `builders`, an admin can `/editgroup wizards -owner admins` to take direct ownership of `wizards`, then use their `wizards` membership to transfer `builders`. This requires being in the intermediate group—there's no automatic transitive power.

Without this flag, members can only manage membership and names of groups they directly own, not create, delete, or modify Supergroup flags.

## Permission Matrix

| Action | Permission Required | Constraints |
|--------|---------------------|-------------|
| **Create group** | Owner, OR in group G where G.Supergroup=true | New group's OwnerGroup must be G (explicitly specified); if in multiple Supergroups, any of them may be specified |
| **Delete group X** | Owner, OR in X.OwnerGroup where it has Supergroup=true | X must be empty (no members), no groups reference X as OwnerGroup, no files reference X (File.ReadGroup or File.WriteGroup) |
| **Add/remove members of X** | Owner, OR in X.OwnerGroup | — |
| **Edit X.Name** | Owner, OR in X.OwnerGroup | Valid name (see Group Name Constraints) |
| **Edit X.OwnerGroup** | Owner, OR in X.OwnerGroup | New value must be a Supergroup you're in (or 0 if Owner); cannot be X itself |
| **Edit X.Supergroup** | Owner, OR in X.OwnerGroup where it has Supergroup=true | Only Supergroup members can grant Supergroup status |

## Commands

All `/`-commands require the user to be a wizard (member of the "wizards" group).

### Group Management

```
/mkgroup <name> <owner>             Create group with specified OwnerGroup
/rmgroup <name>                     Delete group (must be empty and unreferenced)
/editgroup <group> [options]        Edit group properties
    -name <newname>                 Rename the group
    -owner <newowner>               Change OwnerGroup (use "owner" for OwnerGroup=0)
    -super <true|false>             Change Supergroup flag
```

### Membership Management

```
/adduser <user> <group>             Add user to group
/rmuser <user> <group>              Remove user from group
```

### Information Commands

```
/groups [user]                      List group memberships (self if omitted; any wizard can query any user)
/members <group>                    List members of a group
/listgroups                         List all groups with their properties
/checkperm <action> [args...]       Dry-run to check if an action would be allowed
```

### Permission requirements for commands

- `/mkgroup`: Must be in a Supergroup; new group's OwnerGroup must be one of the Supergroups you're in
- `/rmgroup`: Must be in group's OwnerGroup, which must have Supergroup=true
- `/adduser`, `/rmuser`: Must be in group's OwnerGroup
- `/editgroup -name`: Must be in group's OwnerGroup
- `/editgroup -owner`: Must be in group's current OwnerGroup; new owner must be a Supergroup you're in (or "owner" if you're an Owner user)
- `/editgroup -super`: Must be in group's OwnerGroup, which must have Supergroup=true (only Supergroup members can grant Supergroup status)
- `/groups`, `/members`, `/listgroups`: Any wizard can use (group membership is public among wizards)
- `/checkperm`: Any wizard can use

Owner users can use all commands without restrictions.

## Implementation Notes

### Transactions

All operations that check-then-modify must be atomic. Specifically:
- Delete operations must atomically verify no references exist and delete
- OwnerGroup changes must atomically verify the new owner is valid and exists
- Cycle detection must occur within the same transaction as the modification
- Permission is checked within the transaction; if the actor loses permission mid-operation, the operation fails

Use database transactions to prevent race conditions (TOCTOU vulnerabilities).

## Transaction Semantics Implementation

### Why This Is Simple

All authorization-related data lives in SQLite:

| Data | Backend | Authorization Role |
|------|---------|-------------------|
| Groups | SQLite | Defines permission groups |
| GroupMembers | SQLite | Who has what permissions |
| Files | SQLite | ReadGroup/WriteGroup on each file |
| Users | SQLite | Owner flag for superuser bypass |

The tkrzw backends store only content and game state (source file content, objects,
events). They don't store any permission information. File permissions are checked
via the `File` table in SQLite, not by anything in tkrzw.

This means **all group operations are fully transactional** using SQLite's standard
transaction support via `sqly.Write()`.

### Implementation Pattern

Every group operation follows the same pattern:

```go
func (s *Storage) SomeGroupOperation(ctx context.Context, ...) error {
    return s.sql.Write(ctx, func(tx *sqly.Tx) error {
        // 1. Load entities
        group, err := s.loadGroupByName(ctx, tx, name)
        if err != nil {
            return err
        }

        // 2. Check permissions (reads from same transaction)
        if err := s.CheckCallerAccessToGroupID(ctx, group.OwnerGroup); err != nil {
            return err
        }

        // 3. Validate constraints (reads from same transaction)
        // e.g., check no members, no dependent groups, no file references

        // 4. Perform the operation (writes in same transaction)
        return tx.Upsert(ctx, group, true)
    })
    // Transaction commits or rolls back atomically
}
```

### Example: Delete Group with All Checks

```go
func (s *Storage) DeleteGroup(ctx context.Context, name string) error {
    return s.sql.Write(ctx, func(tx *sqly.Tx) error {
        group, err := s.loadGroupByName(ctx, tx, name)
        if err != nil {
            return err
        }

        // Permission: must be in OwnerGroup (which must be Supergroup)
        if err := s.checkCallerCanDeleteGroup(ctx, tx, group); err != nil {
            return err
        }

        // No members
        var memberCount int
        tx.GetContext(ctx, &memberCount,
            "SELECT COUNT(*) FROM GroupMember WHERE `Group` = ?", group.Id)
        if memberCount > 0 {
            return errors.Errorf("group has %d members", memberCount)
        }

        // No dependent groups
        var depCount int
        tx.GetContext(ctx, &depCount,
            "SELECT COUNT(*) FROM `Group` WHERE OwnerGroup = ?", group.Id)
        if depCount > 0 {
            return errors.Errorf("%d groups use this as OwnerGroup", depCount)
        }

        // No file references
        var fileCount int
        tx.GetContext(ctx, &fileCount,
            "SELECT COUNT(*) FROM File WHERE ReadGroup = ? OR WriteGroup = ?",
            group.Id, group.Id)
        if fileCount > 0 {
            return errors.Errorf("%d files reference this group", fileCount)
        }

        // All checks passed atomically - safe to delete
        _, err = tx.ExecContext(ctx, "DELETE FROM `Group` WHERE Id = ?", group.Id)
        return err
    })
}
```

### Cycle Prevention

Cycles in OwnerGroup references must be prevented, not just warned about. The check
is fully transactional since it only reads the `Group` table:

```go
func (s *Storage) detectCycle(ctx context.Context, tx *sqly.Tx, groupID, newOwner int64) bool {
    visited := map[int64]bool{groupID: true}
    current := newOwner

    for current != 0 {
        if visited[current] {
            return true // Cycle detected - reject the operation
        }
        visited[current] = true

        group, err := s.loadGroupByID(ctx, tx, current)
        if err != nil {
            return false // Broken chain, no cycle
        }
        current = group.OwnerGroup
    }
    return false
}
```

If a cycle is detected, the operation fails with an error explaining which groups
would form the cycle.

### The Only Cross-Backend Consideration

The `sourceObjects` tree (tkrzw) maps source file paths to object IDs. When checking
if a source file can be deleted, we query this tree. However:

1. This is a **read-only check** from tkrzw during a delete operation
2. The authoritative permission for deletion is in the SQLite `File` table
3. If the check races with object creation, the worst case is a stale reference
   that gets lazily cleaned up on next access

This isn't a transaction concern for the group system—it's a separate file deletion
concern already handled by the existing `delFileIfExists` logic.

### OwnerGroup Validation

When setting OwnerGroup (on create or edit):
- If value is 0: allowed only for Owner users
- If value is non-zero: must reference an existing group's ID
- Cannot reference the group being created/edited (no self-ownership)

### Group Name Validation

The `loadGroupByName` function currently returns a zero-value `Group{Id: 0}` for empty
string input. This must be fixed to return an error instead, as `Id=0` has special
meaning (Owner-only). Empty or invalid group names should always produce an error.

### Centralized Group Validation

The various validation checks should be implemented as a `Storage.validateGroup` method
that runs inside the transaction. This keeps validation logic in one place. The method
takes an `op` parameter to distinguish operations with different permission requirements:

```go
type GroupOp int

const (
    GroupOpCreate GroupOp = iota  // Requires Supergroup
    GroupOpDelete                 // Requires Supergroup
    GroupOpEditName               // Requires OwnerGroup membership
    GroupOpEditOwner              // Requires Supergroup (for new owner)
    GroupOpEditSupergroup         // Requires Supergroup
    GroupOpMembership             // Requires OwnerGroup membership
)

func (s *Storage) validateGroup(ctx context.Context, tx *sqly.Tx, g *Group, op GroupOp) error {
    // Name constraints
    if !validGroupName(g.Name) {
        return errors.Errorf("invalid group name %q", g.Name)
    }

    caller, ok := AuthenticatedUser(ctx)
    if !ok {
        return errors.New("no authenticated user in context")
    }

    // OwnerGroup must exist (if non-zero)
    if g.OwnerGroup != 0 {
        owner, err := s.loadGroupByID(ctx, tx, g.OwnerGroup)
        if err != nil {
            return errors.Errorf("OwnerGroup %d does not exist", g.OwnerGroup)
        }

        // Caller must be member of OwnerGroup (unless Owner user)
        if !caller.Owner {
            if has, _ := s.userAccessToGroupIDTx(ctx, tx, caller, g.OwnerGroup); !has {
                return errors.Errorf("not a member of OwnerGroup %q", owner.Name)
            }

            // Some operations require OwnerGroup to be a Supergroup
            requiresSupergroup := op == GroupOpCreate || op == GroupOpDelete ||
                                  op == GroupOpEditOwner || op == GroupOpEditSupergroup
            if requiresSupergroup && !owner.Supergroup {
                return errors.Errorf("OwnerGroup %q is not a Supergroup", owner.Name)
            }
        }
    } else {
        // OwnerGroup=0 requires Owner user
        if !caller.Owner {
            return errors.New("only Owner users can set OwnerGroup to 0")
        }
    }

    // No self-ownership (only relevant for edits where g.Id is already set;
    // for creates, g.Id is 0 so this check passes - caller must separately
    // verify the OwnerGroup name doesn't match the new group's name)
    if g.Id != 0 && g.OwnerGroup == g.Id {
        return errors.New("group cannot own itself")
    }

    // No cycles (only relevant for edits where g.Id is already set)
    if g.Id != 0 && s.detectCycle(ctx, tx, g.Id, g.OwnerGroup) {
        return errors.Errorf("would create ownership cycle")
    }

    return nil
}
```

### Setting OwnerGroup to 0

When changing a group's OwnerGroup to 0, a warning should be displayed:
```
Warning: Setting OwnerGroup to 0 makes this group Owner-only.
Only Owner users will be able to manage it.
```

### Self-Removal from Supergroup

When a user removes themselves from a Supergroup (especially if they're the last member), a warning should be displayed:
```
Warning: You are removing yourself from Supergroup "admins".
You will lose administrative privileges over groups owned by "admins".
```

If removing the last member:
```
Warning: You are the last member of Supergroup "admins".
After removal, only Owner users will be able to manage groups owned by "admins".
```

### User Deletion

When a user is deleted, they must first be removed from all groups. A user with group memberships cannot be deleted until `/rmuser` has been called for each membership.

### Database Indexes

For efficient orphan prevention checks, ensure indexes exist on:
- `Group.OwnerGroup`
- `File.ReadGroup`
- `File.WriteGroup`

## Example Configurations

### Strict hierarchy (Owner controls everything)

```
wizards (OwnerGroup=0, Supergroup=false)
builders (OwnerGroup=0, Supergroup=false)
```

Only Owner users can add/remove wizards and builders. No one can create new groups except Owners.

### Delegated administration

```
admins (OwnerGroup=0, Supergroup=true)
wizards (OwnerGroup=admins, Supergroup=false)
builders (OwnerGroup=wizards, Supergroup=false)
```

- Only Owners manage admins
- Admins manage wizards, can create new groups, and can modify Supergroup flags on groups they own
- Wizards manage builders but cannot create groups or change Supergroup flags

### Guild system with delegation

```
admins (OwnerGroup=0, Supergroup=true)
guild-masters (OwnerGroup=admins, Supergroup=true)
guild-foo (OwnerGroup=guild-masters, Supergroup=false)
guild-bar (OwnerGroup=guild-masters, Supergroup=false)
```

- Owners manage admins
- Admins manage guild-masters and can create/delete groups
- Guild-masters can create new guilds: `/mkgroup guild-baz guild-masters`
- Guild-masters manage all guild membership
- Guild-masters can delete empty guilds (guild-masters is a Supergroup that owns them)

### Separate wizard-admins

```
wizard-admins (OwnerGroup=0, Supergroup=false)
wizards (OwnerGroup=wizard-admins, Supergroup=false)
```

- Only Owners manage wizard-admins
- wizard-admins manage wizard membership
- Clear separation between "who is a wizard" and "who decides who is a wizard"

## Edge Cases

### Circular OwnerGroup references

Cycles are prevented at creation time. Any `/editgroup -owner` command that would create a cycle is rejected:

```
> /editgroup groupA -owner groupB
Error: This would create a cycle (groupA -> groupB -> groupA). Operation rejected.
```

This avoids the deadlock scenario where neither group can be managed.

### Empty managing groups

```
admins (OwnerGroup=0, Supergroup=true) — EMPTY
wizards (OwnerGroup=admins)
```

No one is in admins, so no one can manage wizards. The system gracefully degrades to Owner-only management.

### Orphan prevention

You cannot delete a group if:
- It has members (use `/rmuser` to remove all members first)
- Other groups reference it as OwnerGroup
- Files reference it as ReadGroup or WriteGroup

This prevents orphaned references in the system. All checks are performed atomically within a transaction.

### Transferring ownership

If Alice is in both `admins` and `builders` (both with Supergroup=true), she can change a group's OwnerGroup from `admins` to `builders`, effectively transferring it between hierarchies:

```
/editgroup mygroup -owner builders
```

### Indirect deletion via ownership transfer

To delete a group deep in the hierarchy, a Supergroup member can first transfer ownership to their Supergroup, then delete:

```
admins (OwnerGroup=0, Supergroup=true)
wizards (OwnerGroup=admins, Supergroup=false)
builders (OwnerGroup=wizards, Supergroup=false)  <- Alice (in admins) wants to delete this
```

Alice cannot directly delete `builders` (she's not in `wizards`). But she can:
1. `/editgroup builders -owner admins` (transfer to her Supergroup)
2. `/rmgroup builders` (now she's in the OwnerGroup)

## Default Setup

On server initialization:

```go
Group{
    Name:       "wizards",
    OwnerGroup: 0,     // Only Owners manage wizard membership
    Supergroup: false, // Wizards cannot create/delete groups
}
```

Owners can later modify this configuration as needed.

## Implementation Plan

### Phase 1: Schema Changes

1. Add `Supergroup bool` field to `Group` struct in `storage/storage.go`
2. Fix `loadGroupByName("")` to return error instead of zero-value Group
3. Add `index` tag to `Group.OwnerGroup` for efficient orphan checks

### Phase 2: Core Storage Functions (Stubs)

Create stub implementations that return `errors.New("not implemented")`:

```go
// Group CRUD
func (s *Storage) CreateGroup(ctx context.Context, name string, ownerGroupName string, supergroup bool) error
func (s *Storage) DeleteGroup(ctx context.Context, name string) error
func (s *Storage) EditGroupName(ctx context.Context, name string, newName string) error
func (s *Storage) EditGroupOwner(ctx context.Context, name string, newOwnerName string) error
func (s *Storage) EditGroupSupergroup(ctx context.Context, name string, supergroup bool) error

// Membership
func (s *Storage) RemoveUserFromGroup(ctx context.Context, userName string, groupName string) error

// Validation (used internally)
func (s *Storage) validateGroup(ctx context.Context, tx *sqly.Tx, g *Group) error
func (s *Storage) detectCycle(ctx context.Context, tx *sqly.Tx, groupID, newOwner int64) bool
func (s *Storage) userAccessToGroupIDTx(ctx context.Context, tx *sqly.Tx, user *User, groupID int64) (bool, error)

// Query helpers
func (s *Storage) LoadGroup(ctx context.Context, name string) (*Group, error)
func (s *Storage) ListGroups(ctx context.Context) ([]Group, error)
func (s *Storage) GroupMembers(ctx context.Context, groupName string) ([]User, error)
func (s *Storage) loadGroupByID(ctx context.Context, db sqlx.QueryerContext, id int64) (*Group, error)
```

### Phase 3: Semantics Tests

Write comprehensive tests in `storage/storage_test.go` that verify all rules from this document. Tests should initially fail against the stubs.

**Test categories:**

1. **Group creation**
   - Owner user can create group with OwnerGroup=0
   - Owner user can create group with any valid OwnerGroup
   - Non-owner in Supergroup can create group owned by that Supergroup
   - Non-owner in non-Supergroup cannot create groups
   - Non-owner cannot create group with OwnerGroup=0
   - Non-owner cannot create group owned by Supergroup they're not in
   - Invalid group names are rejected
   - Reserved group names (e.g., "owner") are rejected
   - Duplicate group names are rejected

2. **Group deletion**
   - Owner user can delete any empty, unreferenced group
   - Non-owner in Supergroup can delete groups owned by that Supergroup
   - Cannot delete group with members
   - Cannot delete group referenced as OwnerGroup by other groups
   - Cannot delete group referenced by files (ReadGroup or WriteGroup)
   - Non-owner in non-Supergroup cannot delete groups

3. **Membership changes**
   - Owner user can add/remove anyone to/from any group
   - Non-owner in OwnerGroup can add/remove members
   - Non-owner not in OwnerGroup cannot modify membership
   - Self-removal warnings (test warning is returned, not error)

4. **Group editing**
   - Name changes require OwnerGroup membership
   - OwnerGroup changes require current OwnerGroup membership AND new owner must be Supergroup caller is in
   - Supergroup flag changes require Supergroup membership
   - Self-ownership is rejected
   - Cycles are rejected
   - OwnerGroup=0 requires Owner user

5. **Transactional integrity**
   - Concurrent modifications don't cause inconsistent state
   - Permission checks use transaction snapshot (no TOCTOU)

### Phase 4: Implement Storage Functions

Implement each function to make its tests pass, one at a time:

1. `validateGroup` - centralized validation logic
2. `detectCycle` - cycle detection
3. `userAccessToGroupIDTx` - transaction-aware permission check
4. `CreateGroup`
5. `DeleteGroup`
6. `EditGroupName`
7. `EditGroupOwner`
8. `EditGroupSupergroup`
9. `RemoveUserFromGroup`
10. Query helpers

### Phase 5: Commands

Once all storage functions pass their tests, implement the `/` commands in the game layer that call the storage functions:

1. `/mkgroup` → `CreateGroup`
2. `/rmgroup` → `DeleteGroup`
3. `/editgroup` → `EditGroupName`, `EditGroupOwner`, `EditGroupSupergroup`
4. `/adduser` → `AddUserToGroup` (already exists)
5. `/rmuser` → `RemoveUserFromGroup`
6. `/groups` → `UserGroups` (already exists)
7. `/members` → `GroupMembers`
8. `/listgroups` → `ListGroups`
9. `/checkperm` → dry-run versions of above

### Phase 6: Integration Tests

Add happy-path integration tests via SSH to verify commands work end-to-end. The thorough semantics testing is already covered in Phase 3.

## Checkperm Examples

The `/checkperm` command allows dry-run permission checks:

```
> /checkperm mkgroup newgroup admins
OK: You can create group "newgroup" owned by "admins"

> /checkperm rmgroup oldgroup
DENIED: Group "oldgroup" has 3 members (must be empty)

> /checkperm editgroup mygroup -owner othergroup
DENIED: "othergroup" is not a Supergroup you're in

> /checkperm editgroup mygroup -super true
DENIED: You must be in a Supergroup to grant Supergroup status

> /checkperm adduser bob wizards
OK: You can add "bob" to "wizards"
```
