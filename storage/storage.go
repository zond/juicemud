//go:generate sh -c "capnp compile -I `go list -m -f '{{.Dir}}' capnproto.org/go/capnp/v3`/std -ogo object.capnp"
package storage

import (
	"context"
	"encoding/binary"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/estraier/tkrzw-go"
	"github.com/pkg/errors"
	"github.com/zond/juicemud/digest"
	"github.com/zond/sqly"
	"golang.org/x/net/webdav"

	_ "modernc.org/sqlite"
)

func toBytes(i int64) []byte {
	b := make([]byte, 8)
	if _, err := binary.Encode(b, binary.BigEndian, i); err != nil {
		log.Panic(err)
	}
	return b
}

type opener struct {
	dir string
	err error
}

func (o *opener) openHash(name string) *tkrzw.DBM {
	if o.err != nil {
		return nil
	}
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(filepath.Join(o.dir, fmt.Sprintf("%s.tkh", name)), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"restore_mode":     "RESTORE_SYNC|RESTORE_NO_SHORTCUTS|RESTORE_WITH_HARDSYNC",
	})
	if !stat.IsOK() {
		o.err = stat
	}
	return dbm
}

func (o *opener) openTree(name string) *tkrzw.DBM {
	if o.err != nil {
		return nil
	}
	dbm := tkrzw.NewDBM()
	stat := dbm.Open(filepath.Join(o.dir, fmt.Sprintf("%s.tkt", name)), true, map[string]string{
		"update_mode":      "UPDATE_APPENDING",
		"record_comp_mode": "RECORD_COMP_NONE",
		"key_comparator":   "LexicalKeyComparator",
	})
	if !stat.IsOK() {
		o.err = stat
	}
	return dbm
}

func New(ctx context.Context, dir string) (*Storage, error) {
	sql, err := sqly.Open("sqlite", filepath.Join(dir, "sqlite.db"))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if _, err := sql.ExecContext(ctx, "PRAGMA journal_mode = wal2"); err != nil {
		return nil, errors.WithStack(err)
	}
	o := &opener{dir: dir}
	s := &Storage{
		sql:     sql,
		sources: o.openHash("sources"),
		objects: o.openHash("objects"),
		queue:   o.openTree("queue"),
	}
	for _, prototype := range []any{File{}, Group{}, User{}, GroupMember{}} {
		if err := sql.CreateTableIfNotExists(ctx, prototype); err != nil {
			return nil, err
		}
	}
	return s, o.err
}

type Storage struct {
	sql     *sqly.DB
	sources *tkrzw.DBM
	objects *tkrzw.DBM
	queue   *tkrzw.DBM
}

func (s *Storage) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	log.Printf("Mkdir(..., %q, %v)", name, perm)
	return nil
}

func (s *Storage) loadFileAndOrParent(
	ctx context.Context,
	name string,
) (file *davFile, parent *davFile, err error) {
	sqlFile := &File{
		Dir: true,
	}
	sqlParent := sqlFile
	parts := strings.Split(name, "/")
	for index := 0; index < len(parts); index++ {
		part := parts[index]
		if part != "" {
			sqlParent = sqlFile
			if err := s.get(ctx, sqlFile, "SELECT * FROM File WHERE Parent = ? AND Name = ?", sqlParent.Id, part); err != nil {
				if !errors.Is(err, NotFoundErr) {
					return nil, nil, errors.WithStack(err)
				} else {
					sqlFile = nil
					break
				}
			}
		}
	}
	if sqlFile != nil {
		file, err = sqlFile.toDav(ctx, s)
		if err != nil {
			return nil, nil, errors.WithStack(err)
		}
	}
	parent, err = sqlParent.toDav(ctx, s)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	return file, parent, nil
}

func parseFlag(flag int) string {
	flags := []string{}
	for _, pair := range []struct {
		f int
		d string
	}{
		{
			f: os.O_RDONLY,
			d: "O_RDONLY",
		},
		{
			f: os.O_WRONLY,
			d: "O_WRONLY",
		},
		{
			f: os.O_RDWR,
			d: "O_RDWR",
		},
		{
			f: os.O_APPEND,
			d: "O_APPEND",
		},
		{
			f: os.O_CREATE,
			d: "O_CREATE",
		},
		{
			f: os.O_EXCL,
			d: "O_EXCL",
		},
		{
			f: os.O_SYNC,
			d: "O_SYNC",
		},
		{
			f: os.O_TRUNC,
			d: "O_TRUNC",
		},
	} {
		if flag&pair.f == pair.f {
			flags = append(flags, pair.d)
		}
	}
	return strings.Join(flags, "|")
}

func (s *Storage) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	log.Printf("OpenFile(..., %q, %v, %v)", name, parseFlag(flag), perm)
	file, parent, err := s.loadFileAndOrParent(ctx, name)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if file != nil {
		if flag&os.O_TRUNC == os.O_TRUNC {
			if err := file.File.Truncate(0); err != nil {
				return nil, errors.WithStack(err)
			}
		}
		if flag&os.O_APPEND == os.O_APPEND {
			if _, err := file.File.Seek(0, 2); err != nil {
				return nil, errors.WithStack(err)
			}
		}
		return file, nil
	}
	if flag&os.O_CREATE == 0 && flag&os.O_WRONLY == 0 && flag&os.O_RDWR == 0 {
		return nil, os.ErrNotExist
	}
	if parent.mode&0200 != 0200 {
		return nil, os.ErrPermission
	}
	return s.createFile(ctx, &File{
		Parent:     parent.f.Id,
		Name:       name,
		ModTime:    sqly.ToSQLTime(time.Now()),
		Dir:        perm.IsDir(),
		ReadGroup:  parent.f.ReadGroup,
		WriteGroup: parent.f.WriteGroup,
	})
}

func (s *Storage) createFile(ctx context.Context, f *File) (*davFile, error) {
	if err := s.sql.Upsert(ctx, f, true); err != nil {
		return nil, errors.WithStack(err)
	}
	stat := s.sources.Set(toBytes(f.Id), []byte{}, true)
	if !stat.IsOK() {
		return nil, errors.WithStack(stat)
	}
	dav, err := f.toDav(ctx, s)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return dav, nil
}

func (s *Storage) RemoveAll(ctx context.Context, name string) error {
	log.Printf("RemoveAll(..., %q)", name)
	return nil
}

func (s *Storage) Rename(ctx context.Context, oldName, newName string) error {
	log.Printf("Rename(..., %q, %q)", oldName, newName)
	return nil
}

func (s *Storage) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	log.Printf("Stat(..., %q)", name)
	f, _, err := s.loadFileAndOrParent(ctx, name)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if f == nil {
		return nil, os.ErrNotExist
	}
	return f, nil
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

var (
	NotFoundErr = errors.New("Not found")
)

func (s *Storage) get(ctx context.Context, d any, sql string, params ...any) error {
	if err := s.sql.GetContext(ctx, d, sql, params...); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return errors.WithStack(NotFoundErr)
		}
		return errors.WithStack(err)
	}
	return nil
}

func (f *File) toDav(ctx context.Context, s *Storage) (*davFile, error) {
	result := &davFile{
		ctx:  ctx,
		s:    s,
		f:    f,
		mode: 0,
	}
	if is, err := s.callerAccessToGroup(ctx, f.ReadGroup); err != nil {
		return nil, errors.WithStack(err)
	} else if is {
		result.mode = result.mode | 0444
		if f.Dir {
			result.mode = result.mode | 0111
		}
	}
	if is, err := s.callerAccessToGroup(ctx, f.WriteGroup); err != nil {
		return nil, errors.WithStack(err)
	} else if is {
		result.mode = result.mode | 0666
		if f.Dir {
			result.mode = result.mode | 0111
		}
	}
	if f.Dir {
		result.mode = result.mode | fs.ModeDir
	}
	if !f.Dir {
		content, stat := s.sources.Get(toBytes(f.Id))
		if !stat.IsOK() {
			return nil, errors.WithStack(stat)
		}
		tmpFile, err := os.CreateTemp("", "juicemud-storage-*")
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if written, err := tmpFile.Write(content); written != len(content) || err != nil {
			return nil, errors.Errorf("trying to write %v bytes to temporary file, got %v, %v", len(content), written, err)
		}
		if newSeek, err := tmpFile.Seek(0, 0); newSeek != 0 || err != nil {
			return nil, errors.Errorf("trying to seek to 0, 0 in temporary file, got %v, %v", newSeek, err)
		}
		result.size = int64(len(content))
		result.File = tmpFile
	}
	return result, nil
}

type davFile struct {
	*os.File
	ctx  context.Context
	f    *File
	s    *Storage
	size int64
	mode fs.FileMode
}

func (d *davFile) Close() error {
	if err := d.File.Close(); err != nil {
		return errors.WithStack(err)
	}
	defer os.Remove(d.File.Name())
	content, err := os.ReadFile(d.File.Name())
	if err != nil {
		return errors.WithStack(err)
	}
	stat := d.s.sources.Set(toBytes(d.f.Id), content, true)
	if !stat.IsOK() {
		return errors.WithStack(stat)
	}
	return nil
}

func (d *davFile) Size() int64 {
	return d.size
}

func (d *davFile) Name() string {
	return d.f.Name
}

func (d *davFile) Mode() fs.FileMode {
	return d.mode
}

func (d *davFile) ModTime() time.Time {
	return d.f.ModTime.Time()
}

func (d *davFile) IsDir() bool {
	return d.mode.IsDir()
}

func (d *davFile) Sys() any {
	return nil
}

func (d *davFile) Readdir(count int) ([]fs.FileInfo, error) {
	if !d.IsDir() {
		return nil, nil
	}
	files := []File{}
	if err := d.s.sql.Select(&files, "SELECT * FROM File WHERE Parent = ?", d.f.Id); err != nil {
		return nil, errors.WithStack(err)
	}
	davs := make([]fs.FileInfo, len(files))
	for index, file := range files {
		dav, err := file.toDav(d.ctx, d.s)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		davs[index] = dav
	}
	return davs, nil
}

func (d *davFile) Stat() (fs.FileInfo, error) {
	return d, nil
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

func (s *Storage) callerAccessToGroup(ctx context.Context, group int64) (bool, error) {
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
	if err := s.get(ctx, m, "SELECT * FROM GroupMember WHERE User = ? AND Group = ?", user.Id, group); err != nil {
		if errors.Is(err, NotFoundErr) {
			return false, nil
		}
		return false, errors.WithStack(err)
	}
	return true, nil
}

func (s *Storage) GetHA1AndUser(ctx context.Context, username string) (string, bool, any, error) {
	user := &User{}
	if err := s.get(ctx, user, "SELECT * FROM User WHERE Name = ?", username); err != nil {
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
