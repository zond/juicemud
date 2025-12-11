package game

import (
	"context"
	"crypto/subtle"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/buildkite/shellwords"
	"github.com/gliderlabs/ssh"
	"github.com/pkg/errors"
	"github.com/rodaine/table"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/digest"
	"github.com/zond/juicemud/lang"
	"github.com/zond/juicemud/storage"
	"github.com/zond/juicemud/structs"
	"golang.org/x/term"

	goccy "github.com/goccy/go-json"
)

var (
	ErrOperationAborted = fmt.Errorf("operation aborted")
)

var (
	connectionByObjectID = juicemud.NewSyncMap[string, *Connection]()
	consoleByObjectID    = juicemud.NewSyncMap[string, *Fanout]()
)

const (
	loginAttemptInterval = 10 * time.Second
	loginAttemptCleanup  = 1 * time.Minute
)

// loginRateLimiterT tracks failed login attempts per username for rate limiting.
// Entries older than loginAttemptInterval are periodically cleaned up.
type loginRateLimiterT struct {
	mu       sync.Mutex
	attempts map[string]time.Time
	closeCh  chan struct{}
}

var loginRateLimiter = newLoginRateLimiter()

func newLoginRateLimiter() *loginRateLimiterT {
	l := &loginRateLimiterT{
		attempts: make(map[string]time.Time),
		closeCh:  make(chan struct{}),
	}
	go l.cleanup()
	return l
}

// Close stops the cleanup goroutine.
func (l *loginRateLimiterT) Close() {
	close(l.closeCh)
}

// cleanup periodically removes expired login attempt entries.
func (l *loginRateLimiterT) cleanup() {
	ticker := time.NewTicker(loginAttemptCleanup)
	defer ticker.Stop()
	for {
		select {
		case <-l.closeCh:
			return
		case <-ticker.C:
			l.mu.Lock()
			now := time.Now()
			for username, lastAttempt := range l.attempts {
				if now.Sub(lastAttempt) > loginAttemptInterval {
					delete(l.attempts, username)
				}
			}
			l.mu.Unlock()
		}
	}
}

// waitIfNeeded blocks if a recent failed attempt exists for the username.
func (l *loginRateLimiterT) waitIfNeeded(username string, term *term.Terminal) {
	l.mu.Lock()
	last, ok := l.attempts[username]
	l.mu.Unlock()
	if ok {
		if wait := loginAttemptInterval - time.Since(last); wait > 0 {
			fmt.Fprintf(term, "Please wait %v before trying again.\n", wait.Round(time.Second))
			time.Sleep(wait)
		}
	}
}

// recordFailure records a failed login attempt for rate limiting.
func (l *loginRateLimiterT) recordFailure(username string) {
	l.mu.Lock()
	l.attempts[username] = time.Now()
	l.mu.Unlock()
}

// clearFailure removes the rate limit entry on successful login.
func (l *loginRateLimiterT) clearFailure(username string) {
	l.mu.Lock()
	delete(l.attempts, username)
	l.mu.Unlock()
}

func addConsole(id string, term *term.Terminal) {
	consoleByObjectID.WithLock(id, func() {
		if f := consoleByObjectID.Get(id); f != nil {
			f.Push(term)
		} else {
			consoleByObjectID.Set(id, NewFanout(term))
		}
	})
}

func delConsole(id string, term *term.Terminal) {
	consoleByObjectID.WithLock(id, func() {
		if f := consoleByObjectID.Get(id); f != nil {
			f.Drop(term)
			if f.Len() == 0 {
				consoleByObjectID.Del(id)
			}
		}
	})
}

type errs []error

func (e errs) Error() string {
	return fmt.Sprintf("%+v", []error(e))
}

type Connection struct {
	game *Game
	sess ssh.Session
	term *term.Terminal
	user *storage.User
	wiz  bool
	ctx  context.Context // Derived from sess.Context(), updated with session ID and authenticated user
}

// func (c *Connection) Linebreak(s string) string {
// 	result := &bytes.Buffer{}

// }

func (c *Connection) SelectExec(options map[string]func() error) error {
	commandNames := make(sort.StringSlice, 0, len(options))
	for name := range options {
		commandNames = append(commandNames, name)
	}
	sort.Sort(commandNames)
	prompt := fmt.Sprintf("%s\n", lang.Enumerator{Pattern: "[%s]", Operator: "or"}.Do(commandNames...))
	for {
		fmt.Fprint(c.term, prompt)
		line, err := c.term.ReadLine()
		if err != nil {
			return juicemud.WithStack(err)
		}
		if cmd, found := options[line]; found {
			if err := cmd(); err != nil {
				return juicemud.WithStack(err)
			}
			break
		}
	}
	return nil
}

func (c *Connection) SelectReturn(prompt string, options []string) (string, error) {
	for {
		fmt.Fprintf(c.term, "%s [%s]\n", prompt, strings.Join(options, "/"))
		line, err := c.term.ReadLine()
		if err != nil {
			return "", juicemud.WithStack(err)
		}
		for _, option := range options {
			if strings.EqualFold(line, option) {
				return option, nil
			}
		}
	}
}

func (c *Connection) scan() error {
	viewer, neigh, err := c.game.loadDeepNeighbourhoodOf(c.ctx, c.user.Object)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if neigh, err = neigh.Filter(viewer); err != nil {
		return juicemud.WithStack(err)
	}
	if err := c.describeLocation(neigh.Location); err != nil {
		return juicemud.WithStack(err)
	}
	for _, exit := range neigh.Location.Container.GetExits() {
		if neigh, found := neigh.Neighbours[exit.Destination]; found {
			fmt.Fprintln(c.term)
			fmt.Fprintf(c.term, "Via exit %s, you see:\n", exit.Name())
			if err := c.describeLocation(neigh); err != nil {
				return juicemud.WithStack(err)
			}
		}
	}
	return nil
}

func (c *Connection) renderMovement(m *movement) error {
	_, neigh, err := c.game.loadNeighbourhoodOf(c.ctx, c.user.Object)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if m.Source != nil {
		if exit, found := neigh.FindLocation(*m.Source); !found {
			return errors.Errorf("renderMovement got movement from unknown source: %+v", m)
		} else if exit != nil {
			fmt.Fprintf(c.term, "Via exit %s, you see %v leave.\n", exit.Name(), m.Object.Indef())
		} else {
			fmt.Fprintf(c.term, "%v leaves.\n", lang.Capitalize(m.Object.Indef()))
		}
	}
	if m.Destination != nil {
		if exit, found := neigh.FindLocation(*m.Destination); !found {
			return errors.Errorf("renderMovement got movement to unknown destination: %+v", m)
		} else if exit != nil {
			fmt.Fprintf(c.term, "Via exit %s, you see %v arrive.\n", exit.Name(), m.Object.Indef())
		} else {
			fmt.Fprintf(c.term, "%v arrives.\n", lang.Capitalize(m.Object.Indef()))
		}
	}
	return nil
}

func (c *Connection) describeLocation(loc *structs.Location) error {
	fmt.Fprintln(c.term, loc.Container.Name())
	descs := loc.Container.GetDescriptions()
	if len(descs) > 0 && descs[0].Long != "" {
		fmt.Fprintln(c.term)
		fmt.Fprintln(c.term, descs[0].Long)
	}
	if len(loc.Content) > 0 {
		fmt.Fprintln(c.term)
		fmt.Fprintf(c.term, "%s here\n", lang.Enumerator{Tense: lang.Present}.Do(loc.Content.Short()...))
	}
	exits := loc.Container.GetExits()
	if len(exits) > 0 {
		fmt.Fprintln(c.term)
		fmt.Fprintln(c.term, "Exits:")
		fmt.Fprintln(c.term, structs.Exits(exits).Short())
	}
	return nil
}

func (c *Connection) look() error {
	viewer, loc, err := c.game.loadLocationOf(c.ctx, c.user.Object)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if loc, err = loc.Filter(viewer); err != nil {
		return juicemud.WithStack(err)
	}
	return c.describeLocation(loc)
}

type command struct {
	names map[string]bool
	f     func(*Connection, string) error
}

type attempter interface {
	attempt(conn *Connection, name string, line string) (bool, error)
}

type commands []command

func (c commands) attempt(conn *Connection, name string, line string) (bool, error) {
	for _, cmd := range c {
		if cmd.names[name] {
			if err := cmd.f(conn, line); err != nil {
				return true, juicemud.WithStack(err)
			}
			return true, nil
		}
	}
	return false, nil

}

func m(s ...string) map[string]bool {
	res := map[string]bool{}
	for _, p := range s {
		res[p] = true
	}
	return res
}

type defaultObject int

const (
	defaultNone defaultObject = iota
	defaultSelf
	defaultLoc
)

func (c *Connection) identifyingCommand(def defaultObject, f func(c *Connection, self *structs.Object, targets ...*structs.Object) error) func(*Connection, string) error {
	return func(c *Connection, s string) error {
		parts, err := shellwords.SplitPosix(s)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if len(parts) == 1 {
			obj, err := c.game.accessObject(c.ctx, c.user.Object)
			if err != nil {
				return juicemud.WithStack(err)
			}
			switch def {
			case defaultNone:
				fmt.Fprintf(c.term, "usage: %s [target]\n", parts[0])
				return nil
			case defaultSelf:
				return f(c, obj, obj)
			case defaultLoc:
				loc, err := c.game.accessObject(c.ctx, obj.GetLocation())
				if err != nil {
					return juicemud.WithStack(err)
				}
				if loc, err = loc.Filter(obj); err != nil {
					return juicemud.WithStack(err)
				}
				return f(c, obj, loc)
			default:
				return nil
			}
		}
		obj, loc, err := c.game.loadLocationOf(c.ctx, c.user.Object)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if loc, err = loc.Filter(obj); err != nil {
			return juicemud.WithStack(err)
		}
		targets := []*structs.Object{}
		for _, pattern := range parts[1:] {
			if c.wiz && strings.HasPrefix(pattern, "#") {
				target, err := c.game.accessObject(c.ctx, pattern[1:])
				if err != nil {
					fmt.Fprintln(c.term, err.Error())
					return nil
				}
				targets = append(targets, target)
			} else {
				target, err := loc.Identify(pattern)
				if err != nil {
					fmt.Fprintln(c.term, err.Error())
					return nil
				}
				targets = append(targets, target)
			}
		}
		return f(c, obj, targets...)
	}
}

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
	}
}

func (c *Connection) basicCommands() commands {
	return []command{
		{
			names: m("l", "look"),
			f: c.identifyingCommand(defaultLoc, func(c *Connection, obj *structs.Object, targets ...*structs.Object) error {
				for _, target := range targets {
					if obj.GetLocation() == target.GetId() {
						if err := c.look(); err != nil {
							return juicemud.WithStack(err)
						}
					} else {
						fmt.Fprintln(c.term, target.Name())
						descs := target.GetDescriptions()
						if len(descs) > 0 {
							if descs[0].Long != "" {
								fmt.Fprintln(c.term)
								fmt.Fprintln(c.term, descs[0].Long)
							}
						}
					}
				}
				return nil
			}),
		},
		{
			names: m("scan"),
			f: func(c *Connection, s string) error {
				return c.scan()
			},
		},
	}
}

var (
	whitespacePattern = regexp.MustCompile(`\s+`)
)

type objectAttempter struct {
	id string
}

func (o objectAttempter) attempt(c *Connection, name string, line string) (found bool, err error) {
	obj, err := c.game.accessObject(c.ctx, o.id)
	if err != nil {
		return false, juicemud.WithStack(err)
	}
	found, err = c.game.run(c.ctx, obj, &structs.AnyCall{
		Name: name,
		Tag:  commandEventTag,
		Content: map[string]any{
			"name": name,
			"line": line,
		},
	})
	if err != nil {
		return false, juicemud.WithStack(err)
	} else if found {
		return true, nil
	}

	actionCall := &structs.AnyCall{
		Name: name,
		Tag:  actionEventTag,
		Content: map[string]any{
			"name": name,
			"line": line,
		},
	}

	loc, found, err := c.game.loadRun(c.ctx, obj.GetLocation(), actionCall)
	if found || err != nil {
		return found, err
	}

	if loc, err = loc.Filter(obj); err != nil {
		return false, juicemud.WithStack(err)
	}

	for _, exit := range loc.GetExits() {
		if exit.Name() == name {
			if structs.Challenges(exit.UseChallenges).Check(obj, loc.GetId()) > 0 {
				return true, juicemud.WithStack(c.game.moveObject(c.ctx, obj, exit.Destination))
			}
		}
	}

	cont := loc.GetContent()
	delete(cont, o.id)
	for sibID := range cont {
		_, found, err = c.game.loadRun(c.ctx, sibID, actionCall)
		if found || err != nil {
			return found, err
		}
	}
	return false, nil
}

func (c *Connection) Process() error {
	if c.user == nil {
		return errors.New("can't process without user")
	}
	if has, err := c.game.storage.UserAccessToGroup(c.ctx, c.user, wizardsGroup); err != nil {
		return juicemud.WithStack(err)
	} else if has {
		c.wiz = true
	}

	connectionByObjectID.Set(string(c.user.Object), c)
	defer connectionByObjectID.Del(string(c.user.Object))

	commandSets := []attempter{objectAttempter{c.user.Object}, c.basicCommands()}
	if c.wiz {
		commandSets = append([]attempter{c.wizCommands()}, commandSets...)
	}

	for {
		line, err := c.term.ReadLine()
		if err != nil {
			return juicemud.WithStack(err)
		}
		words := whitespacePattern.Split(line, -1)
		if len(words) == 0 {
			continue
		}
		for _, commands := range commandSets {
			if found, err := commands.attempt(c, words[0], line); err != nil {
				fmt.Fprintln(c.term, err)
			} else if found {
				break
			}
		}
	}
}

func (c *Connection) Connect() error {
	// Generate session ID at connection start so all audit events (including failed logins) can be correlated
	c.ctx = storage.SetSessionID(c.ctx, juicemud.NextUniqueID())
	fmt.Fprint(c.term, "Welcome!\n\n")
	sel := func() error {
		return c.SelectExec(map[string]func() error{
			"login user":  c.loginUser,
			"create user": c.createUser,
		})
	}
	var err error
	for err = sel(); errors.Is(err, ErrOperationAborted); err = sel() {
	}
	if err != nil {
		return juicemud.WithStack(err)
	}
	if err := c.look(); err != nil {
		return juicemud.WithStack(err)
	}
	return c.Process()
}

func (c *Connection) loginUser() error {
	fmt.Fprint(c.term, "** Login user **\n\n")
	for c.user == nil {
		fmt.Fprintln(c.term, "Enter username or [abort]:")
		username, err := c.term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return juicemud.WithStack(ErrOperationAborted)
		}

		// Rate limit login attempts per username (only after failed attempts)
		loginRateLimiter.waitIfNeeded(username, c.term)

		fmt.Fprint(c.term, "Enter password or [abort]:\n")
		password, err := c.term.ReadPassword("> ")
		if err != nil {
			return err
		}
		if password == "abort" {
			return juicemud.WithStack(ErrOperationAborted)
		}

		user, err := c.game.storage.LoadUser(c.ctx, username)
		if errors.Is(err, os.ErrNotExist) {
			// User doesn't exist - add delay to prevent timing-based enumeration
			loginRateLimiter.recordFailure(username)
			c.game.storage.AuditLog(c.ctx, "LOGIN_FAILED", storage.AuditLoginFailed{
				User:   storage.Ref(0, username),
				Remote: c.sess.RemoteAddr().String(),
			})
			fmt.Fprintln(c.term, "Invalid credentials!")
			continue
		} else if err != nil {
			return juicemud.WithStack(err)
		}

		ha1 := digest.ComputeHA1(user.Name, juicemud.DAVAuthRealm, password)
		if subtle.ConstantTimeCompare([]byte(ha1), []byte(user.PasswordHash)) != 1 {
			// Record failed attempt for rate limiting
			loginRateLimiter.recordFailure(user.Name)
			c.game.storage.AuditLog(c.ctx, "LOGIN_FAILED", storage.AuditLoginFailed{
				User:   storage.Ref(user.Id, user.Name),
				Remote: c.sess.RemoteAddr().String(),
			})
			fmt.Fprintln(c.term, "Invalid credentials!")
		} else {
			// Successful login - clear any rate limit for this user
			loginRateLimiter.clearFailure(user.Name)
			c.user = user
		}
	}
	c.ctx = storage.AuthenticateUser(c.ctx, c.user)
	c.game.storage.AuditLog(c.ctx, "USER_LOGIN", storage.AuditUserLogin{
		User:   storage.Ref(c.user.Id, c.user.Name),
		Remote: c.sess.RemoteAddr().String(),
	})
	fmt.Fprintf(c.term, "Welcome back, %v!\n\n", c.user.Name)
	if _, _, err := c.game.loadRun(c.ctx, c.user.Object, &structs.AnyCall{
		Name: connectedEventType,
		Tag:  emitEventTag,
		Content: map[string]any{
			"remote":   c.sess.RemoteAddr(),
			"username": c.user.Name,
			"object":   c.user.Object,
			"cause":    "login",
		},
	}); err != nil {
		return juicemud.WithStack(err)
	}
	return nil
}

func (c *Connection) createUser() error {
	fmt.Fprint(c.term, "** Create user **\n\n")
	var user *storage.User
	for user == nil {
		fmt.Fprint(c.term, "Enter new username or [abort]:\n")
		username, err := c.term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return juicemud.WithStack(ErrOperationAborted)
		}
		if err := juicemud.ValidateName(username, "username"); err != nil {
			fmt.Fprintln(c.term, err.Error())
			continue
		}
		if _, err = c.game.storage.LoadUser(c.ctx, username); errors.Is(err, os.ErrNotExist) {
			user = &storage.User{
				Name: username,
			}
		} else if err == nil {
			fmt.Fprintln(c.term, "Username already exists!")
		} else {
			return juicemud.WithStack(err)
		}
	}
	for c.user == nil {
		fmt.Fprintln(c.term, "Enter new password:")
		password, err := c.term.ReadPassword("> ")
		if err != nil {
			return err
		}
		if password == "abort" {
			fmt.Fprintln(c.term, "Password cannot be 'abort' (reserved keyword).")
			continue
		}
		fmt.Fprintln(c.term, "Repeat new password:")
		verification, err := c.term.ReadPassword("> ")
		if err != nil {
			return err
		}
		if password == verification {
			selection, err := c.SelectReturn(fmt.Sprintf("Create user %q with provided password?", user.Name), []string{"y", "n", "abort"})
			if err != nil {
				return err
			}
			if selection == "abort" {
				return juicemud.WithStack(ErrOperationAborted)
			} else if selection == "y" {
				user.PasswordHash = digest.ComputeHA1(user.Name, juicemud.DAVAuthRealm, password)
				c.user = user
			}
		} else {
			fmt.Fprintln(c.term, "Passwords don't match!")
		}
	}
	obj, err := structs.MakeObject(c.ctx)
	if err != nil {
		return juicemud.WithStack(err)
	}
	obj.Unsafe.SourcePath = userSource
	obj.Unsafe.Location = genesisID
	user.Object = obj.Unsafe.Id
	if err := c.game.storage.StoreUser(c.ctx, user, false, c.sess.RemoteAddr().String()); err != nil {
		return juicemud.WithStack(err)
	}
	if err := c.game.createObject(c.ctx, obj); err != nil {
		return juicemud.WithStack(err)
	}
	if _, err = c.game.run(c.ctx, obj, &structs.AnyCall{
		Name: connectedEventType,
		Tag:  emitEventTag,
		Content: map[string]any{
			"remote":   c.sess.RemoteAddr(),
			"username": c.user.Name,
			"object":   c.user.Object,
			"cause":    "create",
		},
	}); err != nil {
		return juicemud.WithStack(err)
	}
	c.ctx = storage.AuthenticateUser(c.ctx, c.user)
	c.game.storage.AuditLog(c.ctx, "USER_LOGIN", storage.AuditUserLogin{
		User:   storage.Ref(c.user.Id, c.user.Name),
		Remote: c.sess.RemoteAddr().String(),
	})
	fmt.Fprintf(c.term, "Welcome %s!\n\n", c.user.Name)
	return nil
}
