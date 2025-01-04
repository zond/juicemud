package game

import (
	"crypto/subtle"
	"fmt"
	"sort"
	"strings"

	"github.com/gliderlabs/ssh"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/digest"
	"github.com/zond/juicemud/lang"
	"github.com/zond/juicemud/storage"
	"golang.org/x/term"
)

var (
	OperationAborted = errors.New("operation aborted")
)

type Env struct {
	game *Game
	sess ssh.Session
	term *term.Terminal
	user *storage.User
}

func (e *Env) SelectExec(options map[string]func() error) error {
	commandNames := make(sort.StringSlice, 0, len(options))
	for name := range options {
		commandNames = append(commandNames, name)
	}
	sort.Sort(commandNames)
	prompt := fmt.Sprintf("%s\n", lang.Enumerator{Pattern: "[%s]", Operator: "or"}.Do(commandNames...))
	for {
		fmt.Fprint(e.term, prompt)
		line, err := e.term.ReadLine()
		if err != nil {
			return errors.WithStack(err)
		}
		if cmd, found := options[line]; found {
			if err := cmd(); err != nil {
				return errors.WithStack(err)
			}
			break
		}
	}
	return nil
}

func (e *Env) SelectReturn(prompt string, options []string) (string, error) {
	for {
		fmt.Fprintf(e.term, "%s [%s]\n", prompt, strings.Join(options, "/"))
		line, err := e.term.ReadLine()
		if err != nil {
			return "", errors.WithStack(err)
		}
		for _, option := range options {
			if strings.EqualFold(line, option) {
				return option, nil
			}
		}
	}
}

func (e *Env) Process() error {
	for {
		line, err := e.term.ReadLine()
		if err != nil {
			return errors.WithStack(err)
		}
		fmt.Fprintf(e.term, "%s\n\n", line)
	}
}

func (e *Env) Connect() error {
	fmt.Fprint(e.term, "Welcome!\n\n")
	sel := func() error {
		return e.SelectExec(map[string]func() error{
			"login user":  e.loginUser,
			"create user": e.createUser,
		})
	}
	for err := sel(); err != nil && e.user == nil; err = sel() {
		if !errors.Is(err, OperationAborted) {
			fmt.Fprintln(e.term, err)
		}
	}
	return e.Process()
}

func (e *Env) loginUser() error {
	fmt.Fprint(e.term, "** Login user **\n\n")
	var user *storage.User
	for user == nil {
		fmt.Fprintln(e.term, "Enter username or [abort]:")
		username, err := e.term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return errors.WithStack(OperationAborted)
		}
		if user, err = e.game.storage.GetUser(e.sess.Context(), username); errors.Is(err, storage.NotFoundErr) {
			fmt.Fprintln(e.term, "Username not found!")
		} else if err != nil {
			return errors.WithStack(err)
		}
	}
	for e.user == nil {
		fmt.Fprint(e.term, "Enter password or [abort]:\n")
		password, err := e.term.ReadPassword("> ")
		if err != nil {
			return err
		}
		ha1 := digest.ComputeHA1(user.Name, juicemud.DAVAuthRealm, password)
		if subtle.ConstantTimeCompare([]byte(ha1), []byte(user.PasswordHash)) != 1 {
			fmt.Fprintln(e.term, "Incorrect password!")
		} else {
			e.user = user
		}
	}
	fmt.Fprintf(e.term, "Welcome back, %v!\n", e.user.Name)
	return nil
}

func (e *Env) createUser() error {
	fmt.Fprint(e.term, "** Create user **\n\n")
	var user *storage.User
	for user == nil {
		fmt.Fprint(e.term, "Enter new username or [abort]:\n")
		username, err := e.term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return errors.WithStack(OperationAborted)
		}
		if _, err = e.game.storage.GetUser(e.sess.Context(), username); errors.Is(err, storage.NotFoundErr) {
			user = &storage.User{
				Name: username,
			}
		} else if err == nil {
			fmt.Fprintln(e.term, "Username already exists!")
		} else {
			return errors.WithStack(err)
		}
	}
	for e.user == nil {
		fmt.Fprintln(e.term, "Enter new password:")
		password, err := e.term.ReadPassword("> ")
		if err != nil {
			return err
		}
		fmt.Fprintln(e.term, "Repeat new password:")
		verification, err := e.term.ReadPassword("> ")
		if err != nil {
			return err
		}
		if password == verification {
			selection, err := e.SelectReturn(fmt.Sprintf("Create user %q with provided password?", user.Name), []string{"y", "n", "abort"})
			if err != nil {
				return err
			}
			if selection == "abort" {
				return errors.WithStack(OperationAborted)
			} else if selection == "y" {
				user.PasswordHash = digest.ComputeHA1(user.Name, juicemud.DAVAuthRealm, password)
				e.user = user
			}
		} else {
			fmt.Fprintln(e.term, "Passwords don't match!")
		}
	}
	object, err := e.game.storage.CreateObject(e.sess.Context())
	if err != nil {
		return errors.WithStack(err)
	}
	objectID, err := object.Id()
	if err != nil {
		return errors.WithStack(err)
	}
	e.user.Object = objectID
	if err := e.game.storage.SetUser(e.sess.Context(), e.user, false); err != nil {
		return errors.WithStack(err)
	}
	fmt.Fprintf(e.term, "Welcome, %v!\n", e.user.Name)
	return nil
}
