package user

import (
	"fmt"

	"github.com/zond/juicemud/termio"
	"golang.org/x/crypto/ssh/terminal"
)

var (
	UserCreationAborted = fmt.Errorf("user creation aborted")
)

func CreateUser(term *terminal.Terminal) (*User, error) {
	selection := ""
	username := ""
	var err error
	for selection != "y" {
		fmt.Fprint(term, "** Create user **\n\nEnter new username:\n\n")
		username, err = term.ReadLine()
		if err != nil {
			return nil, err
		}
		selection, err = termio.TerminalSelect(term, fmt.Sprintf("Username %q, correct?", username), []string{"y", "n", "abort"})
		if err != nil {
			return nil, err
		}
		if selection == "abort" {
			return nil, UserCreationAborted
		}
	}
	selection = ""
	for selection != "y" {
		fmt.Fprint(term, "Enter new password:\n\n")
		password, err := term.ReadPassword("")
		if err != nil {
			return nil, err
		}
		fmt.Fprint(term, "Repeat new password:\n\n")
		verification, err := term.ReadPassword("")
		if err != nil {
			return nil, err
		}
		if password == verification {
			selection, err = termio.TerminalSelect(term, fmt.Sprintf("Create user %q with provided password?", username), []string{"y", "n", "abort"})
			if err != nil {
				return nil, err
			}
			if selection == "abort" {
				return nil, UserCreationAborted
			}
		} else {
			fmt.Fprint(term, "Passwords don't match!\n\n")
		}
	}
	return &User{}, nil
}

type User struct{}
