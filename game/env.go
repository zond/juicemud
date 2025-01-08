package game

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gliderlabs/ssh"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/digest"
	"github.com/zond/juicemud/js"
	"github.com/zond/juicemud/lang"
	"github.com/zond/juicemud/storage"
	"golang.org/x/term"
	"rogchap.com/v8go"
)

var (
	OperationAborted = errors.New("operation aborted")
)

const (
	connectedEventType = "connected"
)

var (
	envByObjectID         = juicemud.NewSyncMap[string, *Env]()
	consoleByObjectID     = juicemud.NewSyncMap[string, *Fanout]()
	consoleByObjectIDLock = juicemud.NewLockMap[string]()
	jsContextLocks        = juicemud.NewLockMap[string]()
)

func addConsole(id string, term *term.Terminal) {
	consoleByObjectIDLock.WithLock(id, func() {
		consoleByObjectID.Get(id).Push(term)
	})
}

func delConsole(id string, term *term.Terminal) {
	consoleByObjectIDLock.WithLock(id, func() {
		consoleByObjectID.Get(id).Drop(term)
	})
}

type errs []error

func (e errs) Error() string {
	return fmt.Sprintf("%+v", []error(e))
}

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

func (e *Env) SelectReturn(prompt string, options []string) (string, error) {
	for {
		fmt.Fprintf(e.term, "%s [%s]\n", prompt, strings.Join(options, "/"))
		line, err := e.term.ReadLine()
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

var (
	commands = map[string]func(e *Env, args []string) error{
		"debug": func(e *Env, args []string) error {
			id := string(e.user.Object)
			if len(args) == 1 {
				if byteID, err := hex.DecodeString(args[0]); err != nil {
					return juicemud.WithStack(err)
				} else {
					id = string(byteID)
				}
			}
			addConsole(id, e.term)
			return nil
		},
		"undebug": func(e *Env, args []string) error {
			id := string(e.user.Object)
			if len(args) == 1 {
				if byteID, err := hex.DecodeString(args[0]); err != nil {
					return juicemud.WithStack(err)
				} else {
					id = string(byteID)
				}
			}
			delConsole(id, e.term)
			return nil
		},
	}
)

var (
	whitespacePattern = regexp.MustCompile("\\s+")
)

func (e *Env) Process() error {
	if e.user == nil {
		return errors.New("can't process without user")
	}
	envByObjectID.Set(string(e.user.Object), e)
	defer envByObjectID.Del(string(e.user.Object))
	for {
		line, err := e.term.ReadLine()
		if err != nil {
			return juicemud.WithStack(err)
		}
		words := whitespacePattern.Split(line, -1)
		if len(words) == 0 {
			continue
		}
		if cmd, found := commands[words[0]]; found {
			if err := cmd(e, words[1:]); err != nil {
				fmt.Fprintln(e.term, err)
			}
		}
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
	b, err := json.Marshal(map[string]any{
		"remote":   e.sess.RemoteAddr(),
		"username": e.user.Name,
		"object":   e.user.Object,
	})
	if err != nil {
		return juicemud.WithStack(err)
	}
	if err := loadAndCall(e.sess.Context(), e.user.Object, connectedEventType, string(b)); err != nil {
		return juicemud.WithStack(err)
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
			return juicemud.WithStack(OperationAborted)
		}
		if user, err = e.game.storage.GetUser(e.sess.Context(), username); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(e.term, "Username not found!")
		} else if err != nil {
			return juicemud.WithStack(err)
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
			return juicemud.WithStack(OperationAborted)
		}
		if _, err = e.game.storage.GetUser(e.sess.Context(), username); errors.Is(err, os.ErrNotExist) {
			user = &storage.User{
				Name: username,
			}
		} else if err == nil {
			fmt.Fprintln(e.term, "Username already exists!")
		} else {
			return juicemud.WithStack(err)
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
				return juicemud.WithStack(OperationAborted)
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
		return juicemud.WithStack(err)
	}
	if err := object.SetSource(userSource); err != nil {
		return juicemud.WithStack(err)
	}
	if err := call(e.sess.Context(), object, "", ""); err != nil {
		return juicemud.WithStack(err)
	}

	objectID, err := object.Id()
	if err != nil {
		return juicemud.WithStack(err)
	}
	e.user.Object = objectID
	if err := e.game.storage.SetUser(e.sess.Context(), e.user, false); err != nil {
		return juicemud.WithStack(err)
	}

	if err := e.game.storage.SetObject(e.sess.Context(), object); err != nil {
		return juicemud.WithStack(err)
	}

	fmt.Fprintf(e.term, "Welcome, %v!\n", e.user.Name)
	return nil
}

func call(ctx context.Context, object *storage.Object, callbackName string, message string) error {
	id, err := object.Id()
	if err != nil {
		return juicemud.WithStack(err)
	}
	sid := string(id)
	origin, err := object.Source()
	if err != nil {
		return juicemud.WithStack(err)
	}
	state, err := object.State()
	if err != nil {
		return juicemud.WithStack(err)
	}
	game, err := GetGame(ctx)
	if err != nil {
		return juicemud.WithStack(err)
	}
	source, err := game.storage.GetSource(ctx, origin)
	if err != nil {
		return juicemud.WithStack(err)
	}
	callbacks := map[string]func(fctx *js.RunContext, info *v8go.FunctionCallbackInfo) *v8go.Value{}
	if console, found := consoleByObjectID.GetHas(sid); found {
		callbacks["log"] = js.Log(console)
	}
	target := js.Target{
		Source:    string(source),
		Origin:    origin,
		State:     state,
		Callbacks: callbacks,
	}
	res, err := target.Call(ctx, callbackName, message, 200*time.Millisecond)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if err := object.SetState(res.State); err != nil {
		return juicemud.WithStack(err)
	}
	newCallbacks, err := object.NewCallbacks(int32(len(res.Callbacks)))
	if err != nil {
		return juicemud.WithStack(err)
	}
	for index, cb := range res.Callbacks {
		if err := newCallbacks.Set(index, cb); err != nil {
			return juicemud.WithStack(err)
		}
	}
	if err := object.SetCallbacks(newCallbacks); err != nil {
		return juicemud.WithStack(err)
	}
	return nil
}

func loadAndCall(ctx context.Context, id []byte, callbackName string, message string) error {
	sid := string(id)
	jsContextLocks.Lock(sid)
	defer jsContextLocks.Unlock(sid)

	game, err := GetGame(ctx)
	if err != nil {
		return juicemud.WithStack(err)
	}
	object, err := game.storage.GetObject(ctx, id)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if err := call(ctx, object, callbackName, message); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(game.storage.SetObject(ctx, object))
}
