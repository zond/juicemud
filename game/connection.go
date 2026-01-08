package game

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gliderlabs/ssh"
	"github.com/goccy/go-json"
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

const defaultTermWidth = 80

// TermWidth returns the terminal width in columns.
// Falls back to defaultTermWidth if PTY info is unavailable.
func (c *Connection) TermWidth() int {
	pty, _, ok := c.sess.Pty()
	if !ok || pty.Window.Width <= 0 {
		return defaultTermWidth
	}
	return pty.Window.Width
}

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
	if neigh, err = neigh.Filter(c.game.Context(c.ctx), viewer); err != nil {
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

// handleEmitEvent handles emit events that should be rendered to the connection.
// This is a convenience for users - JS processing always continues regardless.
// messageContent is the payload for message events.
type messageContent struct {
	Text string
}

func (c *Connection) handleEmitEvent(call *structs.Call) error {
	switch call.Name {
	case movementEventType:
		m := &movement{}
		if err := json.Unmarshal([]byte(call.Message), m); err != nil {
			return juicemud.WithStack(err)
		}
		return c.renderMovement(m)
	case messageEventType:
		msg := &messageContent{}
		if err := json.Unmarshal([]byte(call.Message), msg); err != nil {
			return juicemud.WithStack(err)
		}
		if msg.Text != "" {
			fmt.Fprintln(c.term, msg.Text)
		}
	}
	return nil
}

// renderMovement handles movement rendering for a player connection.
// If the moved object has Movement.Active, uses fast Go-based rendering with the verb.
// Otherwise, calls the renderMovement callback on the moved object and uses the return value.
func (c *Connection) renderMovement(m *movement) error {
	// Compute perspectives from observer's current neighbourhood
	_, neigh, err := c.game.loadNeighbourhoodOf(c.ctx, c.user.Object)
	if err != nil {
		return juicemud.WithStack(err)
	}

	var src, dst *structs.Perspective
	if m.Source != nil {
		if neigh.Location.GetId() == *m.Source {
			src = &structs.Perspective{Here: true}
		} else if exit := neigh.Location.FindExit(*m.Source); exit != nil {
			src = &structs.Perspective{Exit: exit}
		}
	}
	if m.Destination != nil {
		if neigh.Location.GetId() == *m.Destination {
			dst = &structs.Perspective{Here: true}
		} else if exit := neigh.Location.FindExit(*m.Destination); exit != nil {
			dst = &structs.Perspective{Exit: exit}
		}
	}

	// If observer has moved and can no longer see either location, skip rendering.
	// They saw the movement happen (that's why they got the event), but rendering
	// a message now would be confusing since they're no longer in position to see it.
	if src == nil && dst == nil {
		return nil
	}

	mov := m.Object.GetMovement()
	verb := mov.Verb
	if verb == "" {
		verb = "moves"
	}

	if mov.Active {
		// Use default Go-based rendering with verb
		c.renderDefaultMovement(m, src, dst, verb)
	} else if m.Object.HasCallback(renderMovementEventType, emitEventTag) {
		// Call the renderMovement callback directly and use its return value
		value, err := c.game.run(c.ctx, m.Object, &structs.AnyCall{
			Name: renderMovementEventType,
			Tag:  emitEventTag,
			Content: &renderMovementRequest{
				Observer:    c.user.Object,
				Source:      src,
				Destination: dst,
			},
		}, nil)
		if err != nil {
			// JS error - fall back to default rendering
			c.renderDefaultMovement(m, src, dst, verb)
			return nil
		}
		if value == nil {
			// Callback didn't return a value - fall back to default
			c.renderDefaultMovement(m, src, dst, verb)
			return nil
		}
		// Parse the returned message
		var resp movementRenderedResponse
		if err := json.Unmarshal([]byte(*value), &resp); err != nil {
			// Invalid JSON - fall back to default
			c.renderDefaultMovement(m, src, dst, verb)
			return nil
		}
		if resp.Message == "" {
			// Empty message - fall back to default
			c.renderDefaultMovement(m, src, dst, verb)
			return nil
		}
		fmt.Fprintln(c.term, resp.Message)
	} else {
		// Movement.Active is false but no JS callback registered - fall back to default
		c.renderDefaultMovement(m, src, dst, verb)
	}
	return nil
}

// renderDefaultMovement renders a movement message using Go-based rendering.
// src/dst are the pre-computed perspectives (nil if not visible, Here=true for current room,
// or Exit set for neighbors). verb is the movement verb (e.g., "moves", "scurries").
func (c *Connection) renderDefaultMovement(m *movement, src, dst *structs.Perspective, verb string) {
	name := lang.Capitalize(m.Object.Indef())
	srcHere := src != nil && src.Here
	dstHere := dst != nil && dst.Here
	srcNeighbor := src != nil && src.Exit != nil
	dstNeighbor := dst != nil && dst.Exit != nil

	switch {
	case srcHere && dstNeighbor:
		// Leaves current room to visible neighbor
		fmt.Fprintf(c.term, "%s %s %s.\n", name, verb, dst.Exit.Name())

	case srcNeighbor && dstHere:
		// Arrives at current room from visible neighbor
		fmt.Fprintf(c.term, "%s %s in from %s.\n", name, verb, src.Exit.Name())

	case srcNeighbor && dstNeighbor:
		// Passes between two visible neighbors
		fmt.Fprintf(c.term, "%s %s from %s to %s.\n", name, verb, src.Exit.Name(), dst.Exit.Name())

	case srcHere:
		// Leaves current room to unknown/invisible destination
		fmt.Fprintf(c.term, "%s disappears.\n", name)

	case dstHere:
		// Arrives at current room from unknown/invisible source
		fmt.Fprintf(c.term, "%s appears.\n", name)

	case srcNeighbor:
		// Leaves visible neighbor to unknown destination
		fmt.Fprintf(c.term, "Via %s, you see %s leave.\n", src.Exit.Name(), m.Object.Indef())

	case dstNeighbor:
		// Arrives at visible neighbor from unknown source
		fmt.Fprintf(c.term, "Via %s, you see %s arrive.\n", dst.Exit.Name(), m.Object.Indef())
	}
}

func (c *Connection) describeLocation(loc *structs.Location) error {
	fmt.Fprintln(c.term, loc.Container.Name())
	if long := structs.Descriptions(loc.Container.GetDescriptions()).Long(); long != "" {
		fmt.Fprintln(c.term)
		fmt.Fprintln(c.term, long)
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
	if loc, err = loc.Filter(c.game.Context(c.ctx), viewer); err != nil {
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

// parseShellTokens parses up to n shell-style tokens from s and returns them plus the remaining string.
// If n <= 0, parses all tokens.
// Handles single quotes, double quotes, and backslash escapes.
func parseShellTokens(s string, n int) (tokens []string, rest string) {
	i := 0
	for (n <= 0 || len(tokens) < n) && i < len(s) {
		// Skip leading whitespace
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		// Parse one token
		var token strings.Builder
		for i < len(s) && s[i] != ' ' && s[i] != '\t' {
			switch s[i] {
			case '\'':
				i++
				for i < len(s) && s[i] != '\'' {
					token.WriteByte(s[i])
					i++
				}
				if i < len(s) {
					i++ // skip closing quote
				}
			case '"':
				i++
				for i < len(s) && s[i] != '"' {
					if s[i] == '\\' && i+1 < len(s) {
						i++
						token.WriteByte(s[i])
						i++
					} else {
						token.WriteByte(s[i])
						i++
					}
				}
				if i < len(s) {
					i++ // skip closing quote
				}
			case '\\':
				if i+1 < len(s) {
					i++
					token.WriteByte(s[i])
					i++
				} else {
					i++
				}
			default:
				token.WriteByte(s[i])
				i++
			}
		}
		tokens = append(tokens, token.String())
	}
	// Skip whitespace after last token
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return tokens, s[i:]
}

// identifyingCommand wraps a command handler to parse object references from the input.
// maxTargets limits how many object references to parse (0 = parse all remaining parts as targets).
// The callback receives:
//   - self: the player's object
//   - rest: raw remaining string after targets (empty if maxTargets=0)
//   - targets: parsed object references
func (c *Connection) identifyingCommand(def defaultObject, maxTargets int, f func(c *Connection, self *structs.Object, rest string, targets ...*structs.Object) error) func(*Connection, string) error {
	return func(c *Connection, s string) error {
		// Parse command name + up to maxTargets target patterns
		numToParse := 1 + maxTargets // command + targets
		if maxTargets <= 0 {
			numToParse = 0 // parse all
		}
		parts, rest := parseShellTokens(s, numToParse)
		if len(parts) == 0 {
			return nil
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
				return f(c, obj, rest, obj)
			case defaultLoc:
				loc, err := c.game.accessObject(c.ctx, obj.GetLocation())
				if err != nil {
					return juicemud.WithStack(err)
				}
				if loc, err = loc.Filter(c.game.Context(c.ctx), obj); err != nil {
					return juicemud.WithStack(err)
				}
				return f(c, obj, rest, loc)
			default:
				return nil
			}
		}
		obj, loc, err := c.game.loadLocationOf(c.ctx, c.user.Object)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if loc, err = loc.Filter(c.game.Context(c.ctx), obj); err != nil {
			return juicemud.WithStack(err)
		}
		targets := []*structs.Object{}
		for _, pattern := range parts[1:] {
			if pattern == "self" {
				// "self" always resolves to the user's own object
				targets = append(targets, obj)
			} else if c.wiz && strings.HasPrefix(pattern, "#") {
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
		return f(c, obj, rest, targets...)
	}
}

func (c *Connection) basicCommands() commands {
	return []command{
		{
			names: m("l", "look"),
			f: c.identifyingCommand(defaultLoc, 0, func(c *Connection, obj *structs.Object, _ string, targets ...*structs.Object) error {
				for _, target := range targets {
					if obj.GetLocation() == target.GetId() {
						if err := c.look(); err != nil {
							return juicemud.WithStack(err)
						}
					} else {
						fmt.Fprintln(c.term, target.Name())
						if long := structs.Descriptions(target.GetDescriptions()).Long(); long != "" {
							fmt.Fprintln(c.term)
							fmt.Fprintln(c.term, long)
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

	// directionAliases maps short direction commands to their full exit names
	directionAliases = map[string]string{
		"n":  "north",
		"s":  "south",
		"e":  "east",
		"w":  "west",
		"ne": "northeast",
		"nw": "northwest",
		"se": "southeast",
		"sw": "southwest",
		"u":  "up",
		"d":  "down",
	}
)

type objectAttempter struct {
	id string
}

func (o objectAttempter) attempt(c *Connection, name string, line string) (found bool, err error) {
	obj, err := c.game.accessObject(c.ctx, o.id)
	if err != nil {
		// Can't access own object - this is a real error
		return false, juicemud.WithStack(err)
	}
	value, err := c.game.run(c.ctx, obj, &structs.AnyCall{
		Name: name,
		Tag:  commandEventTag,
		Content: map[string]any{
			"name": name,
			"line": line,
		},
	}, nil)
	if value != nil {
		return true, err
	}
	// Continue on errors - broken JS shouldn't block command search.
	// JS errors are already recorded in jsStats by run().

	actionCall := &structs.AnyCall{
		Name: name,
		Tag:  actionEventTag,
		Content: map[string]any{
			"name": name,
			"line": line,
		},
	}

	loc, value, err := c.game.loadRun(c.ctx, obj.GetLocation(), actionCall, nil)
	if value != nil {
		return true, err
	}
	// Continue on errors - broken location JS shouldn't block command search.

	if loc == nil {
		// Location couldn't be loaded at all - can't continue
		return false, nil
	}
	if loc, err = loc.Filter(c.game.Context(c.ctx), obj); err != nil {
		return false, juicemud.WithStack(err)
	}

	// Check for direction alias (e.g., "n" -> "north").
	// We match both the original name AND the expanded alias, so:
	// - "n" matches exits named "n" or "north"
	// - "north" matches exits named "north"
	// This allows short aliases while still supporting custom exit names.
	expandedName := name
	if alias, ok := directionAliases[name]; ok {
		expandedName = alias
	}

	for _, exit := range loc.GetExits() {
		if exit.Name() == name || exit.Name() == expandedName {
			// Use CheckWithDetails to get both score and blamed skill on failure
			score, blamedSkill := exit.UseChallenge.CheckWithDetails(c.game.Context(c.ctx), obj, loc.GetId())
			if score > 0 {
				// Check if the object has a handleMovement callback
				if obj.HasCallback(handleMovementEventType, emitEventTag) {
					// Let JS handle the movement decision
					// JS can call moveObject(getId(), exit.Destination) if it wants to move
					_, err := c.game.run(c.ctx, obj, &structs.AnyCall{
						Name: handleMovementEventType,
						Tag:  emitEventTag,
						Content: &handleMovementRequest{
							Exit:  &exit,
							Score: score,
						},
					}, nil)
					if err == nil {
						// JS handled it (moveObject handles auto-look)
						return true, nil
					}
					// JS error - log for debugging, fall through to default movement
					log.Printf("handleMovement callback error for %s: %v", obj.GetId(), err)
				}
				// Default movement (moveObject handles auto-look)
				return true, juicemud.WithStack(c.game.moveObject(c.ctx, obj, exit.Destination))
			}
			// Challenge failed - get message from container callback or use default
			exitFailedContent := map[string]any{
				"subject":     obj,
				"exit":        exit,
				"score":       score,
				"challenge":   exit.UseChallenge,
				"blamedSkill": blamedSkill,
			}

			// Check if container has renderExitFailed callback for custom messages
			if loc.HasCallback(renderExitFailedEventType, emitEventTag) {
				value, err := c.game.run(c.ctx, loc, &structs.AnyCall{
					Name:    renderExitFailedEventType,
					Tag:     emitEventTag,
					Content: exitFailedContent,
				}, nil)
				if err != nil {
					log.Printf("renderExitFailed callback error for %s: %v", loc.GetId(), err)
				} else if value != nil {
					var resp exitFailedRenderedResponse
					if err := json.Unmarshal([]byte(*value), &resp); err != nil {
						log.Printf("renderExitFailed: invalid JSON from %s: %v", loc.GetId(), err)
					} else if resp.Message != "" {
						fmt.Fprintln(c.term, resp.Message)
					}
				}
			} else if exit.UseChallenge.Message != "" {
				// No callback - use default message if available
				fmt.Fprintln(c.term, exit.UseChallenge.Message)
			}

			// Emit async exitFailed event for additional handling
			exitFailedJSON, err := json.Marshal(exitFailedContent)
			if err != nil {
				return true, juicemud.WithStack(err)
			}
			if err := c.game.emitJSON(c.ctx, c.game.storage.Queue().After(0), loc.GetId(), "exitFailed", string(exitFailedJSON)); err != nil {
				return true, juicemud.WithStack(err)
			}
			return true, nil
		}
	}

	cont := loc.GetContent()
	delete(cont, o.id)
	for sibID := range cont {
		_, value, err = c.game.loadRun(c.ctx, sibID, actionCall, nil)
		if value != nil {
			return true, err
		}
		// Continue on errors - a broken sibling shouldn't block command processing.
		// JS errors are already recorded in jsStats by run().
	}
	return false, nil
}

func (c *Connection) Process() error {
	if c.user == nil {
		return errors.New("can't process without user")
	}
	c.wiz = c.user.Wizard

	c.game.connectionByObjectID.Set(string(c.user.Object), c)
	defer c.game.connectionByObjectID.Del(string(c.user.Object))

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
		if len(words) == 0 || words[0] == "" {
			continue
		}
		handled := false
		for _, commands := range commandSets {
			if found, err := commands.attempt(c, words[0], line); err != nil {
				fmt.Fprintln(c.term, err)
				handled = true
				break
			} else if found {
				handled = true
				break
			}
		}
		if !handled {
			fmt.Fprintf(c.term, "Unknown command: %q\n", words[0])
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
			// Update last login time
			user.SetLastLogin(time.Now().UTC())
			if err := c.game.storage.StoreUser(c.ctx, user, true, c.sess.RemoteAddr().String()); err != nil {
				// Log error but don't fail login - don't expose internal errors to users
				log.Printf("Failed to update last login for user %s: %v", user.Name, err)
			}
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
	}, nil); err != nil {
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
			switch selection {
			case "abort":
				return juicemud.WithStack(ErrOperationAborted)
			case "y":
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
	obj.Unsafe.Location = c.game.getSpawnLocation(c.ctx)
	user.Object = obj.Unsafe.Id
	user.SetLastLogin(time.Now().UTC())
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
	}, nil); err != nil {
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
