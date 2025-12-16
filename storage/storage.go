package storage

import (
	"context"
	"database/sql"
	"iter"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
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
		queue:         queue.New(ctx, queueTree),
		audit:         audit,
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
//  2. Object locks (via structs.WithLock, sorted by ID)
//  3. LiveTypeHash.stageMutex
//  4. Tree.mutex / Hash.mutex (tkrzw internal)
type Storage struct {
	queue           *queue.Queue
	sql             *sqly.DB
	sourcesDir      string
	sourceObjects   *dbm.Tree
	sourceObjectsMu sync.RWMutex // Protects sourceObjects operations
	objects         *dbm.LiveTypeHash[structs.Object, *structs.Object]
	audit           *AuditLogger
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
	if err := s.audit.Close(); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(s.objects.Close())
}

// SourcesDir returns the directory where source files are stored.
func (s *Storage) SourcesDir() string {
	return s.sourcesDir
}

func (s *Storage) Queue() *queue.Queue {
	return s.queue
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
		return errors.Wrapf(err, "Executing %q(%+v):", query, params)
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

	return juicemud.WithStack(s.sourceObjects.SubDel(sourcePath, id))
}

func (s *Storage) CreateObject(ctx context.Context, obj *structs.Object) error {
	if obj.PostUnlock != nil {
		return errors.Errorf("can't create object already known to storage: %+v", obj)
	}

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

type withError[T any] struct {
	T T
	E error
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

func (s *Storage) MoveObject(ctx context.Context, obj *structs.Object, destID string) error {
	if obj.PostUnlock == nil {
		return errors.Errorf("can't move object unknown to storage: %+v", obj)
	}

	id := obj.GetId()
	sourceID := obj.GetLocation()

	source, err := s.objects.Get(sourceID)
	if err != nil {
		return juicemud.WithStack(err)
	}

	dest, err := s.objects.Get(destID)
	if err != nil {
		return juicemud.WithStack(err)
	}

	return juicemud.WithStack(structs.WithLock(func() error {
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
			if err := s.maybeRefresh(ctx, obj, ref); err != nil {
				return nil, juicemud.WithStack(err)
			}
		}
	}
	return res, nil
}

// AccessObject loads the object with the given ID. If a Refresh is given, it will be run if the
// object source is newer than the last run of the object.
func (s *Storage) AccessObject(ctx context.Context, id string, ref Refresh) (*structs.Object, error) {
	res, err := s.objects.Get(id)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	if err := s.maybeRefresh(ctx, res, ref); err != nil {
		return nil, juicemud.WithStack(err)
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

// callerRef returns an AuditRef for the authenticated user, or SystemRef if none.
func (s *Storage) callerRef(ctx context.Context) AuditRef {
	if caller, ok := AuthenticatedUser(ctx); ok {
		return Ref(caller.Id, caller.Name)
	}
	return SystemRef()
}

func (s *Storage) LoadUser(ctx context.Context, name string) (*User, error) {
	user := &User{}
	if err := getSQL(ctx, s.sql, user, "SELECT * FROM User WHERE Name = ?", name); err != nil {
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
// This should only be called after ValidateSources confirms the new directory is valid.
func (s *Storage) SetSourcesDir(dir string) {
	s.sourceObjectsMu.Lock()
	defer s.sourceObjectsMu.Unlock()
	s.sourcesDir = dir
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
