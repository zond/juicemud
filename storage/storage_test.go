package storage

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/bxcodec/faker/v4"
	"github.com/bxcodec/faker/v4/pkg/options"
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

func withStorage(t *testing.T, f func(*Storage)) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "storage_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithCancel(context.Background())

	s, err := New(ctx, tmpDir)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	// Close in correct order: cancel context first to stop background goroutines
	defer func() {
		cancel()
		s.Close()
	}()

	f(s)
}

func TestValidateAndSwitchSources(t *testing.T) {
	withStorage(t, func(s *Storage) {
		ctx := context.Background()

		// Create source directories
		srcV1 := filepath.Join(s.SourcesDir(), "..", "v1")
		srcV2 := filepath.Join(s.SourcesDir(), "..", "v2")
		if err := os.MkdirAll(srcV1, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(srcV2, 0755); err != nil {
			t.Fatal(err)
		}

		// Create a source file in v1
		sourcePath := "/test.js"
		if err := os.WriteFile(filepath.Join(srcV1, sourcePath), []byte("// test"), 0644); err != nil {
			t.Fatal(err)
		}

		// Create an object using this source
		obj := &structs.Object{
			Unsafe: &structs.ObjectDO{
				Id:         "test-obj",
				Location:   "",
				SourcePath: sourcePath,
			},
		}
		if err := s.UNSAFEEnsureObject(ctx, obj); err != nil {
			t.Fatal(err)
		}

		// Try switching to v2 which doesn't have the source - should fail
		missing, err := s.ValidateAndSwitchSources(ctx, srcV2)
		if err != nil {
			t.Fatal(err)
		}
		if len(missing) != 1 {
			t.Errorf("got %d missing, want 1", len(missing))
		}
		if len(missing) > 0 && missing[0].Path != sourcePath {
			t.Errorf("got missing path %q, want %q", missing[0].Path, sourcePath)
		}

		// Sources dir should NOT have changed
		if s.SourcesDir() == srcV2 {
			t.Error("sources dir changed despite validation failure")
		}

		// Create the source file in v2
		if err := os.WriteFile(filepath.Join(srcV2, sourcePath), []byte("// test v2"), 0644); err != nil {
			t.Fatal(err)
		}

		// Now switch should succeed
		missing, err = s.ValidateAndSwitchSources(ctx, srcV2)
		if err != nil {
			t.Fatal(err)
		}
		if len(missing) != 0 {
			t.Errorf("got %d missing, want 0", len(missing))
		}

		// Sources dir should have changed
		if s.SourcesDir() != srcV2 {
			t.Errorf("got sources dir %q, want %q", s.SourcesDir(), srcV2)
		}
	})
}
