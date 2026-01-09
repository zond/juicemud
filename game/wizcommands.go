package game

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buildkite/shellwords"
	"github.com/pkg/errors"
	"github.com/rodaine/table"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/storage"
	"github.com/zond/juicemud/structs"

	goccy "github.com/goccy/go-json"
)

// truncateForDisplay truncates a string to maxLen, prefixing with "..." if truncated.
func truncateForDisplay(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return "..." + s[len(s)-(maxLen-3):]
}

func (c *Connection) wizCommands() commands {
	return []command{
		{
			names: m("/addwiz"),
			f: func(c *Connection, s string) error {
				if !c.user.Owner {
					fmt.Fprintln(c.term, "Only owners can grant wizard privileges.")
					return nil
				}
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) != 2 {
					fmt.Fprintln(c.term, "usage: /addwiz <username>")
					return nil
				}
				username := parts[1]
				if err := c.game.storage.SetUserWizard(c.ctx, username, true); err != nil {
					fmt.Fprintf(c.term, "Error: %v\n", err)
					return nil
				}
				fmt.Fprintf(c.term, "Granted wizard privileges to %q\n", username)
				return nil
			},
		},
		{
			names: m("/delwiz"),
			f: func(c *Connection, s string) error {
				if !c.user.Owner {
					fmt.Fprintln(c.term, "Only owners can revoke wizard privileges.")
					return nil
				}
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) != 2 {
					fmt.Fprintln(c.term, "usage: /delwiz <username>")
					return nil
				}
				username := parts[1]
				if err := c.game.storage.SetUserWizard(c.ctx, username, false); err != nil {
					fmt.Fprintf(c.term, "Error: %v\n", err)
					return nil
				}
				fmt.Fprintf(c.term, "Revoked wizard privileges from %q\n", username)
				return nil
			},
		},
		{
			names: m("/deluser"),
			f: func(c *Connection, s string) error {
				if !c.user.Owner {
					fmt.Fprintln(c.term, "Only owners can delete users.")
					return nil
				}
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) != 2 {
					fmt.Fprintln(c.term, "usage: /deluser <username>")
					return nil
				}
				username := parts[1]

				// Delete user from database (also validates permissions)
				user, err := c.game.storage.DeleteUser(c.ctx, username)
				if err != nil {
					fmt.Fprintf(c.term, "Error: %v\n", err)
					return nil
				}

				// Delete the game object first, before closing their connection.
				// This avoids a race condition where session cleanup tries to
				// interact with a concurrently deleted object.
				objectDeleted := false
				if user.Object != "" {
					obj, err := c.game.storage.AccessObject(c.ctx, user.Object, nil)
					if err != nil {
						fmt.Fprintf(c.term, "Warning: could not access game object #%s: %v\n", user.Object, err)
					} else if err := c.game.removeObject(c.ctx, obj); err != nil {
						fmt.Fprintf(c.term, "Warning: failed to delete game object: %v\n", err)
					} else {
						objectDeleted = true
					}
				}

				// Close their active connection if they're logged in
				if conn, found := c.game.connectionByObjectID.GetHas(user.Object); found {
					fmt.Fprintln(conn.term, "Your account has been deleted by an administrator.")
					conn.sess.Close()
				}

				// Audit log the deletion
				c.game.storage.AuditLog(c.ctx, "USER_DELETE", storage.AuditUserDelete{
					User:      storage.Ref(user.Id, user.Name),
					DeletedBy: storage.Ref(c.user.Id, c.user.Name),
					ObjectID:  user.Object,
				})

				if objectDeleted {
					fmt.Fprintf(c.term, "Deleted user %q and their game object #%s\n", username, user.Object)
				} else {
					fmt.Fprintf(c.term, "Deleted user %q\n", username)
				}
				return nil
			},
		},
		{
			names: m("/move"),
			f: c.identifyingCommand(defaultNone, 0, func(c *Connection, self *structs.Object, _ string, targets ...*structs.Object) error {
				if len(targets) == 1 {
					obj, err := c.game.accessObject(c.ctx, targets[0].GetId())
					if err != nil {
						return juicemud.WithStack(err)
					}
					if obj.GetLocation() == self.GetLocation() {
						if self.GetLocation() == "" {
							return errors.New("Can't move things outside the known universe.")
						}
						loc, err := c.game.accessObject(c.ctx, self.GetLocation())
						if err != nil {
							return juicemud.WithStack(err)
						}
						return juicemud.WithStack(c.game.moveObject(c.ctx, obj, loc.GetLocation()))
					} else {
						return juicemud.WithStack(c.game.moveObject(c.ctx, obj, self.GetLocation()))
					}
				}
				dest := targets[len(targets)-1]
				for _, target := range targets[:len(targets)-1] {
					if dest.GetId() != target.GetLocation() {
						obj, err := c.game.accessObject(c.ctx, target.GetId())
						if err != nil {
							return juicemud.WithStack(err)
						}
						if err := c.game.moveObject(c.ctx, obj, dest.GetId()); err != nil {
							return juicemud.WithStack(err)
						}
					}
				}
				return nil
			}),
		},
		{
			names: m("/remove"),
			f: c.identifyingCommand(defaultNone, 0, func(c *Connection, self *structs.Object, _ string, targets ...*structs.Object) error {
				for _, target := range targets {
					if target.GetId() == self.GetLocation() {
						return errors.New("Can't remove current location.")
					}
					if target.GetId() == self.GetId() {
						return errors.New("Can't remove yourself.")
					}
					var err error
					if target, err = c.game.accessObject(c.ctx, target.GetId()); err != nil {
						return juicemud.WithStack(err)
					}
					if err := c.game.removeObject(c.ctx, target); err != nil {
						return juicemud.WithStack(err)
					}
				}
				return nil
			}),
		},
		{
			names: m("/create"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) != 2 {
					fmt.Fprintln(c.term, "usage: /create [path]")
					return nil
				}
				// Normalize path to ensure consistent storage (avoids /foo/bar vs foo/bar vs ./foo/bar)
				sourcePath := filepath.Clean(parts[1])
				// Reject empty or root paths that would point to the sources directory itself
				if sourcePath == "." || sourcePath == "/" {
					fmt.Fprintln(c.term, "path must specify a source file, not the sources directory")
					return nil
				}
				exists, err := c.game.storage.SourceExists(c.ctx, sourcePath)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if !exists {
					fmt.Fprintf(c.term, "%q doesn't exist\n", sourcePath)
					return nil
				}
				self, err := c.game.accessObject(c.ctx, c.user.Object)
				if err != nil {
					return juicemud.WithStack(err)
				}
				obj, err := structs.MakeObject(c.ctx)
				if err != nil {
					return juicemud.WithStack(err)
				}
				obj.Unsafe.SourcePath = sourcePath
				obj.Unsafe.Location = self.GetLocation()
				if err := c.game.createObject(c.ctx, obj); err != nil {
					return juicemud.WithStack(err)
				}
				if _, err := c.game.run(c.ctx, obj, &structs.AnyCall{
					Name: createdEventType,
					Tag:  emitEventTag,
					Content: map[string]any{
						"creator": self,
					},
				}, nil); err != nil {
					return juicemud.WithStack(err)
				}
				fmt.Fprintf(c.term, "Created #%s\n", obj.GetId())
				return nil
			},
		},
		{
			names: m("/inspect"),
			f: c.identifyingCommand(defaultSelf, 1, func(c *Connection, _ *structs.Object, rest string, targets ...*structs.Object) error {
				target := targets[0]
				path := strings.TrimSpace(rest)

				// If no path, show the entire object
				if path == "" {
					pretty, err := goccy.MarshalIndent(target, "", "  ")
					if err != nil {
						return juicemud.WithStack(err)
					}
					fmt.Fprintln(c.term, string(pretty))
					return nil
				}

				// Marshal and parse as map for path navigation
				js, err := goccy.Marshal(target)
				if err != nil {
					return juicemud.WithStack(err)
				}
				var data map[string]any
				if err := goccy.Unmarshal(js, &data); err != nil {
					return juicemud.WithStack(err)
				}

				value := navigatePath(data, path)
				if value == nil {
					fmt.Fprintf(c.term, "Path %q not found\n", path)
					return nil
				}
				pretty, err := goccy.MarshalIndent(value, "", "  ")
				if err != nil {
					return juicemud.WithStack(err)
				}
				fmt.Fprintln(c.term, string(pretty))
				return nil
			}),
		},
		{
			names: m("/debug"),
			f: c.identifyingCommand(defaultSelf, 0, func(c *Connection, _ *structs.Object, _ string, targets ...*structs.Object) error {
				for _, target := range targets {
					objectID := target.GetId()
					// Dump buffered messages first
					if buffered := c.game.consoleSwitchboard.GetBuffered(objectID); len(buffered) > 0 {
						fmt.Fprintf(c.term, "---- buffered console output for #%s/%s ----\n", target.Name(), objectID)
						for _, msg := range buffered {
							c.term.Write(msg)
						}
						fmt.Fprintf(c.term, "---- end of buffer ----\n")
					}
					c.game.consoleSwitchboard.Attach(objectID, c.term)
					fmt.Fprintf(c.term, "#%s/%s connected to console\n", target.Name(), objectID)
				}
				return nil
			}),
		},
		{
			names: m("/undebug"),
			f: c.identifyingCommand(defaultSelf, 0, func(c *Connection, _ *structs.Object, _ string, targets ...*structs.Object) error {
				for _, target := range targets {
					c.game.consoleSwitchboard.Detach(target.GetId(), c.term)
					fmt.Fprintf(c.term, "#%s/%s disconnected from console\n", target.Name(), target.GetId())
				}
				return nil
			}),
		},
		{
			names: m("/enter"),
			f: c.identifyingCommand(defaultLoc, 0, func(c *Connection, obj *structs.Object, _ string, targets ...*structs.Object) error {
				if len(targets) != 1 {
					fmt.Fprintln(c.term, "usage: /enter [target]")
					return nil
				}
				target := targets[0]
				if obj.GetId() == target.GetId() {
					fmt.Fprintln(c.term, "Unable to climb into your own navel.")
					return nil
				}
				if obj.GetLocation() == target.GetId() {
					return nil
				}
				return juicemud.WithStack(c.game.moveObject(c.ctx, obj, target.GetId()))
			}),
		},
		{
			names: m("/exit"),
			f: func(c *Connection, s string) error {
				obj, err := c.game.accessObject(c.ctx, c.user.Object)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if obj.GetLocation() == "" {
					fmt.Fprintln(c.term, "Unable to leave the universe.")
					return nil
				}
				loc, err := c.game.accessObject(c.ctx, obj.GetLocation())
				if err != nil {
					return juicemud.WithStack(err)
				}
				if loc.GetLocation() == "" {
					fmt.Fprintln(c.term, "Unable to leave the universe.")
					return nil
				}
				return juicemud.WithStack(c.game.moveObject(c.ctx, obj, loc.GetLocation()))
			},
		},
		{
			names: m("/ls"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}

				// Parse flags
				recursive := false
				maxDepth := 10 // default recursive depth for source trees
				targetPath := "/"

				for i := 1; i < len(parts); i++ {
					switch parts[i] {
					case "-r", "--recursive":
						recursive = true
						// Check if next arg is a number (depth), bounded 1-100
						if i+1 < len(parts) {
							if d, err := strconv.Atoi(parts[i+1]); err == nil && d > 0 && d <= 100 {
								maxDepth = d
								i++
							}
						}
					default:
						targetPath = parts[i]
					}
				}

				// Normalize path
				targetPath = filepath.Clean(targetPath)
				fullPath, err := c.game.storage.SafeSourcePath(targetPath)
				if err != nil {
					fmt.Fprintf(c.term, "Error: %v\n", err)
					return nil
				}

				info, err := os.Stat(fullPath)
				if errors.Is(err, os.ErrNotExist) {
					fmt.Fprintf(c.term, "Path not found: %s\n", targetPath)
					return nil
				} else if err != nil {
					return juicemud.WithStack(err)
				}

				if info.IsDir() {
					// Directory listing
					if recursive {
						c.printSourceTreeRecursive(targetPath, fullPath, "", true, maxDepth, 0)
					} else {
						c.printSourceTreeFlat(targetPath, fullPath)
					}
				} else {
					// Single file - show objects using it
					c.printSourceFile(targetPath)
				}
				return nil
			},
		},
		{
			names: m("/tree"),
			f: c.identifyingCommand(defaultLoc, 1, func(c *Connection, _ *structs.Object, rest string, targets ...*structs.Object) error {
				target := targets[0]

				// Parse flags from rest string: -r [depth]
				recursive := false
				maxDepth := 5 // default recursive depth

				args := strings.Fields(rest)
				for i := range len(args) {
					if args[i] == "-r" || args[i] == "--recursive" {
						recursive = true
						// Check if next arg is a number (depth), bounded 1-100
						if i+1 < len(args) {
							if d, err := strconv.Atoi(args[i+1]); err == nil && d > 0 && d <= 100 {
								maxDepth = d
							}
						}
					}
				}

				// Print tree
				if recursive {
					c.printTreeRecursive(target, "", true, maxDepth, 0)
				} else {
					c.printTreeFlat(target)
				}
				return nil
			}),
		},
		{
			names: m("/stats"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				return executeStatsCommand(c, parts)
			},
		},
		{
			names: m("/intervals"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				intervals := c.game.storage.Intervals()
				now := c.game.storage.Queue().Now()

				// Subcommands: (default: list all), <objectID>, clear <intervalID>
				if len(parts) < 2 {
					// List all intervals
					t := table.New("Interval ID", "Object ID", "Event", "Interval", "Next Fire").WithWriter(c.term)
					count := 0
					for interval, err := range intervals.Each() {
						if err != nil {
							fmt.Fprintf(c.term, "Error iterating: %v\n", err)
							continue
						}
						// Calculate time until next fire
						nextFireAt := interval.NextFireTime
						var nextFireStr string
						if nextFireAt <= int64(now) {
							overdue := time.Duration(int64(now) - nextFireAt)
							nextFireStr = fmt.Sprintf("%v ago (overdue)", overdue.Truncate(time.Second))
						} else {
							untilFire := time.Duration(nextFireAt - int64(now))
							nextFireStr = fmt.Sprintf("in %v", untilFire.Truncate(time.Second))
						}
						t.AddRow(
							interval.IntervalID,
							interval.ObjectID,
							interval.EventName,
							fmt.Sprintf("%dms", interval.IntervalMS),
							nextFireStr,
						)
						count++
					}
					if count == 0 {
						fmt.Fprintln(c.term, "No active intervals.")
						return nil
					}
					fmt.Fprintf(c.term, "Active intervals (%d total):\n", count)
					t.Print()
					return nil
				}

				subcmd := parts[1]

				// Check for "clear" subcommand
				if subcmd == "clear" {
					if len(parts) < 3 {
						fmt.Fprintln(c.term, "usage: /intervals clear <intervalID>")
						return nil
					}
					targetIntervalID := parts[2]

					// Find the interval first (need objectID to delete).
					// Collect info before deleting to avoid deadlock: Each() holds RLock,
					// Del() needs Lock on the same mutex.
					var targetObjectID, targetEventName string
					for interval, err := range intervals.Each() {
						if err != nil {
							continue
						}
						if interval.IntervalID == targetIntervalID {
							targetObjectID = interval.ObjectID
							targetEventName = interval.EventName
							break
						}
					}
					if targetObjectID == "" {
						fmt.Fprintf(c.term, "Interval %q not found.\n", targetIntervalID)
						return nil
					}
					if err := intervals.Del(targetObjectID, targetIntervalID); err != nil {
						fmt.Fprintf(c.term, "Error deleting interval: %v\n", err)
						return nil
					}
					fmt.Fprintf(c.term, "Cleared interval %s (object: %s, event: %s)\n",
						targetIntervalID, targetObjectID, targetEventName)
					return nil
				}

				// Otherwise treat as objectID - list intervals for that object
				objectID := strings.TrimPrefix(subcmd, "#")
				count, err := intervals.CountForObject(objectID)
				if err != nil {
					fmt.Fprintf(c.term, "Error counting intervals: %v\n", err)
					return nil
				}
				if count == 0 {
					fmt.Fprintf(c.term, "No intervals for object %q\n", objectID)
					return nil
				}

				t := table.New("Interval ID", "Event", "Interval", "Next Fire").WithWriter(c.term)
				for interval, err := range intervals.EachForObject(objectID) {
					if err != nil {
						fmt.Fprintf(c.term, "Error iterating: %v\n", err)
						continue
					}
					// Calculate time until next fire
					nextFireAt := interval.NextFireTime
					var nextFireStr string
					if nextFireAt <= int64(now) {
						overdue := time.Duration(int64(now) - nextFireAt)
						nextFireStr = fmt.Sprintf("%v ago (overdue)", overdue.Truncate(time.Second))
					} else {
						untilFire := time.Duration(nextFireAt - int64(now))
						nextFireStr = fmt.Sprintf("in %v", untilFire.Truncate(time.Second))
					}
					t.AddRow(
						interval.IntervalID,
						interval.EventName,
						fmt.Sprintf("%dms", interval.IntervalMS),
						nextFireStr,
					)
				}
				fmt.Fprintf(c.term, "Intervals for object %s (%d total):\n", objectID, count)
				t.Print()
				return nil
			},
		},
		{
			names: m("/setstate"),
			f: c.identifyingCommand(defaultNone, 1, func(c *Connection, _ *structs.Object, rest string, targets ...*structs.Object) error {
				if len(targets) != 1 {
					fmt.Fprintln(c.term, "usage: /setstate #objectID PATH VALUE")
					fmt.Fprintln(c.term, "  PATH: dot-separated path (e.g., Spawn.Container), use . for root")
					fmt.Fprintln(c.term, "  VALUE: JSON value, or unquoted string")
					return nil
				}

				// Parse PATH and VALUE from rest
				parts, valueStr := parseShellTokens(rest, 1)
				if len(parts) == 0 || valueStr == "" {
					fmt.Fprintln(c.term, "usage: /setstate #objectID PATH VALUE")
					return nil
				}
				path := parts[0]

				// Parse the value - try JSON first, fall back to string
				var value any
				if err := goccy.Unmarshal([]byte(valueStr), &value); err != nil {
					// Not valid JSON, treat as string
					value = valueStr
				}

				target := targets[0]
				// Serialize with JS execution to prevent race conditions on state updates
				target.JSLock()
				defer target.JSUnlock()
				state := target.GetState()
				if state == "" {
					state = "{}"
				}

				var data map[string]any
				if err := goccy.Unmarshal([]byte(state), &data); err != nil {
					fmt.Fprintf(c.term, "Error parsing current state: %v\n", err)
					return nil
				}

				// Set the value at path
				if path == "" || path == "." {
					// Replace entire state - value must be an object
					newData, ok := value.(map[string]any)
					if !ok {
						fmt.Fprintln(c.term, "Error: root value must be a JSON object")
						return nil
					}
					data = newData
				} else {
					if err := setPath(data, path, value); err != nil {
						fmt.Fprintf(c.term, "Error: %v\n", err)
						return nil
					}
				}

				// Marshal back to JSON
				newState, err := goccy.Marshal(data)
				if err != nil {
					fmt.Fprintf(c.term, "Error encoding state: %v\n", err)
					return nil
				}

				// Special validation for root object (server config)
				isServerConfig := target.GetId() == ""
				if isServerConfig {
					var config structs.ServerConfig
					if err := goccy.Unmarshal(newState, &config); err != nil {
						fmt.Fprintf(c.term, "Error: invalid server config: %v\n", err)
						return nil
					}
					// Validate spawn location if set
					if spawn := config.GetSpawn(); spawn != "" {
						if _, err := c.game.storage.AccessObject(c.ctx, spawn, nil); err != nil {
							fmt.Fprintf(c.term, "Warning: spawn location %q does not exist\n", spawn)
						}
					}
				}

				// Update the object
				target.SetState(string(newState))

				// Audit log server config changes after successful update
				if isServerConfig {
					oldValue := navigatePath(func() map[string]any {
						var old map[string]any
						goccy.Unmarshal([]byte(state), &old)
						return old
					}(), path)
					oldJSON, err := goccy.Marshal(oldValue)
					if err != nil {
						panic(fmt.Sprintf("audit log marshal oldValue failed: %v", err))
					}
					newJSON, err := goccy.Marshal(value)
					if err != nil {
						panic(fmt.Sprintf("audit log marshal newValue failed: %v", err))
					}
					c.game.storage.AuditLog(c.ctx, "SERVER_CONFIG_CHANGE", storage.AuditServerConfigChange{
						ChangedBy: storage.Ref(c.user.Id, c.user.Name),
						Path:      path,
						OldValue:  string(oldJSON),
						NewValue:  string(newJSON),
					})
				}

				fmt.Fprintln(c.term, "OK")
				return nil
			}),
		},
		{
			names: m("/skills"),
			f: c.identifyingCommand(defaultSelf, 1, func(c *Connection, _ *structs.Object, rest string, targets ...*structs.Object) error {
				target := targets[0]
				args := strings.Fields(rest)

				// No args: show all skills
				if len(args) == 0 {
					skills := target.GetSkills()
					if len(skills) == 0 {
						fmt.Fprintf(c.term, "#%s has no skills\n", target.GetId())
						return nil
					}
					t := table.New("Skill", "Theoretical", "Practical", "Base", "LastUsed").WithWriter(c.term)
					for name, skill := range skills {
						lastUsed := "never"
						if skill.LastUsedAt > 0 {
							lastUsed = time.Unix(0, int64(skill.LastUsedAt)).Format("2006-01-02 15:04")
						}
						t.AddRow(name, fmt.Sprintf("%.1f", skill.Theoretical), fmt.Sprintf("%.1f", skill.Practical), fmt.Sprintf("%.1f", skill.LastBase), lastUsed)
					}
					t.Print()
					return nil
				}

				// Set skill: /skills <target> <skillname> <theoretical> <practical>
				if len(args) < 3 {
					fmt.Fprintln(c.term, "usage: /skills [target]")
					fmt.Fprintln(c.term, "       /skills [target] <skillname> <theoretical> <practical>")
					return nil
				}

				skillName := args[0]
				theoretical, err := strconv.ParseFloat(args[1], 32)
				if err != nil {
					fmt.Fprintf(c.term, "invalid theoretical value: %v\n", err)
					return nil
				}
				practical, err := strconv.ParseFloat(args[2], 32)
				if err != nil {
					fmt.Fprintf(c.term, "invalid practical value: %v\n", err)
					return nil
				}

				// Update the skill
				target.Lock()
				if target.Unsafe.Skills == nil {
					target.Unsafe.Skills = map[string]structs.Skill{}
				}
				skill := target.Unsafe.Skills[skillName]
				skill.Name = skillName
				skill.Theoretical = float32(theoretical)
				skill.Practical = float32(practical)
				target.Unsafe.Skills[skillName] = skill
				target.Unlock()

				fmt.Fprintf(c.term, "Set %s on #%s: theoretical=%.1f practical=%.1f\n", skillName, target.GetId(), theoretical, practical)
				return nil
			}),
		},
		{
			names: m("/emit"),
			f: c.identifyingCommand(defaultNone, 1, func(c *Connection, _ *structs.Object, rest string, targets ...*structs.Object) error {
				if len(targets) != 1 {
					fmt.Fprintln(c.term, "usage: /emit <target> <eventName> <tag> <message>")
					fmt.Fprintln(c.term, "  target: object ID (e.g., #abc123)")
					fmt.Fprintln(c.term, "  eventName: event type (e.g., message, tick)")
					fmt.Fprintln(c.term, "  tag: emit, command, or action")
					fmt.Fprintln(c.term, "  message: JSON content")
					return nil
				}

				// Parse: eventName tag message
				parts, messageStr := parseShellTokens(rest, 2)
				if len(parts) < 2 || messageStr == "" {
					fmt.Fprintln(c.term, "usage: /emit <target> <eventName> <tag> <message>")
					return nil
				}

				eventName := parts[0]
				tag := parts[1]

				// Validate tag
				switch tag {
				case emitEventTag, commandEventTag, actionEventTag:
					// OK
				default:
					fmt.Fprintf(c.term, "invalid tag %q: must be emit, command, or action\n", tag)
					return nil
				}

				// Validate message is valid JSON
				var jsonCheck any
				if err := goccy.Unmarshal([]byte(messageStr), &jsonCheck); err != nil {
					fmt.Fprintf(c.term, "invalid JSON message: %v\n", err)
					return nil
				}

				target := targets[0]

				// Queue the event
				if err := c.game.storage.Queue().Push(c.ctx, &structs.Event{
					At:     uint64(c.game.storage.Queue().After(0)),
					Object: target.GetId(),
					Call: structs.Call{
						Name:    eventName,
						Message: messageStr,
						Tag:     tag,
					},
				}); err != nil {
					return juicemud.WithStack(err)
				}

				fmt.Fprintf(c.term, "Emitted %q (%s) to #%s\n", eventName, tag, target.GetId())
				return nil
			}),
		},
	}
}

// navigatePath navigates into a map using a dot-separated path.
// Returns the value at the path, or nil if not found.
// Empty path or "." returns the entire map.
func navigatePath(data map[string]any, path string) any {
	if path == "" || path == "." {
		return data
	}
	parts := strings.Split(path, ".")
	var current any = data
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = m[part]
		if !ok {
			return nil
		}
	}
	return current
}

// maxPathDepth limits how deeply nested paths can be to prevent DoS.
const maxPathDepth = 20

// setPath sets a value at a dot-separated path in a map.
// Creates intermediate maps as needed.
func setPath(data map[string]any, path string, value any) error {
	if path == "" || path == "." {
		return errors.New("path cannot be empty (use root replacement for entire state)")
	}
	parts := strings.Split(path, ".")
	if len(parts) > maxPathDepth {
		return errors.Errorf("path too deep (max %d levels)", maxPathDepth)
	}
	current := data
	for i, part := range parts[:len(parts)-1] {
		next, exists := current[part]
		if !exists {
			// Create intermediate map
			newMap := make(map[string]any)
			current[part] = newMap
			current = newMap
		} else if m, ok := next.(map[string]any); ok {
			current = m
		} else {
			return errors.Errorf("path %q is not an object", strings.Join(parts[:i+1], "."))
		}
	}
	current[parts[len(parts)-1]] = value
	return nil
}

// loadAndSortChildren loads and sorts child objects by name.
func (c *Connection) loadAndSortChildren(obj *structs.Object) []*structs.Object {
	content := obj.GetContent()
	children := make([]*structs.Object, 0, len(content))

	for id := range content {
		child, err := c.game.storage.AccessObject(c.ctx, id, nil)
		if err == nil {
			children = append(children, child)
		}
	}

	// Sort by name for consistent output
	sort.Slice(children, func(i, j int) bool {
		return children[i].Name() < children[j].Name()
	})

	return children
}

// printTreeFlat prints immediate children of an object.
func (c *Connection) printTreeFlat(obj *structs.Object) {
	name := obj.Name()
	content := obj.GetContent()

	if len(content) == 0 {
		fmt.Fprintf(c.term, "#%s  %s (empty)\n", obj.GetId(), name)
		return
	}

	fmt.Fprintf(c.term, "#%s  %s (%d items)\n", obj.GetId(), name, len(content))

	children := c.loadAndSortChildren(obj)
	for i, child := range children {
		prefix := "├── "
		if i == len(children)-1 {
			prefix = "└── "
		}
		fmt.Fprintf(c.term, "%s#%s  %s\n", prefix, child.GetId(), child.Name())
	}
}

// printTreeRecursive prints object hierarchy recursively.
func (c *Connection) printTreeRecursive(obj *structs.Object, indent string, isLast bool, maxDepth, currentDepth int) {
	name := obj.Name()

	// Print current node
	if currentDepth == 0 {
		fmt.Fprintf(c.term, "#%s  %s\n", obj.GetId(), name)
	} else {
		connector := "├── "
		if isLast {
			connector = "└── "
		}
		fmt.Fprintf(c.term, "%s%s#%s  %s\n", indent, connector, obj.GetId(), name)
	}

	// Stop if at max depth
	if currentDepth >= maxDepth {
		return
	}

	// Print children
	children := c.loadAndSortChildren(obj)
	childIndent := indent
	if currentDepth > 0 {
		if isLast {
			childIndent += "    "
		} else {
			childIndent += "│   "
		}
	}

	for i, child := range children {
		c.printTreeRecursive(child, childIndent, i == len(children)-1, maxDepth, currentDepth+1)
	}
}

// printSourceTreeFlat prints immediate children of a source directory.
func (c *Connection) printSourceTreeFlat(path, fullPath string) {
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		fmt.Fprintf(c.term, "Error reading directory: %v\n", err)
		return
	}

	// Filter and sort entries
	var validEntries []os.DirEntry
	for _, entry := range entries {
		entryPath := filepath.Clean(filepath.Join(path, entry.Name()))
		if _, err := c.game.storage.SafeSourcePath(entryPath); err == nil {
			validEntries = append(validEntries, entry)
		}
	}

	if len(validEntries) == 0 {
		fmt.Fprintf(c.term, "%s (empty)\n", path)
		return
	}

	fmt.Fprintf(c.term, "%s (%d items)\n", path, len(validEntries))

	for i, entry := range validEntries {
		prefix := "├── "
		if i == len(validEntries)-1 {
			prefix = "└── "
		}

		name := entry.Name()
		suffix := ""
		if entry.IsDir() {
			suffix = "/"
		} else {
			entryPath := filepath.Clean(filepath.Join(path, entry.Name()))
			if objCount, _ := c.game.storage.CountSourceObjects(c.ctx, entryPath); objCount > 0 {
				suffix = fmt.Sprintf(" (%d)", objCount)
			}
		}
		fmt.Fprintf(c.term, "%s%s%s\n", prefix, name, suffix)
	}
}

// printSourceTreeRecursive prints source tree recursively.
func (c *Connection) printSourceTreeRecursive(path, fullPath, indent string, isLast bool, maxDepth, currentDepth int) {
	info, err := os.Stat(fullPath)
	if err != nil {
		return
	}

	// Print current node
	name := filepath.Base(path)
	if currentDepth == 0 {
		name = path
	}

	suffix := ""
	if info.IsDir() {
		suffix = "/"
	} else {
		if objCount, _ := c.game.storage.CountSourceObjects(c.ctx, path); objCount > 0 {
			suffix = fmt.Sprintf(" (%d)", objCount)
		}
	}

	if currentDepth == 0 {
		fmt.Fprintf(c.term, "%s%s\n", name, suffix)
	} else {
		connector := "├── "
		if isLast {
			connector = "└── "
		}
		fmt.Fprintf(c.term, "%s%s%s%s\n", indent, connector, name, suffix)
	}

	// Stop if not a directory or at max depth
	if !info.IsDir() || currentDepth >= maxDepth {
		return
	}

	// Read and filter children
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return
	}

	var validEntries []os.DirEntry
	for _, entry := range entries {
		entryPath := filepath.Clean(filepath.Join(path, entry.Name()))
		if _, err := c.game.storage.SafeSourcePath(entryPath); err == nil {
			validEntries = append(validEntries, entry)
		}
	}

	// Calculate child indent
	childIndent := indent
	if currentDepth > 0 {
		if isLast {
			childIndent += "    "
		} else {
			childIndent += "│   "
		}
	}

	for i, entry := range validEntries {
		entryPath := filepath.Clean(filepath.Join(path, entry.Name()))
		entryFullPath := filepath.Join(fullPath, entry.Name())
		c.printSourceTreeRecursive(entryPath, entryFullPath, childIndent, i == len(validEntries)-1, maxDepth, currentDepth+1)
	}
}

// printSourceFile shows a single source file and objects using it.
func (c *Connection) printSourceFile(path string) {
	objCount, _ := c.game.storage.CountSourceObjects(c.ctx, path)

	if objCount == 0 {
		fmt.Fprintf(c.term, "%s (no objects)\n", path)
		return
	}

	fmt.Fprintf(c.term, "%s (%d objects)\n", path, objCount)

	// Collect objects using this source
	var objects []*structs.Object
	for id, err := range c.game.storage.EachSourceObject(c.ctx, path) {
		if err != nil {
			continue
		}
		obj, err := c.game.storage.AccessObject(c.ctx, id, nil)
		if err == nil {
			objects = append(objects, obj)
		}
	}

	// Sort by name
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].Name() < objects[j].Name()
	})

	for i, obj := range objects {
		prefix := "├── "
		if i == len(objects)-1 {
			prefix = "└── "
		}
		fmt.Fprintf(c.term, "%s#%s  %s\n", prefix, obj.GetId(), obj.Name())
	}
}
