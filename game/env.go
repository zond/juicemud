package game

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/timshannon/badgerhold"
	"github.com/zond/juicemud/lang"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh/terminal"
)

var (
	OperationAborted  = fmt.Errorf("operation aborted")
	WhitespacePattern = regexp.MustCompile("\\s+")
)

// Represents the environment of a connected user.
type Env struct {
	Game     *Game
	User     *User
	Term     *terminal.Terminal
	Location *Object
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
	fmt.Fprint(e.Term, "Welcome!\n\n")
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
	e.Location = e.Game.Objects[e.User.LocationID]
	return e.Process()
}

func (e *Env) Process() error {
	desc, err := e.Location.ShortDescription()
	if err != nil {
		fmt.Fprintln(e.Term, err.Error())
	}
	fmt.Fprintf(e.Term, "%s\n\n", desc)
	for {
		line, err := e.Term.ReadLine()
		if err != nil {
			return err
		}
		words := WhitespacePattern.Split(line, -1)
		if len(words) == 0 {
			continue
		}
		if cmd, found := map[string]func(params []string) error{
			"l":      e.look,
			"look":   e.look,
			"help":   e.help,
			"create": e.create,
		}[words[0]]; found {
			if err := cmd(words); err != nil {
				fmt.Fprintln(e.Term, err.Error())
			}
		}
	}
	return nil
}

func (e *Env) help([]string) error {
	fmt.Fprint(e.Term, "Try [look] or [create 'name'].\n\n")
	return nil
}

func (e *Env) create([]string) error {
	return nil
}

func (e *Env) look([]string) error {
	shortDesc, err := e.Location.ShortDescription()
	if err != nil {
		fmt.Fprintln(e.Term, err.Error())
	}
	longDesc, err := e.Location.LongDescription()
	if err != nil {
		fmt.Fprintln(e.Term, err.Error())
	}
	fmt.Fprintf(e.Term, "%s\n\n%s\n\n", shortDesc, longDesc)
	return nil
}

func (e *Env) loginUser() error {
	fmt.Fprint(e.Term, "** Login user **\n\n")
	user := &User{}
	for {
		fmt.Fprint(e.Term, "Enter username or [abort]:\n")
		username, err := e.Term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return OperationAborted
		}
		if err = e.Game.DB.Get(username, user); err == badgerhold.ErrNotFound {
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
	user := &User{}
	for {
		fmt.Fprint(e.Term, "Enter new username or [abort]:\n")
		username, err := e.Term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return OperationAborted
		}
		if err = e.Game.DB.Get(username, user); err == nil {
			fmt.Fprint(e.Term, "Username already exists!\n")
		} else if err == badgerhold.ErrNotFound {
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
	return e.Game.DB.Insert(user.Username, user)
}
