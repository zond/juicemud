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
	Game *Game
	Sess ssh.Session
	User *storage.User
	Term *term.Terminal
}

func (e *Env) SelectExec(options map[string]func() error) error {
	commandNames := make(sort.StringSlice, 0, len(options))
	for name := range options {
		commandNames = append(commandNames, name)
	}
	sort.Sort(commandNames)
	prompt := fmt.Sprintf("%s\n", lang.Enumerator{Pattern: "[%s]", Operator: "or"}.Do(commandNames...))
	for {
		fmt.Fprint(e.Term, prompt)
		line, err := e.Term.ReadLine()
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
		fmt.Fprintf(e.Term, "%s [%s]\n", prompt, strings.Join(options, "/"))
		line, err := e.Term.ReadLine()
		if err != nil {
			return "", errors.WithStack(err)
		}
		for _, option := range options {
			if strings.ToLower(line) == strings.ToLower(option) {
				return option, nil
			}
		}
	}
}

func (e *Env) Process() error {
	for {
		line, err := e.Term.ReadLine()
		if err != nil {
			return errors.WithStack(err)
		}
		fmt.Fprintf(e.Term, "%s\n\n", line)
	}
}

func (e *Env) Connect() error {
	fmt.Fprint(e.Term, "Welcome!\n\n")
	sel := func() error {
		return e.SelectExec(map[string]func() error{
			"login user":  e.loginUser,
			"create user": e.createUser,
		})
	}
	for err := sel(); err != nil && e.User == nil; err = sel() {
		if !errors.Is(err, OperationAborted) {
			fmt.Fprintln(e.Term, err)
		}
	}
	return e.Process()
}

func (e *Env) loginUser() error {
	fmt.Fprint(e.Term, "** Login user **\n\n")
	var user *storage.User
	for user == nil {
		fmt.Fprintln(e.Term, "Enter username or [abort]:")
		username, err := e.Term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return errors.WithStack(OperationAborted)
		}
		if user, err = e.Game.storage.GetUser(e.Sess.Context(), username); errors.Is(err, storage.NotFoundErr) {
			fmt.Fprintln(e.Term, "Username not found!")
		} else if err != nil {
			return errors.WithStack(err)
		}
	}
	for e.User == nil {
		fmt.Fprint(e.Term, "Enter password or [abort]:\n")
		password, err := e.Term.ReadPassword("> ")
		if err != nil {
			return err
		}
		ha1 := digest.ComputeHA1(user.Name, juicemud.DAVAuthRealm, password)
		if subtle.ConstantTimeCompare([]byte(ha1), []byte(user.PasswordHash)) != 1 {
			fmt.Fprintln(e.Term, "Incorrect password!")
		} else {
			e.User = user
		}
	}
	fmt.Fprintf(e.Term, "Welcome back, %v!\n", e.User.Name)
	return nil
}

func (e *Env) createUser() error {
	fmt.Fprint(e.Term, "** Create user **\n\n")
	var user *storage.User
	for user == nil {
		fmt.Fprint(e.Term, "Enter new username or [abort]:\n")
		username, err := e.Term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return errors.WithStack(OperationAborted)
		}
		if _, err = e.Game.storage.GetUser(e.Sess.Context(), username); errors.Is(err, storage.NotFoundErr) {
			user = &storage.User{
				Name: username,
			}
		} else if err == nil {
			fmt.Fprintln(e.Term, "Username already exists!")
		} else {
			return errors.WithStack(err)
		}
	}
	for e.User == nil {
		fmt.Fprintln(e.Term, "Enter new password:")
		password, err := e.Term.ReadPassword("> ")
		if err != nil {
			return err
		}
		fmt.Fprintln(e.Term, "Repeat new password:")
		verification, err := e.Term.ReadPassword("> ")
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
				e.User = user
			}
		} else {
			fmt.Fprintln(e.Term, "Passwords don't match!")
		}
	}
	if err := e.Game.storage.SetUser(e.Sess.Context(), e.User, false); err != nil {
		return errors.WithStack(err)
	}
	fmt.Fprintf(e.Term, "Welcome, %v!\n", e.User.Name)
	return nil
}
