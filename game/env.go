package game

import (
	"fmt"
	"sort"
	"strings"

	"github.com/asdine/storm"
	"github.com/zond/juicemud/lang"
	"github.com/zond/juicemud/user"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh/terminal"
)

var (
	OperationAborted = fmt.Errorf("operation aborted")
)

// Represents the environment of a connected user.
type Env struct {
	DB   *storm.DB
	User *user.User
	Term *terminal.Terminal
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
			return err
		}
		if cmd, found := options[line]; found {
			if err := cmd(); err != nil {
				return err
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
			return "", err
		}
		for _, option := range options {
			if strings.ToLower(line) == strings.ToLower(option) {
				return option, nil
			}
		}
	}
}

func (e *Env) Connect() error {
	fmt.Fprintf(e.Term, "Welcome!\n\n")
	for {
		err := e.SelectExec(map[string]func() error{
			"login user":  e.loginUser,
			"create user": e.createUser,
		})
		if err == nil {
			break
		} else if err != OperationAborted {
			return err
		}
	}
	return nil
}

func (e *Env) loginUser() error {
	fmt.Fprintf(e.Term, "** Login user **\n\n")
	user := &user.User{}
	for {
		fmt.Fprintf(e.Term, "Enter username or [abort]:\n")
		username, err := e.Term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return OperationAborted
		}
		if err = e.DB.One("Username", username, user); err == storm.ErrNotFound {
			fmt.Fprint(e.Term, "Username not found!\n")
		} else if err == nil {
			break
		} else {
			return err
		}
	}
	for {
		fmt.Fprint(e.Term, "Enter password or [abort]:\n")
		password, err := e.Term.ReadPassword("")
		if err != nil {
			return err
		}
		if err = bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(password)); err == nil {
			e.User = user
			break
		} else {
			fmt.Fprint(e.Term, "Incorrect password!\n")
		}
	}
	return nil
}

func (e *Env) createUser() error {
	fmt.Fprint(e.Term, "** Create user **\n\n")
	user := &user.User{}
	for {
		fmt.Fprint(e.Term, "Enter new username or [abort]:\n")
		username, err := e.Term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return OperationAborted
		}
		if err = e.DB.One("Username", username, user); err == nil {
			fmt.Fprint(e.Term, "Username already exists!\n")
		} else if err == storm.ErrNotFound {
			user.Username = username
			break
		} else {
			return err
		}
	}
	for {
		fmt.Fprint(e.Term, "Enter new password:\n")
		password, err := e.Term.ReadPassword("")
		if err != nil {
			return err
		}
		fmt.Fprint(e.Term, "Repeat new password:\n")
		verification, err := e.Term.ReadPassword("")
		if err != nil {
			return err
		}
		if password == verification {
			selection, err := e.SelectReturn(fmt.Sprintf("Create user %q with provided password?", user.Username), []string{"y", "n", "abort"})
			if err != nil {
				return err
			}
			if selection == "abort" {
				return OperationAborted
			} else if selection == "y" {
				if user.PasswordHash, err = bcrypt.GenerateFromPassword([]byte(password), 16); err != nil {
					return err
				}
				e.User = user
				break
			}
		} else {
			fmt.Fprint(e.Term, "Passwords don't match!\n")
		}
	}
	return e.DB.Save(user)
}
