# Group System

> **Design Document**: This describes the proposed group permission system. The current
> implementation only has basic group membership. The following features need to be added:
> - `Supergroup` field on the Group struct
> - Commands: `/mkgroup`, `/rmgroup`, `/adduser`, `/rmuser`, `/editgroup`, `/listgroups`, `/members`, `/checkperm`
> - `RemoveUserFromGroup` storage function
> - OwnerGroup-based permission checks
> - Orphan prevention (can't delete groups that are referenced)
> - OwnerGroup validation (must reference existing group or be 0)
> - Cycle detection warnings
> - Group name validation

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
- **Indirect admin**: Members can iteratively take ownership of groups lower in the hierarchy by repeatedly using `/editgroup -owner` to pull groups up to their Supergroup, then exercise direct admin rights. This is not implemented as a shortcut—each level must be explicitly transferred.

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

### OwnerGroup Validation

When setting OwnerGroup (on create or edit):
- If value is 0: allowed only for Owner users
- If value is non-zero: must reference an existing group's ID
- Cannot reference the group being created/edited (no self-ownership)

### Cycle Detection

When creating or modifying OwnerGroup relationships, warn if the operation would create a cycle. Cycles are not prevented (they simply create Owner-only groups), but a warning helps administrators avoid unintentional lockouts. Cycle detection is performed within the same transaction as the modification.

Example warning:
```
Warning: This creates a cycle (A -> B -> A). Both groups will only be manageable by Owners.
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

```
Group A: OwnerGroup=B
Group B: OwnerGroup=A
```

If both groups are empty, neither can be managed (deadlock). Only Owners can break the cycle. The system warns when creating cycles but does not prevent them—they simply create Owner-only groups.

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
