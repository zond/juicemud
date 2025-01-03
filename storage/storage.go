//go:generate sh -c "capnp compile -I `go list -m -f '{{.Dir}}' capnproto.org/go/capnp/v3`/std -ogo object.capnp"
package storage

import (
	"context"
	"encoding/binary"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/estraier/tkrzw-go"
	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
	"github.com/zond/juicemud/digest"
	"github.com/zond/sqly"

	_ "modernc.org/sqlite"
)

func toBytes(i int64) []byte {
	b := make([]byte, 8)
	if _, err := binary.Encode(b, binary.BigEndian, i); err != nil {
		log.Panic(err)
	}
	return b
}

func New(ctx context.Context, dir string) (*Storage, error) {
	sql, err := sqly.Open("sqlite", filepath.Join(dir, "sqlite.db"))
	if err != nil {
		return nil, errors.WithStack(err)
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
	for _, prototype := range []any{File{}, Group{}, User{}, GroupMember{}} {
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
			return errors.WithStack(NotFoundErr)
		}
		return errors.WithStack(err)
	}
	return nil
}

func (s *Storage) GetSource(ctx context.Context, id int64) ([]byte, error) {
	value, stat := s.sources.Get(toBytes(id))
	if stat.GetCode() == tkrzw.StatusNotFoundError {
		return []byte{}, nil
	} else if !stat.IsOK() {
		return nil, errors.WithStack(stat)
	}
	return value, nil
}

func (s *Storage) SetSource(ctx context.Context, id int64, content []byte) error {
	file := &File{}
	if err := getSQL(ctx, s.sql, file, "SELECT * FROM File WHERE Id = ?", id); err != nil {
		return errors.WithStack(err)
	}
	if stat := s.sources.Set(toBytes(id), content, true); !stat.IsOK() {
		return errors.WithStack(stat)
	}
	return nil
}

func (s *Storage) DelSource(ctx context.Context, id int64) error {
	if stat := s.sources.Remove(toBytes(id)); !stat.IsOK() && stat.GetCode() != tkrzw.StatusNotFoundError {
		return errors.WithStack(stat)
	}
	return nil
}

type File struct {
	Id         int64 `sqly:"pkey"`
	Parent     int64 `sqly:"uniqueWith(Name)"`
	Name       string
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
			return errors.WithStack(err)
		}
		parent, err := getFile(ctx, tx, filepath.Dir(path))
		if err != nil {
			return errors.WithStack(err)
		}
		file = &File{
			Parent:     parent.Id,
			Name:       filepath.Base(path),
			ModTime:    sqly.ToSQLTime(time.Now()),
			Dir:        false,
			ReadGroup:  parent.ReadGroup,
			WriteGroup: parent.WriteGroup,
		}
		if err := tx.Upsert(ctx, file, true); err != nil {
			return errors.WithStack(err)
		}
		return nil
	}); err != nil {
		return nil, errors.WithStack(err)
	}
	return file, nil
}

func (s *Storage) MoveFile(ctx context.Context, oldPath string, newPath string) error {
	return s.sql.Write(ctx, func(tx *sqly.Tx) error {
		oldFile, err := getFile(ctx, tx, oldPath)
		if err != nil {
			return errors.WithStack(err)
		}
		var newParent *File
		if newPath == "/" {
			newParent = &File{
				Dir: true,
			}
		} else {
			newParent, err = getFile(ctx, tx, filepath.Dir(newPath))
			if err != nil {
				return errors.WithStack(err)
			}
		}
		newFile, err := getFile(ctx, tx, newPath)
		if err != nil && !errors.Is(err, NotFoundErr) {
			return errors.WithStack(err)
		}
		if newFile != nil {
			if err := delFile(ctx, tx, newFile); err != nil {
				return errors.WithStack(err)
			}
		}
		oldFile.Parent = newParent.Id
		if err := tx.Upsert(ctx, oldFile, true); err != nil {
			return errors.WithStack(err)
		}
		return nil
	})
}

func getChildren(ctx context.Context, db sqlx.QueryerContext, parent int64) ([]File, error) {
	result := []File{}
	if err := sqlx.SelectContext(ctx, db, &result, "SELECT * FROM File WHERE Parent = ?", parent); err != nil {
		return nil, errors.WithStack(err)
	}
	return result, nil
}

func (s *Storage) GetChildren(ctx context.Context, parent int64) ([]File, error) {
	return getChildren(ctx, s.sql, parent)
}

func getFile(ctx context.Context, db sqlx.QueryerContext, path string) (*File, error) {
	file := &File{
		Dir: true,
	}
	for _, part := range strings.Split(path, "/") {
		if part != "" {
			if err := getSQL(ctx, db, file, "SELECT * FROM File WHERE Parent = ? AND Name = ?", file.Id, part); err != nil {
				return nil, errors.WithStack(err)
			}
		}
	}
	return file, nil
}

func (s *Storage) GetFile(ctx context.Context, path string) (*File, error) {
	return getFile(ctx, s.sql, path)
}

func delFile(ctx context.Context, db sqlx.ExtContext, file *File) error {
	children, err := getChildren(ctx, db, file.Id)
	if err != nil {
		return errors.WithStack(err)
	}
	if len(children) > 0 {
		return errors.Errorf("%q contains files", file.Name)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM File WHERE Id = ?", file.Id); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

func (s *Storage) DelFile(ctx context.Context, file *File) error {
	return s.sql.Write(ctx, func(tx *sqly.Tx) error {
		return delFile(ctx, tx, file)
	})
}

func (s *Storage) CreateDir(ctx context.Context, path string) error {
	return s.sql.Write(ctx, func(tx *sqly.Tx) error {
		if _, err := getFile(ctx, tx, path); err == nil {
			return nil
		} else if !errors.Is(err, NotFoundErr) {
			return errors.WithStack(err)
		}
		parent, err := getFile(ctx, tx, filepath.Dir(path))
		if err != nil {
			return errors.WithStack(err)
		}
		file := &File{
			Name:       filepath.Base(path),
			ModTime:    sqly.ToSQLTime(time.Now()),
			Dir:        true,
			ReadGroup:  parent.ReadGroup,
			WriteGroup: parent.WriteGroup,
		}
		if err := tx.Upsert(ctx, file, true); err != nil {
			return errors.WithStack(err)
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
		return false, errors.WithStack(err)
	}
	return true, nil
}

func (s *Storage) GetHA1AndUser(ctx context.Context, username string) (string, bool, any, error) {
	user := &User{}
	if err := getSQL(ctx, s.sql, user, "SELECT * FROM User WHERE Name = ?", username); err != nil {
		if errors.Is(err, NotFoundErr) {
			return "", false, nil, nil
		}
		return "", false, nil, errors.WithStack(err)
	}
	return user.PasswordHash, true, user, nil
}

type GroupMember struct {
	Id    int64 `sqly:"pkey"`
	User  int64 `sqly:"index"`
	Group int64 `sqly:"uniqueWith(User)"`
}
