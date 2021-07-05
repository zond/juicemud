package game

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/timshannon/badgerhold/v2"
	"github.com/zond/juicemud/editor"
	"github.com/zond/juicemud/lang"
	"github.com/zond/juicemud/storage"
	"github.com/zond/sshtcelltty"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh/terminal"
)

var (
	OperationAborted  = fmt.Errorf("operation aborted")
	WhitespacePattern = regexp.MustCompile("\\s+")
)

// Represents the environment of a connected user.
type Env struct {
	Game   *Game
	User   *storage.User
	Object storage.Object
	Sess   sshtcelltty.InterleavedSSHSession
	Term   *terminal.Terminal
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
	return e.Process()
}

func (e *Env) Process() error {
	loc, err := e.Object.Location()
	if err != nil {
		return err
	}
	sd, err := loc.ShortDescription()
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Term, "%s\n\n", sd)
	e.listContent(loc)
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
			"l":           e.look,
			"look":        e.look,
			"help":        e.help,
			"create":      e.create,
			"inventorize": e.inventorize,
			"edit":        e.edit,
		}[words[0]]; found {
			if err := cmd(words); err != nil {
				fmt.Fprintln(e.Term, err.Error())
			}
		}
	}
	return nil
}

var lorem = `What is Lorem Ipsum?

Lorem Ipsum is <tag>simply<another tag> dummy text of the printing and typesetting industry. Lorem Ipsum has been the industry's standard dummy text ever since the 1500s, when an unknown printer took a galley of type and scrambled it to make a type specimen book.
It has survived not only five &centuries, &amp;but also the leap into electronic typesetting, remaining essentially unchanged. It was popularised in the 1960s with the release of Letraset sheets containing Lorem Ipsum passages, and more recently with desktop publishing software like Aldus PageMaker including versions of Lorem Ipsum.

There are many variations of passages of Lorem Ipsum available, but the majority have suffered alteration in some form, by injected humour, or randomised words which don't look even slightly believable. If you are going to use a passage of Lorem Ipsum, you need to be sure there isn't anything embarrassing hidden in the middle of text. All the Lorem Ipsum generators on the Internet tend to repeat predefined chunks as necessary, making this the first true generator on the Internet. It uses a dictionary of over 200 Latin words, combined with a handful of model sentence structures, to generate Lorem Ipsum which looks reasonable. The generated Lorem Ipsum is therefore always free from repetition, injected humour, or non-characteristic words etc.

indent 1
  indent 2
 indent 2
  indent 2
indent 1

`

func (e *Env) edit([]string) error {
	_, err := editor.Edit(e.Sess, lorem)
	return err
}

func (e *Env) help([]string) error {
	fmt.Fprint(e.Term, "Try [look] or [create 'name'].\n\n")
	return nil
}

func (e *Env) create([]string) error {
	return nil
}

func (e *Env) listContent(obj storage.Object) error {
	content, err := obj.Content()
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return nil
	}
	names := make(sort.StringSlice, len(content))
	for idx := range content {
		if e.Object.UID() == content[idx].UID() {
			names[idx] = "you"
		} else {
			if names[idx], err = content[idx].Name(false); err != nil {
				return err
			}
		}
	}
	sort.Sort(names)
	fmt.Fprintf(e.Term, "There is %s here.\n\n", lang.Enumerator{}.Do(names...))
	return nil
}

func (e *Env) lookAt(target storage.Object) error {
	sd, err := target.ShortDescription()
	if err != nil {
		return err
	}
	ld, err := target.LongDescription()
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Term, "#%v: %s\n\n%s\n\n", target.UID(), sd, ld)
	return e.listContent(target)
}

type ambiguousIdentityError struct {
	tag     string
	matches []identifiableObject
}

func (a ambiguousIdentityError) Error() string {
	return fmt.Sprintf("%q is ambiguous among %+v", a.tag, a.matches)
}

type notFoundError struct {
	tag string
}

func (n notFoundError) Error() string {
	return fmt.Sprintf("%q not found", n.tag)
}

type identifiableObject struct {
	obj  storage.Object
	tags []string
}

func (e *Env) listInventory(objs []identifiableObject) error {
	for _, obj := range objs {
		if len(obj.tags) == 1 {
			fmt.Fprintf(e.Term, "%v\n", obj.tags[0])
		} else {
			fmt.Fprintf(e.Term, "%v, also known as %v\n", obj.tags[0], lang.Enumerator{Operator: "or"}.Do(obj.tags[1:]...))
		}
	}
	return nil
}

func (e *Env) inventorize([]string) error {
	loc, err := e.Object.Location()
	if err != nil {
		return err
	}
	objs, err := e.identifiableObjects(loc)
	if err != nil {
		return err
	}
	if err := e.listInventory(objs); err != nil {
		return err
	}
	fmt.Fprintln(e.Term)
	return nil
}

func (e *Env) identifiableObjects(loc storage.Object) ([]identifiableObject, error) {
	candidates, err := loc.Content()
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, loc)
	sort.Sort(candidates)

	res := []identifiableObject{}
	candidatesByTag := map[string][]*identifiableObject{}
	for _, c := range candidates {
		tags, err := c.Tags()
		if err != nil {
			return nil, err
		}
		idKeyword := fmt.Sprintf("#%v", c.UID())
		tags = append(tags, idKeyword)
		idObj := &identifiableObject{
			obj:  c,
			tags: tags,
		}
		res = append(res, *idObj)
		for _, t := range tags {
			candidatesByTag[t] = append(candidatesByTag[t], idObj)
		}
	}

	for idx := range res {
		cand := res[idx]
		newTags := []string{}
		for _, oldTag := range cand.tags {
			tagHolders := candidatesByTag[oldTag]
			if len(tagHolders) == 1 {
				newTags = append(newTags, oldTag)
			} else {
				for tagIdx, tagHolder := range tagHolders {
					if tagHolder.obj.UID() == cand.obj.UID() {
						newTags = append(newTags, fmt.Sprintf("%v.%v", tagIdx, oldTag))
						break
					}
				}
			}
		}
		res[idx].tags = newTags
	}

	return res, nil
}

func (e *Env) identify(searchTag string) (storage.Object, error) {
	// Quick check for common alternatives.
	loc, err := e.Object.Location()
	if err != nil {
		return nil, err
	}
	switch searchTag {
	case "me":
		fallthrough
	case "self":
		return e.Object, nil
	case "here":
		return loc, nil
	}

	idObjs, err := e.identifiableObjects(loc)
	if err != nil {
		return nil, err
	}

	nameTag := searchTag
	if parts := strings.Split(searchTag, "."); len(parts) == 2 {
		nameTag = parts[1]
	}

	matches := []identifiableObject{}
	for _, idObj := range idObjs {
		for _, tag := range idObj.tags {
			if tag == searchTag {
				return idObj.obj, nil
			} else if parts := strings.Split(tag, "."); len(parts) == 2 && parts[1] == nameTag {
				matches = append(matches, idObj)
			}
		}
	}

	if len(matches) == 0 {
		return nil, notFoundError{tag: searchTag}
	}

	return nil, ambiguousIdentityError{tag: searchTag, matches: matches}
}

func (e *Env) look(args []string) error {
	target, err := e.Object.Location()
	if err != nil {
		return err
	}
	if len(args) > 1 {
		if target, err = e.identify(args[1]); err != nil {
			switch verr := err.(type) {
			case ambiguousIdentityError:
				if parts := strings.Split(verr.tag, "."); len(parts) == 2 {
					fmt.Fprintf(e.Term, "No %q here, but some close matches:\n", verr.tag)
				} else {
					fmt.Fprintf(e.Term, "Multiple %q here:\n", verr.tag)
				}
				if err := e.listInventory(verr.matches); err != nil {
					return err
				}
				fmt.Fprintln(e.Term)
				return nil
			case notFoundError:
				fmt.Fprintf(e.Term, "No %q here.\n", verr.tag)
				return nil
			}
			return err
		}
	}
	return e.lookAt(target)
}

func (e *Env) loginUser() error {
	fmt.Fprint(e.Term, "** Login user **\n\n")
	var user *storage.User
	for {
		fmt.Fprint(e.Term, "Enter username or [abort]:\n")
		username, err := e.Term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return OperationAborted
		}
		if user, err = e.Game.Storage.LoadUser(username); err == badgerhold.ErrNotFound {
			fmt.Fprint(e.Term, "Username not found!\n")
		} else if err == nil {
			break
		} else {
			return err
		}
	}
	for {
		fmt.Fprint(e.Term, "Enter password or [abort]:\n")
		password, err := e.Term.ReadPassword("> ")
		if err != nil {
			return err
		}
		if err = bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(password)); err == nil {
			e.User = user
			if e.Object, err = e.Game.Storage.GetObject(e.User.ObjectID); err != nil {
				return err
			}
			break
		} else {
			fmt.Fprint(e.Term, "Incorrect password!\n")
		}
	}
	return nil
}

func (e *Env) createUser() error {
	fmt.Fprint(e.Term, "** Create user **\n\n")
	var user *storage.User
	for {
		fmt.Fprint(e.Term, "Enter new username or [abort]:\n")
		username, err := e.Term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return OperationAborted
		}
		if user, err = e.Game.Storage.LoadUser(username); err == nil {
			fmt.Fprint(e.Term, "Username already exists!\n")
		} else if err == badgerhold.ErrNotFound {
			user = &storage.User{
				Username: username,
			}
			break
		} else {
			return err
		}
	}
	for {
		fmt.Fprint(e.Term, "Enter new password:\n")
		password, err := e.Term.ReadPassword("> ")
		if err != nil {
			return err
		}
		fmt.Fprint(e.Term, "Repeat new password:\n")
		verification, err := e.Term.ReadPassword("> ")
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
				if user.PasswordHash, err = bcrypt.GenerateFromPassword([]byte(password), 15); err != nil {
					return err
				}
				e.User = user
				break
			}
		} else {
			fmt.Fprint(e.Term, "Passwords don't match!\n")
		}
	}
	err := e.Game.Storage.CreateUser(user)
	if err != nil {
		return err
	}
	if e.Object, err = e.Game.Storage.GetObject(user.ObjectID); err != nil {
		return err
	}
	return nil
}
