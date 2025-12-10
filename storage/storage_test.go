package storage

import (
	"context"
	"log"
	"testing"

	"github.com/bxcodec/faker/v4"
	"github.com/bxcodec/faker/v4/pkg/options"
	"github.com/zond/juicemud"
	"github.com/zond/juicemud/structs"
	"rogchap.com/v8go"

	goccy "github.com/goccy/go-json"
)

var (
	fakeObject structs.Object
)

func init() {
	err := faker.FakeData(&fakeObject, options.WithRandomMapAndSliceMaxSize(10))
	if err != nil {
		log.Panic(err)
	}
}

func BenchmarkV8JSON(b *testing.B) {
	b.StopTimer()
	iso := v8go.NewIsolate()
	ctx := v8go.NewContext(iso)
	by, err := goccy.Marshal(&fakeObject)
	if err != nil {
		b.Fatal(err)
	}
	js := string(by)
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		o, err := v8go.JSONParse(ctx, js)
		if err != nil {
			b.Fatal(err)
		}
		ser, err := v8go.JSONStringify(ctx, o)
		if err != nil {
			b.Fatal(err)
		}
		js = ser
	}
}

func BenchmarkBenc(b *testing.B) {
	o := &structs.Object{}
	for i := 0; i < b.N; i++ {
		by := make([]byte, fakeObject.Size())
		fakeObject.Marshal(by)
		if err := o.Unmarshal(by); err != nil {
			b.Fatal(err)
		}
	}

}

func BenchmarkGoccy(b *testing.B) {
	o := &structs.Object{}
	for i := 0; i < b.N; i++ {
		by, err := goccy.Marshal(&fakeObject)
		if err != nil {
			b.Fatal(err)
		}
		if err := goccy.Unmarshal(by, o); err != nil {
			b.Fatal(err)
		}
	}
}

// === File Operation Tests ===

func TestLoadSource_FileWithoutContent(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create root directory
	if err := s.CreateDir(juicemud.MakeMainContext(context.Background()), "/"); err != nil {
		t.Fatal(err)
	}

	// Create a file using EnsureFile (no content stored)
	if _, _, err := s.EnsureFile(ctx, "/test.js"); err != nil {
		t.Fatal(err)
	}

	// Read the file (should succeed and return empty content)
	content, modTime, err := s.LoadSource(ctx, "/test.js")
	if err != nil {
		t.Fatalf("LoadSource failed on file without content: %v", err)
	}
	if len(content) != 0 {
		t.Errorf("Expected empty content, got %d bytes", len(content))
	}
	if modTime != 0 {
		t.Errorf("Expected modTime 0, got %d", modTime)
	}
}

func TestDelFile_FileWithoutContent(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create root directory
	if err := s.CreateDir(juicemud.MakeMainContext(context.Background()), "/"); err != nil {
		t.Fatal(err)
	}

	// Create a file using EnsureFile (no content stored)
	if _, _, err := s.EnsureFile(ctx, "/test.js"); err != nil {
		t.Fatal(err)
	}

	// Delete the file (should succeed even without content)
	if err := s.DelFile(ctx, "/test.js"); err != nil {
		t.Fatalf("DelFile failed on file without content: %v", err)
	}
}

func TestMoveFile_FileWithoutContent(t *testing.T) {
	s, cleanup := testStorage(t)
	defer cleanup()

	owner := createTestUser(t, s, "owner", true)
	ctx := userContext(owner)

	// Create root directory
	if err := s.CreateDir(juicemud.MakeMainContext(context.Background()), "/"); err != nil {
		t.Fatal(err)
	}

	// Create a file using EnsureFile (no content stored)
	if _, _, err := s.EnsureFile(ctx, "/old.js"); err != nil {
		t.Fatal(err)
	}

	// Move the file (should succeed even without content)
	if err := s.MoveFile(ctx, "/old.js", "/new.js"); err != nil {
		t.Fatalf("MoveFile failed on file without content: %v", err)
	}

	// Verify the old file no longer exists
	if _, err := s.LoadFile(ctx, "/old.js"); err == nil {
		t.Error("Old file should not exist after move")
	}

	// Verify the new file exists
	file, err := s.LoadFile(ctx, "/new.js")
	if err != nil {
		t.Fatalf("New file should exist after move: %v", err)
	}
	if file.Name != "new.js" {
		t.Errorf("Expected file name 'new.js', got %q", file.Name)
	}
}
