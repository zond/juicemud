package storage

import (
	"context"
	"encoding/binary"
	"fmt"
	"iter"
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
	sourceObjects, err := dbm.OpenTree(filepath.Join(dir, "sourceObjects"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	audit, err := NewAuditLogger(filepath.Join(dir, "audit.log"))
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	s := &Storage{
		sql:           sql,
		sources:       sources,
		sourceObjects: sourceObjects,
		modTimes:      modTimes,
		objects:       objects,
		queue:         queue.New(ctx, queueTree),
		audit:         audit,
	}
	for _, prototype := range []any{File{}, FileSync{}, Group{}, User{}, GroupMember{}} {
		if err := sql.CreateTableIfNotExists(ctx, prototype); err != nil {
			return nil, err
		}
	}
	return s, nil
}

type Storage struct {
	queue         *queue.Queue
	sql           *sqly.DB
	sources       *dbm.Hash
	sourceObjects *dbm.Tree
	modTimes      *dbm.Hash
	objects       *dbm.LiveTypeHash[structs.Object, *structs.Object]
	audit         *AuditLogger
}

func (s *Storage) Close() error {
	if err := s.queue.Close(); err != nil {
		return juicemud.WithStack(err)
	}
	if err := s.sql.Close(); err != nil {
		return juicemud.WithStack(err)
	}
	if err := s.sources.Close(); err != nil {
		return juicemud.WithStack(err)
	}
	if err := s.sourceObjects.Close(); err != nil {
		return juicemud.WithStack(err)
	}
	if err := s.modTimes.Close(); err != nil {
		return juicemud.WithStack(err)
	}
	if err := s.audit.Close(); err != nil {
		return juicemud.WithStack(err)
	}
	return juicemud.WithStack(s.objects.Close())
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

func (s *Storage) StartObjects(_ context.Context) error {
	return s.objects.Start()
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
	callerRef := s.callerRef(ctx)
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
	s.AuditLog(ctx, "FILE_UPDATE", AuditFileUpdate{
		Caller: callerRef,
		Path:   path,
	})
	return s.sync(ctx)
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

	loc, err := s.objects.Get(locID)
	if err != nil {
		return juicemud.WithStack(err)
	}

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

	return juicemud.WithStack(s.sourceObjects.SubDel(obj.Unsafe.SourcePath, id))
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
		if delerr := s.sourceObjects.SubDel(obj.Unsafe.SourcePath, obj.Unsafe.Id); !errors.Is(delerr, os.ErrNotExist) && delerr != nil {
			return fmt.Errorf("trying to remove source user when handling %w: %w", err, delerr)
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

func (s *Storage) EachSourceObject(ctx context.Context, path string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		entries := []withError[dbm.BEntry]{}
		for entry, err := range s.sourceObjects.SubEach(path) {
			entries = append(entries, withError[dbm.BEntry]{T: entry, E: err})
		}
		for _, entry := range entries {
			if entry.E != nil {
				if !yield("", juicemud.WithStack(entry.E)) {
					break
				}
			} else {
				if s.objects.Has(entry.T.K) {
					if !yield(entry.T.K, nil) {
						break
					}
				} else {
					if err := s.sourceObjects.SubDel(path, entry.T.K); err != nil {
						if !yield("", juicemud.WithStack(err)) {
							break
						}
					}
				}
			}
		}
	}
}

func (s *Storage) ChangeSource(ctx context.Context, obj *structs.Object, newSourcePath string) error {
	if obj.PostUnlock == nil {
		return errors.Errorf("can't set source of an object unknown to storage: %+v", obj)
	}

	if err := s.sourceObjects.SubSet(newSourcePath, obj.Unsafe.Id, nil); err != nil {
		return juicemud.WithStack(err)
	}

	oldSourcePath := obj.GetSourcePath()
	if oldSourcePath == "" {
		return errors.Errorf("can't change the source of an object that doesn't have a source: %+v", obj)
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

// UNSAFEEnsureObject creates an object if it doesn't exist, bypassing normal validation.
// Used only during initialization to bootstrap the world.
func (s *Storage) UNSAFEEnsureObject(ctx context.Context, obj *structs.Object) error {
	if err := s.sourceObjects.SubSet(obj.GetSourcePath(), obj.GetId(), nil); err != nil {
		return juicemud.WithStack(err)
	}
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
	if (fileSync.Remove == "") == (fileSync.Set == "") {
		return errors.Errorf("invalid FileSync %+v: Remove == \"\" and Set == \"\"", fileSync)
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
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, juicemud.WithStack(err)
	}
	if len(b) < 8 {
		return 0, errors.New("corrupted modtime data")
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

// sync processes pending file operations from the FileSync table to the actual storage.
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
		if _, err := s.sql.ExecContext(ctx, "DELETE FROM FileSync WHERE Id = ?", oldestSync.Id); err != nil && !errors.Is(err, os.ErrNotExist) {
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
	ReadGroup  int64 `sqly:"index"`
	WriteGroup int64 `sqly:"index"`
}

func (s *Storage) ChwriteFile(ctx context.Context, path string, writer string) error {
	var callerRef AuditRef
	var oldGroupRef, newGroupRef *AuditRef
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		file, err := s.loadFile(ctx, tx, path)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if err := s.CheckCallerAccessToGroupID(ctx, file.WriteGroup); err != nil {
			return juicemud.WithStack(err)
		}
		oldGroup, err := s.loadGroupByID(ctx, tx, file.WriteGroup)
		if err != nil {
			return juicemud.WithStack(err)
		}
		oldGroupRef = RefPtr(oldGroup.Id, oldGroup.Name)
		wg, err := s.loadGroupByName(ctx, tx, writer)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if err := s.CheckCallerAccessToGroupID(ctx, wg.Id); err != nil {
			return juicemud.WithStack(err)
		}
		newGroupRef = RefPtr(wg.Id, wg.Name)
		callerRef = s.callerRef(ctx)
		file.WriteGroup = wg.Id
		if err := tx.Upsert(ctx, file, true); err != nil {
			return juicemud.WithStack(err)
		}
		return nil
	}); err != nil {
		return juicemud.WithStack(err)
	}
	// Only audit if the group actually changed.
	// Both refs are guaranteed non-nil with valid IDs since the transaction succeeded.
	if *oldGroupRef.ID != *newGroupRef.ID {
		s.AuditLog(ctx, "FILE_CHMOD", AuditFileChmod{
			Caller:     callerRef,
			Path:       path,
			Permission: "write",
			OldGroup:   oldGroupRef,
			NewGroup:   newGroupRef,
		})
	}
	return nil
}

func (s *Storage) ChreadFile(ctx context.Context, path string, reader string) error {
	var callerRef AuditRef
	var oldGroupRef, newGroupRef *AuditRef
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		file, err := s.loadFile(ctx, tx, path)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if err := s.CheckCallerAccessToGroupID(ctx, file.WriteGroup); err != nil {
			return juicemud.WithStack(err)
		}
		oldGroup, err := s.loadGroupByID(ctx, tx, file.ReadGroup)
		if err != nil {
			return juicemud.WithStack(err)
		}
		oldGroupRef = RefPtr(oldGroup.Id, oldGroup.Name)
		rg, err := s.loadGroupByName(ctx, tx, reader)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if err := s.CheckCallerAccessToGroupID(ctx, rg.Id); err != nil {
			return juicemud.WithStack(err)
		}
		newGroupRef = RefPtr(rg.Id, rg.Name)
		callerRef = s.callerRef(ctx)
		file.ReadGroup = rg.Id
		if err := tx.Upsert(ctx, file, true); err != nil {
			return juicemud.WithStack(err)
		}
		return nil
	}); err != nil {
		return juicemud.WithStack(err)
	}
	// Only audit if the group actually changed.
	// Both refs are guaranteed non-nil with valid IDs since the transaction succeeded.
	if *oldGroupRef.ID != *newGroupRef.ID {
		s.AuditLog(ctx, "FILE_CHMOD", AuditFileChmod{
			Caller:     callerRef,
			Path:       path,
			Permission: "read",
			OldGroup:   oldGroupRef,
			NewGroup:   newGroupRef,
		})
	}
	return nil
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
	if created {
		s.AuditLog(ctx, "FILE_CREATE", AuditFileCreate{
			Caller: s.callerRef(ctx),
			Path:   path,
			IsDir:  false,
		})
	}
	return file, created, nil
}

func (s *Storage) MoveFile(ctx context.Context, oldPath string, newPath string) error {
	callerRef := s.callerRef(ctx)
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
	s.AuditLog(ctx, "FILE_MOVE", AuditFileMove{
		Caller:  callerRef,
		OldPath: oldPath,
		NewPath: newPath,
	})
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
	if err := row.Scan(&count); err != nil {
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

type httpError struct {
	err  error
	code int
}

func (h httpError) HTTPError() (int, string) {
	return h.code, h.err.Error()
}

func (h httpError) Error() string {
	return h.err.Error()
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
	if count, err := s.sourceObjects.SubCount(path); err != nil {
		return juicemud.WithStack(err)
	} else if count > 0 {
		return httpError{code: 422, err: errors.Errorf("%q is used by %v objects", path, count)}
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
	var isDir bool
	var deleted bool
	callerRef := s.callerRef(ctx)
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		// Check if file exists and get its type before deleting
		file, err := s.loadFile(ctx, tx, path)
		if errors.Is(err, os.ErrNotExist) {
			deleted = false
			return nil
		}
		if err != nil {
			return juicemud.WithStack(err)
		}
		isDir = file.Dir
		if err := s.delFileIfExists(ctx, tx, path, true); err != nil {
			return juicemud.WithStack(err)
		}
		deleted = true
		return nil
	}); err != nil {
		return juicemud.WithStack(err)
	}
	if deleted {
		s.AuditLog(ctx, "FILE_DELETE", AuditFileDelete{
			Caller: callerRef,
			Path:   path,
			IsDir:  isDir,
		})
	}
	return s.sync(ctx)
}

func (s *Storage) CreateDir(ctx context.Context, path string) error {
	var created bool
	callerRef := s.callerRef(ctx)
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		if existing, err := s.loadFile(ctx, tx, path); err == nil {
			if !existing.Dir {
				return errors.Wrapf(os.ErrExist, "%q already exists, is not directory", path)
			}
			created = false
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
			Parent:     parent.Id,
			Path:       path,
			Name:       filepath.Base(path),
			Dir:        true,
			ReadGroup:  parent.ReadGroup,
			WriteGroup: parent.WriteGroup,
		}
		if err := tx.Upsert(ctx, file, true); err != nil {
			return juicemud.WithStack(err)
		}
		created = true
		return nil
	}); err != nil {
		return juicemud.WithStack(err)
	}
	if created {
		s.AuditLog(ctx, "FILE_CREATE", AuditFileCreate{
			Caller: callerRef,
			Path:   path,
			IsDir:  true,
		})
	}
	return nil
}

type Group struct {
	Id         int64  `sqly:"pkey"`
	Name       string `sqly:"unique"`
	OwnerGroup int64  `sqly:"index"`
	Supergroup bool
}

type Groups []Group

func (g Groups) Len() int {
	return len(g)
}

func (g Groups) Swap(i, j int) {
	g[i], g[j] = g[j], g[i]
}

func (g Groups) Less(i, j int) bool {
	return strings.Compare(g[i].Name, g[j].Name) < 0
}

func (s *Storage) loadGroupByName(ctx context.Context, db sqlx.QueryerContext, name string) (*Group, error) {
	if name == "" {
		return nil, errors.New("group name cannot be empty")
	}
	result := &Group{}
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

// CallerAccessToGroupID checks if the context's authenticated user belongs to the group.
// Returns true for the main context (server startup) which bypasses access control.
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

type Source struct {
	Path    string
	Content string
}

func (s *Storage) EachSource(ctx context.Context) iter.Seq2[Source, error] {
	return func(yield func(Source, error) bool) {
		for entry, err := range s.sources.Each() {
			if !yield(Source{
				Path:    entry.K,
				Content: string(entry.V),
			}, err) {
				break
			}
		}
	}
}

func (s *Storage) EachObject(_ context.Context) iter.Seq2[*structs.Object, error] {
	return s.objects.Each()
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

// AddUserToGroup adds a user to a group by name.
// Caller must be in the group's OwnerGroup (or be an Owner user).
// This operation is idempotent: adding an already-present member succeeds silently.
// Note: RemoveUserFromGroup is NOT idempotent and errors if user is not a member.
func (s *Storage) AddUserToGroup(ctx context.Context, user *User, groupName string) error {
	if user == nil {
		return errors.New("user cannot be nil")
	}
	var callerRef AuditRef
	var groupRef AuditRef
	var added bool
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		group, err := s.loadGroupByName(ctx, tx, groupName)
		if err != nil {
			return juicemud.WithStack(err)
		}
		groupRef = Ref(group.Id, group.Name)

		// Check permission: caller must be in OwnerGroup (or be Owner user)
		caller, callerExists := AuthenticatedUser(ctx)
		if !callerExists && !juicemud.IsMainContext(ctx) {
			return errors.New("no authenticated user in context")
		}
		callerRef = SystemRef()
		if callerExists {
			callerRef = Ref(caller.Id, caller.Name)
			if !caller.Owner {
				if group.OwnerGroup == 0 {
					return errors.New("only Owner users can modify groups with OwnerGroup=0")
				}
				has, err := s.userAccessToGroupIDTx(ctx, tx, caller, group.OwnerGroup)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if !has {
					return errors.New("not a member of OwnerGroup")
				}
			}
		}

		// Check if user is already a member (idempotent operation)
		existing := &GroupMember{}
		err = getSQL(ctx, tx, existing, "SELECT * FROM GroupMember WHERE `User` = ? AND `Group` = ?", user.Id, group.Id)
		if err == nil {
			added = false
			return nil // Already a member - success (no audit log for idempotent no-op)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}

		member := &GroupMember{
			User:  user.Id,
			Group: group.Id,
		}
		if err := tx.Upsert(ctx, member, false); err != nil {
			return juicemud.WithStack(err)
		}
		added = true
		return nil
	}); err != nil {
		return juicemud.WithStack(err)
	}

	if added {
		s.audit.Log(ctx, "MEMBER_ADD", AuditMemberAdd{
			Caller: callerRef,
			Added:  Ref(user.Id, user.Name),
			Group:  groupRef,
		})
	}
	return nil
}

// validGroupName checks if a group name meets the naming constraints.
// Rules: 1-16 chars, starts with letter, rest can be letters/digits/hyphen/underscore.
// Reserved names like "owner" are rejected.
func validGroupName(name string) bool {
	if len(name) < 1 || len(name) > 16 {
		return false
	}
	if name == "owner" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
				return false
			}
		} else {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
				return false
			}
		}
	}
	return true
}

// validateGroup checks that a group's state satisfies all invariants.
// This should be called before committing any group create or edit operation.
// Invariants checked:
//   - Name is valid (1-16 chars, starts with letter, not reserved)
//   - OwnerGroup is 0 or references an existing group
//   - No self-ownership (OwnerGroup != Id)
//   - No cycles in the OwnerGroup chain
//
// Permission checks are NOT done here - they belong in each operation.
func (s *Storage) validateGroup(ctx context.Context, tx *sqly.Tx, g *Group) error {
	// Name constraints
	if !validGroupName(g.Name) {
		return errors.Errorf("invalid group name %q", g.Name)
	}

	// OwnerGroup must exist (if non-zero)
	if g.OwnerGroup != 0 {
		if _, err := s.loadGroupByID(ctx, tx, g.OwnerGroup); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return errors.Errorf("OwnerGroup %d does not exist", g.OwnerGroup)
			}
			return juicemud.WithStack(err)
		}
	}

	// No self-ownership (only relevant for existing groups)
	if g.Id != 0 && g.OwnerGroup == g.Id {
		return errors.New("group cannot own itself")
	}

	// No cycles (only relevant for existing groups)
	if g.Id != 0 {
		hasCycle, err := s.detectCycle(ctx, tx, g.Id, g.OwnerGroup)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if hasCycle {
			return errors.New("would create ownership cycle")
		}
	}

	return nil
}

// detectCycle checks if setting groupID's OwnerGroup to newOwner would create a cycle.
// Returns (true, nil) if a cycle would be created, (false, nil) if no cycle,
// or (false, error) if a database error occurred.
func (s *Storage) detectCycle(ctx context.Context, tx *sqly.Tx, groupID, newOwner int64) (bool, error) {
	if newOwner == 0 {
		return false, nil
	}
	visited := map[int64]bool{groupID: true}
	current := newOwner

	for current != 0 {
		if visited[current] {
			return true, nil
		}
		visited[current] = true

		group, err := s.loadGroupByID(ctx, tx, current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil // Broken chain (missing group), no cycle
			}
			return false, juicemud.WithStack(err) // Actual database error
		}
		current = group.OwnerGroup
	}
	return false, nil
}

// userAccessToGroupIDTx checks if a user has access to a group within a transaction.
func (s *Storage) userAccessToGroupIDTx(ctx context.Context, tx *sqly.Tx, user *User, groupID int64) (bool, error) {
	if user.Owner {
		return true, nil
	}
	m := &GroupMember{}
	if err := getSQL(ctx, tx, m, "SELECT * FROM GroupMember WHERE User = ? AND `Group` = ?", user.Id, groupID); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, juicemud.WithStack(err)
	}
	return true, nil
}

// CreateGroup creates a new group with the specified owner.
// ownerGroupName can be "owner" (or empty) for OwnerGroup=0, otherwise must be an existing Supergroup the caller is in.
func (s *Storage) CreateGroup(ctx context.Context, name string, ownerGroupName string, supergroup bool) error {
	var callerRef AuditRef
	var groupRef AuditRef
	var ownerRef AuditRef
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		// Get caller
		caller, callerExists := AuthenticatedUser(ctx)
		if !callerExists && !juicemud.IsMainContext(ctx) {
			return errors.New("no authenticated user in context")
		}
		callerRef = SystemRef()
		if callerExists {
			callerRef = Ref(caller.Id, caller.Name)
		}

		// Check if group already exists
		if _, err := s.loadGroupByName(ctx, tx, name); err == nil {
			return errors.Errorf("group %q already exists", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}

		// Resolve OwnerGroup and check permissions
		var ownerGroupID int64
		if ownerGroupName == "" || ownerGroupName == "owner" {
			// OwnerGroup=0 requires Owner user or main context
			if callerExists && !caller.Owner {
				return errors.New("only Owner users can create groups with OwnerGroup=0")
			}
			ownerGroupID = 0
			ownerRef = OwnerRef()
		} else {
			ownerGroup, err := s.loadGroupByName(ctx, tx, ownerGroupName)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return errors.Errorf("OwnerGroup %q does not exist", ownerGroupName)
				}
				return juicemud.WithStack(err)
			}
			// For non-Owner users: must be in OwnerGroup and it must be a Supergroup
			if callerExists && !caller.Owner {
				if !ownerGroup.Supergroup {
					return errors.Errorf("OwnerGroup %q is not a Supergroup", ownerGroupName)
				}
				has, err := s.userAccessToGroupIDTx(ctx, tx, caller, ownerGroup.Id)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if !has {
					return errors.Errorf("not a member of OwnerGroup %q", ownerGroupName)
				}
			}
			ownerGroupID = ownerGroup.Id
			ownerRef = Ref(ownerGroup.Id, ownerGroup.Name)
		}

		group := &Group{
			Name:       name,
			OwnerGroup: ownerGroupID,
			Supergroup: supergroup,
		}

		// Validate invariants
		if err := s.validateGroup(ctx, tx, group); err != nil {
			return juicemud.WithStack(err)
		}

		if err := tx.Upsert(ctx, group, false); err != nil {
			return juicemud.WithStack(err)
		}
		groupRef = Ref(group.Id, group.Name)
		return nil
	}); err != nil {
		return juicemud.WithStack(err)
	}

	s.audit.Log(ctx, "GROUP_CREATE", AuditGroupCreate{
		Caller:     callerRef,
		Group:      groupRef,
		Owner:      ownerRef,
		Supergroup: supergroup,
	})
	return nil
}

// DeleteGroup deletes a group by name.
// The group must be empty (no members) and unreferenced (no groups or files reference it).
// Caller must be in a Supergroup that owns this group (or be an Owner user).
func (s *Storage) DeleteGroup(ctx context.Context, name string) error {
	var callerRef AuditRef
	var groupRef AuditRef
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		group, err := s.loadGroupByName(ctx, tx, name)
		if err != nil {
			return juicemud.WithStack(err)
		}
		groupRef = Ref(group.Id, group.Name)

		// Check permission: caller must be in OwnerGroup which must be a Supergroup
		caller, callerExists := AuthenticatedUser(ctx)
		if !callerExists && !juicemud.IsMainContext(ctx) {
			return errors.New("no authenticated user in context")
		}
		callerRef = SystemRef()
		if callerExists {
			callerRef = Ref(caller.Id, caller.Name)
			if !caller.Owner {
				if group.OwnerGroup == 0 {
					return errors.New("only Owner users can delete groups with OwnerGroup=0")
				}
				owner, err := s.loadGroupByID(ctx, tx, group.OwnerGroup)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if !owner.Supergroup {
					return errors.Errorf("OwnerGroup %q is not a Supergroup", owner.Name)
				}
				has, err := s.userAccessToGroupIDTx(ctx, tx, caller, group.OwnerGroup)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if !has {
					return errors.Errorf("not a member of OwnerGroup %q", owner.Name)
				}
			}
		}

		// Check no members
		var memberCount int
		if err := tx.GetContext(ctx, &memberCount, "SELECT COUNT(*) FROM GroupMember WHERE `Group` = ?", group.Id); err != nil {
			return juicemud.WithStack(err)
		}
		if memberCount > 0 {
			return errors.Errorf("group has %d members", memberCount)
		}

		// Check no dependent groups (groups that use this as OwnerGroup)
		var depCount int
		if err := tx.GetContext(ctx, &depCount, "SELECT COUNT(*) FROM `Group` WHERE OwnerGroup = ?", group.Id); err != nil {
			return juicemud.WithStack(err)
		}
		if depCount > 0 {
			return errors.Errorf("%d groups use this as OwnerGroup", depCount)
		}

		// Check no file references
		var fileCount int
		if err := tx.GetContext(ctx, &fileCount, "SELECT COUNT(*) FROM File WHERE ReadGroup = ? OR WriteGroup = ?", group.Id, group.Id); err != nil {
			return juicemud.WithStack(err)
		}
		if fileCount > 0 {
			return errors.Errorf("%d files reference this group", fileCount)
		}

		// All checks passed - delete
		_, err = tx.ExecContext(ctx, "DELETE FROM `Group` WHERE Id = ?", group.Id)
		return juicemud.WithStack(err)
	}); err != nil {
		return juicemud.WithStack(err)
	}

	s.audit.Log(ctx, "GROUP_DELETE", AuditGroupDelete{
		Caller: callerRef,
		Group:  groupRef,
	})
	return nil
}

// groupEditAudit holds info needed for audit logging after a group edit transaction commits.
type groupEditAudit struct {
	Caller       AuditRef
	Group        AuditRef
	OldName      string
	NewName      string
	OldOwnerRef  *AuditRef
	NewOwnerRef  *AuditRef
	OldSuper     bool
	NewSuper     bool
	NameChanged  bool
	OwnerChanged bool
	SuperChanged bool
}

// editGroup updates a group's properties within a transaction.
// The group is identified by its Id field. Name, OwnerGroup, and Supergroup can be modified.
// Returns audit info to be logged after transaction commits.
// Permission rules:
//   - Caller must be in current OwnerGroup (or Owner user)
//   - Changing OwnerGroup: new owner must be a Supergroup caller is in (or 0 if Owner)
//   - Changing Supergroup: current OwnerGroup must be a Supergroup (or caller is Owner user)
func (s *Storage) editGroup(ctx context.Context, tx *sqly.Tx, updated *Group, audit *groupEditAudit) error {
	// Load current state
	current, err := s.loadGroupByID(ctx, tx, updated.Id)
	if err != nil {
		return juicemud.WithStack(err)
	}

	// Get caller
	caller, callerExists := AuthenticatedUser(ctx)
	if !callerExists && !juicemud.IsMainContext(ctx) {
		return errors.New("no authenticated user in context")
	}
	audit.Caller = SystemRef()
	if callerExists {
		audit.Caller = Ref(caller.Id, caller.Name)
	}
	audit.Group = Ref(current.Id, current.Name)

	// Check base permission: caller must be in current OwnerGroup
	if callerExists && !caller.Owner {
		if current.OwnerGroup == 0 {
			return errors.New("only Owner users can modify groups with OwnerGroup=0")
		}
		has, err := s.userAccessToGroupIDTx(ctx, tx, caller, current.OwnerGroup)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if !has {
			return errors.New("not a member of OwnerGroup")
		}

		// If changing Supergroup flag, current OwnerGroup must be a Supergroup
		if updated.Supergroup != current.Supergroup {
			owner, err := s.loadGroupByID(ctx, tx, current.OwnerGroup)
			if err != nil {
				return juicemud.WithStack(err)
			}
			if !owner.Supergroup {
				return errors.Errorf("OwnerGroup %q is not a Supergroup", owner.Name)
			}
		}

		// If changing OwnerGroup, caller must be in new OwnerGroup and it must be a Supergroup
		if updated.OwnerGroup != current.OwnerGroup {
			if updated.OwnerGroup == 0 {
				return errors.New("only Owner users can set OwnerGroup to 0")
			}
			newOwner, err := s.loadGroupByID(ctx, tx, updated.OwnerGroup)
			if err != nil {
				return juicemud.WithStack(err)
			}
			if !newOwner.Supergroup {
				return errors.Errorf("new OwnerGroup %q is not a Supergroup", newOwner.Name)
			}
			has, err := s.userAccessToGroupIDTx(ctx, tx, caller, updated.OwnerGroup)
			if err != nil {
				return juicemud.WithStack(err)
			}
			if !has {
				return errors.Errorf("not a member of new OwnerGroup %q", newOwner.Name)
			}
		}
	}

	// Validate the updated group state (checks name, OwnerGroup exists, cycles, etc.)
	if err := s.validateGroup(ctx, tx, updated); err != nil {
		return juicemud.WithStack(err)
	}

	// Check if new name already exists (different from current)
	if updated.Name != current.Name {
		if existing, err := s.loadGroupByName(ctx, tx, updated.Name); err == nil && existing.Id != updated.Id {
			return errors.Errorf("group %q already exists", updated.Name)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return juicemud.WithStack(err)
		}
	}

	if err := tx.Upsert(ctx, updated, true); err != nil {
		return juicemud.WithStack(err)
	}

	// Populate audit info for logging after commit
	if updated.Name != current.Name {
		audit.OldName = current.Name
		audit.NewName = updated.Name
		audit.NameChanged = true
	}
	if updated.OwnerGroup != current.OwnerGroup {
		// Build owner refs with names
		if current.OwnerGroup == 0 {
			audit.OldOwnerRef = OwnerRefPtr()
		} else {
			oldOwner, err := s.loadGroupByID(ctx, tx, current.OwnerGroup)
			if err != nil {
				return juicemud.WithStack(err)
			}
			audit.OldOwnerRef = RefPtr(oldOwner.Id, oldOwner.Name)
		}
		if updated.OwnerGroup == 0 {
			audit.NewOwnerRef = OwnerRefPtr()
		} else {
			newOwner, err := s.loadGroupByID(ctx, tx, updated.OwnerGroup)
			if err != nil {
				return juicemud.WithStack(err)
			}
			audit.NewOwnerRef = RefPtr(newOwner.Id, newOwner.Name)
		}
		audit.OwnerChanged = true
	}
	if updated.Supergroup != current.Supergroup {
		audit.OldSuper = current.Supergroup
		audit.NewSuper = updated.Supergroup
		audit.SuperChanged = true
	}

	return nil
}

// logGroupEdit writes the GROUP_EDIT audit entry if there were changes.
func (s *Storage) logGroupEdit(ctx context.Context, audit *groupEditAudit) {
	if !audit.NameChanged && !audit.OwnerChanged && !audit.SuperChanged {
		return
	}
	entry := AuditGroupEdit{
		Caller: audit.Caller,
		Group:  audit.Group,
	}
	if audit.NameChanged {
		entry.NameFrom = audit.OldName
		entry.NameTo = audit.NewName
	}
	if audit.OwnerChanged {
		entry.OwnerFrom = audit.OldOwnerRef
		entry.OwnerTo = audit.NewOwnerRef
	}
	if audit.SuperChanged {
		entry.SupergroupFrom = &audit.OldSuper
		entry.SupergroupTo = &audit.NewSuper
	}
	s.audit.Log(ctx, "GROUP_EDIT", entry)
}

// EditGroupName renames a group.
// Caller must be in the group's OwnerGroup (or be an Owner user).
func (s *Storage) EditGroupName(ctx context.Context, currentName, newName string) error {
	var audit groupEditAudit
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		group, err := s.loadGroupByName(ctx, tx, currentName)
		if err != nil {
			return juicemud.WithStack(err)
		}
		group.Name = newName
		return s.editGroup(ctx, tx, group, &audit)
	}); err != nil {
		return juicemud.WithStack(err)
	}
	s.logGroupEdit(ctx, &audit)
	return nil
}

// EditGroupOwner changes a group's OwnerGroup.
// newOwnerName can be "owner" for OwnerGroup=0 (requires Owner user).
// Caller must be in the current OwnerGroup (or be an Owner user).
// For non-Owner users: new owner must be a Supergroup the caller is in.
func (s *Storage) EditGroupOwner(ctx context.Context, groupName, newOwnerName string) error {
	var audit groupEditAudit
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		group, err := s.loadGroupByName(ctx, tx, groupName)
		if err != nil {
			return juicemud.WithStack(err)
		}
		if newOwnerName == "" || newOwnerName == "owner" {
			group.OwnerGroup = 0
		} else {
			newOwner, err := s.loadGroupByName(ctx, tx, newOwnerName)
			if err != nil {
				return juicemud.WithStack(err)
			}
			group.OwnerGroup = newOwner.Id
		}
		return s.editGroup(ctx, tx, group, &audit)
	}); err != nil {
		return juicemud.WithStack(err)
	}
	s.logGroupEdit(ctx, &audit)
	return nil
}

// EditGroupSupergroup changes a group's Supergroup flag.
// Caller must be in the group's OwnerGroup which must be a Supergroup (or be an Owner user).
func (s *Storage) EditGroupSupergroup(ctx context.Context, groupName string, supergroup bool) error {
	var audit groupEditAudit
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		group, err := s.loadGroupByName(ctx, tx, groupName)
		if err != nil {
			return juicemud.WithStack(err)
		}
		group.Supergroup = supergroup
		return s.editGroup(ctx, tx, group, &audit)
	}); err != nil {
		return juicemud.WithStack(err)
	}
	s.logGroupEdit(ctx, &audit)
	return nil
}

// RemoveUserFromGroup removes a user from a group.
// Caller must be in the group's OwnerGroup (or be an Owner user).
// Returns an error if the user is not a member (non-idempotent).
func (s *Storage) RemoveUserFromGroup(ctx context.Context, userName string, groupName string) error {
	var callerRef AuditRef
	var removedRef AuditRef
	var groupRef AuditRef
	if err := s.sql.Write(ctx, func(tx *sqly.Tx) error {
		// Load the group
		group, err := s.loadGroupByName(ctx, tx, groupName)
		if err != nil {
			return juicemud.WithStack(err)
		}
		groupRef = Ref(group.Id, group.Name)

		// Load the user to remove
		user := &User{}
		if err := getSQL(ctx, tx, user, "SELECT * FROM User WHERE Name = ?", userName); err != nil {
			return juicemud.WithStack(err)
		}
		removedRef = Ref(user.Id, user.Name)

		// Check permission: caller must be in OwnerGroup (or be Owner user)
		caller, callerExists := AuthenticatedUser(ctx)
		if !callerExists && !juicemud.IsMainContext(ctx) {
			return errors.New("no authenticated user in context")
		}
		callerRef = SystemRef()
		if callerExists {
			callerRef = Ref(caller.Id, caller.Name)
			if !caller.Owner {
				if group.OwnerGroup == 0 {
					return errors.New("only Owner users can modify groups with OwnerGroup=0")
				}
				has, err := s.userAccessToGroupIDTx(ctx, tx, caller, group.OwnerGroup)
				if err != nil {
					return juicemud.WithStack(err)
				}
				if !has {
					return errors.New("not a member of OwnerGroup")
				}
			}
		}

		// Check user is actually a member (non-idempotent - error if not a member)
		member := &GroupMember{}
		if err := getSQL(ctx, tx, member, "SELECT * FROM GroupMember WHERE User = ? AND `Group` = ?", user.Id, group.Id); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return errors.Errorf("user %q is not a member of group %q", userName, groupName)
			}
			return juicemud.WithStack(err)
		}

		// Remove the membership
		_, err = tx.ExecContext(ctx, "DELETE FROM GroupMember WHERE User = ? AND `Group` = ?", user.Id, group.Id)
		return juicemud.WithStack(err)
	}); err != nil {
		return juicemud.WithStack(err)
	}

	s.audit.Log(ctx, "MEMBER_REMOVE", AuditMemberRemove{
		Caller:  callerRef,
		Removed: removedRef,
		Group:   groupRef,
	})
	return nil
}

// LoadGroup loads a group by name.
func (s *Storage) LoadGroup(ctx context.Context, name string) (*Group, error) {
	return s.loadGroupByName(ctx, s.sql, name)
}

// LoadGroupByID loads a group by ID.
func (s *Storage) LoadGroupByID(ctx context.Context, id int64) (*Group, error) {
	return s.loadGroupByID(ctx, s.sql, id)
}

// ListGroups returns all groups.
func (s *Storage) ListGroups(ctx context.Context) ([]Group, error) {
	result := []Group{}
	if err := s.sql.SelectContext(ctx, &result, "SELECT * FROM `Group` ORDER BY Name"); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return result, nil
}

// GroupMembers returns all users in a group.
func (s *Storage) GroupMembers(ctx context.Context, groupName string) ([]User, error) {
	group, err := s.loadGroupByName(ctx, s.sql, groupName)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	users := []User{}
	if err := s.sql.SelectContext(ctx, &users,
		"SELECT User.* FROM User INNER JOIN GroupMember ON User.Id = GroupMember.User WHERE GroupMember.`Group` = ? ORDER BY User.Name",
		group.Id); err != nil {
		return nil, juicemud.WithStack(err)
	}
	return users, nil
}
