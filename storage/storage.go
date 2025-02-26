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
	objects, err := dbm.OpenLiveTypeHash[structs.Object](filepath.Join(dir, "objects"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	queueTree, err := dbm.OpenTypeTree[structs.Event](filepath.Join(dir, "queue"))
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
	queue    *queue.Queue
	sql      *sqly.DB
	sources  *dbm.Hash
	modTimes *dbm.Hash
	objects  *dbm.LiveTypeHash[structs.Object, *structs.Object]
}

func (s *Storage) Queue() *queue.Queue {
	return s.queue
}

func (s *Storage) StartObjects(ctx context.Context) error {
	return s.objects.Start(ctx)
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
		if err := s.CheckCallerAccessToGroupID(ctx, file.WriteGroup); err != nil {
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

	return juicemud.WithStack(structs.WithLock(func() error {
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
			s.objects.LProc(id, func(_ string, loc *structs.Object) (*structs.Object, error) {
				loc.Unsafe.Content[id] = true
				return loc, nil
			}),
		}))
	}, obj, loc))
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

// TODO: Rename to AccessObject
// Loads the object with the given ID. If a Refresh is given, it will be run if the
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

func (s *Storage) UNSAFEEnsureObject(ctx context.Context, obj *structs.Object) error {
	return juicemud.WithStack(s.objects.SetIfMissing(obj))
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

func (s *Storage) ChwriteFile(ctx context.Context, path string, writer string) error {
	return juicemud.WithStack(s.sql.Write(ctx, func(tx *sqly.Tx) error {
		file, err := s.loadFile(ctx, tx, path)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if err := s.CheckCallerAccessToGroupID(ctx, file.WriteGroup); err != nil {
			return juicemud.WithStack(err)
		}
		wg, err := s.loadGroupByName(ctx, tx, writer)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if err := s.CheckCallerAccessToGroupID(ctx, wg.Id); err != nil {
			return juicemud.WithStack(err)
		}
		file.WriteGroup = wg.Id
		if err := tx.Upsert(ctx, file, true); err != nil {
			return juicemud.WithStack(err)
		}
		return nil
	}))
}

func (s *Storage) ChreadFile(ctx context.Context, path string, reader string) error {
	return juicemud.WithStack(s.sql.Write(ctx, func(tx *sqly.Tx) error {
		file, err := s.loadFile(ctx, tx, path)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if err := s.CheckCallerAccessToGroupID(ctx, file.WriteGroup); err != nil {
			return juicemud.WithStack(err)
		}
		rg, err := s.loadGroupByName(ctx, tx, reader)
		if err != nil {
			return juicemud.WithStack(err)
		}
		file.ReadGroup = rg.Id
		if err := tx.Upsert(ctx, file, true); err != nil {
			return juicemud.WithStack(err)
		}
		return nil
	}))
}

func (s *Storage) EnsureFile(ctx context.Context, path string) (file *File, created bool, err error) {
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		file, err = s.loadFile(ctx, tx, path)
		if err == nil {
			if err := s.CheckCallerAccessToGroupID(ctx, file.WriteGroup); err != nil {
				return juicemud.WithStack(err)
			}
			created = false
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}
		parent, err := s.loadFile(ctx, tx, filepath.Dir(path))
		if err != nil {
			return juicemud.WithStack(err)
		}
		if err := s.CheckCallerAccessToGroupID(ctx, parent.WriteGroup); err != nil {
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
		if err := s.CheckCallerAccessToGroupID(ctx, toMove.WriteGroup); err != nil {
			return juicemud.WithStack(err)
		}
		content, modTime, err := s.LoadSource(ctx, oldPath)
		if err != nil {
			return juicemud.WithStack(err)
		}
		newParent, err := s.loadFile(ctx, tx, filepath.Dir(newPath))
		if err != nil {
			return juicemud.WithStack(err)
		}
		if err := s.CheckCallerAccessToGroupID(ctx, newParent.WriteGroup); err != nil {
			return juicemud.WithStack(err)
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

func (s *Storage) FileExists(ctx context.Context, path string) (bool, error) {
	row := s.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM File WHERE PATH = ?", path)
	count := 0
	row.Scan(&count)
	if err := row.Err(); err != nil {
		return false, juicemud.WithStack(err)
	}
	return count > 0, nil
}

func (s *Storage) loadFile(ctx context.Context, db sqlx.QueryerContext, path string) (*File, error) {
	file := &File{}
	if err := getSQL(ctx, db, file, "SELECT * FROM File WHERE Path = ?", path); err != nil {
		return nil, juicemud.WithStack(err)
	}
	if err := s.CheckCallerAccessToGroupID(ctx, file.ReadGroup); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return file, nil
}

func (s *Storage) LoadFile(ctx context.Context, path string) (*File, error) {
	return s.loadFile(ctx, s.sql, path)
}

func (s *Storage) FileGroups(ctx context.Context, file *File) (*Group, *Group, error) {
	reader, err := s.loadGroupByID(ctx, s.sql, file.ReadGroup)
	if err != nil {
		return nil, nil, juicemud.WithStack(err)
	}
	writer, err := s.loadGroupByID(ctx, s.sql, file.WriteGroup)
	if err != nil {
		return nil, nil, juicemud.WithStack(err)
	}
	return reader, writer, nil
}

func (s *Storage) delFileIfExists(ctx context.Context, db sqlx.ExtContext, path string, recursive bool) error {
	file, err := s.loadFile(ctx, db, path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := s.CheckCallerAccessToGroupID(ctx, file.WriteGroup); err != nil {
		return juicemud.WithStack(err)
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
		if existing, err := s.loadFile(ctx, tx, path); err == nil {
			if !existing.Dir {
				return errors.Wrapf(os.ErrExist, "%q already exists, is not directory", path)
			}
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}
		var parent *File
		if path == "/" {
			parent = &File{}
		} else {
			var err error
			parent, err = s.loadFile(ctx, tx, filepath.Dir(path))
			if err != nil {
				return juicemud.WithStack(err)
			}
		}
		if err := s.CheckCallerAccessToGroupID(ctx, parent.WriteGroup); err != nil {
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

func (s *Storage) loadGroupByName(ctx context.Context, db sqlx.QueryerContext, name string) (*Group, error) {
	result := &Group{}
	if name == "" {
		return result, nil
	}
	if err := getSQL(ctx, db, result, "SELECT * FROM `Group` WHERE Name = ?", name); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return result, nil
}

func (s *Storage) loadGroupByID(ctx context.Context, db sqlx.QueryerContext, id int64) (*Group, error) {
	result := &Group{}
	if id == 0 {
		return result, nil
	}
	if err := getSQL(ctx, db, result, "SELECT * FROM `Group` WHERE Id = ?", id); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return result, nil
}

func (s *Storage) EnsureGroup(ctx context.Context, group *Group) (created bool, err error) {
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		found, err := s.loadGroupByName(ctx, tx, group.Name)
		if err == nil {
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

type contextKey int

var (
	authenticatedUser contextKey = 0
)

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

type settableContext interface {
	SetValue(any, any)
}

func AuthenticateUser(ctx context.Context, u *User) context.Context {
	if sctx, ok := ctx.(settableContext); ok {
		sctx.SetValue(authenticatedUser, u)
		return ctx
	} else {
		return context.WithValue(ctx, authenticatedUser, u)
	}
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
		group, err := s.loadGroupByID(ctx, s.sql, id)
		if err != nil {
			return nil, juicemud.WithStack(err)
		}
		result = append(result, *group)
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
	g, err := s.loadGroupByName(ctx, s.sql, groupName)
	if err != nil {
		return false, juicemud.WithStack(err)
	}
	return s.UserAccessToGroupID(ctx, user, g.Id)
}

func (s *Storage) CheckCallerAccessToGroupID(ctx context.Context, groupID int64) error {
	if has, err := s.CallerAccessToGroupID(ctx, groupID); err != nil {
		return juicemud.WithStack(err)
	} else if !has {
		return errors.Wrapf(os.ErrPermission, "not member of group %v", groupID)
	}
	return nil
}

func (s *Storage) CallerAccessToGroupID(ctx context.Context, groupID int64) (bool, error) {
	if juicemud.IsMainContext(ctx) {
		return true, nil
	}
	user, found := AuthenticatedUser(ctx)
	if !found {
		return false, nil
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

func (s *Storage) GetHA1AndUser(ctx context.Context, username string) (string, bool, *User, error) {
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
