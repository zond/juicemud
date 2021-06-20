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
	for selection != "y" {
		fmt.Fprintf(term, "** Create user **\n\nEnter username:\n\n")
		username, err := term.ReadLine()
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
	fmt.Fprint(term, "OKOK!\n\n")
	return &User{}, nil
}

type User struct{}
