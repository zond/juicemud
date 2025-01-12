//go:generate sh -c "capnp compile -I `go list -m -f '{{.Dir}}' capnproto.org/go/capnp/v3`/std -ogo object.capnp"
package storage

// TODO(zond): Implement read/write group access restrictions.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"capnproto.org/go/capnp/v3"
	"github.com/estraier/tkrzw-go"
	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/digest"
	"github.com/zond/sqly"

	_ "modernc.org/sqlite"
)

var (
	lastObjectTimePart uint64 = 0
)

const (
	objectIDLen = 16
)

func nextObjectID() ([]byte, error) {
	newTimePart := uint64(0)
	for {
		newTimePart = uint64(time.Now().UnixNano())
		lastTimePart := atomic.LoadUint64(&lastObjectTimePart)
		if newTimePart > lastTimePart && atomic.CompareAndSwapUint64(&lastObjectTimePart, lastTimePart, newTimePart) {
			break
		}
	}
	timeSize := binary.Size(newTimePart)
	result := make([]byte, objectIDLen)
	binary.BigEndian.PutUint64(result, newTimePart)
	if _, err := rand.Read(result[timeSize:]); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return result, nil
}

func New(ctx context.Context, dir string) (*Storage, error) {
	sql, err := sqly.Open("sqlite", filepath.Join(dir, "sqlite.db"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	o := &opener{dir: dir}
	s := &Storage{
		sql:     sql,
		sources: o.openHash("sources"),
		objects: o.openHash("objects"),
		queue:   o.openTree("queue"),
	}
	if o.err != nil {
		return nil, o.err
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
	sources *tkrzw.DBM
	objects *tkrzw.DBM
	queue   *tkrzw.DBM
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
	value, stat := s.sources.Get([]byte(path))
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return []byte{}, nil
	} else if !stat.IsOK() {
		return nil, juicemud.WithStack(stat)
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

func (s *Storage) GetObject(ctx context.Context, id []byte) (*Object, error) {
	b, stat := s.objects.Get(id)
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return nil, juicemud.WithStack(os.ErrNotExist)
	} else if !stat.IsOK() {
		return nil, juicemud.WithStack(stat)
	}
	return readObject(b)
}

func (s *Storage) EnsureObject(ctx context.Context, id []byte, setup func(*Object) error) error {
	return juicemud.WithStack(processMulti(s.objects, []funcPair{
		{
			Key: id,
			Func: func(k, v []byte) (any, error) {
				if v != nil {
					return nil, nil
				}
				object, err := MakeObject(ctx)
				if err != nil {
					return nil, juicemud.WithStack(err)
				}
				if err := setup(object); err != nil {
					return nil, juicemud.WithStack(err)
				}
				marshalledObject, err := object.Message().Marshal()
				if err != nil {
					return nil, juicemud.WithStack(err)
				}
				return marshalledObject, nil
			},
		},
	}, true))
}

func MakeObject(ctx context.Context) (*Object, error) {
	arena := capnp.SingleSegment(nil)
	_, seg, err := capnp.NewMessage(arena)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	object, err := NewRootObject(seg)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	newID, err := nextObjectID()
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	object.SetId(newID)
	return &object, nil
}

type funcPair struct {
	Key  any
	Func func(k, v []byte) (any, error)
}

func processMulti(dbm *tkrzw.DBM, funcPairs []funcPair, write bool) (err error) {
	procPairs := make([]tkrzw.KeyProcPair, len(funcPairs))
	for index, fpIter := range funcPairs {
		fp := fpIter
		procPairs[index] = tkrzw.KeyProcPair{
			Key: fp.Key,
			Proc: func(k, v []byte) any {
				if err != nil {
					return tkrzw.NilString
				}
				var res any
				if res, err = fp.Func(k, v); err != nil {
					return tkrzw.NilString
				}
				return res
			},
		}
	}
	if stat := dbm.ProcessMulti(procPairs, write); !stat.IsOK() {
		return juicemud.WithStack(stat)
	}
	return juicemud.WithStack(err)
}

func (s *Storage) SetObject(ctx context.Context, claimedOldLocation []byte, object *Object) error {
	id, err := object.Id()
	if err != nil {
		return juicemud.WithStack(err)
	}
	newLocation, err := object.Location()
	if err != nil {
		return juicemud.WithStack(err)
	}
	marshalledObject, err := object.Message().Marshal()
	if err != nil {
		return juicemud.WithStack(err)
	}
	var pairs []funcPair
	if claimedOldLocation == nil || bytes.Equal(claimedOldLocation, newLocation) {
		// Loc is unchanged, just verify that it's what's there right now.
		pairs = []funcPair{
			{
				Key: id,
				Func: func(key []byte, value []byte) (any, error) {
					oldObject, err := readObject(value)
					if err != nil {
						return nil, juicemud.WithStack(err)
					}
					realOldLocation, err := oldObject.Location()
					if err != nil {
						return nil, juicemud.WithStack(err)
					}
					if !bytes.Equal(realOldLocation, newLocation) {
						return nil, errors.Errorf("object is moved without updating old location")
					}
					return marshalledObject, nil
				},
			},
		}
	} else {
		// Loc is changed, verify that the old one is what's there right now, that obj can
		// be removed from old loc, and added to new loc, before all are saved.
		var marshalledNewContainer []byte
		pairs = []funcPair{
			{
				Key: id,
				Func: func(key []byte, value []byte) (any, error) {
					oldObject, err := readObject(value)
					if err != nil {
						return nil, juicemud.WithStack(err)
					}
					realOldLocation, err := oldObject.Location()
					if err != nil {
						return nil, juicemud.WithStack(err)
					}
					if !bytes.Equal(realOldLocation, newLocation) {
						return nil, errors.Errorf("object is moved without updating old location")
					}
					return nil, nil
				},
			},
			{
				Key: newLocation,
				Func: func(key []byte, value []byte) (any, error) {
					newContainer, err := readObject(value)
					if err != nil {
						return nil, juicemud.WithStack(err)
					}
					if err = OH(newContainer).Content().Append(id); err != nil {
						return nil, juicemud.WithStack(err)
					}
					if marshalledNewContainer, err = newContainer.Message().Marshal(); err != nil {
						return nil, juicemud.WithStack(err)
					}
					return nil, nil
				},
			},
		}
		if claimedOldLocation != nil {
			pairs = append(pairs, funcPair{
				Key: claimedOldLocation,
				Func: func(key []byte, value []byte) (any, error) {
					oldContainer, err := readObject(value)
					if err != nil {
						return nil, juicemud.WithStack(err)
					}
					var found bool
					if found, err = OH(oldContainer).Content().Has(id); err != nil {
						return nil, juicemud.WithStack(err)
					} else if !found {
						return nil, errors.Errorf("object claimed to be contained by %+v, but wasn't", claimedOldLocation)
					}
					if err = OH(oldContainer).Content().Remove(id); err != nil {
						return nil, juicemud.WithStack(err)
					}
					if b, err := oldContainer.Message().Marshal(); err != nil {
						return nil, juicemud.WithStack(err)
					} else {
						return b, nil
					}
				},
			})
		}
		pairs = append(pairs,
			funcPair{
				Key: newLocation,
				Func: func(key []byte, value []byte) (any, error) {
					return marshalledNewContainer, nil
				},
			},
			funcPair{
				Key: id,
				Func: func(key []byte, value []byte) (any, error) {
					return marshalledObject, nil
				},
			},
		)
	}
	if err := processMulti(s.objects, pairs, true); err != nil {
		return juicemud.WithStack(err)
	}
	return nil
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
		if stat := s.sources.Remove([]byte(fileSync.Remove)); !stat.IsOK() && stat.GetCode() != tkrzw.StatusNotFoundError {
			return juicemud.WithStack(stat)
		}
	} else if fileSync.Set != "" {
		if stat := s.sources.Set([]byte(fileSync.Set), fileSync.Content, true); !stat.IsOK() {
			return juicemud.WithStack(stat)
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
	Object       []byte
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

func readObject(b []byte) (*Object, error) {
	msg, err := capnp.Unmarshal(b)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	object, err := ReadRootObject(msg)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return &object, nil
}
