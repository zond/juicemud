//go:generate sh -c "capnp compile -I `go list -m -f '{{.Dir}}' capnproto.org/go/capnp/v3`/std -ogo object.capnp"
package storage

// TODO(zond): Implement read/write group access restrictions.

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/digest"
	"github.com/zond/juicemud/storage/dbm"
	"github.com/zond/juicemud/structs"
	"github.com/zond/sqly"

	goccy "github.com/goccy/go-json"
	_ "modernc.org/sqlite"
)

func New(ctx context.Context, dir string) (*Storage, error) {
	sql, err := sqly.Open("sqlite", filepath.Join(dir, "sqlite.db"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	o := dbm.Opener{Dir: dir}
	s := &Storage{
		sql:     sql,
		sources: o.Hash("sources"),
		objects: o.Hash("objects"),
		queue:   o.Tree("queue"),
	}
	if o.Err != nil {
		return nil, juicemud.WithStack(o.Err)
	}
	for _, prototype := range []any{File{}, FileSync{}, Group{}, User{}, GroupMember{}} {
		if err := sql.CreateTableIfNotExists(ctx, prototype); err != nil {
			return nil, err
		}
	}
	return s, nil
}

type Storage struct {
	sql     *sqly.DB
	sources dbm.Hash
	objects dbm.Hash
	queue   dbm.Tree
}

func (s *Storage) Queue(ctx context.Context, fun func(context.Context, *Event)) *Queue {
	return NewQueue(ctx, s.queue, fun)
}

func getSQL(ctx context.Context, db sqlx.QueryerContext, d any, sql string, params ...any) error {
	if err := sqlx.GetContext(ctx, db, d, sql, params...); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return juicemud.WithStack(os.ErrNotExist)
		}
		return juicemud.WithStack(err)
	}
	return nil
}

func (s *Storage) GetSource(ctx context.Context, path string) ([]byte, error) {
	value, err := s.sources.Get(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, nil
	} else if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return value, nil
}

func (s *Storage) SetSource(ctx context.Context, path string, content []byte) error {
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		file, err := getFile(ctx, tx, path)
		if err != nil {
			return juicemud.WithStack(err)
		}
		return juicemud.WithStack(logSync(ctx, tx, &FileSync{
			Set:     file.Path,
			Content: content,
		}))
	}); err != nil {
		return juicemud.WithStack(err)
	}
	return s.sync(ctx)
}

func (s *Storage) GetObjects(ctx context.Context, ids map[string]bool) (map[string]*structs.Object, error) {
	return dbm.GetJSONMulti[structs.Object](s.objects, ids)
}

func (s *Storage) GetObject(ctx context.Context, id string) (*structs.Object, error) {
	res := &structs.Object{}
	if err := s.objects.GetJSON(id, res); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return res, nil
}

func (s *Storage) EnsureObject(ctx context.Context, id string, setup func(*structs.Object) error) error {
	return juicemud.WithStack(s.objects.Proc([]dbm.Proc{
		dbm.JProc[structs.Object]{
			K: id,
			F: func(k string, v *structs.Object) (*structs.Object, error) {
				if v != nil {
					return v, nil
				}
				object := &structs.Object{Id: id}
				if err := setup(object); err != nil {
					return nil, juicemud.WithStack(err)
				}
				return object, nil
			},
		},
	}, true))
}

func (s *Storage) SetObject(ctx context.Context, claimedOldLocation *string, object *structs.Object) error {
	var pairs []dbm.Proc
	if claimedOldLocation == nil || *claimedOldLocation == object.Location {
		pairs = []dbm.Proc{
			dbm.JProc[structs.Object]{
				// Make sure location exists and has object.
				K: object.Location,
				F: func(key string, value *structs.Object) (*structs.Object, error) {
					if value == nil {
						return nil, errors.Wrapf(os.ErrNotExist, "can't find location %q", object.Location)
					}
					value.Content[object.Id] = true
					return value, nil
				},
			},
			dbm.JProc[structs.Object]{
				// Make sure object didn't exist, or was where it claimed to be.
				K: object.Id,
				F: func(key string, value *structs.Object) (*structs.Object, error) {
					if value == nil {
						return value, nil
					}
					if value.Location != object.Location {
						return nil, errors.Errorf("object is moved from %q to %q without updating old location", value.Location, object.Location)
					}
					return value, nil
				},
			},
		}
	} else {
		// Loc is changed, verify that the old one is what's there right now, that obj can
		// be removed from old loc, and added to new loc, before all are saved.
		pairs = []dbm.Proc{
			// Make sure object exists and is where it claimed to be.
			dbm.JProc[structs.Object]{
				K: object.Id,
				F: func(key string, value *structs.Object) (*structs.Object, error) {
					if value == nil {
						return nil, errors.Errorf("can't find old version of %q", object.Id)
					}
					if value.Location != object.Location {
						return nil, errors.Errorf("object in %q claims to move from %q to %q", value.Location, *claimedOldLocation, object.Location)
					}
					return value, nil
				},
			},
			dbm.JProc[structs.Object]{
				// Make sure new location exists and contains object.
				K: object.Location,
				F: func(key string, value *structs.Object) (*structs.Object, error) {
					if value == nil {
						return nil, errors.Errorf("can't find new location %q", object.Location)
					}
					value.Content[object.Id] = true
					return value, nil
				},
			},
			dbm.JProc[structs.Object]{
				// Make sure old location exists and contained object (but no longer).
				K: *claimedOldLocation,
				F: func(key string, value *structs.Object) (*structs.Object, error) {
					if value == nil {
						return nil, errors.Errorf("can't find old location %q", object.Location)
					}
					if _, found := value.Content[object.Id]; !found {
						return nil, errors.Errorf("object claimed to be contained by %q, but wasn't", *claimedOldLocation)
					}
					delete(value.Content, object.Id)
					return value, nil
				},
			},
		}
	}
	return juicemud.WithStack(s.objects.Proc(pairs, true))
}

type FileSync struct {
	Id      int64 `sqly:"pkey,autoinc"`
	Remove  string
	Set     string
	Content []byte
}

func logSync(ctx context.Context, db sqlx.ExtContext, fileSync *FileSync) error {
	if fileSync.Remove == "" && fileSync.Set == "" {
		return errors.Errorf("invalid FileSync %+v: Remove == \"\" and Set == \"\"", fileSync)
	}
	if fileSync.Remove != "" && fileSync.Set != "" {
		return errors.Errorf("invalid FileSync %+v: Remove != \"\" and Set != \"\"", fileSync)
	}
	if fileSync.Id != 0 {
		return errors.Errorf("invalid FileSync %+v: Id != 0", fileSync)
	}
	return sqly.Upsert(ctx, db, fileSync, false)
}

func (s *Storage) runSync(_ context.Context, fileSync *FileSync) error {
	if fileSync.Remove != "" {
		if err := s.sources.Del(fileSync.Remove); err != nil && !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}
	} else if fileSync.Set != "" {
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
	ModTime    sqly.SQLTime
	Dir        bool
	ReadGroup  int64
	WriteGroup int64
}

func (s *Storage) EnsureFile(ctx context.Context, path string) (file *File, created bool, err error) {
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		file, err = getFile(ctx, tx, path)
		if err == nil {
			created = false
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}
		parent, err := getFile(ctx, tx, filepath.Dir(path))
		if err != nil {
			return juicemud.WithStack(err)
		}
		file = &File{
			Parent:     parent.Id,
			Path:       path,
			Name:       filepath.Base(path),
			ModTime:    sqly.ToSQLTime(time.Now()),
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
		toMove, err := getFile(ctx, tx, oldPath)
		if err != nil {
			return juicemud.WithStack(err)
		}
		content, err := s.GetSource(ctx, oldPath)
		if err != nil {
			return juicemud.WithStack(err)
		}
		var newParent *File
		if newParentPath := filepath.Dir(newPath); newParentPath == "/" {
			newParent = &File{
				Dir: true,
			}
		} else {
			newParent, err = getFile(ctx, tx, filepath.Dir(newPath))
			if err != nil {
				return juicemud.WithStack(err)
			}
		}
		if err := delFileIfExists(ctx, tx, newPath, false); err != nil {
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

func (s *Storage) GetChildren(ctx context.Context, parent int64) ([]File, error) {
	return getChildren(ctx, s.sql, parent)
}

func getFile(ctx context.Context, db sqlx.QueryerContext, path string) (*File, error) {
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

func (s *Storage) GetFile(ctx context.Context, path string) (*File, error) {
	return getFile(ctx, s.sql, path)
}

func delFileIfExists(ctx context.Context, db sqlx.ExtContext, path string, recursive bool) error {
	file, err := getFile(ctx, db, path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	children, err := getChildren(ctx, db, file.Id)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if recursive {
		for _, child := range children {
			if err := delFileIfExists(ctx, db, child.Path, true); err != nil {
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
		if err := delFileIfExists(ctx, tx, path, true); err != nil {
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
		if _, err := getFile(ctx, tx, path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}
		parent, err := getFile(ctx, tx, filepath.Dir(path))
		if err != nil {
			return juicemud.WithStack(err)
		}
		file := &File{
			Path:       path,
			Name:       filepath.Base(path),
			ModTime:    sqly.ToSQLTime(time.Now()),
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

type User struct {
	Id           int64  `sqly:"pkey"`
	Name         string `sqly:"unique"`
	PasswordHash string
	Owner        bool
	Object       string
}

func (s *Storage) GetUser(ctx context.Context, name string) (*User, error) {
	user := &User{}
	if err := getSQL(ctx, s.sql, user, "SELECT * FROM User WHERE Name = ?", name); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return user, nil
}

func (s *Storage) SetUser(ctx context.Context, user *User, overwrite bool) error {
	return s.sql.Upsert(ctx, user, overwrite)
}

func (s *Storage) CallerAccessToGroup(ctx context.Context, group int64) (bool, error) {
	userIf, found := digest.AuthenticatedUser(ctx)
	if !found {
		return false, nil
	}
	user, ok := userIf.(*User)
	if !ok {
		return false, errors.Errorf("context user %v is not *User", userIf)
	}
	if user.Owner {
		return true, nil
	}
	m := &GroupMember{}
	if err := getSQL(ctx, s.sql, m, "SELECT * FROM GroupMember WHERE User = ? AND Group = ?", user.Id, group); err != nil {
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

func readObject(b []byte) (*structs.Object, error) {
	result := &structs.Object{}
	if len(b) == 0 {
		return result, nil
	}
	if err := goccy.Unmarshal(b, result); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return result, nil
}
