package game

import (
	"crypto/subtle"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

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

func addConsole(id string, term *term.Terminal) {
	consoleByObjectID.WithLock(id, func() {
		consoleByObjectID.Set(id, consoleByObjectID.Get(id).Push(term))
	})
}

func delConsole(id string, term *term.Terminal) {
	consoleByObjectID.WithLock(id, func() {
		consoleByObjectID.Set(id, consoleByObjectID.Get(id).Drop(term))
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
	viewer, neigh, err := c.game.loadDeepNeighbourhoodOf(c.sess.Context(), c.user.Object)
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
	_, neigh, err := c.game.loadNeighbourhoodOf(c.sess.Context(), c.user.Object)
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
	viewer, loc, err := c.game.loadLocationOf(c.sess.Context(), c.user.Object)
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
			obj, err := c.game.storage.AccessObject(c.sess.Context(), c.user.Object, c.game.runSource)
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
				loc, err := c.game.storage.AccessObject(c.sess.Context(), obj.GetLocation(), c.game.runSource)
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
		obj, loc, err := c.game.loadLocationOf(c.sess.Context(), c.user.Object)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if loc, err = loc.Filter(obj); err != nil {
			return juicemud.WithStack(err)
		}
		targets := []*structs.Object{}
		for _, pattern := range parts[1:] {
			target, err := loc.Identify(pattern)
			if err != nil {
				fmt.Fprintln(c.term, err.Error())
				return nil
			}
			targets = append(targets, target)
		}
		return f(c, obj, targets...)
	}
}

func (c *Connection) wizCommands() commands {
	return []command{
		{
			names: m("/groups"),
			f: func(c *Connection, s string) error {
				groups, err := c.game.storage.UserGroups(c.sess.Context(), c.user)
				if err != nil {
					return juicemud.WithStack(err)
				}
				sort.Sort(groups)
				fmt.Fprintf(c.term, "Member of %v\n", lang.Card(len(groups), "groups"))
				for _, group := range groups {
					fmt.Fprintln(c.term, group.Name)
				}
				return nil
			},
		},
		{
			names: m("/move"),
			f: c.identifyingCommand(defaultNone, func(c *Connection, self *structs.Object, targets ...*structs.Object) error {
				if len(targets) == 1 {
					if targets[0].GetId() == self.GetLocation() {
						return errors.New("Can't move current location.")
					}
					if self.GetLocation() == "" {
						return errors.New("Can't move things outside the known universe.")
					}
					obj, err := c.game.storage.AccessObject(c.sess.Context(), targets[0].GetId(), c.game.runSource)
					if err != nil {
						return juicemud.WithStack(err)
					}
					loc, err := c.game.storage.AccessObject(c.sess.Context(), self.GetLocation(), c.game.runSource)
					if err != nil {
						return juicemud.WithStack(err)
					}
					return juicemud.WithStack(c.game.moveObject(c.sess.Context(), obj, loc.GetLocation()))
				}
				dest := targets[len(targets)-1]
				if dest.GetId() == self.GetLocation() {
					return errors.New("Skipping moving things to where they already are.")
				}
				for _, target := range targets[:len(targets)-1] {
					if target.GetId() == self.GetLocation() {
						return errors.New("Can't move current location.")
					}
					obj, err := c.game.storage.AccessObject(c.sess.Context(), target.GetId(), c.game.runSource)
					if err != nil {
						return juicemud.WithStack(err)
					}
					if err := c.game.moveObject(c.sess.Context(), obj, dest.GetId()); err != nil {
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
				exists, err := c.game.storage.FileExists(c.sess.Context(), parts[1])
				if err != nil {
					return juicemud.WithStack(err)
				}
				if !exists {
					fmt.Fprintf(c.term, "%q doesn't exist", parts[1])
					return nil
				}
				self, err := c.game.storage.AccessObject(c.sess.Context(), c.user.Object, c.game.runSource)
				if err != nil {
					return juicemud.WithStack(err)
				}
				obj, err := structs.MakeObject(c.sess.Context())
				if err != nil {
					return juicemud.WithStack(err)
				}
				obj.Unsafe.SourcePath = parts[1]
				obj.Unsafe.Location = self.GetLocation()
				if err := c.game.createObject(c.sess.Context(), obj); err != nil {
					return juicemud.WithStack(err)
				}
				if _, err := c.game.run(c.sess.Context(), obj, &structs.AnyCall{
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
				if err := c.game.moveObject(c.sess.Context(), obj, target.GetId()); err != nil {
					return juicemud.WithStack(err)
				}
				return juicemud.WithStack(c.look())
			}),
		},
		{
			names: m("/exit"),
			f: func(c *Connection, s string) error {
				obj, err := c.game.storage.AccessObject(c.sess.Context(), c.user.Object, c.game.runSource)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if obj.GetLocation() == "" {
					fmt.Fprintln(c.term, "Unable to leave the universe.")
					return nil
				}
				loc, err := c.game.storage.AccessObject(c.sess.Context(), obj.GetLocation(), c.game.runSource)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if err := c.game.moveObject(c.sess.Context(), obj, loc.GetLocation()); err != nil {
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
					if err := c.game.storage.ChwriteFile(c.sess.Context(), parts[1], ""); err != nil {
						return juicemud.WithStack(err)
					}
				} else if len(parts) == 3 {
					if err := c.game.storage.ChwriteFile(c.sess.Context(), parts[1], parts[2]); err != nil {
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
					if err := c.game.storage.ChreadFile(c.sess.Context(), parts[1], ""); err != nil {
						return juicemud.WithStack(err)
					}
				} else if len(parts) == 3 {
					if err := c.game.storage.ChreadFile(c.sess.Context(), parts[1], parts[2]); err != nil {
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
				for _, part := range parts {
					f, err := c.game.storage.LoadFile(c.sess.Context(), part)
					if errors.Is(err, os.ErrNotExist) {
						t.AddRow(fmt.Sprintf("%s: %v", part, err), "", "")
						continue
					} else if err != nil {
						return juicemud.WithStack(err)
					}
					r, w, err := c.game.storage.FileGroups(c.sess.Context(), f)
					if err != nil {
						return juicemud.WithStack(err)
					}
					t.AddRow(f.Path, r.Name, w.Name)
					if f.Dir {
						children, err := c.game.storage.LoadChildren(c.sess.Context(), f.Id)
						if err != nil {
							return juicemud.WithStack(err)
						}
						for _, child := range children {
							r, w, err := c.game.storage.FileGroups(c.sess.Context(), &child)
							if err != nil {
								return juicemud.WithStack(err)
							}
							t.AddRow(child.Path, r.Name, w.Name)
						}

					}
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
							} else if descs[1].Short != "" {
								fmt.Fprintln(c.term)
								fmt.Fprintln(c.term, descs[0].Short)
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
	obj, err := c.game.storage.AccessObject(c.sess.Context(), o.id, c.game.runSource)
	if err != nil {
		return false, juicemud.WithStack(err)
	}
	found, err = c.game.run(c.sess.Context(), obj, &structs.AnyCall{
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

	loc, found, err := c.game.loadRun(c.sess.Context(), obj.GetLocation(), actionCall)
	if found || err != nil {
		return found, err
	}

	if loc, err = loc.Filter(obj); err != nil {
		return false, juicemud.WithStack(err)
	}

	for _, exit := range loc.GetExits() {
		if exit.Name() == name {
			if structs.Challenges(exit.UseChallenges).Check(obj, loc.GetId()) {
				return true, juicemud.WithStack(c.game.moveObject(c.sess.Context(), obj, exit.Destination))
			}
		}
	}

	cont := loc.GetContent()
	delete(cont, o.id)
	for sibID := range cont {
		_, found, err = c.game.loadRun(c.sess.Context(), sibID, actionCall)
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
	connectionByObjectID.Set(string(c.user.Object), c)
	defer connectionByObjectID.Del(string(c.user.Object))

	commandSets := []attempter{objectAttempter{c.user.Object}, c.basicCommands()}
	if has, err := c.game.storage.UserAccessToGroup(c.sess.Context(), c.user, wizardsGroup); err != nil {
		return juicemud.WithStack(err)
	} else if has {
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
	var user *storage.User
	for user == nil {
		fmt.Fprintln(c.term, "Enter username or [abort]:")
		username, err := c.term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return juicemud.WithStack(ErrOperationAborted)
		}
		if user, err = c.game.storage.LoadUser(c.sess.Context(), username); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(c.term, "Username not found!")
		} else if err != nil {
			return juicemud.WithStack(err)
		}
	}
	for c.user == nil {
		fmt.Fprint(c.term, "Enter password or [abort]:\n")
		password, err := c.term.ReadPassword("> ")
		if err != nil {
			return err
		}
		ha1 := digest.ComputeHA1(user.Name, juicemud.DAVAuthRealm, password)
		if subtle.ConstantTimeCompare([]byte(ha1), []byte(user.PasswordHash)) != 1 {
			fmt.Fprintln(c.term, "Incorrect password!")
		} else {
			c.user = user
		}
	}
	storage.AuthenticateUser(c.sess.Context(), c.user)
	fmt.Fprintf(c.term, "Welcome back, %v!\n\n", c.user.Name)
	if _, _, err := c.game.loadRun(c.sess.Context(), c.user.Object, &structs.AnyCall{
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
		if _, err = c.game.storage.LoadUser(c.sess.Context(), username); errors.Is(err, os.ErrNotExist) {
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
	obj, err := structs.MakeObject(c.sess.Context())
	if err != nil {
		return juicemud.WithStack(err)
	}
	obj.Unsafe.SourcePath = userSource
	obj.Unsafe.Location = genesisID
	user.Object = obj.Unsafe.Id
	if err := c.game.storage.StoreUser(c.sess.Context(), user, false); err != nil {
		return juicemud.WithStack(err)
	}
	if err := c.game.createObject(c.sess.Context(), obj); err != nil {
		return juicemud.WithStack(err)
	}
	if _, err = c.game.run(c.sess.Context(), obj, &structs.AnyCall{
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
	storage.AuthenticateUser(c.sess.Context(), c.user)
	fmt.Fprintf(c.term, "Welcome %s!\n\n", c.user.Name)
	return nil
}
