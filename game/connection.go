package game

import (
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/gliderlabs/ssh"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/digest"
	"github.com/zond/juicemud/lang"
	"github.com/zond/juicemud/storage"
	"github.com/zond/juicemud/structs"
	"golang.org/x/term"
	"rogchap.com/v8go"
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

func (c *Connection) describe(long bool) error {
	obj, err := c.object()
	if err != nil {
		return juicemud.WithStack(err)
	}
	// TODO: If long, load neighbourhood and compute every detectable description and show them suitably.
	//       If short, load location and siblings and show short descriptions of them.
	_, err = c.game.loadNeighbourhood(c.sess.Context(), obj)
	if err != nil {
		return juicemud.WithStack(err)
	}
	return nil
}

var (
	commands = map[string]func(e *Connection, args []string) error{
		"debug": func(c *Connection, args []string) error {
			id := string(c.user.Object)
			if len(args) == 1 {
				if byteID, err := hex.DecodeString(args[0]); err != nil {
					return juicemud.WithStack(err)
				} else {
					id = string(byteID)
				}
			}
			addConsole(id, c.term)
			return nil
		},
		"undebug": func(c *Connection, args []string) error {
			id := string(c.user.Object)
			if len(args) == 1 {
				if byteID, err := hex.DecodeString(args[0]); err != nil {
					return juicemud.WithStack(err)
				} else {
					id = string(byteID)
				}
			}
			delConsole(id, c.term)
			return nil
		},
		"l": func(c *Connection, args []string) error {
			return c.describe(true)
		},
		"look": func(c *Connection, args []string) error {
			return c.describe(true)
		},
	}
)

var (
	whitespacePattern = regexp.MustCompile(`\\s+`)
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
		if cmd, found := commands[words[0]]; found {
			if err := cmd(c, words[1:]); err != nil {
				fmt.Fprintln(c.term, err)
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
	if err := c.game.emitAny(c.sess.Context(), c.game.storage.Queue().After(0), c.user.Object, connectedEventType, map[string]any{
		"remote":   c.sess.RemoteAddr(),
		"username": c.user.Name,
		"object":   c.user.Object,
	}); err != nil {
		return juicemud.WithStack(err)
	}
	return c.Process()
}

func (c *Connection) loadAndRun(id string, call *structs.Call) error {
	if err := c.game.loadRunSave(c.sess.Context(), id, call); err != nil {
		jserr := &v8go.JSError{}
		if errors.As(err, &jserr) {
			switch rand.Intn(3) {
			case 0:
				fmt.Fprintln(c.term, "[reality stutters]")
			case 1:
				fmt.Fprintln(c.term, "[reality flickers]")
			case 2:
				fmt.Fprintln(c.term, "[reality jitters]")
			}
		} else {
			return juicemud.WithStack(err)
		}
	}
	return nil
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
	fmt.Fprintf(c.term, "Welcome back, %v!\n", c.user.Name)
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
	fmt.Fprintf(c.term, "Welcome %s!\n", c.user.Name)
	return nil
}
