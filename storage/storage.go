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
	return nil
}

func (s *Storage) loadFile(ctx context.Context, name string) (*davFile, error) {
	username, found := digest.AuthenticatedUsername(ctx)
	log.Printf("username: %q, found: %v", username, found)
	result := &File{
		Dir: true,
	}
	if err := s.sql.Read(ctx, func(tx *sqly.Tx) error {
		for _, fileName := range strings.Split(name, "/") {
			if fileName != "" {
				if err := s.sql.Get(result, "SELECT * FROM File WHERE Parent = ? AND Name = ?", result.Id, fileName); err != nil {
					return errors.WithStack(err)
				}
			}
		}
		return nil
	}); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, os.ErrNotExist
		}
		return nil, errors.WithStack(err)
	}
	dav, err := result.toDav(s)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return dav, nil
}

func (s *Storage) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	return s.loadFile(ctx, name)
}

func (s *Storage) RemoveAll(ctx context.Context, name string) error {
	return nil
}

func (s *Storage) Rename(ctx context.Context, oldName, newName string) error {
	return nil
}

func (s *Storage) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	f, err := s.loadFile(ctx, name)
	if err != nil {
		return nil, errors.WithStack(err)
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

func (f *File) toDav(s *Storage) (*davFile, error) {
	result := &davFile{
		s:    s,
		f:    f,
		mode: 0777,
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
			return nil, errors.Errorf("trying to write %v bytes to temporary file, got %v, %v", written, err)
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
	f    *File
	s    *Storage
	size int64
	mode fs.FileMode
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
		dav, err := file.toDav(d.s)
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

func (s *Storage) GetHA1(username string) (string, bool, error) {
	user := &User{}
	if err := s.sql.Get(user, "SELECT * FROM User WHERE Name = ?", username); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return "", false, nil
		}
		return "", false, errors.WithStack(err)
	}
	return user.PasswordHash, true, nil
}

type GroupMember struct {
	Id    int64 `sqly:"pkey"`
	User  int64 `sqly:"index"`
	Group int64 `sqly:"uniqueWith(User)"`
}
