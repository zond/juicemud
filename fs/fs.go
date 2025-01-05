package fs

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"time"

	"github.com/zond/juicemud"
	"github.com/zond/juicemud/dav"
	"github.com/zond/juicemud/storage"
)

type Fs struct {
	Storage *storage.Storage
}

func (f *Fs) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	content, err := f.Storage.GetSource(ctx, path)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return io.NopCloser(bytes.NewBuffer(content)), nil
}

type writeBuffer struct {
	bytes.Buffer
	ctx context.Context
	f   *storage.File
	s   *storage.Storage
}

func (w *writeBuffer) Write(b []byte) (int, error) {
	i, e := w.Buffer.Write(b)
	return i, e
}

func (w *writeBuffer) Close() error {
	if err := w.s.SetSource(w.ctx, w.f.Path, w.Bytes()); err != nil {
		return juicemud.WithStack(err)
	}
	return nil
}

func (f *Fs) Write(ctx context.Context, path string) (io.WriteCloser, error) {
	file, err := f.Storage.EnsureFile(ctx, path)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return &writeBuffer{
		Buffer: bytes.Buffer{},
		ctx:    ctx,
		f:      file,
		s:      f.Storage,
	}, nil
}

func (f *Fs) stat(ctx context.Context, file *storage.File) (*dav.FileInfo, error) {
	if file.Id == 0 {
		return &dav.FileInfo{
			Name:    "/",
			Size:    0,
			Mode:    0777,
			ModTime: time.Time{},
			IsDir:   true,
		}, nil
	}
	content, err := f.Storage.GetSource(ctx, file.Path)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return &dav.FileInfo{
		Name:    file.Path,
		Size:    int64(len(content)),
		Mode:    0777,
		ModTime: file.ModTime.Time(),
		IsDir:   file.Dir,
	}, nil
}

func (f *Fs) Stat(ctx context.Context, path string) (*dav.FileInfo, error) {
	file, err := f.Storage.GetFile(ctx, path)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	return f.stat(ctx, file)
}

func (f *Fs) Remove(ctx context.Context, path string) error {
	return f.Storage.DelFile(ctx, path)
}

func (f *Fs) Mkdir(ctx context.Context, path string) error {
	return f.Storage.CreateDir(ctx, path)
}

func (f *Fs) List(ctx context.Context, path string) ([]*dav.FileInfo, error) {
	file, err := f.Storage.GetFile(ctx, path)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	children, err := f.Storage.GetChildren(ctx, file.Id)
	if err != nil {
		return nil, juicemud.WithStack(err)
	}
	result := make([]*dav.FileInfo, len(children))
	for index, child := range children {
		if result[index], err = f.stat(ctx, &child); err != nil {
			return nil, juicemud.WithStack(err)
		}
	}
	return result, nil
}

func (f *Fs) Rename(ctx context.Context, oldPath string, newURL *url.URL) error {
	return f.Storage.MoveFile(ctx, oldPath, newURL.Path)
}
