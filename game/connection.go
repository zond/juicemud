package game

import (
	"crypto/subtle"
	"fmt"
	"log"
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
	OperationAborted = errors.New("operation aborted")
)

var (
	envByObjectID     = juicemud.NewSyncMap[string, *Connection]()
	consoleByObjectID = juicemud.NewSyncMap[string, *Fanout]()
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
	viewer, neigh, err := c.game.loadNeighbourhoodOf(c.sess.Context(), c.user.Object)
	if err != nil {
		return juicemud.WithStack(err)
	}
	neigh.Filter(viewer)
	if err := c.describeLocation(neigh.Location); err != nil {
		return juicemud.WithStack(err)
	}
	for _, exit := range neigh.Location.Container.Exits {
		if neigh, found := neigh.Neighbours[exit.Destination]; found {
			fmt.Fprintln(c.term)
			fmt.Fprintln(c.term, "Via %s, you see:")
			if err := c.describeLocation(neigh); err != nil {
				return juicemud.WithStack(err)
			}
		}
	}
	return nil
}

func (c *Connection) describeLocation(loc *structs.Location) error {
	fmt.Fprintln(c.term, loc.Container.Name())
	if len(loc.Container.Descriptions) > 0 && loc.Container.Descriptions[0].Long != "" {
		fmt.Fprintln(c.term)
		fmt.Fprintln(c.term, loc.Container.Descriptions[0].Long)
	}
	if len(loc.Content) > 0 {
		fmt.Fprintln(c.term)
		fmt.Fprintf(c.term, "%s here\n", lang.Enumerator{Tense: lang.Present}.Do(loc.Content.Short()...))
	}
	if len(loc.Container.Exits) > 0 {
		fmt.Fprintln(c.term)
		fmt.Fprintln(c.term, structs.Exits(loc.Container.Exits).Short())
	}
	return nil
}

func (c *Connection) look() error {
	viewer, loc, err := c.game.loadLocationOf(c.sess.Context(), c.user.Object)
	if err != nil {
		return juicemud.WithStack(err)
	}
	loc.Filter(viewer)
	return c.describeLocation(loc)
}

type command struct {
	names  map[string]bool
	wizard bool
	f      func(*Connection, string) error
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
	defaultSelf defaultObject = iota
	defaultLoc
)

func (c *Connection) identifyingCommand(def defaultObject, f func(c *Connection, self *structs.Object, target *structs.Object) error) func(*Connection, string) error {
	return func(c *Connection, s string) error {
		parts, err := shellwords.SplitPosix(s)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if len(parts) == 1 {
			obj, err := c.game.storage.LoadObject(c.sess.Context(), c.user.Object, c.game.rerunSource)
			if err != nil {
				return juicemud.WithStack(err)
			}
			switch def {
			case defaultSelf:
				return f(c, obj, obj)
			case defaultLoc:
				loc, err := c.game.storage.LoadObject(c.sess.Context(), obj.Location, c.game.rerunSource)
				if err != nil {
					return juicemud.WithStack(err)
				}
				loc.Filter(obj)
				return f(c, obj, loc)
			}
		} else if len(parts) == 2 {
			obj, loc, err := c.game.loadLocationOf(c.sess.Context(), c.user.Object)
			if err != nil {
				return juicemud.WithStack(err)
			}
			loc.Filter(obj)
			target, err := loc.Identify(parts[1])
			if err != nil {
				fmt.Fprintln(c.term, err.Error())
				return nil
			}
			return f(c, obj, target)
		}
		fmt.Fprintln(c.term, "usage: /state [pattern]?")
		return nil
	}
}

func (c *Connection) commands() []command {
	return []command{
		{
			names:  m("/groups"),
			wizard: true,
			f: func(c *Connection, s string) error {
				groups, err := c.game.storage.UserGroups(c.sess.Context(), c.user)
				if err != nil {
					return juicemud.WithStack(err)
				}
				sort.Sort(groups)
				fmt.Fprintf(c.term, "Member of %v\n", lang.Declare(len(groups), "groups"))
				for _, group := range groups {
					fmt.Fprintln(c.term, group.Name)
				}
				return nil
			},
		},
		{
			names:  m("/create"),
			wizard: true,
			f: func(c *Connection, s string) error {
				parts, err := shellwords.SplitPosix(s)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if len(parts) != 2 {
					fmt.Fprintln(c.term, "usage: /create [path]")
				}
				return nil
			},
		},
		{
			names:  m("/inspect"),
			wizard: true,
			f: c.identifyingCommand(defaultSelf, func(c *Connection, _ *structs.Object, target *structs.Object) error {
				js, err := goccy.MarshalIndent(target, "", "  ")
				if err != nil {
					return juicemud.WithStack(err)
				}
				fmt.Fprintf(c.term, "#%s/%s\n", target.Name(), target.Id)
				fmt.Fprintln(c.term, string(js))
				return nil
			}),
		},
		{
			names:  m("/debug"),
			wizard: true,
			f: c.identifyingCommand(defaultSelf, func(c *Connection, _ *structs.Object, target *structs.Object) error {
				addConsole(target.Id, c.term)
				fmt.Fprintln(c.term, "#%s/%s connected to console", target.Name(), target.Id)
				return nil
			}),
		},
		{
			names:  m("/undebug"),
			wizard: true,
			f: c.identifyingCommand(defaultSelf, func(c *Connection, _ *structs.Object, target *structs.Object) error {
				delConsole(target.Id, c.term)
				fmt.Fprintln(c.term, "#%s/%s disconnected from console", target.Name(), target.Id)
				return nil
			}),
		},
		{
			names: m("l", "look"),
			f: c.identifyingCommand(defaultLoc, func(c *Connection, obj *structs.Object, target *structs.Object) error {
				if obj.Location == target.Id {
					return c.look()
				}
				fmt.Fprintln(c.term, target.Name())
				if len(target.Descriptions) > 0 && target.Descriptions[0].Long != "" {
					fmt.Fprintln(c.term)
					fmt.Fprintln(c.term, target.Descriptions[0].Long)
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
		{
			names:  m("/enter"),
			wizard: true,
			f: c.identifyingCommand(defaultLoc, func(c *Connection, obj *structs.Object, target *structs.Object) error {
				if obj.Id == target.Id {
					fmt.Fprintln(c.term, "Unable to climb into your own navel.")
					return nil
				}
				if obj.Location == target.Id {
					return nil
				}
				oldLoc := obj.Location
				obj.Location = target.Id
				if err := c.game.storage.StoreObject(c.sess.Context(), &oldLoc, obj); err != nil {
					return juicemud.WithStack(err)
				}
				return juicemud.WithStack(c.look())
			}),
		},
		{
			names:  m("/exit"),
			wizard: true,
			f: func(c *Connection, s string) error {
				obj, err := c.game.storage.LoadObject(c.sess.Context(), c.user.Object, c.game.rerunSource)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if obj.Location == "" {
					fmt.Fprintln(c.term, "Unable to leave the universe.")
					return nil
				}
				loc, err := c.game.storage.LoadObject(c.sess.Context(), obj.Location, c.game.rerunSource)
				if err != nil {
					return juicemud.WithStack(err)
				}
				obj.Location = loc.Location
				if err := c.game.storage.StoreObject(c.sess.Context(), &loc.Id, obj); err != nil {
					return juicemud.WithStack(err)
				}
				return juicemud.WithStack(c.look())
			},
		},
		{
			names:  m("/chwrite"),
			wizard: true,
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
			names:  m("/chread"),
			wizard: true,
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
			names:  m("/ls"),
			wizard: true,
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

var (
	whitespacePattern = regexp.MustCompile(`\s+`)
)

/*
Command priority:
- debug command (defined here as Go, examples: "debug", "undebug")
- self commands  (defined in the User Object as JS, examples: "emote", "say", "kill")
- env commands (defined here as Go, examples: "l", "look", "inv")
- location directions (defined in Location Object as JS, examples: "n", "se")
- location commands  (defined in Location Object as JS, examples: "open door", "pull switch")
- sibling commands (defined in sibling Objects as JS, examples: "turn on robot", "give money")
All commands should be in the Object so that we don't need to run JS to find matches.
*/
func (c *Connection) Process() error {
	if c.user == nil {
		return errors.New("can't process without user")
	}
	envByObjectID.Set(string(c.user.Object), c)
	defer envByObjectID.Del(string(c.user.Object))
	for {
		line, err := c.term.ReadLine()
		if err != nil {
			return juicemud.WithStack(err)
		}
		words := whitespacePattern.Split(line, -1)
		if len(words) == 0 {
			continue
		}
		for _, cmd := range c.commands() {
			if cmd.names[words[0]] {
				if cmd.wizard {
					if has, err := c.game.storage.UserAccessToGroup(c.sess.Context(), c.user, wizardsGroup); err != nil {
						return juicemud.WithStack(err)
					} else if has {
						if err := cmd.f(c, line); err != nil {
							log.Printf("%s\n%s", err, juicemud.StackTrace(err))
							fmt.Fprintln(c.term, err)
						}
					}
				} else {
					if err := cmd.f(c, line); err != nil {
						fmt.Fprintln(c.term, err)
					}
				}
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
	for err = sel(); errors.Is(err, OperationAborted); err = sel() {
	}
	if err != nil {
		return juicemud.WithStack(err)
	}
	if err := c.game.loadRunSave(c.sess.Context(), c.user.Object, &structs.AnyCall{
		Name: connectedEventType,
		Tag:  emitEventTag,
		Content: map[string]any{
			"remote":   c.sess.RemoteAddr(),
			"username": c.user.Name,
			"object":   c.user.Object,
		},
	}); err != nil {
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
			return juicemud.WithStack(OperationAborted)
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
			return juicemud.WithStack(OperationAborted)
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
				return juicemud.WithStack(OperationAborted)
			} else if selection == "y" {
				user.PasswordHash = digest.ComputeHA1(user.Name, juicemud.DAVAuthRealm, password)
				c.user = user
			}
		} else {
			fmt.Fprintln(c.term, "Passwords don't match!")
		}
	}
	if err := c.game.createUser(c.sess.Context(), c.user); err != nil {
		return juicemud.WithStack(err)
	}
	storage.AuthenticateUser(c.sess.Context(), c.user)
	fmt.Fprintf(c.term, "Welcome %s!\n\n", c.user.Name)
	return nil
}
