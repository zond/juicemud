package fs

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"time"

	"github.com/pkg/errors"
	"github.com/zond/juicemud/dav"
	"github.com/zond/juicemud/storage"
)

type Fs struct {
	Storage *storage.Storage
}

func (f *Fs) Read(ctx context.Context, name string) (io.ReadCloser, error) {
	file, err := f.Storage.GetFile(ctx, name)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	content, err := f.Storage.GetSource(ctx, file.Id)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return io.NopCloser(bytes.NewBuffer(content)), nil
}

type writeBuffer struct {
	bytes.Buffer
	ctx context.Context
	id  int64
	s   *storage.Storage
}

func (w *writeBuffer) Write(b []byte) (int, error) {
	i, e := w.Buffer.Write(b)
	return i, e
}

func (w *writeBuffer) Close() error {
	if err := w.s.SetSource(w.ctx, w.id, w.Bytes()); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

func (f *Fs) Write(ctx context.Context, name string) (io.WriteCloser, error) {
	file, err := f.Storage.EnsureFile(ctx, name)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return &writeBuffer{
		Buffer: bytes.Buffer{},
		ctx:    ctx,
		id:     file.Id,
		s:      f.Storage,
	}, nil
}

func (f *Fs) stat(ctx context.Context, file *storage.File, path string) (*dav.FileInfo, error) {
	if file.Id == 0 {
		return &dav.FileInfo{
			Name:    "/",
			Size:    0,
			Mode:    0777,
			ModTime: time.Time{},
			IsDir:   true,
		}, nil
	}
	content, err := f.Storage.GetSource(ctx, file.Id)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return &dav.FileInfo{
		Name:    path,
		Size:    int64(len(content)),
		Mode:    0777,
		ModTime: file.ModTime.Time(),
		IsDir:   file.Dir,
	}, nil
}

func (f *Fs) Stat(ctx context.Context, name string) (*dav.FileInfo, error) {
	file, err := f.Storage.GetFile(ctx, name)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return f.stat(ctx, file, name)
}

func (f *Fs) Remove(ctx context.Context, name string) error {
	file, err := f.Storage.GetFile(ctx, name)
	if err != nil {
		return errors.WithStack(err)
	}
	if !file.Dir {
		if err := f.Storage.DelSource(ctx, file.Id); err != nil {
			return errors.WithStack(err)
		}
	}
	return f.Storage.DelFile(ctx, file)
}

func (f *Fs) Mkdir(ctx context.Context, name string) error {
	return f.Storage.CreateDir(ctx, name)
}

func (f *Fs) List(ctx context.Context, name string) ([]*dav.FileInfo, error) {
	file, err := f.Storage.GetFile(ctx, name)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	children, err := f.Storage.GetChildren(ctx, file.Id)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	result := make([]*dav.FileInfo, len(children))
	for index, child := range children {
		if result[index], err = f.stat(ctx, &child, filepath.Join(name, child.Name)); err != nil {
			return nil, errors.WithStack(err)
		}
	}
	return result, nil
}

func (f *Fs) Rename(ctx context.Context, oldName, newName string) error {
	return f.Storage.MoveFile(ctx, oldName, newName)
}