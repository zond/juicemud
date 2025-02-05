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
	OperationAborted = errors.New("operation aborted")
)

var (
	envByObjectID     = juicemud.NewSyncMap[string, *Connection]()
	consoleByObjectID = juicemud.NewSyncMap[string, *Fanout]()
	jsContextLocks    = juicemud.NewSyncMap[string, bool]()
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

func (c *Connection) object() (*structs.Object, error) {
	return c.game.storage.LoadObject(c.sess.Context(), c.user.Object, c.game.rerunSource)
}

func (c *Connection) describeLong() error {
	obj, err := c.object()
	if err != nil {
		return juicemud.WithStack(err)
	}
	// TODO: Load neighbourhood and compute every detectable description and show them suitably.
	neigh, err := c.game.loadNeighbourhood(c.sess.Context(), obj)
	if err != nil {
		return juicemud.WithStack(err)
	}
	desc, exits, siblings := neigh.Location.Inspect(obj)
	if desc != nil {
		fmt.Fprintln(c.term, desc.Short)
		fmt.Fprintln(c.term)
		fmt.Fprintln(c.term, desc.Long)
	}
	if len(siblings) > 0 {
		fmt.Fprintln(c.term)
		fmt.Fprintf(c.term, "%s here\n", lang.Enumerator{Active: true}.Do(siblings.Short()...))
	}
	if len(exits) > 0 {
		fmt.Fprintln(c.term)
		fmt.Fprintln(c.term, exits.Short())
	}
	return nil
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

var (
	commands = []command{
		{
			names: m("groups"),
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
			names:  m("/state"),
			wizard: true,
			f: func(c *Connection, s string) error {
				obj, err := c.game.storage.LoadObject(c.sess.Context(), c.user.Object, c.game.rerunSource)
				if err != nil {
					return juicemud.WithStack(err)
				}
				state := map[string]any{}
				if err := goccy.Unmarshal([]byte(obj.State), &state); err != nil {
					return juicemud.WithStack(err)
				}
				js, err := goccy.MarshalIndent(state, "  ", "  ")
				if err != nil {
					return juicemud.WithStack(err)
				}
				fmt.Fprintln(c.term, string(js))
				return nil
			},
		},
		{
			names:  m("/debug"),
			wizard: true,
			f: func(c *Connection, s string) error {
				addConsole(string(c.user.Object), c.term)
				return nil
			},
		},
		{
			names:  m("/undebug"),
			wizard: true,
			f: func(c *Connection, s string) error {
				delConsole(string(c.user.Object), c.term)
				return nil
			},
		},
		{
			names: m("l", "look"),
			f: func(c *Connection, s string) error {
				return c.describeLong()
			},
		},
		{
			names:  m("!chwrite"),
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
			names:  m("!chread"),
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
			names:  m("!ls"),
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
)

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
		for _, cmd := range commands {
			if cmd.names[words[0]] {
				if cmd.wizard {
					if has, err := c.game.storage.UserAccessToGroup(c.sess.Context(), c.user, wizardsGroup); err != nil {
						return juicemud.WithStack(err)
					} else if has {
						if err := cmd.f(c, line); err != nil {
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
	if err := c.game.loadRunSave(c.sess.Context(), c.user.Object, &AnyCall{
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
	if err := c.describeLong(); err != nil {
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
