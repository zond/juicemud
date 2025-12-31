package game

import (
	"fmt"
	"os"
	"path/filepath"
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
				if err := c.game.moveObject(c.ctx, obj, target.GetId()); err != nil {
					return juicemud.WithStack(err)
				}
				return juicemud.WithStack(c.look())
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
				if err := c.game.moveObject(c.ctx, obj, loc.GetLocation()); err != nil {
					return juicemud.WithStack(err)
				}
				return juicemud.WithStack(c.look())
			},
		},
		{
			names: m("/ls"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) < 2 {
					parts = append(parts, "/")
				}
				t := table.New("Path", "Type", "Objects").WithWriter(c.term)
				for _, rawPart := range parts[1:] {
					// Normalize path for consistent display and querying
					part := filepath.Clean(rawPart)
					fullPath, err := c.game.storage.SafeSourcePath(part)
					if err != nil {
						t.AddRow(part, "error", err.Error())
						continue
					}
					info, err := os.Stat(fullPath)
					if errors.Is(err, os.ErrNotExist) {
						t.AddRow(part, "not found", "")
						continue
					} else if err != nil {
						return juicemud.WithStack(err)
					}

					if info.IsDir() {
						// List directory contents
						entries, err := os.ReadDir(fullPath)
						if err != nil {
							return juicemud.WithStack(err)
						}
						t.AddRow(part+"/", "dir", "")
						for _, entry := range entries {
							// Normalize and validate the constructed path
							entryPath := filepath.Clean(filepath.Join(part, entry.Name()))
							if _, err := c.game.storage.SafeSourcePath(entryPath); err != nil {
								// Skip entries with invalid paths (shouldn't happen normally)
								continue
							}
							entryType := "file"
							displayPath := entryPath
							if entry.IsDir() {
								entryType = "dir"
								displayPath += "/"
							}
							// Count objects using this source
							objCount := 0
							if !entry.IsDir() {
								objCount, _ = c.game.storage.CountSourceObjects(c.ctx, entryPath)
							}
							objStr := ""
							if objCount > 0 {
								objStr = fmt.Sprintf("%d", objCount)
							}
							t.AddRow(displayPath, entryType, objStr)
						}
					} else {
						// Single file
						objCount, _ := c.game.storage.CountSourceObjects(c.ctx, part)
						objStr := ""
						if objCount > 0 {
							objStr = fmt.Sprintf("%d", objCount)
						}
						t.AddRow(part, "file", objStr)
						// Show which objects use this source
						if objCount > 0 {
							fmt.Fprint(c.term, "\nUsed by:\n")
							for id, err := range c.game.storage.EachSourceObject(c.ctx, part) {
								if err != nil {
									return juicemud.WithStack(err)
								}
								fmt.Fprintf(c.term, "  %q\n", id)
							}
						}
					}
				}
				t.Print()
				return nil
			},
		},
		{
			names: m("/stats"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				stats := c.game.jsStats

				// Subcommands: summary (default), errors, perf, scripts, objects, intervals, reset
				subcmd := "summary"
				if len(parts) >= 2 {
					subcmd = parts[1]
				}

				switch subcmd {
				case "summary", "dashboard":
					g := stats.GlobalSnapshot()
					fmt.Fprintf(c.term, "JS Statistics (uptime: %s)\n\n", g.Uptime.Round(time.Second))

					fmt.Fprintln(c.term, "EXECUTIONS")
					fmt.Fprintf(c.term, "  Total: %d    Rate: %.2f/s (1m: %.1f/s, 1h: %.0f/s)\n",
						g.TotalExecs, g.ExecRates.PerSecond, g.ExecRates.PerMinute, g.ExecRates.PerHour)
					fmt.Fprintf(c.term, "  Avg: %.1fms    Slow: %d (%.1f%%)\n",
						g.AvgTimeMs, g.TotalSlow, g.SlowPercent)
					fmt.Fprintf(c.term, "  JS CPU: %.1f%%\n", g.TimeRates.PerSecond*100)

					fmt.Fprintln(c.term, "\nERRORS")
					fmt.Fprintf(c.term, "  Total: %d (%.2f%%)    Rate: %.2f/s (1m: %.2f/s, 1h: %.1f/s)\n",
						g.TotalErrors, g.ErrorPercent, g.ErrorRates.PerSecond, g.ErrorRates.PerMinute, g.ErrorRates.PerHour)
					if len(g.ByCategory) > 0 {
						fmt.Fprint(c.term, "  ")
						first := true
						for cat, count := range g.ByCategory {
							if !first {
								fmt.Fprint(c.term, ", ")
							}
							fmt.Fprintf(c.term, "%s: %d", cat, count)
							first = false
						}
						fmt.Fprintln(c.term)
					}

					// Show top issues
					topLocs := stats.TopLocations(3)
					slowRecs := stats.RecentSlowExecutions(3)
					if len(topLocs) > 0 || len(slowRecs) > 0 {
						fmt.Fprintln(c.term, "\nTOP ISSUES")
						if len(topLocs) > 0 {
							fmt.Fprintln(c.term, "  Errors:")
							for _, loc := range topLocs {
								fmt.Fprintf(c.term, "    %s  %d\n", truncateForDisplay(loc.Location, 30), loc.Count)
							}
						}
						if len(slowRecs) > 0 {
							fmt.Fprintln(c.term, "  Slow:")
							for _, rec := range slowRecs {
								fmt.Fprintf(c.term, "    %s  %.0fms\n", truncateForDisplay(rec.SourcePath, 25), float64(rec.Duration.Milliseconds()))
							}
						}
					}

					// User statistics
					fmt.Fprintln(c.term, "\nUSERS")
					totalUsers, _ := c.game.storage.CountUsers(c.ctx, storage.UserFilterAll)
					wizardCount, _ := c.game.storage.CountUsers(c.ctx, storage.UserFilterWizards)
					ownerCount, _ := c.game.storage.CountUsers(c.ctx, storage.UserFilterOwners)
					onlineCount := c.game.connectionByObjectID.Len()
					fmt.Fprintf(c.term, "  Total: %d    Wizards: %d    Owners: %d\n", totalUsers, wizardCount, ownerCount)
					if recentUser, err := c.game.storage.GetMostRecentLogin(c.ctx); err == nil && recentUser != nil {
						ago := time.Since(recentUser.LastLogin()).Truncate(time.Second)
						fmt.Fprintf(c.term, "  Online: %d    Last login: %v ago (%s)\n", onlineCount, ago, recentUser.Name)
					} else {
						fmt.Fprintf(c.term, "  Online: %d\n", onlineCount)
					}

				case "errors":
					// Sub-subcommands: summary (default), categories, locations, recent
					errSubcmd := "summary"
					if len(parts) >= 3 {
						errSubcmd = parts[2]
					}

					switch errSubcmd {
					case "summary":
						g := stats.GlobalSnapshot()
						fmt.Fprintf(c.term, "Error Summary (total: %d, rate: %.2f%%)\n\n", g.TotalErrors, g.ErrorPercent)
						if len(g.ByCategory) > 0 {
							fmt.Fprintln(c.term, "By category:")
							for cat, count := range g.ByCategory {
								fmt.Fprintf(c.term, "  %s: %d\n", cat, count)
							}
						}
						fmt.Fprintf(c.term, "\nError rates: %.2f/s, %.1f/m, %.0f/h\n",
							g.ErrorRates.PerSecond, g.ErrorRates.PerMinute, g.ErrorRates.PerHour)

						// Show recent errors
						recent := stats.RecentErrors(5)
						if len(recent) > 0 {
							fmt.Fprintln(c.term, "\nRecent errors:")
							for _, rec := range recent {
								locStr := rec.Location.String()
								fmt.Fprintf(c.term, "  [%s] %s %s: %s\n",
									rec.Timestamp.Format("15:04:05"),
									rec.Category,
									locStr,
									rec.Message)
							}
						}

					case "categories":
						g := stats.GlobalSnapshot()
						if len(g.ByCategory) == 0 {
							fmt.Fprintln(c.term, "No errors recorded.")
							return nil
						}
						t := table.New("Category", "Count").WithWriter(c.term)
						for cat, count := range g.ByCategory {
							t.AddRow(string(cat), count)
						}
						t.Print()

					case "locations":
						n := 20
						if len(parts) >= 4 {
							if parsed, err := strconv.Atoi(parts[3]); err == nil && parsed > 0 {
								n = parsed
							}
						}
						locs := stats.TopLocations(n)
						if len(locs) == 0 {
							fmt.Fprintln(c.term, "No error locations recorded.")
							return nil
						}
						t := table.New("Location", "Count", "Last Seen").WithWriter(c.term)
						for _, loc := range locs {
							t.AddRow(loc.Location, loc.Count, loc.LastSeen.Format(time.RFC3339))
						}
						t.Print()

					case "recent":
						n := 10
						if len(parts) >= 4 {
							if parsed, err := strconv.Atoi(parts[3]); err == nil && parsed > 0 {
								n = parsed
							}
						}
						recent := stats.RecentErrors(n)
						if len(recent) == 0 {
							fmt.Fprintln(c.term, "No recent errors.")
							return nil
						}
						for _, rec := range recent {
							locStr := rec.Location.String()
							fmt.Fprintf(c.term, "[%s] %s %s @ %s: %s\n",
								rec.Timestamp.Format("15:04:05"),
								rec.ObjectID,
								rec.Category,
								locStr,
								rec.Message)
						}

					default:
						fmt.Fprintln(c.term, "usage: /stats errors [subcommand]")
						fmt.Fprintln(c.term, "  summary              Show error summary (default)")
						fmt.Fprintln(c.term, "  categories           Show errors by category")
						fmt.Fprintln(c.term, "  locations [n]        Show top n error locations (default 20)")
						fmt.Fprintln(c.term, "  recent [n]           Show n most recent errors (default 10)")
					}

				case "perf":
					// Sub-subcommands: summary (default), scripts, slow
					perfSubcmd := "summary"
					if len(parts) >= 3 {
						perfSubcmd = parts[2]
					}

					switch perfSubcmd {
					case "summary":
						g := stats.GlobalSnapshot()
						fmt.Fprintf(c.term, "Performance Summary\n\n")
						fmt.Fprintf(c.term, "Total executions: %d\n", g.TotalExecs)
						fmt.Fprintf(c.term, "Total JS time: %.1fs\n", g.TotalTimeMs/1000)
						fmt.Fprintf(c.term, "Average time: %.1fms\n", g.AvgTimeMs)
						fmt.Fprintf(c.term, "Slow executions: %d (%.2f%%)\n", g.TotalSlow, g.SlowPercent)
						fmt.Fprintf(c.term, "\nExecution rates: %.2f/s, %.1f/m, %.0f/h\n",
							g.ExecRates.PerSecond, g.ExecRates.PerMinute, g.ExecRates.PerHour)
						fmt.Fprintf(c.term, "Time rates (JS seconds per wall time):\n")
						fmt.Fprintf(c.term, "  Current: %.3fs/s (%.1f%% CPU)\n", g.TimeRates.PerSecond, g.TimeRates.PerSecond*100)
						fmt.Fprintf(c.term, "  Per minute: %.1fs/m\n", g.TimeRates.PerMinute)
						fmt.Fprintf(c.term, "  Per hour: %.0fs/h\n", g.TimeRates.PerHour)

					case "scripts":
						sortBy := SortScriptByTime
						if len(parts) >= 4 {
							switch parts[3] {
							case "time":
								sortBy = SortScriptByTime
							case "execs":
								sortBy = SortScriptByExecs
							case "slow":
								sortBy = SortScriptBySlow
							}
						}
						n := 20
						if len(parts) >= 5 {
							if parsed, err := strconv.Atoi(parts[4]); err == nil && parsed > 0 {
								n = parsed
							}
						}
						scripts := stats.TopScripts(sortBy, n)
						if len(scripts) == 0 {
							fmt.Fprintln(c.term, "No scripts recorded.")
							return nil
						}
						t := table.New("Source Path", "Execs", "Avg(ms)", "Max(ms)", "Slow%").WithWriter(c.term)
						for _, script := range scripts {
							t.AddRow(
								script.SourcePath,
								script.Executions,
								fmt.Sprintf("%.1f", script.AvgTimeMs),
								fmt.Sprintf("%.1f", script.MaxTimeMs),
								fmt.Sprintf("%.1f", script.SlowPercent),
							)
						}
						t.Print()

					case "slow":
						n := 10
						if len(parts) >= 4 {
							if parsed, err := strconv.Atoi(parts[3]); err == nil && parsed > 0 {
								n = parsed
							}
						}
						recent := stats.RecentSlowExecutions(n)
						if len(recent) == 0 {
							fmt.Fprintln(c.term, "No slow executions recorded.")
							return nil
						}
						for _, rec := range recent {
							fmt.Fprintf(c.term, "[%s] #%s %s %.1fms\n",
								rec.Timestamp.Format("15:04:05"),
								rec.ObjectID,
								rec.SourcePath,
								float64(rec.Duration.Milliseconds()))
							if len(rec.ImportChain) > 1 {
								fmt.Fprintf(c.term, "  Imports: ")
								for i, dep := range rec.ImportChain[1:] { // Skip first (the source itself)
									if i > 0 {
										fmt.Fprint(c.term, ", ")
									}
									fmt.Fprint(c.term, dep)
								}
								fmt.Fprintln(c.term)
							}
						}

					default:
						fmt.Fprintln(c.term, "usage: /stats perf [subcommand]")
						fmt.Fprintln(c.term, "  summary              Show performance summary (default)")
						fmt.Fprintln(c.term, "  scripts [sort] [n]   Show top n scripts (sort: time|execs|slow)")
						fmt.Fprintln(c.term, "  slow [n]             Show n most recent slow executions (default 10)")
					}

				case "scripts":
					sortBy := SortScriptByTime
					if len(parts) >= 3 {
						switch parts[2] {
						case "time":
							sortBy = SortScriptByTime
						case "execs":
							sortBy = SortScriptByExecs
						case "slow":
							sortBy = SortScriptBySlow
						case "errors":
							sortBy = SortScriptByErrors
						case "errorrate":
							sortBy = SortScriptByErrorRate
						}
					}
					n := 20
					if len(parts) >= 4 {
						if parsed, err := strconv.Atoi(parts[3]); err == nil && parsed > 0 {
							n = parsed
						}
					}
					scripts := stats.TopScripts(sortBy, n)
					if len(scripts) == 0 {
						fmt.Fprintln(c.term, "No scripts recorded.")
						return nil
					}
					t := table.New("Source Path", "Execs", "Avg(ms)", "Max(ms)", "Slow%", "Errs", "Err%").WithWriter(c.term)
					for _, script := range scripts {
						t.AddRow(
							script.SourcePath,
							script.Executions,
							fmt.Sprintf("%.1f", script.AvgTimeMs),
							fmt.Sprintf("%.1f", script.MaxTimeMs),
							fmt.Sprintf("%.1f", script.SlowPercent),
							script.Errors,
							fmt.Sprintf("%.1f", script.ErrorPercent),
						)
					}
					t.Print()

				case "script":
					if len(parts) < 3 {
						fmt.Fprintln(c.term, "usage: /stats script <path>")
						return nil
					}
					sourcePath := parts[2]
					script := stats.ScriptSnapshot(sourcePath)
					if script == nil {
						fmt.Fprintf(c.term, "No stats for script %q\n", sourcePath)
						return nil
					}
					fmt.Fprintf(c.term, "Script: %s\n", script.SourcePath)
					fmt.Fprintf(c.term, "Executions: %d\n", script.Executions)
					fmt.Fprintf(c.term, "Time: avg=%.1fms, min=%.1fms, max=%.1fms\n",
						script.AvgTimeMs, script.MinTimeMs, script.MaxTimeMs)
					fmt.Fprintf(c.term, "Slow: %d (%.2f%%)\n", script.SlowCount, script.SlowPercent)
					fmt.Fprintf(c.term, "Errors: %d (%.2f%%)\n", script.Errors, script.ErrorPercent)
					if !script.LastExecution.IsZero() {
						fmt.Fprintf(c.term, "Last execution: %s\n", script.LastExecution.Format(time.RFC3339))
					}
					if !script.LastError.IsZero() {
						fmt.Fprintf(c.term, "Last error: %s\n", script.LastError.Format(time.RFC3339))
					}
					fmt.Fprintf(c.term, "\nExecution rates: %.2f/s, %.1f/m, %.0f/h\n",
						script.ExecRates.PerSecond, script.ExecRates.PerMinute, script.ExecRates.PerHour)
					fmt.Fprintf(c.term, "Time rates: %.3fs/s, %.1fs/m, %.0fs/h\n",
						script.TimeRates.PerSecond, script.TimeRates.PerMinute, script.TimeRates.PerHour)
					fmt.Fprintf(c.term, "Error rates: %.2f/s, %.1f/m, %.0f/h\n",
						script.ErrorRates.PerSecond, script.ErrorRates.PerMinute, script.ErrorRates.PerHour)
					if len(script.ByCategory) > 0 {
						fmt.Fprintln(c.term, "\nErrors by category:")
						for cat, count := range script.ByCategory {
							fmt.Fprintf(c.term, "  %s: %d\n", cat, count)
						}
					}
					if len(script.ByLocation) > 0 {
						fmt.Fprintln(c.term, "\nErrors by location:")
						for loc, count := range script.ByLocation {
							fmt.Fprintf(c.term, "  %s: %d\n", loc, count)
						}
					}
					if len(script.ImportChain) > 1 {
						fmt.Fprintln(c.term, "\nImport chain:")
						for _, dep := range script.ImportChain {
							fmt.Fprintf(c.term, "  %s\n", dep)
						}
					}

				case "objects":
					sortBy := SortObjectByTime
					if len(parts) >= 3 {
						switch parts[2] {
						case "time":
							sortBy = SortObjectByTime
						case "execs":
							sortBy = SortObjectByExecs
						case "slow":
							sortBy = SortObjectBySlow
						case "errors":
							sortBy = SortObjectByErrors
						case "errorrate":
							sortBy = SortObjectByErrorRate
						}
					}
					n := 20
					if len(parts) >= 4 {
						if parsed, err := strconv.Atoi(parts[3]); err == nil && parsed > 0 {
							n = parsed
						}
					}
					objs := stats.TopObjects(sortBy, n)
					if len(objs) == 0 {
						fmt.Fprintln(c.term, "No objects recorded.")
						return nil
					}
					t := table.New("Object ID", "Source", "Execs", "Avg(ms)", "Slow%", "Errs", "Err%").WithWriter(c.term)
					for _, obj := range objs {
						t.AddRow(
							obj.ObjectID,
							truncateForDisplay(obj.SourcePath, 20),
							obj.Executions,
							fmt.Sprintf("%.1f", obj.AvgTimeMs),
							fmt.Sprintf("%.1f", obj.SlowPercent),
							obj.Errors,
							fmt.Sprintf("%.1f", obj.ErrorPercent),
						)
					}
					t.Print()

				case "object":
					if len(parts) < 3 {
						fmt.Fprintln(c.term, "usage: /stats object <id>")
						return nil
					}
					objectID := parts[2]
					obj := stats.ObjectExecSnapshot(objectID)
					if obj == nil {
						fmt.Fprintf(c.term, "No stats for object %q\n", objectID)
						return nil
					}
					fmt.Fprintf(c.term, "Object: %s\n", obj.ObjectID)
					fmt.Fprintf(c.term, "Source: %s\n", obj.SourcePath)
					fmt.Fprintf(c.term, "Executions: %d\n", obj.Executions)
					fmt.Fprintf(c.term, "Time: avg=%.1fms, min=%.1fms, max=%.1fms\n",
						obj.AvgTimeMs, obj.MinTimeMs, obj.MaxTimeMs)
					fmt.Fprintf(c.term, "Slow: %d (%.2f%%)\n", obj.SlowCount, obj.SlowPercent)
					fmt.Fprintf(c.term, "Errors: %d (%.2f%%)\n", obj.Errors, obj.ErrorPercent)
					if !obj.LastExecution.IsZero() {
						fmt.Fprintf(c.term, "Last execution: %s\n", obj.LastExecution.Format(time.RFC3339))
					}
					if !obj.LastError.IsZero() {
						fmt.Fprintf(c.term, "Last error: %s\n", obj.LastError.Format(time.RFC3339))
					}
					fmt.Fprintf(c.term, "\nExecution rates: %.2f/s, %.1f/m, %.0f/h\n",
						obj.ExecRates.PerSecond, obj.ExecRates.PerMinute, obj.ExecRates.PerHour)
					fmt.Fprintf(c.term, "Time rates: %.3fs/s, %.1fs/m, %.0fs/h\n",
						obj.TimeRates.PerSecond, obj.TimeRates.PerMinute, obj.TimeRates.PerHour)
					fmt.Fprintf(c.term, "Error rates: %.2f/s, %.1f/m, %.0f/h\n",
						obj.ErrorRates.PerSecond, obj.ErrorRates.PerMinute, obj.ErrorRates.PerHour)
					if len(obj.ByCategory) > 0 {
						fmt.Fprintln(c.term, "\nErrors by category:")
						for cat, count := range obj.ByCategory {
							fmt.Fprintf(c.term, "  %s: %d\n", cat, count)
						}
					}
					if len(obj.ByLocation) > 0 {
						fmt.Fprintln(c.term, "\nErrors by location:")
						for loc, count := range obj.ByLocation {
							fmt.Fprintf(c.term, "  %s: %d\n", loc, count)
						}
					}
					if len(obj.ByScript) > 0 {
						fmt.Fprintln(c.term, "\nErrors by script:")
						for script, count := range obj.ByScript {
							fmt.Fprintf(c.term, "  %s: %d\n", script, count)
						}
					}

				case "intervals":
					sortBy := SortIntervalByTime
					n := 20
					if len(parts) >= 3 {
						switch parts[2] {
						case "time":
							sortBy = SortIntervalByTime
						case "execs":
							sortBy = SortIntervalByExecs
						case "slow":
							sortBy = SortIntervalBySlow
						case "errors":
							sortBy = SortIntervalByErrors
						case "errorrate":
							sortBy = SortIntervalByErrorRate
						default:
							// If it looks like a number, use it as n
							if parsed, err := strconv.Atoi(parts[2]); err == nil && parsed > 0 {
								n = parsed
							}
						}
					}
					// Check for n in parts[3] (only if parts[2] was a sort key)
					if len(parts) >= 4 {
						if parsed, err := strconv.Atoi(parts[3]); err == nil && parsed > 0 {
							n = parsed
						}
					}
					intervals := stats.TopIntervals(sortBy, n)
					if len(intervals) == 0 {
						fmt.Fprintln(c.term, "No interval executions recorded.")
						return nil
					}
					t := table.New("Interval ID", "Object ID", "Event", "Execs", "Avg(ms)", "Slow%", "Errs").WithWriter(c.term)
					for _, iv := range intervals {
						t.AddRow(
							iv.IntervalID,
							iv.ObjectID,
							iv.EventName,
							iv.Executions,
							fmt.Sprintf("%.1f", iv.AvgTimeMs),
							fmt.Sprintf("%.1f", iv.SlowPercent),
							iv.Errors,
						)
					}
					t.Print()

				case "users":
					filter := storage.UserFilterAll
					sortBy := storage.UserSortByName
					n := 20
					argIdx := 2

					// Parse filter (optional, first argument)
					if len(parts) > argIdx {
						switch parts[argIdx] {
						case "all":
							filter = storage.UserFilterAll
							argIdx++
						case "owners":
							filter = storage.UserFilterOwners
							argIdx++
						case "wizards":
							filter = storage.UserFilterWizards
							argIdx++
						case "players":
							filter = storage.UserFilterPlayers
							argIdx++
						}
					}

					// Parse sort (optional)
					if len(parts) > argIdx {
						switch parts[argIdx] {
						case "name":
							sortBy = storage.UserSortByName
							argIdx++
						case "id":
							sortBy = storage.UserSortByID
							argIdx++
						case "login", "recent":
							sortBy = storage.UserSortByLastLogin
							argIdx++
						case "stale", "oldest":
							sortBy = storage.UserSortByLastLoginAsc
							argIdx++
						default:
							// Might be a number for limit
							if parsed, err := strconv.Atoi(parts[argIdx]); err == nil && parsed > 0 {
								n = parsed
								argIdx++
							}
						}
					}

					// Parse limit (optional)
					if len(parts) > argIdx {
						if parsed, err := strconv.Atoi(parts[argIdx]); err == nil && parsed > 0 {
							n = parsed
						}
					}

					users, err := c.game.storage.ListUsers(c.ctx, filter, sortBy, n)
					if err != nil {
						fmt.Fprintf(c.term, "Error: %v\n", err)
						return nil
					}
					if len(users) == 0 {
						fmt.Fprintln(c.term, "No users found.")
						return nil
					}

					total, _ := c.game.storage.CountUsers(c.ctx, filter)
					fmt.Fprintf(c.term, "Users (%d shown, %d total):\n", len(users), total)

					t := table.New("Name", "Role", "Object", "Last Login", "Online").WithWriter(c.term)
					for _, user := range users {
						role := ""
						if user.Owner {
							role = "owner"
						} else if user.Wizard {
							role = "wizard"
						}

						online := ""
						if _, found := c.game.connectionByObjectID.GetHas(user.Object); found {
							online = "*"
						}

						lastLoginStr := "never"
						if !user.LastLogin().IsZero() {
							lastLoginStr = user.LastLogin().Format("2006-01-02 15:04")
						}

						t.AddRow(user.Name, role, user.Object, lastLoginStr, online)
					}
					t.Print()

				case "reset":
					stats.Reset()
					fmt.Fprintln(c.term, "Statistics reset.")

				case "flush":
					health := c.game.storage.FlushHealth()
					if health.Healthy() {
						fmt.Fprintln(c.term, "Flush status: OK")
						if !health.LastFlushAt.IsZero() {
							fmt.Fprintf(c.term, "Last successful flush: %v ago\n", time.Since(health.LastFlushAt).Truncate(time.Second))
						}
					} else {
						fmt.Fprintln(c.term, "Flush status: FAILING")
						if !health.LastFlushAt.IsZero() {
							fmt.Fprintf(c.term, "Last successful flush: %v ago\n", time.Since(health.LastFlushAt).Truncate(time.Second))
						}
						if health.LastError != nil {
							fmt.Fprintf(c.term, "Last error: %v\n", health.LastError)
						}
						if !health.LastErrorAt.IsZero() {
							fmt.Fprintf(c.term, "Last failure: %v ago\n", time.Since(health.LastErrorAt).Truncate(time.Second))
						}
					}

				default:
					fmt.Fprintln(c.term, "usage: /stats [subcommand]")
					fmt.Fprintln(c.term, "  summary                    Dashboard view (default)")
					fmt.Fprintln(c.term, "  errors [sub]               Error stats (sub: summary|categories|locations|recent)")
					fmt.Fprintln(c.term, "  perf [sub]                 Performance stats (sub: summary|scripts|slow)")
					fmt.Fprintln(c.term, "  scripts [sort] [n]         Top n scripts (sort: time|execs|slow|errors|errorrate)")
					fmt.Fprintln(c.term, "  script <path>              Detailed stats for specific script")
					fmt.Fprintln(c.term, "  objects [sort] [n]         Top n objects (sort: time|execs|slow|errors|errorrate)")
					fmt.Fprintln(c.term, "  object <id>                Detailed stats for specific object")
					fmt.Fprintln(c.term, "  intervals [sort] [n]       Top n intervals (sort: time|execs|slow|errors|errorrate)")
					fmt.Fprintln(c.term, "  users [filter] [sort] [n]  List users (filter: all|owners|wizards|players; sort: name|id|login|stale)")
					fmt.Fprintln(c.term, "  flush                      Show database flush health status")
					fmt.Fprintln(c.term, "  reset                      Clear all statistics")
				}
				return nil
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
					var config ServerConfig
					if err := goccy.Unmarshal(newState, &config); err != nil {
						fmt.Fprintf(c.term, "Error: invalid server config: %v\n", err)
						return nil
					}
					// Validate spawn location if set
					if config.Spawn.Container != "" {
						if _, err := c.game.storage.AccessObject(c.ctx, config.Spawn.Container, nil); err != nil {
							fmt.Fprintf(c.term, "Warning: spawn location %q does not exist\n", config.Spawn.Container)
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
