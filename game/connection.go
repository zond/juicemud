package game

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/buildkite/shellwords"
	"github.com/gliderlabs/ssh"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/lang"
	"github.com/zond/juicemud/storage"
	"github.com/zond/juicemud/structs"
	"golang.org/x/crypto/argon2"
	"golang.org/x/term"
)

var (
	ErrOperationAborted = fmt.Errorf("operation aborted")
)

// Argon2id parameters (OWASP recommended)
const (
	argon2Time    = 1
	argon2Memory  = 64 * 1024 // 64 MB
	argon2Threads = 4
	argon2KeyLen  = 32
	argon2SaltLen = 16
)

// hashPassword creates an Argon2id hash of the password.
// Returns the hash in PHC string format: $argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>
func hashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2Memory, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash)), nil
}

// verifyPassword checks if the password matches the stored hash.
// Supports Argon2id hashes in PHC string format.
func verifyPassword(password, encodedHash string) bool {
	// Parse PHC string format
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false
	}

	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}

	hash := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(expectedHash)))
	return subtle.ConstantTimeCompare(hash, expectedHash) == 1
}

var (
	connectionByObjectID = juicemud.NewSyncMap[string, *Connection]()
	consoleSwitchboard   = NewSwitchboard()
)

const (
	loginAttemptInterval = 10 * time.Second
	loginAttemptCleanup  = 1 * time.Minute
)

// loginRateLimiter tracks failed login attempts per username for rate limiting.
// Entries older than loginAttemptInterval are periodically cleaned up every
// loginAttemptCleanup interval. This bounds memory usage: even if an attacker
// spams unique usernames, entries expire within ~70 seconds (10s interval + 60s
// cleanup period). At typical network speeds, this limits the map to manageable size.
type loginRateLimiter struct {
	mu       sync.RWMutex
	attempts map[string]time.Time
}

// newLoginRateLimiter creates a new login rate limiter and starts the cleanup
// loop. The loop runs until the context is cancelled.
func newLoginRateLimiter(ctx context.Context) *loginRateLimiter {
	l := &loginRateLimiter{
		attempts: make(map[string]time.Time),
	}
	go l.runCleanupLoop(ctx)
	return l
}

// runCleanupLoop periodically removes expired login attempt entries.
func (l *loginRateLimiter) runCleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(loginAttemptCleanup)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.mu.Lock()
			now := time.Now()
			for username, lastAttempt := range l.attempts {
				if now.Sub(lastAttempt) > loginAttemptInterval {
					delete(l.attempts, username)
				}
			}
			l.mu.Unlock()
		}
	}
}

// waitIfNeeded blocks if a recent failed attempt exists for the username.
func (l *loginRateLimiter) waitIfNeeded(username string, term *term.Terminal) {
	l.mu.RLock()
	last, ok := l.attempts[username]
	l.mu.RUnlock()
	if ok {
		if wait := loginAttemptInterval - time.Since(last); wait > 0 {
			fmt.Fprintf(term, "Please wait %v before trying again.\n", wait.Round(time.Second))
			time.Sleep(wait)
		}
	}
}

// recordFailure records a failed login attempt for rate limiting.
func (l *loginRateLimiter) recordFailure(username string) {
	l.mu.Lock()
	l.attempts[username] = time.Now()
	l.mu.Unlock()
}

// clearFailure removes the rate limit entry on successful login.
func (l *loginRateLimiter) clearFailure(username string) {
	l.mu.Lock()
	delete(l.attempts, username)
	l.mu.Unlock()
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
	wiz  bool
	ctx  context.Context // Derived from sess.Context(), updated with session ID and authenticated user
}

// func (c *Connection) Linebreak(s string) string {
// 	result := &bytes.Buffer{}

// }

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

func (c *Connection) scan() error {
	viewer, neigh, err := c.game.loadDeepNeighbourhoodOf(c.ctx, c.user.Object)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if neigh, err = neigh.Filter(viewer); err != nil {
		return juicemud.WithStack(err)
	}
	if err := c.describeLocation(neigh.Location); err != nil {
		return juicemud.WithStack(err)
	}
	for _, exit := range neigh.Location.Container.GetExits() {
		if neigh, found := neigh.Neighbours[exit.Destination]; found {
			fmt.Fprintln(c.term)
			fmt.Fprintf(c.term, "Via exit %s, you see:\n", exit.Name())
			if err := c.describeLocation(neigh); err != nil {
				return juicemud.WithStack(err)
			}
		}
	}
	return nil
}

func (c *Connection) renderMovement(m *movement) error {
	_, neigh, err := c.game.loadNeighbourhoodOf(c.ctx, c.user.Object)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if m.Source != nil {
		if exit, found := neigh.FindLocation(*m.Source); found {
			if exit != nil {
				fmt.Fprintf(c.term, "Via exit %s, you see %v leave.\n", exit.Name(), m.Object.Indef())
			} else {
				fmt.Fprintf(c.term, "%v leaves.\n", lang.Capitalize(m.Object.Indef()))
			}
		}
		// If !found, source not visible - just don't show leaving message
	}
	if m.Destination != nil {
		if exit, found := neigh.FindLocation(*m.Destination); found {
			if exit != nil {
				fmt.Fprintf(c.term, "Via exit %s, you see %v arrive.\n", exit.Name(), m.Object.Indef())
			} else {
				fmt.Fprintf(c.term, "%v arrives.\n", lang.Capitalize(m.Object.Indef()))
			}
		}
		// If !found, destination not visible - just don't show arriving message
	}
	return nil
}

func (c *Connection) describeLocation(loc *structs.Location) error {
	fmt.Fprintln(c.term, loc.Container.Name())
	descs := loc.Container.GetDescriptions()
	if len(descs) > 0 && descs[0].Long != "" {
		fmt.Fprintln(c.term)
		fmt.Fprintln(c.term, descs[0].Long)
	}
	if len(loc.Content) > 0 {
		fmt.Fprintln(c.term)
		fmt.Fprintf(c.term, "%s here\n", lang.Enumerator{Tense: lang.Present}.Do(loc.Content.Short()...))
	}
	exits := loc.Container.GetExits()
	if len(exits) > 0 {
		fmt.Fprintln(c.term)
		fmt.Fprintln(c.term, "Exits:")
		fmt.Fprintln(c.term, structs.Exits(exits).Short())
	}
	return nil
}

func (c *Connection) look() error {
	viewer, loc, err := c.game.loadLocationOf(c.ctx, c.user.Object)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if loc, err = loc.Filter(viewer); err != nil {
		return juicemud.WithStack(err)
	}
	return c.describeLocation(loc)
}

type command struct {
	names map[string]bool
	f     func(*Connection, string) error
}

type attempter interface {
	attempt(conn *Connection, name string, line string) (bool, error)
}

type commands []command

func (c commands) attempt(conn *Connection, name string, line string) (bool, error) {
	for _, cmd := range c {
		if cmd.names[name] {
			if err := cmd.f(conn, line); err != nil {
				return true, juicemud.WithStack(err)
			}
			return true, nil
		}
	}
	return false, nil

}

func m(s ...string) map[string]bool {
	res := map[string]bool{}
	for _, p := range s {
		res[p] = true
	}
	return res
}

type defaultObject int

const (
	defaultNone defaultObject = iota
	defaultSelf
	defaultLoc
)

func (c *Connection) identifyingCommand(def defaultObject, f func(c *Connection, self *structs.Object, targets ...*structs.Object) error) func(*Connection, string) error {
	return func(c *Connection, s string) error {
		parts, err := shellwords.SplitPosix(s)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if len(parts) == 1 {
			obj, err := c.game.accessObject(c.ctx, c.user.Object)
			if err != nil {
				return juicemud.WithStack(err)
			}
			switch def {
			case defaultNone:
				fmt.Fprintf(c.term, "usage: %s [target]\n", parts[0])
				return nil
			case defaultSelf:
				return f(c, obj, obj)
			case defaultLoc:
				loc, err := c.game.accessObject(c.ctx, obj.GetLocation())
				if err != nil {
					return juicemud.WithStack(err)
				}
				if loc, err = loc.Filter(obj); err != nil {
					return juicemud.WithStack(err)
				}
				return f(c, obj, loc)
			default:
				return nil
			}
		}
		obj, loc, err := c.game.loadLocationOf(c.ctx, c.user.Object)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if loc, err = loc.Filter(obj); err != nil {
			return juicemud.WithStack(err)
		}
		targets := []*structs.Object{}
		for _, pattern := range parts[1:] {
			if c.wiz && strings.HasPrefix(pattern, "#") {
				target, err := c.game.accessObject(c.ctx, pattern[1:])
				if err != nil {
					fmt.Fprintln(c.term, err.Error())
					return nil
				}
				targets = append(targets, target)
			} else {
				target, err := loc.Identify(pattern)
				if err != nil {
					fmt.Fprintln(c.term, err.Error())
					return nil
				}
				targets = append(targets, target)
			}
		}
		return f(c, obj, targets...)
	}
}

func (c *Connection) basicCommands() commands {
	return []command{
		{
			names: m("l", "look"),
			f: c.identifyingCommand(defaultLoc, func(c *Connection, obj *structs.Object, targets ...*structs.Object) error {
				for _, target := range targets {
					if obj.GetLocation() == target.GetId() {
						if err := c.look(); err != nil {
							return juicemud.WithStack(err)
						}
					} else {
						fmt.Fprintln(c.term, target.Name())
						descs := target.GetDescriptions()
						if len(descs) > 0 {
							if descs[0].Long != "" {
								fmt.Fprintln(c.term)
								fmt.Fprintln(c.term, descs[0].Long)
							}
						}
					}
				}
				return nil
			}),
		},
		{
			names: m("scan"),
			f: func(c *Connection, s string) error {
				return c.scan()
			},
		},
	}
}

var (
	whitespacePattern = regexp.MustCompile(`\s+`)
)

type objectAttempter struct {
	id string
}

func (o objectAttempter) attempt(c *Connection, name string, line string) (found bool, err error) {
	obj, err := c.game.accessObject(c.ctx, o.id)
	if err != nil {
		return false, juicemud.WithStack(err)
	}
	found, err = c.game.run(c.ctx, obj, &structs.AnyCall{
		Name: name,
		Tag:  commandEventTag,
		Content: map[string]any{
			"name": name,
			"line": line,
		},
	})
	if err != nil {
		return false, juicemud.WithStack(err)
	} else if found {
		return true, nil
	}

	actionCall := &structs.AnyCall{
		Name: name,
		Tag:  actionEventTag,
		Content: map[string]any{
			"name": name,
			"line": line,
		},
	}

	loc, found, err := c.game.loadRun(c.ctx, obj.GetLocation(), actionCall)
	if found || err != nil {
		return found, err
	}

	if loc, err = loc.Filter(obj); err != nil {
		return false, juicemud.WithStack(err)
	}

	for _, exit := range loc.GetExits() {
		if exit.Name() == name {
			if structs.Challenges(exit.UseChallenges).Check(obj, loc.GetId()) > 0 {
				return true, juicemud.WithStack(c.game.moveObject(c.ctx, obj, exit.Destination))
			}
		}
	}

	cont := loc.GetContent()
	delete(cont, o.id)
	for sibID := range cont {
		_, found, err = c.game.loadRun(c.ctx, sibID, actionCall)
		if found || err != nil {
			return found, err
		}
	}
	return false, nil
}

func (c *Connection) Process() error {
	if c.user == nil {
		return errors.New("can't process without user")
	}
	c.wiz = c.user.Wizard

	connectionByObjectID.Set(string(c.user.Object), c)
	defer connectionByObjectID.Del(string(c.user.Object))

	commandSets := []attempter{objectAttempter{c.user.Object}, c.basicCommands()}
	if c.wiz {
		commandSets = append([]attempter{c.wizCommands()}, commandSets...)
	}

	for {
		// Update prompt for wizards to show flush health status
		if c.wiz {
			if health := c.game.storage.FlushHealth(); !health.Healthy() {
				c.term.SetPrompt("[!]> ")
			} else {
				c.term.SetPrompt("> ")
			}
		}

		line, err := c.term.ReadLine()
		if err != nil {
			return juicemud.WithStack(err)
		}
		words := whitespacePattern.Split(line, -1)
		if len(words) == 0 {
			continue
		}
		for _, commands := range commandSets {
			if found, err := commands.attempt(c, words[0], line); err != nil {
				fmt.Fprintln(c.term, err)
			} else if found {
				break
			}
		}
	}
}

func (c *Connection) Connect() error {
	// Generate session ID at connection start so all audit events (including failed logins) can be correlated
	c.ctx = storage.SetSessionID(c.ctx, juicemud.NextUniqueID())
	fmt.Fprint(c.term, "Welcome!\n\n")
	sel := func() error {
		return c.SelectExec(map[string]func() error{
			"login user":  c.loginUser,
			"create user": c.createUser,
		})
	}
	var err error
	for err = sel(); errors.Is(err, ErrOperationAborted); err = sel() {
	}
	if err != nil {
		return juicemud.WithStack(err)
	}
	if err := c.look(); err != nil {
		return juicemud.WithStack(err)
	}
	return c.Process()
}

func (c *Connection) loginUser() error {
	fmt.Fprint(c.term, "** Login user **\n\n")
	for c.user == nil {
		fmt.Fprintln(c.term, "Enter username or [abort]:")
		username, err := c.term.ReadLine()
		if err != nil {
			return err
		}
		if username == "abort" {
			return juicemud.WithStack(ErrOperationAborted)
		}

		// Rate limit login attempts per username (only after failed attempts)
		c.game.loginRateLimiter.waitIfNeeded(username, c.term)

		fmt.Fprint(c.term, "Enter password or [abort]:\n")
		password, err := c.term.ReadPassword("> ")
		if err != nil {
			return err
		}
		if password == "abort" {
			return juicemud.WithStack(ErrOperationAborted)
		}

		user, err := c.game.storage.LoadUser(c.ctx, username)
		if errors.Is(err, os.ErrNotExist) {
			// User doesn't exist - add delay to prevent timing-based enumeration
			c.game.loginRateLimiter.recordFailure(username)
			c.game.storage.AuditLog(c.ctx, "LOGIN_FAILED", storage.AuditLoginFailed{
				User:   storage.Ref(0, username),
				Remote: c.sess.RemoteAddr().String(),
			})
			fmt.Fprintln(c.term, "Invalid credentials!")
			continue
		} else if err != nil {
			return juicemud.WithStack(err)
		}

		if !verifyPassword(password, user.PasswordHash) {
			// Record failed attempt for rate limiting
			c.game.loginRateLimiter.recordFailure(user.Name)
			c.game.storage.AuditLog(c.ctx, "LOGIN_FAILED", storage.AuditLoginFailed{
				User:   storage.Ref(user.Id, user.Name),
				Remote: c.sess.RemoteAddr().String(),
			})
			fmt.Fprintln(c.term, "Invalid credentials!")
		} else {
			// Successful login - clear any rate limit for this user
			c.game.loginRateLimiter.clearFailure(user.Name)
			c.user = user
		}
	}
	c.ctx = storage.AuthenticateUser(c.ctx, c.user)
	c.game.storage.AuditLog(c.ctx, "USER_LOGIN", storage.AuditUserLogin{
		User:   storage.Ref(c.user.Id, c.user.Name),
		Remote: c.sess.RemoteAddr().String(),
	})
	fmt.Fprintf(c.term, "Welcome back, %v!\n\n", c.user.Name)

	// Warn wizards if database flush is failing
	if c.user.Wizard {
		if health := c.game.storage.FlushHealth(); !health.Healthy() {
			if health.LastFlushAt.IsZero() {
				fmt.Fprint(c.term, "WARNING: Database flush failing (no successful flush yet)\n")
			} else {
				fmt.Fprintf(c.term, "WARNING: Database flush failing (last success %v ago)\n", time.Since(health.LastFlushAt).Truncate(time.Second))
			}
			fmt.Fprintf(c.term, "Error: %v\n\n", health.LastError)
		}
	}
	if _, _, err := c.game.loadRun(c.ctx, c.user.Object, &structs.AnyCall{
		Name: connectedEventType,
		Tag:  emitEventTag,
		Content: map[string]any{
			"remote":   c.sess.RemoteAddr(),
			"username": c.user.Name,
			"object":   c.user.Object,
			"cause":    "login",
		},
	}); err != nil {
		return juicemud.WithStack(err)
	}
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
			return juicemud.WithStack(ErrOperationAborted)
		}
		if err := validateUsername(username); err != nil {
			fmt.Fprintln(c.term, err.Error())
			continue
		}
		if _, err = c.game.storage.LoadUser(c.ctx, username); errors.Is(err, os.ErrNotExist) {
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
		if password == "abort" {
			fmt.Fprintln(c.term, "Password cannot be 'abort' (reserved keyword).")
			continue
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
				return juicemud.WithStack(ErrOperationAborted)
			} else if selection == "y" {
				hash, err := hashPassword(password)
				if err != nil {
					return juicemud.WithStack(err)
				}
				user.PasswordHash = hash
				c.user = user
			}
		} else {
			fmt.Fprintln(c.term, "Passwords don't match!")
		}
	}
	obj, err := structs.MakeObject(c.ctx)
	if err != nil {
		return juicemud.WithStack(err)
	}
	obj.Unsafe.SourcePath = userSource
	obj.Unsafe.Location = genesisID
	user.Object = obj.Unsafe.Id
	if err := c.game.storage.StoreUser(c.ctx, user, false, c.sess.RemoteAddr().String()); err != nil {
		return juicemud.WithStack(err)
	}
	if err := c.game.createObject(c.ctx, obj); err != nil {
		return juicemud.WithStack(err)
	}
	if _, err = c.game.run(c.ctx, obj, &structs.AnyCall{
		Name: connectedEventType,
		Tag:  emitEventTag,
		Content: map[string]any{
			"remote":   c.sess.RemoteAddr(),
			"username": c.user.Name,
			"object":   c.user.Object,
			"cause":    "create",
		},
	}); err != nil {
		return juicemud.WithStack(err)
	}
	c.ctx = storage.AuthenticateUser(c.ctx, c.user)
	c.game.storage.AuditLog(c.ctx, "USER_LOGIN", storage.AuditUserLogin{
		User:   storage.Ref(c.user.Id, c.user.Name),
		Remote: c.sess.RemoteAddr().String(),
	})
	fmt.Fprintf(c.term, "Welcome %s!\n\n", c.user.Name)
	return nil
}
