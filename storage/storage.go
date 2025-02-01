package storage

// TODO(zond): Implement read/write group access restrictions.

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/digest"
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
	sources, err := dbm.OpenHash(filepath.Join(dir, "source"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	objects, err := dbm.OpenTypeHash[structs.Object](filepath.Join(dir, "objects"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	queueTree, err := dbm.OpenTree(filepath.Join(dir, "queue"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	modTimes, err := dbm.OpenHash(filepath.Join(dir, "modTimes"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	s := &Storage{
		sql:      sql,
		sources:  sources,
		modTimes: modTimes,
		objects:  objects,
		queue:    queue.New(ctx, queueTree),
	}
	for _, prototype := range []any{File{}, FileSync{}, Group{}, User{}, GroupMember{}} {
		if err := sql.CreateTableIfNotExists(ctx, prototype); err != nil {
			return nil, err
		}
	}
	return s, nil
}

type Storage struct {
	queue           *queue.Queue
	sql             *sqly.DB
	sources         dbm.Hash
	modTimes        dbm.Hash
	objects         dbm.TypeHash[structs.Object, *structs.Object]
	movementHandler MovementHandler
}

func (s *Storage) Queue() *queue.Queue {
	return s.queue
}

type EventHandler func(context.Context, *structs.Event)

type MovementHandler func(context.Context, *Movement) error

func (s *Storage) StartQueue(ctx context.Context, eventHandler EventHandler, movementHandler MovementHandler) error {
	s.movementHandler = movementHandler
	return juicemud.WithStack(s.queue.Start(ctx, eventHandler))
}

func getSQL(ctx context.Context, db sqlx.QueryerContext, d any, sql string, params ...any) error {
	if err := sqlx.GetContext(ctx, db, d, sql, params...); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return juicemud.WithStack(os.ErrNotExist)
		}
		return errors.Wrapf(err, "Executing %q(%+v):", sql, params)
	}
	return nil
}

func (s *Storage) LoadSource(ctx context.Context, path string) ([]byte, int64, error) {
	value, err := s.sources.Get(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, 0, nil
	} else if err != nil {
		return nil, 0, juicemud.WithStack(err)
	}
	t, err := s.SourceModTime(ctx, path)
	if err != nil {
		return nil, 0, juicemud.WithStack(err)
	}
	return value, t, nil
}

func (s *Storage) StoreSource(ctx context.Context, path string, content []byte) error {
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		file, err := s.loadFile(ctx, tx, path)
		if err != nil {
			return juicemud.WithStack(err)
		}
		return juicemud.WithStack(logSync(ctx, tx, &FileSync{
			Set:     file.Path,
			Content: content,
			ModTime: time.Now().UnixNano(),
		}))
	}); err != nil {
		return juicemud.WithStack(err)
	}
	return s.sync(ctx)
}

type Refresh func(ctx context.Context, object *structs.Object) error

func (s *Storage) maybeRefresh(ctx context.Context, obj *structs.Object, ref Refresh) error {
	if ref != nil {
		t, err := s.SourceModTime(ctx, obj.SourcePath)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if t > obj.SourceModTime {
			oldLoc := obj.Location
			if err := ref(ctx, obj); err != nil {
				return juicemud.WithStack(err)
			}
			obj.SourceModTime = t
			if err := s.StoreObject(ctx, &oldLoc, obj); err != nil {
				return juicemud.WithStack(err)
			}
		}
	}
	return nil
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

// Loads the object with the given ID. If a Refresh is given, it will be run if the
// object source is newer than the last run of the object.
func (s *Storage) LoadObject(ctx context.Context, id string, ref Refresh) (*structs.Object, error) {
	res, err := s.objects.Get(id)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	if err := s.maybeRefresh(ctx, res, ref); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return res, nil
}

func (s *Storage) EnsureObject(ctx context.Context, id string, setup func(*structs.Object) error) error {
	return juicemud.WithStack(s.objects.Proc([]dbm.Proc{
		s.objects.SProc(id, func(k string, v *structs.Object) (*structs.Object, error) {
			if v != nil {
				return v, nil
			}
			object := &structs.Object{Id: id}
			if err := setup(object); err != nil {
				return nil, juicemud.WithStack(err)
			}
			return object, nil
		}),
	}, true))
}

type Movement struct {
	Object      *structs.Object
	Source      string
	Destination string
}

func (s *Storage) StoreObject(ctx context.Context, claimedOldLocation *string, object *structs.Object) error {
	var m *Movement
	var pairs []dbm.Proc
	if claimedOldLocation == nil || *claimedOldLocation == object.Location {
		if object.Location == "" {
			pairs = []dbm.Proc{
				s.objects.SProc(object.Id, func(key string, value *structs.Object) (*structs.Object, error) {
					if value == nil {
						return object, nil
					}
					if value.Location != object.Location {
						return nil, errors.Errorf("object is moved from %q to %q without updating old location", value.Location, object.Location)
					}
					return object, nil
				}),
			}
		} else {
			pairs = []dbm.Proc{
				s.objects.SProc(object.Location, func(key string, value *structs.Object) (*structs.Object, error) {
					if value == nil {
						return nil, errors.Wrapf(os.ErrNotExist, "can't find location %q", object.Location)
					}
					value.Content[object.Id] = true
					return value, nil
				}),
				s.objects.SProc(object.Id, func(key string, value *structs.Object) (*structs.Object, error) {
					if value == nil {
						return object, nil
					}
					if value.Location != object.Location {
						return nil, errors.Errorf("object is moved from %q to %q without updating old location", value.Location, object.Location)
					}
					return object, nil
				}),
			}
		}
	} else {
		m = &Movement{
			Object:      object,
			Source:      *claimedOldLocation,
			Destination: object.Location,
		}
		// Loc is changed, verify that the old one is what's there right now, that obj can
		// be removed from old loc, and added to new loc, before all are saved.
		pairs = []dbm.Proc{
			s.objects.SProc(object.Id, func(key string, value *structs.Object) (*structs.Object, error) {
				if value == nil {
					return nil, errors.Errorf("can't find old version of %q", object.Id)
				}
				if value.Location != *claimedOldLocation {
					return nil, errors.Errorf("object in %q claims to move from %q to %q", value.Location, *claimedOldLocation, object.Location)
				}
				return object, nil
			}),
			s.objects.SProc(object.Location, func(key string, value *structs.Object) (*structs.Object, error) {
				if value == nil {
					return nil, errors.Errorf("can't find new location %q", object.Location)
				}
				value.Content[object.Id] = true
				return value, nil
			}),
			s.objects.SProc(*claimedOldLocation, func(key string, value *structs.Object) (*structs.Object, error) {
				if value == nil {
					return nil, errors.Errorf("can't find old location %q", object.Location)
				}
				if _, found := value.Content[object.Id]; !found {
					return nil, errors.Errorf("object claimed to be contained by %q, but wasn't", *claimedOldLocation)
				}
				delete(value.Content, object.Id)
				return value, nil
			}),
		}
	}
	if err := s.objects.Proc(pairs, true); err != nil {
		return juicemud.WithStack(err)
	}
	if m != nil {
		if err := s.movementHandler(ctx, m); err != nil {
			return juicemud.WithStack(err)
		}
	}
	return nil
}

type FileSync struct {
	Id      int64 `sqly:"pkey,autoinc"`
	Remove  string
	Set     string
	Content []byte
	ModTime int64
}

func logSync(ctx context.Context, db sqlx.ExtContext, fileSync *FileSync) error {
	if fileSync.Remove == "" && fileSync.Set == "" {
		return errors.Errorf("invalid FileSync %+v: Remove == \"\" and Set == \"\"", fileSync)
	}
	if fileSync.Remove != "" && fileSync.Set != "" {
		return errors.Errorf("invalid FileSync %+v: Remove != \"\" and Set != \"\"", fileSync)
	}
	if fileSync.Set != "" && fileSync.ModTime == 0 {
		return errors.Errorf("invalid FileSync %+v: Set != \"\" and ModTime == 0", fileSync)
	}
	if fileSync.Id != 0 {
		return errors.Errorf("invalid FileSync %+v: Id != 0", fileSync)
	}
	return sqly.Upsert(ctx, db, fileSync, false)
}

func (s *Storage) SourceModTime(_ context.Context, path string) (int64, error) {
	b, err := s.modTimes.Get(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, juicemud.WithStack(err)
	}
	return int64(binary.BigEndian.Uint64(b)), nil
}

func (s *Storage) runSync(_ context.Context, fileSync *FileSync) error {
	if fileSync.Remove != "" {
		if err := s.sources.Del(fileSync.Remove); err != nil && !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}
		if err := s.modTimes.Del(fileSync.Remove); err != nil && !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}
	} else if fileSync.Set != "" {
		t := uint64(fileSync.ModTime)
		b := make([]byte, binary.Size(t))
		binary.BigEndian.PutUint64(b, t)
		if err := s.modTimes.Set(fileSync.Set, b, true); err != nil {
			return juicemud.WithStack(err)
		}
		if err := s.sources.Set(fileSync.Set, fileSync.Content, true); err != nil {
			return juicemud.WithStack(err)
		}
	}
	return nil
}

func (s *Storage) sync(ctx context.Context) error {
	getOldestSync := func() (*FileSync, error) {
		result := &FileSync{}
		if err := getSQL(ctx, s.sql, result, "SELECT * FROM FileSync ORDER BY Id ASC LIMIT 1"); errors.Is(err, os.ErrNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, juicemud.WithStack(err)
		}
		return result, nil
	}
	oldestSync, err := getOldestSync()
	for ; err == nil && oldestSync != nil; oldestSync, err = getOldestSync() {
		if err := s.runSync(ctx, oldestSync); err != nil {
			return juicemud.WithStack(err)
		}
		if _, err := s.sql.ExecContext(ctx, "DELETE FROM FileSync WHERE Id = ?", oldestSync.Id); err != nil && errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}
	}
	return nil
}

type File struct {
	Id         int64 `sqly:"pkey"`
	Parent     int64 `sqly:"uniqueWith(Name)"`
	Name       string
	Path       string `sqly:"unique"`
	Dir        bool
	ReadGroup  int64
	WriteGroup int64
}

func (s *Storage) EnsureFile(ctx context.Context, path string) (file *File, created bool, err error) {
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		file, err = s.loadFile(ctx, tx, path)
		if err == nil {
			created = false
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}
		parent, err := s.loadFile(ctx, tx, filepath.Dir(path))
		if err != nil {
			return juicemud.WithStack(err)
		}
		file = &File{
			Parent:     parent.Id,
			Path:       path,
			Name:       filepath.Base(path),
			Dir:        false,
			ReadGroup:  parent.ReadGroup,
			WriteGroup: parent.WriteGroup,
		}
		if err := tx.Upsert(ctx, file, true); err != nil {
			return juicemud.WithStack(err)
		}
		created = true
		return nil
	}); err != nil {
		return nil, false, juicemud.WithStack(err)
	}
	return file, created, nil
}

func (s *Storage) MoveFile(ctx context.Context, oldPath string, newPath string) error {
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		toMove, err := s.loadFile(ctx, tx, oldPath)
		if err != nil {
			return juicemud.WithStack(err)
		}
		content, modTime, err := s.LoadSource(ctx, oldPath)
		if err != nil {
			return juicemud.WithStack(err)
		}
		var newParent *File
		if newParentPath := filepath.Dir(newPath); newParentPath == "/" {
			newParent = &File{
				Dir: true,
			}
		} else {
			newParent, err = s.loadFile(ctx, tx, filepath.Dir(newPath))
			if err != nil {
				return juicemud.WithStack(err)
			}
		}
		if err := s.delFileIfExists(ctx, tx, newPath, false); err != nil {
			return juicemud.WithStack(err)
		}
		toMove.Parent = newParent.Id
		toMove.Path = newPath
		toMove.Name = filepath.Base(newPath)
		if err := tx.Upsert(ctx, toMove, true); err != nil {
			return juicemud.WithStack(err)
		}
		if err := logSync(ctx, tx, &FileSync{
			Remove: oldPath,
		}); err != nil {
			return juicemud.WithStack(err)
		}
		return juicemud.WithStack(logSync(ctx, tx, &FileSync{
			Set:     newPath,
			Content: content,
			ModTime: modTime,
		}))
	}); err != nil {
		return juicemud.WithStack(err)
	}
	return s.sync(ctx)
}

func getChildren(ctx context.Context, db sqlx.QueryerContext, parent int64) ([]File, error) {
	result := []File{}
	if err := sqlx.SelectContext(ctx, db, &result, "SELECT * FROM File WHERE Parent = ?", parent); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return result, nil
}

func (s *Storage) LoadChildren(ctx context.Context, parent int64) ([]File, error) {
	return getChildren(ctx, s.sql, parent)
}

func (s *Storage) loadFile(ctx context.Context, db sqlx.QueryerContext, path string) (*File, error) {
	if path == "/" {
		return &File{
			Dir:  true,
			Path: "/",
		}, nil
	}
	file := &File{}
	if err := getSQL(ctx, db, file, "SELECT * FROM File WHERE Path = ?", path); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return file, nil
}

func (s *Storage) LoadFile(ctx context.Context, path string) (*File, error) {
	return s.loadFile(ctx, s.sql, path)
}

func (s *Storage) delFileIfExists(ctx context.Context, db sqlx.ExtContext, path string, recursive bool) error {
	file, err := s.loadFile(ctx, db, path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	children, err := getChildren(ctx, db, file.Id)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if recursive {
		for _, child := range children {
			if err := s.delFileIfExists(ctx, db, child.Path, true); err != nil {
				return juicemud.WithStack(err)
			}
		}
	} else {
		if len(children) > 0 {
			return errors.Errorf("%q is not empty", path)
		}
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM File WHERE Id = ?", file.Id); err != nil {
		return juicemud.WithStack(err)
	}
	if err := logSync(ctx, db, &FileSync{
		Remove: path,
	}); err != nil {
		return juicemud.WithStack(err)
	}
	return nil
}

func (s *Storage) DelFile(ctx context.Context, path string) error {
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		if err := s.delFileIfExists(ctx, tx, path, true); err != nil {
			return juicemud.WithStack(err)
		}
		return nil
	}); err != nil {
		return juicemud.WithStack(err)
	}
	return s.sync(ctx)
}

func (s *Storage) CreateDir(ctx context.Context, path string) error {
	return s.sql.Write(ctx, func(tx *sqly.Tx) error {
		if _, err := s.loadFile(ctx, tx, path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}
		parent, err := s.loadFile(ctx, tx, filepath.Dir(path))
		if err != nil {
			return juicemud.WithStack(err)
		}
		file := &File{
			Path:       path,
			Name:       filepath.Base(path),
			Dir:        true,
			ReadGroup:  parent.ReadGroup,
			WriteGroup: parent.WriteGroup,
		}
		if err := tx.Upsert(ctx, file, true); err != nil {
			return juicemud.WithStack(err)
		}
		return nil
	})
}

type Group struct {
	Id         int64  `sqly:"pkey"`
	Name       string `sqly:"unique"`
	OwnerGroup int64
}

type Groups []Group

func (g Groups) Len() int {
	return len(g)
}

func (g Groups) Swap(i, j int) {
	g[i], g[j] = g[j], g[i]
}

func (g Groups) Less(i, j int) bool {
	return strings.Compare(g[i].Name, g[j].Name) < 1
}

func (s *Storage) EnsureGroup(ctx context.Context, group *Group) (created bool, err error) {
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		found := &Group{}
		if err := getSQL(ctx, tx, found, "SELECT * FROM `Group` WHERE Name = ?", group.Name); err == nil {
			if found.OwnerGroup != group.OwnerGroup {
				return errors.Errorf("%+v exists, but it doesn't have owner %v", found, group.OwnerGroup)
			}
			created = false
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}
		if err := tx.Upsert(ctx, group, true); err != nil {
			return juicemud.WithStack(err)
		}
		created = true
		return nil
	}); err != nil {
		return false, juicemud.WithStack(err)
	}
	return created, nil
}

type User struct {
	Id           int64  `sqly:"pkey"`
	Name         string `sqly:"unique"`
	PasswordHash string
	Owner        bool
	Object       string
}

func (s *Storage) LoadUser(ctx context.Context, name string) (*User, error) {
	user := &User{}
	if err := getSQL(ctx, s.sql, user, "SELECT * FROM User WHERE Name = ?", name); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return user, nil
}

func (s *Storage) UserGroups(ctx context.Context, user *User) (Groups, error) {
	members := []GroupMember{}
	if err := s.sql.SelectContext(ctx, &members, "SELECT * FROM GroupMember WHERE User = ?", user.Id); err != nil {
		return nil, juicemud.WithStack(err)
	}
	ids := map[int64]bool{}
	for _, member := range members {
		ids[member.Group] = true
	}
	result := make(Groups, 0, len(ids))
	for id := range ids {
		group := Group{}
		if err := getSQL(ctx, s.sql, &group, "SELECT * FROM `Group` WHERE Id = ?", id); err != nil {
			return nil, juicemud.WithStack(err)
		}
		result = append(result, group)
	}
	return result, nil
}

func (s *Storage) StoreUser(ctx context.Context, user *User, overwrite bool) error {
	return s.sql.Upsert(ctx, user, overwrite)
}

func (s *Storage) UserAccessToGroup(ctx context.Context, user *User, groupName string) (bool, error) {
	if user.Owner {
		return true, nil
	}
	g := &Group{}
	if err := getSQL(ctx, s.sql, g, "SELECT * FROM `Group` WHERE Name = ?", groupName); err != nil {
		return false, juicemud.WithStack(err)
	}
	return s.UserAccessToGroupID(ctx, user, g.Id)
}

func (s *Storage) CheckCallerAccessToGroupID(ctx context.Context, groupID int64) error {
	if has, err := s.CallerAccessToGroupID(ctx, groupID); err != nil {
		return juicemud.WithStack(err)
	} else if !has {
		return errors.Errorf("not member of group %v", groupID)
	}
	return nil
}

func (s *Storage) CallerAccessToGroupID(ctx context.Context, groupID int64) (bool, error) {
	if juicemud.IsMainContext(ctx) {
		return true, nil
	}
	userIf, found := digest.AuthenticatedUser(ctx)
	if !found {
		return false, nil
	}
	user, ok := userIf.(*User)
	if !ok {
		return false, errors.Errorf("context user %v is not *User", userIf)
	}
	return s.UserAccessToGroupID(ctx, user, groupID)
}

func (s *Storage) UserAccessToGroupID(ctx context.Context, user *User, groupID int64) (bool, error) {
	if user.Owner {
		return true, nil
	}
	m := &GroupMember{}
	if err := getSQL(ctx, s.sql, m, "SELECT * FROM GroupMember WHERE User = ? AND `Group` = ?", user.Id, groupID); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, juicemud.WithStack(err)
	}
	return true, nil
}

func (s *Storage) GetHA1AndUser(ctx context.Context, username string) (string, bool, any, error) {
	user := &User{}
	if err := getSQL(ctx, s.sql, user, "SELECT * FROM User WHERE Name = ?", username); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil, nil
		}
		return "", false, nil, juicemud.WithStack(err)
	}
	return user.PasswordHash, true, user, nil
}

type GroupMember struct {
	Id    int64 `sqly:"pkey"`
	User  int64 `sqly:"index"`
	Group int64 `sqly:"uniqueWith(User)"`
}
