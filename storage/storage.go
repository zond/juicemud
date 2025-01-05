//go:generate sh -c "capnp compile -I `go list -m -f '{{.Dir}}' capnproto.org/go/capnp/v3`/std -ogo object.capnp"
package storage

import (
	"context"
	"crypto/rand"
	"encoding/binary"
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

var (
	NotFoundErr = errors.New("Not found")
)

func getSQL(ctx context.Context, db sqlx.QueryerContext, d any, sql string, params ...any) error {
	if err := sqlx.GetContext(ctx, db, d, sql, params...); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return juicemud.WithStack(NotFoundErr)
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
		if err := s.logSync(ctx, tx, &FileSync{
			Set:          file.Path,
			SetToContent: content,
		}); err != nil {
			return juicemud.WithStack(err)
		}
		return nil
	}); err != nil {
		return juicemud.WithStack(err)
	}
	return s.fileSync(ctx)
}

func (s *Storage) GetObject(ctx context.Context, id []byte) (*Object, error) {
	b, stat := s.objects.Get(id)
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return nil, juicemud.WithStack(NotFoundErr)
	} else if !stat.IsOK() {
		return nil, juicemud.WithStack(stat)
	}
	msg, err := capnp.Unmarshal(b)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	result, err := ReadRootObject(msg)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return &result, nil
}

func (s *Storage) CreateObject(ctx context.Context) (*Object, error) {
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
	if err := s.SetObject(ctx, &object); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return &object, nil
}

func (s *Storage) SetObject(ctx context.Context, object *Object) error {
	id, err := object.Id()
	if err != nil {
		return juicemud.WithStack(err)
	}
	if len(id) != objectIDLen {
		return errors.Errorf("Object ID %+v isn't %v long", id, objectIDLen)
	}
	b, err := object.Message().Marshal()
	if err != nil {
		return juicemud.WithStack(err)
	}
	if stat := s.objects.Set(id, b, false); !stat.IsOK() {
		return juicemud.WithStack(stat)
	}
	return nil
}

type FileSync struct {
	Id           int64 `sqly:"pkey"`
	Remove       string
	Set          string
	SetToRemoved bool
	SetToContent []byte
}

func (s *Storage) logSync(ctx context.Context, db sqlx.ExtContext, fileSync *FileSync) error {
	if fileSync.Remove == "" && fileSync.Set == "" {
		return errors.Errorf("invalid FileSync %+v: Remove == \"\" and Set == \"\"", fileSync)
	}
	if fileSync.Set != "" && fileSync.SetToRemoved && len(fileSync.SetToContent) > 0 {
		return errors.Errorf("invalid FileSync %+v: Set != \"\", SetToRemoved, and non-empty SetToContent", fileSync)
	}
	count := int64(-1)
	if err := sqlx.GetContext(ctx, db, &count, "SELECT COUNT(*) FROM FileSync"); err != nil {
		return juicemud.WithStack(err)
	}
	if count < 0 {
		return errors.Errorf("invalid FileSync count %v", count)
	}
	fileSync.Id = count
	return sqly.Upsert(ctx, db, fileSync, false)
}

func (s *Storage) fileSync(ctx context.Context) error {
	return s.sql.Write(ctx, func(tx *sqly.Tx) error {
		fileSyncs := []FileSync{}
		if err := tx.SelectContext(ctx, &fileSyncs, "SELECT * FROM FileSync ORDER BY Id ASC"); err != nil {
			return juicemud.WithStack(err)
		}
		for _, fileSync := range fileSyncs {
			pairs := []tkrzw.KeyProcPair{}
			removed := []byte{}
			if fileSync.Remove != "" {
				pairs = append(pairs, tkrzw.KeyProcPair{
					Key: []byte(fileSync.Remove),
					Proc: func(key []byte, value []byte) any {
						removed = value
						return tkrzw.RemoveBytes
					},
				})
			}
			if fileSync.Set != "" {
				pairs = append(pairs, tkrzw.KeyProcPair{
					Key: []byte(fileSync.Set),
					Proc: func(key []byte, value []byte) any {
						if fileSync.SetToRemoved {
							return removed
						} else {
							return []byte(fileSync.SetToContent)
						}
					},
				})
			}
			if len(pairs) > 0 {
				if stat := s.sources.ProcessMulti(pairs, true); !stat.IsOK() {
					return juicemud.WithStack(stat)
				}
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM FileSync WHERE Id = ?", fileSync.Id); err != nil {
				return juicemud.WithStack(err)
			}
		}
		return nil
	})
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

func (s *Storage) EnsureFile(ctx context.Context, path string) (file *File, err error) {
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		file, err = getFile(ctx, tx, path)
		if err == nil {
			return nil
		} else if !errors.Is(err, NotFoundErr) {
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
		return nil
	}); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return file, nil
}

func (s *Storage) MoveFile(ctx context.Context, oldPath string, newPath string) error {
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		toMove, err := getFile(ctx, tx, oldPath)
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
		if err := delFileIfExistsCheckEmpty(ctx, tx, newPath); err != nil {
			return juicemud.WithStack(err)
		}
		toMove.Parent = newParent.Id
		toMove.Path = newPath
		toMove.Name = filepath.Base(newPath)
		if err := tx.Upsert(ctx, toMove, true); err != nil {
			return juicemud.WithStack(err)
		}
		if err := s.logSync(ctx, tx, &FileSync{
			Remove:       oldPath,
			Set:          newPath,
			SetToRemoved: true,
		}); err != nil {
			return juicemud.WithStack(err)
		}
		return nil
	}); err != nil {
		return juicemud.WithStack(err)
	}
	return s.fileSync(ctx)
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
	if err := getSQL(ctx, db, file, "SELECT * FROM File WHERE Path = ?"); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return file, nil
}

func (s *Storage) GetFile(ctx context.Context, path string) (*File, error) {
	return getFile(ctx, s.sql, path)
}

func delFileIfExistsCheckEmpty(ctx context.Context, db sqlx.ExtContext, path string) error {
	file, err := getFile(ctx, db, path)
	if errors.Is(err, NotFoundErr) {
		return nil
	}
	children, err := getChildren(ctx, db, file.Id)
	if err != nil {
		return juicemud.WithStack(err)
	}
	if len(children) > 0 {
		return errors.Errorf("%q contains files", file.Path)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM File WHERE Id = ?", file.Id); err != nil {
		return juicemud.WithStack(err)
	}
	return nil
}

func (s *Storage) DelFile(ctx context.Context, path string) error {
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		if err := delFileIfExistsCheckEmpty(ctx, tx, path); err != nil {
			return juicemud.WithStack(err)
		}
		if err := s.logSync(ctx, tx, &FileSync{
			Remove: path,
		}); err != nil {
			return juicemud.WithStack(err)
		}
		return nil
	}); err != nil {
		return juicemud.WithStack(err)
	}
	return s.fileSync(ctx)
}

func (s *Storage) CreateDir(ctx context.Context, path string) error {
	return s.sql.Write(ctx, func(tx *sqly.Tx) error {
		if _, err := getFile(ctx, tx, path); err == nil {
			return nil
		} else if !errors.Is(err, NotFoundErr) {
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
		if errors.Is(err, NotFoundErr) {
			return false, nil
		}
		return false, juicemud.WithStack(err)
	}
	return true, nil
}

func (s *Storage) GetHA1AndUser(ctx context.Context, username string) (string, bool, any, error) {
	user := &User{}
	if err := getSQL(ctx, s.sql, user, "SELECT * FROM User WHERE Name = ?", username); err != nil {
		if errors.Is(err, NotFoundErr) {
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
