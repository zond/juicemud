package storage

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/js/imports"
	"github.com/zond/juicemud/storage/dbm"
	"github.com/zond/juicemud/storage/queue"
	"github.com/zond/juicemud/structs"
	"github.com/zond/sqly"

	_ "modernc.org/sqlite"
)

func New(ctx context.Context, dir string) (*Storage, error) {
	sql, err := sqly.Open("sqlite", filepath.Join(dir, "sqlite.db"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	objects, err := dbm.OpenLiveTypeHash[structs.Object](ctx, filepath.Join(dir, "objects"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	queueTree, err := dbm.OpenTypeTree[structs.Event](filepath.Join(dir, "queue"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	sourceObjects, err := dbm.OpenTree(filepath.Join(dir, "sourceObjects"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	intervalsTree, err := dbm.OpenTypeTree[structs.Interval, *structs.Interval](filepath.Join(dir, "intervals"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	sourcesDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(sourcesDir, 0755); err != nil {
		return nil, juicemud.WithStack(err)
	}
	audit := NewAuditLogger(filepath.Join(dir, "audit.log"))
	s := &Storage{
		sql:           sql,
		sourcesDir:    sourcesDir,
		sourceObjects: sourceObjects,
		objects:       objects,
		intervals:     NewIntervals(intervalsTree),
		queue:         queue.New(ctx, queueTree),
		audit:         audit,
		resolver:      imports.NewResolver(),
	}
	for _, prototype := range []any{User{}} {
		if err := sql.CreateTableIfNotExists(ctx, prototype); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Storage manages persistent storage for the game.
//
// Lock ordering (must always be acquired in this order to prevent deadlocks):
//  1. sourceObjectsMu (Storage-level)
//  2. Object.jsMutex (JS execution serialization)
//  3. Object.mutex (field access via Lock/RLock, sorted by ID via structs.WithLock)
//  4. LiveTypeHash.stageMutex
//  5. Tree.mutex / Hash.mutex (tkrzw internal)
type Storage struct {
	queue           *queue.Queue
	sql             *sqly.DB
	sourcesDir      string
	sourceObjects   *dbm.Tree
	sourceObjectsMu sync.RWMutex // Protects sourceObjects operations
	objects         *dbm.LiveTypeHash[structs.Object, *structs.Object]
	intervals       *Intervals
	audit           *AuditLogger
	resolver        *imports.Resolver
}

func (s *Storage) Close() error {
	// Note: Queue shutdown is handled by context cancellation, not Close().
	// The caller (Game) is responsible for cancelling the queue context.
	if err := s.sql.Close(); err != nil {
		return juicemud.WithStack(err)
	}
	if err := s.sourceObjects.Close(); err != nil {
		return juicemud.WithStack(err)
	}
	if err := s.intervals.Close(); err != nil {
		return juicemud.WithStack(err)
	}
	if err := s.audit.Close(); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(s.objects.Close())
}

// SourcesDir returns the directory where source files are stored.
func (s *Storage) SourcesDir() string {
	s.sourceObjectsMu.RLock()
	defer s.sourceObjectsMu.RUnlock()
	return s.sourcesDir
}

func (s *Storage) Queue() *queue.Queue {
	return s.queue
}

// Intervals returns the interval storage for managing recurring timers.
func (s *Storage) Intervals() *Intervals {
	return s.intervals
}

// ImportResolver returns the import resolver for JavaScript source imports.
func (s *Storage) ImportResolver() *imports.Resolver {
	return s.resolver
}

// FlushHealth returns the current health state of the object flush loop.
func (s *Storage) FlushHealth() dbm.FlushHealth {
	return s.objects.FlushHealth()
}

// AuditLog writes a structured audit entry to the log.
// Note: The "remote" field in login events reflects the direct connection peer,
// which may be a proxy/load balancer rather than the actual client if the server
// is deployed behind such infrastructure.
func (s *Storage) AuditLog(ctx context.Context, event string, data AuditData) {
	s.audit.Log(ctx, event, data)
}

func getSQL(ctx context.Context, db sqlx.QueryerContext, d any, query string, params ...any) error {
	if err := sqlx.GetContext(ctx, db, d, query, params...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return juicemud.WithStack(os.ErrNotExist)
		}
		return juicemud.WithStack(fmt.Errorf("executing %q(%+v): %w", query, params, err))
	}
	return nil
}

// safePath validates and constructs a safe filesystem path, preventing path traversal attacks.
func (s *Storage) safePath(path string) (string, error) {
	// Prevent null byte injection
	if strings.ContainsRune(path, 0) {
		return "", errors.New("invalid path: contains null byte")
	}

	cleanPath := filepath.Clean(path)
	fullPath := filepath.Join(s.sourcesDir, cleanPath)

	absSourcesDir, _ := filepath.Abs(s.sourcesDir)
	absFullPath, _ := filepath.Abs(fullPath)

	if !strings.HasPrefix(absFullPath, absSourcesDir+string(filepath.Separator)) &&
		absFullPath != absSourcesDir {
		return "", errors.New("path traversal detected")
	}
	return fullPath, nil
}

// SafeSourcePath validates a relative path and returns the full filesystem path if safe.
// Returns an error if the path would escape the sources directory.
func (s *Storage) SafeSourcePath(path string) (string, error) {
	return s.safePath(path)
}

// LoadSource loads a source file from the filesystem.
func (s *Storage) LoadSource(ctx context.Context, path string) ([]byte, int64, error) {
	fullPath, err := s.safePath(path)
	if err != nil {
		return nil, 0, juicemud.WithStack(err)
	}

	info, err := os.Stat(fullPath)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, 0, nil
	} else if err != nil {
		return nil, 0, juicemud.WithStack(err)
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, 0, juicemud.WithStack(err)
	}

	return content, info.ModTime().UnixNano(), nil
}

// SourceModTime returns the modification time of a source file.
func (s *Storage) SourceModTime(_ context.Context, path string) (int64, error) {
	fullPath, err := s.safePath(path)
	if err != nil {
		return 0, juicemud.WithStack(err)
	}

	info, err := os.Stat(fullPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, juicemud.WithStack(err)
	}
	return info.ModTime().UnixNano(), nil
}

// SetSource writes a source file to the filesystem.
func (s *Storage) SetSource(_ context.Context, path string, content []byte) error {
	fullPath, err := s.safePath(path)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return juicemud.WithStack(err)
	}
	return os.WriteFile(fullPath, content, 0644)
}

// RemoveSource removes a source file from the filesystem.
func (s *Storage) RemoveSource(_ context.Context, path string) error {
	fullPath, err := s.safePath(path)
	if err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(os.Remove(fullPath))
}

// LoadResolvedSource loads a source file with all `// @import` directives resolved.
// Returns the concatenated source with dependencies prepended in topological order,
// the maximum modification time across all files in the dependency tree, and any error.
func (s *Storage) LoadResolvedSource(ctx context.Context, path string) ([]byte, int64, error) {
	// Check if cache needs invalidation by comparing current mtimes to cached max
	cachedMtime := s.resolver.GetCachedMaxMtime(path)
	if cachedMtime > 0 {
		currentMaxMtime, err := s.ResolvedSourceModTime(ctx, path)
		if err != nil {
			return nil, 0, juicemud.WithStack(err)
		}
		if currentMaxMtime > cachedMtime {
			// Only invalidate if the cache still has the stale entry.
			// This prevents a race where we invalidate a fresh cache entry
			// that another goroutine just created after we checked.
			s.resolver.InvalidateCacheIfStale(path, cachedMtime)
		}
	}

	result, err := s.resolver.Resolve(ctx, path, s.LoadSource)
	if err != nil {
		return nil, 0, juicemud.WithStack(err)
	}
	return []byte(result.Source), result.MaxMtime, nil
}

// ResolvedSourceModTime returns the maximum modification time across all files
// in a source's dependency tree. This is the fast path for checking if any file
// in the dependency chain has been modified without loading the actual source.
func (s *Storage) ResolvedSourceModTime(ctx context.Context, path string) (int64, error) {
	deps := s.resolver.GetCachedDeps(path)
	var maxMtime int64
	for _, dep := range deps {
		mtime, err := s.SourceModTime(ctx, dep)
		if err != nil {
			return 0, juicemud.WithStack(err)
		}
		if mtime > maxMtime {
			maxMtime = mtime
		}
	}
	return maxMtime, nil
}

// SourceExists checks if a source file exists on the filesystem.
func (s *Storage) SourceExists(ctx context.Context, path string) (bool, error) {
	fullPath, err := s.safePath(path)
	if err != nil {
		return false, juicemud.WithStack(err)
	}

	_, err = os.Stat(fullPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, juicemud.WithStack(err)
	}
	return true, nil
}

type Refresh func(ctx context.Context, object *structs.Object) error

// maybeRefresh runs the Refresh callback if the object's source file has been modified.
func (s *Storage) maybeRefresh(ctx context.Context, obj *structs.Object, ref Refresh) error {
	if ref != nil {
		t, err := s.SourceModTime(ctx, obj.GetSourcePath())
		if err != nil {
			return juicemud.WithStack(err)
		}
		needRefresh := t > obj.GetSourceModTime()
		if needRefresh {
			if err := ref(ctx, obj); err != nil {
				return juicemud.WithStack(err)
			}
		}
	}
	return nil
}

func (s *Storage) RemoveObject(ctx context.Context, obj *structs.Object) error {
	if obj.PostUnlock == nil {
		return errors.Errorf("can't remove object not known to storage: %v", obj)
	}

	id := obj.GetId()
	locID := obj.GetLocation()
	sourcePath := obj.GetSourcePath()

	loc, err := s.objects.Get(locID)
	if err != nil {
		return juicemud.WithStack(err)
	}

	// Lock before object removal to ensure EachSourceObject sees a consistent state.
	s.sourceObjectsMu.Lock()
	defer s.sourceObjectsMu.Unlock()

	if err := structs.WithLock(func() error {
		if obj.Unsafe.Location != locID {
			return errors.Errorf("%q no longer located in %q", id, locID)
		}
		if _, found := loc.Unsafe.Content[id]; !found {
			return errors.Errorf("%q doesn't contain %q", locID, id)
		}
		if len(obj.Unsafe.Content) > 0 {
			return errors.Errorf("%q isn't empty", id)
		}
		if err := s.objects.Proc([]dbm.LProc[structs.Object, *structs.Object]{
			s.objects.LProc(id, func(_ string, _ *structs.Object) (*structs.Object, error) {
				return nil, nil
			}),
			s.objects.LProc(locID, func(_ string, loc *structs.Object) (*structs.Object, error) {
				delete(loc.Unsafe.Content, id)
				return loc, nil
			}),
		}); err != nil {
			return juicemud.WithStack(err)
		}
		return nil
	}, obj, loc); err != nil {
		return juicemud.WithStack(err)
	}

	// Clean up intervals for the deleted object
	if err := s.intervals.DelAllForObject(id); err != nil {
		return juicemud.WithStack(err)
	}

	return juicemud.WithStack(s.sourceObjects.SubDel(sourcePath, id))
}

func (s *Storage) CreateObject(ctx context.Context, obj *structs.Object) error {
	if obj.PostUnlock != nil {
		return errors.Errorf("can't create object already known to storage: %+v", obj)
	}

	// Set default movement: use Go-based rendering with "moves" verb
	obj.SetMovement(structs.Movement{Active: true, Verb: "moves"})

	id := obj.GetId()
	locID := obj.GetLocation()

	loc, err := s.objects.Get(locID)
	if err != nil {
		return juicemud.WithStack(err)
	}

	s.sourceObjectsMu.Lock()
	defer s.sourceObjectsMu.Unlock()

	if err := s.sourceObjects.SubSet(obj.Unsafe.SourcePath, obj.Unsafe.Id, nil); err != nil {
		return juicemud.WithStack(err)
	}

	if err := structs.WithLock(func() error {
		if obj.Unsafe.Location != locID {
			return errors.Errorf("%q no longer located in %q", id, locID)
		}
		if _, found := loc.Unsafe.Content[id]; found {
			return errors.Errorf("%q already contains %q", locID, id)
		}
		return juicemud.WithStack(s.objects.Proc([]dbm.LProc[structs.Object, *structs.Object]{
			s.objects.LProc(id, func(_ string, _ *structs.Object) (*structs.Object, error) {
				return obj, nil
			}),
			s.objects.LProc(locID, func(_ string, loc *structs.Object) (*structs.Object, error) {
				loc.Unsafe.Content[id] = true
				return loc, nil
			}),
		}))
	}, obj, loc); err != nil {
		// Rollback: remove from sourceObjects on failure.
		// This is safe because object IDs are unique and an object can only be created once.
		// If rollback fails, log but don't compound errors - the primary error is more important,
		// and orphaned sourceObjects entries are cleaned up by EachSourceObject.
		if delerr := s.sourceObjects.SubDel(obj.Unsafe.SourcePath, obj.Unsafe.Id); delerr != nil && !errors.Is(delerr, os.ErrNotExist) {
			log.Printf("CreateObject rollback failed for %q/%q: %v", obj.Unsafe.SourcePath, obj.Unsafe.Id, delerr)
		}
		return juicemud.WithStack(err)
	}
	return nil
}


func (s *Storage) CountSourceObjects(ctx context.Context, path string) (int, error) {
	c := 0
	for _, err := range s.EachSourceObject(ctx, path) {
		if err != nil {
			return 0, err
		}
		c++
	}
	return c, nil
}

// EachSourceObject iterates over all object IDs created from the given source file path.
// It also performs cleanup: if an object no longer exists but is still indexed under this
// source path, it removes the stale index entry.
//
// Errors are yielded to callers but do not stop iteration - callers may receive errors
// interspersed with valid IDs and should handle both.
func (s *Storage) EachSourceObject(ctx context.Context, path string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		s.sourceObjectsMu.Lock()
		defer s.sourceObjectsMu.Unlock()

		var staleIDs []string
		for entry, err := range s.sourceObjects.SubEach(path) {
			if err != nil {
				if !yield("", juicemud.WithStack(err)) {
					return
				}
				continue
			}
			if s.objects.Has(entry.K) {
				if !yield(entry.K, nil) {
					return
				}
			} else {
				staleIDs = append(staleIDs, entry.K)
			}
		}
		// Clean up stale entries while still holding lock
		for _, id := range staleIDs {
			if err := s.sourceObjects.SubDel(path, id); err != nil {
				if !yield("", juicemud.WithStack(err)) {
					return
				}
			}
		}
	}
}

// ChangeSource updates the source path for an object.
// PRECONDITION: Caller must have exclusive access to the object (typically via holding its lock
// or being the only goroutine accessing it).
func (s *Storage) ChangeSource(ctx context.Context, obj *structs.Object, newSourcePath string) error {
	if obj.PostUnlock == nil {
		return errors.Errorf("can't set source of an object unknown to storage: %+v", obj)
	}

	oldSourcePath := obj.GetSourcePath()
	if oldSourcePath == "" {
		return errors.Errorf("can't change the source of an object that doesn't have a source: %+v", obj)
	}

	s.sourceObjectsMu.Lock()
	defer s.sourceObjectsMu.Unlock()

	if err := s.sourceObjects.SubSet(newSourcePath, obj.Unsafe.Id, nil); err != nil {
		return juicemud.WithStack(err)
	}

	obj.SetSourcePath(newSourcePath)
	obj.SetSourceModTime(0)

	if err := s.objects.Flush(); err != nil {
		return juicemud.WithStack(err)
	}

	return juicemud.WithStack(s.sourceObjects.SubDel(oldSourcePath, obj.Unsafe.Id))
}

// ErrCircularContainer is returned when trying to move an object into itself or
// into something contained within it, which would create a containment cycle.
var ErrCircularContainer = errors.New("cannot move object into itself or its contents")

// maxContainmentDepth is the maximum depth for containment chain walks.
// This prevents infinite loops on corrupted data and bounds performance.
// 1000 is chosen as significantly deeper than any legitimate game hierarchy
// (typically < 10 levels) while still completing quickly (in-memory lookups
// take ~100μs for 1000 iterations, disk lookups ~500ms worst case).
const maxContainmentDepth = 1000

// checkCircularContainment walks up the containment chain from destID to verify
// that objID is not an ancestor of destID. Returns ErrCircularContainer if moving
// objID into destID would create a cycle.
//
// The locked map contains already-loaded objects (keyed by ID) that should be
// used instead of calling s.objects.Get. Pass nil for the quick pre-lock check.
func (s *Storage) checkCircularContainment(objID, destID string, locked map[string]*structs.Object) error {
	currentID := destID
	for depth := 0; currentID != "" && depth < maxContainmentDepth; depth++ {
		if currentID == objID {
			return ErrCircularContainer
		}
		current := locked[currentID]
		if current == nil {
			var err error
			current, err = s.objects.Get(currentID)
			if err != nil {
				return juicemud.WithStack(err)
			}
			// Use GetLocation() for objects not in the locked set - they aren't
			// locked and we need the mutex protection.
			currentID = current.GetLocation()
		} else {
			// For objects in the locked set, we already hold their mutex via WithLock,
			// so we must access Unsafe directly to avoid deadlock (GetLocation tries
			// to acquire RLock which blocks on the held write lock).
			currentID = current.Unsafe.Location
		}
	}
	if currentID != "" {
		// This indicates corrupted data - either a cycle already exists or
		// the hierarchy is unreasonably deep. Log for admin investigation.
		log.Printf("WARNING: containment chain exceeded max depth %d while checking move of objID=%s to destID=%s, possible data corruption", maxContainmentDepth, objID, destID)
		return errors.New("containment chain too deep or already circular")
	}
	return nil
}

func (s *Storage) MoveObject(ctx context.Context, obj *structs.Object, destID string) error {
	if obj.PostUnlock == nil {
		return errors.Errorf("can't move object unknown to storage: %+v", obj)
	}
	if destID == "" {
		return errors.New("cannot move objects to root container (only genesis belongs there)")
	}

	id := obj.GetId()
	sourceID := obj.GetLocation()

	// Quick check for circular containment before acquiring locks.
	// This is an optimization to fail fast in the common case.
	// We re-check inside the lock to prevent TOCTOU races.
	if err := s.checkCircularContainment(id, destID, nil); err != nil {
		return err
	}

	source, err := s.objects.Get(sourceID)
	if err != nil {
		return juicemud.WithStack(err)
	}

	dest, err := s.objects.Get(destID)
	if err != nil {
		return juicemud.WithStack(err)
	}

	return juicemud.WithStack(structs.WithLock(func() error {
		// Re-check for circular containment under lock to prevent TOCTOU races.
		// Pass already-loaded objects to avoid deadlock from calling Get on them.
		if err := s.checkCircularContainment(id, destID, map[string]*structs.Object{
			id:       obj,
			sourceID: source,
			destID:   dest,
		}); err != nil {
			return err
		}

		if obj.Unsafe.Location != sourceID {
			return errors.Errorf("%q no longer located in %q", id, sourceID)
		}
		if _, found := source.Unsafe.Content[id]; !found {
			return errors.Errorf("%q doesn't contain %q", sourceID, id)
		}
		if _, found := dest.Unsafe.Content[id]; found {
			return errors.Errorf("%q already contains %q", destID, id)
		}
		return juicemud.WithStack(s.objects.Proc([]dbm.LProc[structs.Object, *structs.Object]{
			s.objects.LProc(id, func(_ string, obj *structs.Object) (*structs.Object, error) {
				obj.Unsafe.Location = destID
				return obj, nil
			}),
			s.objects.LProc(sourceID, func(_ string, oldLocation *structs.Object) (*structs.Object, error) {
				delete(oldLocation.Unsafe.Content, id)
				return oldLocation, nil
			}),
			s.objects.LProc(destID, func(_ string, newLocation *structs.Object) (*structs.Object, error) {
				newLocation.Unsafe.Content[id] = true
				return newLocation, nil
			}),
		}))
	}, obj, source, dest))
}

// Loads the objects with the given IDs. If a Refresh is given, it will be run if an
// object source is newer than the last run of that object.
func (s *Storage) LoadObjects(ctx context.Context, ids map[string]bool, ref Refresh) (map[string]*structs.Object, error) {
	res, err := s.objects.GetMulti(ids)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	if ref != nil {
		for _, obj := range res {
			// Continue on refresh errors - the object is still usable with old state.
			// JS errors are also recorded in jsStats by run().
			if err := s.maybeRefresh(ctx, obj, ref); err != nil {
				log.Printf("refresh error for object %s (%s): %v", obj.GetId(), obj.GetSourcePath(), err)
			}
		}
	}
	return res, nil
}

// AccessObject loads the object with the given ID. If a Refresh is given, it will be run if the
// object source is newer than the last run of the object.
// Refresh errors are logged but don't fail the operation - the object is still usable with old state.
// JS errors are also recorded in jsStats by run().
func (s *Storage) AccessObject(ctx context.Context, id string, ref Refresh) (*structs.Object, error) {
	res, err := s.objects.Get(id)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	// Continue on refresh errors - the object is still usable with old state.
	// JS errors are also recorded in jsStats by run().
	if err := s.maybeRefresh(ctx, res, ref); err != nil {
		log.Printf("refresh error for object %s (%s): %v", res.GetId(), res.GetSourcePath(), err)
	}
	return res, nil
}

type Movement struct {
	Object      *structs.Object
	Source      string
	Destination string
}

// UNSAFEEnsureObject creates an object if it doesn't exist, bypassing normal validation.
// Used only during initialization to bootstrap the world.
func (s *Storage) UNSAFEEnsureObject(ctx context.Context, obj *structs.Object) error {
	s.sourceObjectsMu.Lock()
	defer s.sourceObjectsMu.Unlock()

	if err := s.sourceObjects.SubSet(obj.GetSourcePath(), obj.GetId(), nil); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(s.objects.SetIfMissing(obj))
}

type User struct {
	Id           int64  `sqly:"pkey"`
	Name         string `sqly:"unique"`
	PasswordHash string
	Owner        bool
	Wizard       bool
	Object       string
	LastLoginAt  int64 `json:",omitempty" sqly:"index"` // Unix timestamp of last login (0 = never)
}

// LastLogin returns the last login time as a time.Time.
// Returns zero time if user has never logged in.
func (u *User) LastLogin() time.Time {
	if u.LastLoginAt == 0 {
		return time.Time{}
	}
	return time.Unix(u.LastLoginAt, 0).UTC()
}

// SetLastLogin sets the last login time from a time.Time.
func (u *User) SetLastLogin(t time.Time) {
	if t.IsZero() {
		u.LastLoginAt = 0
	} else {
		u.LastLoginAt = t.Unix()
	}
}

type contextKey int

const (
	authenticatedUser contextKey = iota
	sessionIDKey
)

// SessionID retrieves the session ID from context if set.
func SessionID(ctx context.Context) (string, bool) {
	val := ctx.Value(sessionIDKey)
	if val == nil {
		return "", false
	}
	if s, ok := val.(string); ok {
		return s, true
	}
	return "", false
}

// SetSessionID stores a session ID in the context for audit logging.
func SetSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// AuthenticatedUser retrieves the user from context if authenticated.
func AuthenticatedUser(ctx context.Context) (*User, bool) {
	val := ctx.Value(authenticatedUser)
	if val == nil {
		return nil, false
	}
	if u, ok := val.(*User); ok {
		return u, true
	}
	return nil, false
}

// AuthenticateUser stores the user in the context for access control checks.
func AuthenticateUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, authenticatedUser, u)
}


func (s *Storage) LoadUser(ctx context.Context, name string) (*User, error) {
	user := &User{}
	if err := getSQL(ctx, s.sql, user, "SELECT * FROM User WHERE Name = ?", name); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return user, nil
}

// UserSortField specifies which field to sort users by.
type UserSortField int

const (
	UserSortByName         UserSortField = iota // Alphabetical by username
	UserSortByID                                // By database ID (creation order)
	UserSortByLastLogin                         // Most recent login first
	UserSortByLastLoginAsc                      // Least recent login first (stale accounts)
)

// UserFilter specifies which users to include.
type UserFilter int

const (
	UserFilterAll     UserFilter = iota // All users
	UserFilterOwners                    // Only owners
	UserFilterWizards                   // Only wizards (includes owners since owners have Wizard=true)
	UserFilterPlayers                   // Only non-wizards
)

// ListUsers returns users matching the filter, sorted by the specified field, limited to n results.
// If limit <= 0, returns all matching users.
func (s *Storage) ListUsers(ctx context.Context, filter UserFilter, sortBy UserSortField, limit int) ([]User, error) {
	var whereClause string
	switch filter {
	case UserFilterOwners:
		whereClause = "WHERE Owner = 1"
	case UserFilterWizards:
		whereClause = "WHERE Wizard = 1"
	case UserFilterPlayers:
		whereClause = "WHERE Wizard = 0"
	default:
		whereClause = ""
	}

	var orderBy string
	switch sortBy {
	case UserSortByName:
		orderBy = "Name ASC"
	case UserSortByID:
		orderBy = "Id ASC"
	case UserSortByLastLogin:
		orderBy = "LastLoginAt DESC"
	case UserSortByLastLoginAsc:
		orderBy = "LastLoginAt ASC"
	default:
		orderBy = "Name ASC"
	}

	query := fmt.Sprintf("SELECT * FROM User %s ORDER BY %s", whereClause, orderBy)
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	var users []User
	if err := sqlx.SelectContext(ctx, s.sql, &users, query); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return users, nil
}

// CountUsers returns the number of users matching the filter.
func (s *Storage) CountUsers(ctx context.Context, filter UserFilter) (int, error) {
	var whereClause string
	switch filter {
	case UserFilterOwners:
		whereClause = "WHERE Owner = 1"
	case UserFilterWizards:
		whereClause = "WHERE Wizard = 1"
	case UserFilterPlayers:
		whereClause = "WHERE Wizard = 0"
	default:
		whereClause = ""
	}

	query := fmt.Sprintf("SELECT COUNT(*) FROM User %s", whereClause)
	var count int
	if err := sqlx.GetContext(ctx, s.sql, &count, query); err != nil {
		return 0, juicemud.WithStack(err)
	}
	return count, nil
}

// GetMostRecentLogin returns the user with the most recent login time.
// Returns nil if no users have logged in yet.
func (s *Storage) GetMostRecentLogin(ctx context.Context) (*User, error) {
	user := &User{}
	err := getSQL(ctx, s.sql, user, "SELECT * FROM User WHERE LastLoginAt > 0 ORDER BY LastLoginAt DESC LIMIT 1")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return user, nil
}

func (s *Storage) StoreUser(ctx context.Context, user *User, overwrite bool, remote string) error {
	if err := s.sql.Upsert(ctx, user, overwrite); err != nil {
		return juicemud.WithStack(err)
	}
	if !overwrite {
		s.audit.Log(ctx, "USER_CREATE", AuditUserCreate{
			User:   Ref(user.Id, user.Name),
			Remote: remote,
		})
	}
	return nil
}

// DeleteUser removes a user from the database.
// Only owners can delete users. Cannot delete owners or yourself.
// Returns the deleted user so caller can clean up related resources (game object, connection).
func (s *Storage) DeleteUser(ctx context.Context, username string) (*User, error) {
	caller, ok := AuthenticatedUser(ctx)
	if !ok || !caller.Owner {
		return nil, errors.New("only owners can delete users")
	}
	user, err := s.LoadUser(ctx, username)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	if user.Owner {
		return nil, errors.New("cannot delete an owner")
	}
	if user.Id == caller.Id {
		return nil, errors.New("cannot delete yourself")
	}
	if _, err := s.sql.ExecContext(ctx, "DELETE FROM User WHERE Id = ?", user.Id); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return user, nil
}

// SetUserWizard sets the Wizard flag for a user.
// Only owners can modify wizard status.
func (s *Storage) SetUserWizard(ctx context.Context, username string, wizard bool) error {
	caller, ok := AuthenticatedUser(ctx)
	if !ok || !caller.Owner {
		return errors.New("only owners can modify wizard status")
	}
	user, err := s.LoadUser(ctx, username)
	if err != nil {
		return juicemud.WithStack(err)
	}
	user.Wizard = wizard
	if err := s.sql.Upsert(ctx, user, true); err != nil {
		return juicemud.WithStack(err)
	}
	// Audit log the change
	targetRef := Ref(user.Id, user.Name)
	callerRef := Ref(caller.Id, caller.Name)
	if wizard {
		s.audit.Log(ctx, "WIZARD_GRANT", AuditWizardGrant{
			Target:    targetRef,
			GrantedBy: callerRef,
		})
	} else {
		s.audit.Log(ctx, "WIZARD_REVOKE", AuditWizardRevoke{
			Target:    targetRef,
			RevokedBy: callerRef,
		})
	}
	return nil
}

func (s *Storage) EachObject(_ context.Context) iter.Seq2[*structs.Object, error] {
	return s.objects.Each()
}

// MissingSource describes a source file that is missing and the objects that reference it.
type MissingSource struct {
	Path      string
	ObjectIDs []string
}

// ValidateSources checks that all object source paths exist in the given root directory.
// Returns nil if all sources exist, or a list of missing sources with their affected objects.
func (s *Storage) ValidateSources(ctx context.Context, rootDir string) ([]MissingSource, error) {
	// Collect all source paths and their objects
	sourceToObjects := make(map[string][]string)
	for obj, err := range s.EachObject(ctx) {
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
		sourcePath := obj.GetSourcePath()
		if sourcePath != "" {
			sourceToObjects[sourcePath] = append(sourceToObjects[sourcePath], obj.GetId())
		}
	}

	// Check each source path exists
	var missing []MissingSource
	for sourcePath, objectIDs := range sourceToObjects {
		fullPath := filepath.Join(rootDir, sourcePath)
		if _, err := os.Stat(fullPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				missing = append(missing, MissingSource{
					Path:      sourcePath,
					ObjectIDs: objectIDs,
				})
			} else {
				return nil, juicemud.WithStack(err)
			}
		}
	}

	return missing, nil
}

// SetSourcesDir atomically updates the sources directory.
// Prefer ValidateAndSwitchSources for external use to avoid TOCTOU races.
func (s *Storage) SetSourcesDir(dir string) {
	s.sourceObjectsMu.Lock()
	defer s.sourceObjectsMu.Unlock()
	s.sourcesDir = dir
}

// ValidateAndSwitchSources validates that all source files exist in the new directory
// and atomically switches to it. The validation and switch are done under the same lock
// to prevent TOCTOU races between checking and switching.
// Returns missing sources if validation fails, or nil on successful switch.
//
// Note: This function performs filesystem I/O (os.Stat) while holding locks.
// The sources directory should be on fast local storage to avoid contention.
func (s *Storage) ValidateAndSwitchSources(ctx context.Context, newDir string) ([]MissingSource, error) {
	s.sourceObjectsMu.Lock()
	defer s.sourceObjectsMu.Unlock()

	// Iterate unique source paths using the sourceObjects index
	var missing []MissingSource
	for sourcePath, err := range s.sourceObjects.EachSet() {
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
		fullPath := filepath.Join(newDir, sourcePath)
		if _, err := os.Stat(fullPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Collect object IDs that use this source
				var objectIDs []string
				for entry, err := range s.sourceObjects.SubEach(sourcePath) {
					if err != nil {
						return nil, juicemud.WithStack(err)
					}
					objectIDs = append(objectIDs, entry.K)
				}
				missing = append(missing, MissingSource{
					Path:      sourcePath,
					ObjectIDs: objectIDs,
				})
			} else {
				return nil, juicemud.WithStack(err)
			}
		}
	}

	// Only switch if validation passed
	if len(missing) == 0 {
		s.sourcesDir = newDir
	}

	return missing, nil
}

// ResolveSourcePath resolves symlinks in a source path and returns the real path.
// If the path is relative, it's joined with baseDir first.
func ResolveSourcePath(baseDir, sourcePath string) (string, error) {
	var fullPath string
	if filepath.IsAbs(sourcePath) {
		fullPath = sourcePath
	} else {
		fullPath = filepath.Join(baseDir, sourcePath)
	}

	resolved, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return "", juicemud.WithStack(err)
	}

	return resolved, nil
}
