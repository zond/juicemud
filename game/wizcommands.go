package game

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/buildkite/shellwords"
	"github.com/pkg/errors"
	"github.com/rodaine/table"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/lang"
	"github.com/zond/juicemud/storage"
	"github.com/zond/juicemud/structs"

	goccy "github.com/goccy/go-json"
)

func (c *Connection) wizCommands() commands {
	return []command{
		{
			names: m("/groups"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				var targetUser *storage.User
				var userName string
				if len(parts) >= 2 {
					// Query another user's groups
					userName = parts[1]
					targetUser, err = c.game.storage.LoadUser(c.ctx, userName)
					if err != nil {
						fmt.Fprintf(c.term, "Error: user %q not found\n", userName)
						return nil
					}
				} else {
					// Query own groups
					targetUser = c.user
					userName = c.user.Name
				}
				groups, err := c.game.storage.UserGroups(c.ctx, targetUser)
				if err != nil {
					return juicemud.WithStack(err)
				}
				sort.Sort(groups)
				fmt.Fprintf(c.term, "%s is member of %v:\n", userName, lang.Card(len(groups), "groups"))
				for _, group := range groups {
					fmt.Fprintf(c.term, "  %s\n", group.Name)
				}
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
				exists, err := c.game.storage.FileExists(c.ctx, parts[1])
				if err != nil {
					return juicemud.WithStack(err)
				}
				if !exists {
					fmt.Fprintf(c.term, "%q doesn't exist", parts[1])
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
				obj.Unsafe.SourcePath = parts[1]
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
					addConsole(target.GetId(), c.term)
					fmt.Fprintf(c.term, "#%s/%s connected to console\n", target.Name(), target.GetId())
				}
				return nil
			}),
		},
		{
			names: m("/undebug"),
			f: c.identifyingCommand(defaultSelf, func(c *Connection, _ *structs.Object, targets ...*structs.Object) error {
				for _, target := range targets {
					delConsole(target.GetId(), c.term)
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
			names: m("/chwrite"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) == 2 {
					if err := c.game.storage.ChwriteFile(c.ctx, parts[1], ""); err != nil {
						return juicemud.WithStack(err)
					}
				} else if len(parts) == 3 {
					if err := c.game.storage.ChwriteFile(c.ctx, parts[1], parts[2]); err != nil {
						return juicemud.WithStack(err)
					}
				} else {
					fmt.Fprintln(c.term, "usage: /chwrite [path] [writer group]")
				}
				return nil
			},
		},
		{
			names: m("/chread"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) == 2 {
					if err := c.game.storage.ChreadFile(c.ctx, parts[1], ""); err != nil {
						return juicemud.WithStack(err)
					}
				} else if len(parts) == 3 {
					if err := c.game.storage.ChreadFile(c.ctx, parts[1], parts[2]); err != nil {
						return juicemud.WithStack(err)
					}
				} else {
					fmt.Fprintln(c.term, "usage: /chread [path] [reader group]")
				}
				return nil
			},
		},
		{
			names: m("/ls"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) < 1 {
					return nil
				}
				parts = parts[1:]
				t := table.New("Path", "Read", "Write").WithWriter(c.term)
				lastWasFile := false
				for _, part := range parts {
					f, err := c.game.storage.LoadFile(c.ctx, part)
					if errors.Is(err, os.ErrNotExist) {
						t.AddRow(fmt.Sprintf("%s: %v", part, err), "", "")
						continue
					} else if err != nil {
						return juicemud.WithStack(err)
					}
					lastWasFile = !f.Dir
					r, w, err := c.game.storage.FileGroups(c.ctx, f)
					if err != nil {
						return juicemud.WithStack(err)
					}
					t.AddRow(f.Path, r.Name, w.Name)
					if f.Dir {
						children, err := c.game.storage.LoadChildren(c.ctx, f.Id)
						if err != nil {
							return juicemud.WithStack(err)
						}
						for _, child := range children {
							r, w, err := c.game.storage.FileGroups(c.ctx, &child)
							if err != nil {
								return juicemud.WithStack(err)
							}
							t.AddRow(child.Path, r.Name, w.Name)
						}

					}
				}
				t.Print()
				if len(parts) == 1 && lastWasFile {
					objectIDs := []string{}
					for id, err := range c.game.storage.EachSourceObject(c.ctx, parts[0]) {
						if err != nil {
							return juicemud.WithStack(err)
						}
						objectIDs = append(objectIDs, id)
					}
					if len(objectIDs) > 0 {
						fmt.Fprint(c.term, "\nUsed by:\n")
						for _, id := range objectIDs {
							fmt.Fprintf(c.term, "  %q\n", id)
						}
					}
				}
				return nil
			},
		},
		// Group management commands
		{
			names: m("/mkgroup"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) < 3 {
					fmt.Fprintln(c.term, "usage: /mkgroup <name> <owner> [super]")
					fmt.Fprintln(c.term, "  owner: group name or 'owner' for OwnerGroup=0")
					fmt.Fprintln(c.term, "  super: 'true' to make this a Supergroup (default: false)")
					return nil
				}
				name := parts[1]
				ownerName := parts[2]
				supergroup := false
				if len(parts) >= 4 && parts[3] == "true" {
					supergroup = true
				}
				if err := c.game.storage.CreateGroup(c.ctx, name, ownerName, supergroup); err != nil {
					fmt.Fprintf(c.term, "Error: %v\n", err)
					return nil
				}
				fmt.Fprintf(c.term, "Created group %q\n", name)
				return nil
			},
		},
		{
			names: m("/rmgroup"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) != 2 {
					fmt.Fprintln(c.term, "usage: /rmgroup <name>")
					return nil
				}
				name := parts[1]
				if err := c.game.storage.DeleteGroup(c.ctx, name); err != nil {
					fmt.Fprintf(c.term, "Error: %v\n", err)
					return nil
				}
				fmt.Fprintf(c.term, "Deleted group %q\n", name)
				return nil
			},
		},
		{
			names: m("/editgroup"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) < 4 {
					fmt.Fprintln(c.term, "usage: /editgroup <group> <option> <value>")
					fmt.Fprintln(c.term, "  options:")
					fmt.Fprintln(c.term, "    -name <newname>     Rename the group")
					fmt.Fprintln(c.term, "    -owner <newowner>   Change OwnerGroup ('owner' for OwnerGroup=0)")
					fmt.Fprintln(c.term, "    -super <true|false> Change Supergroup flag")
					return nil
				}
				groupName := parts[1]
				option := parts[2]
				value := parts[3]
				switch option {
				case "-name":
					if err := c.game.storage.EditGroupName(c.ctx, groupName, value); err != nil {
						fmt.Fprintf(c.term, "Error: %v\n", err)
						return nil
					}
					fmt.Fprintf(c.term, "Renamed group %q to %q\n", groupName, value)
				case "-owner":
					if err := c.game.storage.EditGroupOwner(c.ctx, groupName, value); err != nil {
						fmt.Fprintf(c.term, "Error: %v\n", err)
						return nil
					}
					fmt.Fprintf(c.term, "Changed OwnerGroup of %q to %q\n", groupName, value)
				case "-super":
					supergroup := value == "true"
					if err := c.game.storage.EditGroupSupergroup(c.ctx, groupName, supergroup); err != nil {
						fmt.Fprintf(c.term, "Error: %v\n", err)
						return nil
					}
					fmt.Fprintf(c.term, "Changed Supergroup of %q to %v\n", groupName, supergroup)
				default:
					fmt.Fprintf(c.term, "Unknown option: %s\n", option)
					fmt.Fprintln(c.term, "Valid options: -name, -owner, -super")
				}
				return nil
			},
		},
		{
			names: m("/adduser"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) != 3 {
					fmt.Fprintln(c.term, "usage: /adduser <user> <group>")
					return nil
				}
				userName := parts[1]
				groupName := parts[2]
				user, err := c.game.storage.LoadUser(c.ctx, userName)
				if err != nil {
					fmt.Fprintf(c.term, "Error: user %q not found\n", userName)
					return nil
				}
				if err := c.game.storage.AddUserToGroup(c.ctx, user, groupName); err != nil {
					fmt.Fprintf(c.term, "Error: %v\n", err)
					return nil
				}
				fmt.Fprintf(c.term, "Added %q to %q\n", userName, groupName)
				return nil
			},
		},
		{
			names: m("/rmuser"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) != 3 {
					fmt.Fprintln(c.term, "usage: /rmuser <user> <group>")
					return nil
				}
				userName := parts[1]
				groupName := parts[2]
				if err := c.game.storage.RemoveUserFromGroup(c.ctx, userName, groupName); err != nil {
					fmt.Fprintf(c.term, "Error: %v\n", err)
					return nil
				}
				fmt.Fprintf(c.term, "Removed %q from %q\n", userName, groupName)
				return nil
			},
		},
		{
			names: m("/members"),
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) != 2 {
					fmt.Fprintln(c.term, "usage: /members <group>")
					return nil
				}
				groupName := parts[1]
				members, err := c.game.storage.GroupMembers(c.ctx, groupName)
				if err != nil {
					fmt.Fprintf(c.term, "Error: %v\n", err)
					return nil
				}
				fmt.Fprintf(c.term, "Members of %q (%s):\n", groupName, lang.Card(len(members), "members"))
				for _, user := range members {
					fmt.Fprintf(c.term, "  %s\n", user.Name)
				}
				return nil
			},
		},
		{
			names: m("/listgroups"),
			f: func(c *Connection, s string) error {
				groups, err := c.game.storage.ListGroups(c.ctx)
				if err != nil {
					fmt.Fprintf(c.term, "Error: %v\n", err)
					return nil
				}
				// Build a map of group IDs to names to avoid N+1 queries
				groupNames := make(map[int64]string, len(groups))
				for _, g := range groups {
					groupNames[g.Id] = g.Name
				}
				t := table.New("Name", "OwnerGroup", "Supergroup")
				t.WithWriter(c.term)
				for _, g := range groups {
					ownerName := "owner"
					if g.OwnerGroup != 0 {
						if name, ok := groupNames[g.OwnerGroup]; ok {
							ownerName = name
						} else {
							ownerName = fmt.Sprintf("#%d", g.OwnerGroup)
						}
					}
					superStr := ""
					if g.Supergroup {
						superStr = "yes"
					}
					t.AddRow(g.Name, ownerName, superStr)
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
	}
}
