package user

import (
	"fmt"

	"github.com/asdine/storm"
	"github.com/zond/juicemud/termio"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh/terminal"
)

var (
	UserCreationAborted = fmt.Errorf("user creation aborted")
	UserLoginAborted    = fmt.Errorf("user login aborted")
)

func LoginUser(db *storm.DB, term *terminal.Terminal) (*User, error) {
	fmt.Fprintf(term, "** Login user **\n\n")
	user := &User{}
	for {
		fmt.Fprintf(term, "Enter username or [abort]:\n\n")
		username, err := term.ReadLine()
		if err != nil {
			return nil, err
		}
		if username == "abort" {
			return nil, UserLoginAborted
		}
		if err = db.One("Username", username, user); err == storm.ErrNotFound {
			fmt.Fprint(term, "Username not found!\n\n")
		} else if err == nil {
			break
		} else {
			return nil, err
		}
	}
	for {
		fmt.Fprint(term, "Enter password or [abort]:\n\n")
		password, err := term.ReadPassword("")
		if err != nil {
			return nil, err
		}
		if err = bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(password)); err == nil {
			return user, nil
		} else {
			fmt.Fprint(term, "Incorrect password!\n\n")
		}
	}
	return nil, nil
}

func CreateUser(db *storm.DB, term *terminal.Terminal) (*User, error) {
	fmt.Fprint(term, "** Create user **\n\n")
	user := &User{}
	for {
		fmt.Fprint(term, "Enter new username or [abort]:\n\n")
		username, err := term.ReadLine()
		if err != nil {
			return nil, err
		}
		if username == "abort" {
			return nil, UserCreationAborted
		}
		if err = db.One("Username", username, user); err == nil {
			fmt.Fprint(term, "Username already exists!\n\n")
		} else if err == storm.ErrNotFound {
			user.Username = username
			break
		} else {
			return nil, err
		}
	}
	for {
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
			selection, err := termio.TerminalSelect(term, fmt.Sprintf("Create user %q with provided password?", user.Username), []string{"y", "n", "abort"})
			if err != nil {
				return nil, err
			}
			if selection == "abort" {
				return nil, UserCreationAborted
			} else if selection == "y" {
				if user.PasswordHash, err = bcrypt.GenerateFromPassword([]byte(password), 16); err != nil {
					return nil, err
				}
				break
			}
		} else {
			fmt.Fprint(term, "Passwords don't match!\n\n")
		}
	}
	return user, db.Save(user)
}

type User struct {
	Username     string `storm:"id"`
	PasswordHash []byte
}
