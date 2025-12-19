package game

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/buildkite/shellwords"
	"github.com/pkg/errors"
	"github.com/rodaine/table"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/structs"

	goccy "github.com/goccy/go-json"
)

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
			names: m("/move"),
			f: c.identifyingCommand(defaultNone, func(c *Connection, self *structs.Object, targets ...*structs.Object) error {
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
			f: c.identifyingCommand(defaultNone, func(c *Connection, self *structs.Object, targets ...*structs.Object) error {
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
				}); err != nil {
					return juicemud.WithStack(err)
				}
				fmt.Fprintf(c.term, "Created #%s\n", obj.GetId())
				return nil
			},
		},
		{
			names: m("/inspect"),
			f: c.identifyingCommand(defaultSelf, func(c *Connection, _ *structs.Object, targets ...*structs.Object) error {
				for _, target := range targets {
					js, err := goccy.MarshalIndent(target, "", "  ")
					if err != nil {
						return juicemud.WithStack(err)
					}
					fmt.Fprintln(c.term, string(js))
				}
				return nil
			}),
		},
		{
			names: m("/debug"),
			f: c.identifyingCommand(defaultSelf, func(c *Connection, _ *structs.Object, targets ...*structs.Object) error {
				for _, target := range targets {
					consoleSwitchboard.Attach(target.GetId(), c.term)
					fmt.Fprintf(c.term, "#%s/%s connected to console\n", target.Name(), target.GetId())
				}
				return nil
			}),
		},
		{
			names: m("/undebug"),
			f: c.identifyingCommand(defaultSelf, func(c *Connection, _ *structs.Object, targets ...*structs.Object) error {
				for _, target := range targets {
					consoleSwitchboard.Detach(target.GetId(), c.term)
					fmt.Fprintf(c.term, "#%s/%s disconnected from console\n", target.Name(), target.GetId())
				}
				return nil
			}),
		},
		{
			names: m("/enter"),
			f: c.identifyingCommand(defaultLoc, func(c *Connection, obj *structs.Object, targets ...*structs.Object) error {
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
			names: m("/queuestats"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				qs := c.game.queueStats

				// Subcommands: summary (default), categories, locations, objects, recent, object <id>, reset
				subcmd := "summary"
				if len(parts) >= 2 {
					subcmd = parts[1]
				}

				switch subcmd {
				case "summary":
					g := qs.GlobalSnapshot()
					fmt.Fprintf(c.term, "Queue Statistics (uptime: %s)\n\n", g.Uptime.Round(time.Second))
					fmt.Fprintf(c.term, "Total events: %d\n", g.TotalEvents)
					fmt.Fprintf(c.term, "Total errors: %d (%.2f%%)\n", g.TotalErrors, g.ErrorRate*100)
					fmt.Fprintf(c.term, "\nEvent rates:\n")
					fmt.Fprintf(c.term, "  Per second: %.2f\n", g.EventRates.PerSecond)
					fmt.Fprintf(c.term, "  Per minute: %.1f\n", g.EventRates.PerMinute)
					fmt.Fprintf(c.term, "  Per hour:   %.0f\n", g.EventRates.PerHour)
					fmt.Fprintf(c.term, "\nError rates:\n")
					fmt.Fprintf(c.term, "  Per second: %.2f\n", g.ErrorRates.PerSecond)
					fmt.Fprintf(c.term, "  Per minute: %.1f\n", g.ErrorRates.PerMinute)
					fmt.Fprintf(c.term, "  Per hour:   %.0f\n", g.ErrorRates.PerHour)

				case "categories":
					cats := qs.TopCategories()
					if len(cats) == 0 {
						fmt.Fprintln(c.term, "No errors recorded.")
						return nil
					}
					t := table.New("Category", "Count", "Last Seen").WithWriter(c.term)
					for _, cat := range cats {
						t.AddRow(cat.Category, cat.Count, cat.LastSeen.Format(time.RFC3339))
					}
					t.Print()

				case "locations":
					n := 20
					if len(parts) >= 3 {
						if parsed, err := strconv.Atoi(parts[2]); err == nil && parsed > 0 {
							n = parsed
						}
					}
					locs := qs.TopLocations(n)
					if len(locs) == 0 {
						fmt.Fprintln(c.term, "No errors recorded.")
						return nil
					}
					t := table.New("Location", "Count", "Last Seen").WithWriter(c.term)
					for _, loc := range locs {
						t.AddRow(loc.Location, loc.Count, loc.LastSeen.Format(time.RFC3339))
					}
					t.Print()

				case "objects":
					n := 20
					sortBy := SortByErrors
					if len(parts) >= 3 {
						switch parts[2] {
						case "errors":
							sortBy = SortByErrors
						case "events":
							sortBy = SortByEvents
						case "rate":
							sortBy = SortByErrorRate
						}
					}
					if len(parts) >= 4 {
						if parsed, err := strconv.Atoi(parts[3]); err == nil && parsed > 0 {
							n = parsed
						}
					}
					objs := qs.TopObjects(sortBy, n, 10) // min 10 events for rate sorting
					if len(objs) == 0 {
						fmt.Fprintln(c.term, "No objects recorded.")
						return nil
					}
					t := table.New("Object", "Events", "Errors", "Rate%").WithWriter(c.term)
					for _, obj := range objs {
						t.AddRow(obj.ObjectID, obj.Events, obj.Errors, fmt.Sprintf("%.1f", obj.ErrorRate*100))
					}
					t.Print()

				case "object":
					if len(parts) < 3 {
						fmt.Fprintln(c.term, "usage: /queuestats object <objectID>")
						return nil
					}
					objectID := parts[2]
					obj := qs.ObjectSnapshot(objectID)
					if obj == nil {
						fmt.Fprintf(c.term, "No stats for object %q\n", objectID)
						return nil
					}
					fmt.Fprintf(c.term, "Object: %s\n", obj.ObjectID)
					fmt.Fprintf(c.term, "Events: %d, Errors: %d (%.2f%%)\n", obj.Events, obj.Errors, obj.ErrorRate*100)
					if !obj.LastEvent.IsZero() {
						fmt.Fprintf(c.term, "Last event: %s\n", obj.LastEvent.Format(time.RFC3339))
					}
					if !obj.LastError.IsZero() {
						fmt.Fprintf(c.term, "Last error: %s\n", obj.LastError.Format(time.RFC3339))
					}
					fmt.Fprintf(c.term, "\nEvent rates: %.2f/s, %.1f/m, %.0f/h\n",
						obj.EventRates.PerSecond, obj.EventRates.PerMinute, obj.EventRates.PerHour)
					fmt.Fprintf(c.term, "Error rates: %.2f/s, %.1f/m, %.0f/h\n",
						obj.ErrorRates.PerSecond, obj.ErrorRates.PerMinute, obj.ErrorRates.PerHour)
					if len(obj.ByCategory) > 0 {
						fmt.Fprintln(c.term, "\nBy category:")
						for cat, count := range obj.ByCategory {
							fmt.Fprintf(c.term, "  %s: %d\n", cat, count)
						}
					}
					if len(obj.ByLocation) > 0 {
						fmt.Fprintln(c.term, "\nBy location:")
						for loc, count := range obj.ByLocation {
							fmt.Fprintf(c.term, "  %s: %d\n", loc, count)
						}
					}

				case "recent":
					n := 10
					if len(parts) >= 3 {
						if parsed, err := strconv.Atoi(parts[2]); err == nil && parsed > 0 {
							n = parsed
						}
					}
					recent := qs.RecentErrors(n)
					if len(recent) == 0 {
						fmt.Fprintln(c.term, "No recent errors.")
						return nil
					}
					for _, rec := range recent {
						fmt.Fprintf(c.term, "[%s] %s %s @ %s: %s\n",
							rec.Timestamp.Format("15:04:05"),
							rec.ObjectID,
							rec.Category,
							rec.Location,
							rec.Message)
					}

				case "reset":
					qs.Reset()
					fmt.Fprintln(c.term, "Queue statistics reset.")

				default:
					fmt.Fprintln(c.term, "usage: /queuestats [subcommand]")
					fmt.Fprintln(c.term, "  summary              Show global statistics (default)")
					fmt.Fprintln(c.term, "  categories           Show errors by category")
					fmt.Fprintln(c.term, "  locations [n]        Show top n error locations (default 20)")
					fmt.Fprintln(c.term, "  objects [sort] [n]   Show top n objects (sort: errors|events|rate)")
					fmt.Fprintln(c.term, "  object <id>          Show stats for specific object")
					fmt.Fprintln(c.term, "  recent [n]           Show n most recent errors (default 10)")
					fmt.Fprintln(c.term, "  reset                Clear all statistics")
				}
				return nil
			},
		},
		{
			names: m("/flushstatus"),
			f: func(c *Connection, s string) error {
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
					} else {
						fmt.Fprintln(c.term, "Last successful flush: never")
					}
					fmt.Fprintf(c.term, "Consecutive errors: %d\n", health.ConsecErrors)
					fmt.Fprintf(c.term, "Current backoff: %v\n", health.CurrentBackoff)
					fmt.Fprintf(c.term, "Last error: %v\n", health.LastError)
				}
				return nil
			},
		},
	}
}
