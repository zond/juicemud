package storage

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestListUsers(t *testing.T) {
	withStorage(t, func(s *Storage) {
		ctx := context.Background()

		// Create test users with various roles
		owner := &User{Name: "alice", PasswordHash: "hash", Owner: true, Wizard: true, Object: "obj1"}
		owner.SetLastLogin(time.Now().Add(-1 * time.Hour))
		wizard := &User{Name: "bob", PasswordHash: "hash", Owner: false, Wizard: true, Object: "obj2"}
		wizard.SetLastLogin(time.Now().Add(-2 * time.Hour))
		player1 := &User{Name: "charlie", PasswordHash: "hash", Owner: false, Wizard: false, Object: "obj3"}
		player1.SetLastLogin(time.Now().Add(-3 * time.Hour))
		player2 := &User{Name: "diana", PasswordHash: "hash", Owner: false, Wizard: false, Object: "obj4"} // Never logged in

		for _, u := range []*User{owner, wizard, player1, player2} {
			if err := s.StoreUser(ctx, u, false, "test"); err != nil {
				t.Fatalf("StoreUser failed: %v", err)
			}
		}

		// Test UserFilterAll
		users, err := s.ListUsers(ctx, UserFilterAll, UserSortByName, 0)
		if err != nil {
			t.Fatalf("ListUsers failed: %v", err)
		}
		if len(users) != 4 {
			t.Errorf("UserFilterAll: got %d users, want 4", len(users))
		}

		// Test UserFilterOwners
		users, err = s.ListUsers(ctx, UserFilterOwners, UserSortByName, 0)
		if err != nil {
			t.Fatalf("ListUsers failed: %v", err)
		}
		if len(users) != 1 || users[0].Name != "alice" {
			t.Errorf("UserFilterOwners: got %v, want [alice]", users)
		}

		// Test UserFilterWizards (includes owners)
		users, err = s.ListUsers(ctx, UserFilterWizards, UserSortByName, 0)
		if err != nil {
			t.Fatalf("ListUsers failed: %v", err)
		}
		if len(users) != 2 {
			t.Errorf("UserFilterWizards: got %d users, want 2", len(users))
		}

		// Test UserFilterPlayers
		users, err = s.ListUsers(ctx, UserFilterPlayers, UserSortByName, 0)
		if err != nil {
			t.Fatalf("ListUsers failed: %v", err)
		}
		if len(users) != 2 {
			t.Errorf("UserFilterPlayers: got %d users, want 2", len(users))
		}

		// Test sorting by name
		users, err = s.ListUsers(ctx, UserFilterAll, UserSortByName, 0)
		if err != nil {
			t.Fatalf("ListUsers failed: %v", err)
		}
		if users[0].Name != "alice" || users[1].Name != "bob" {
			t.Errorf("UserSortByName: got %s, %s; want alice, bob", users[0].Name, users[1].Name)
		}

		// Test sorting by ID (creation order)
		users, err = s.ListUsers(ctx, UserFilterAll, UserSortByID, 0)
		if err != nil {
			t.Fatalf("ListUsers failed: %v", err)
		}
		if users[0].Name != "alice" {
			t.Errorf("UserSortByID: first user should be alice, got %s", users[0].Name)
		}

		// Test sorting by last login (most recent first)
		users, err = s.ListUsers(ctx, UserFilterAll, UserSortByLastLogin, 0)
		if err != nil {
			t.Fatalf("ListUsers failed: %v", err)
		}
		if users[0].Name != "alice" {
			t.Errorf("UserSortByLastLogin: first user should be alice (most recent), got %s", users[0].Name)
		}

		// Test sorting by last login ascending (stale first)
		users, err = s.ListUsers(ctx, UserFilterAll, UserSortByLastLoginAsc, 0)
		if err != nil {
			t.Fatalf("ListUsers failed: %v", err)
		}
		if users[0].Name != "diana" {
			t.Errorf("UserSortByLastLoginAsc: first user should be diana (never logged in), got %s", users[0].Name)
		}

		// Test limit
		users, err = s.ListUsers(ctx, UserFilterAll, UserSortByName, 2)
		if err != nil {
			t.Fatalf("ListUsers failed: %v", err)
		}
		if len(users) != 2 {
			t.Errorf("limit: got %d users, want 2", len(users))
		}
	})
}

func TestCountUsers(t *testing.T) {
	withStorage(t, func(s *Storage) {
		ctx := context.Background()

		// Create test users
		users := []*User{
			{Name: "alice", PasswordHash: "hash", Owner: true, Wizard: true, Object: "obj1"},
			{Name: "bob", PasswordHash: "hash", Owner: false, Wizard: true, Object: "obj2"},
			{Name: "charlie", PasswordHash: "hash", Owner: false, Wizard: false, Object: "obj3"},
			{Name: "diana", PasswordHash: "hash", Owner: false, Wizard: false, Object: "obj4"},
		}
		for _, u := range users {
			if err := s.StoreUser(ctx, u, false, "test"); err != nil {
				t.Fatalf("StoreUser failed: %v", err)
			}
		}

		// Test counts
		count, err := s.CountUsers(ctx, UserFilterAll)
		if err != nil {
			t.Fatalf("CountUsers failed: %v", err)
		}
		if count != 4 {
			t.Errorf("UserFilterAll: got %d, want 4", count)
		}

		count, err = s.CountUsers(ctx, UserFilterOwners)
		if err != nil {
			t.Fatalf("CountUsers failed: %v", err)
		}
		if count != 1 {
			t.Errorf("UserFilterOwners: got %d, want 1", count)
		}

		count, err = s.CountUsers(ctx, UserFilterWizards)
		if err != nil {
			t.Fatalf("CountUsers failed: %v", err)
		}
		if count != 2 {
			t.Errorf("UserFilterWizards: got %d, want 2", count)
		}

		count, err = s.CountUsers(ctx, UserFilterPlayers)
		if err != nil {
			t.Fatalf("CountUsers failed: %v", err)
		}
		if count != 2 {
			t.Errorf("UserFilterPlayers: got %d, want 2", count)
		}
	})
}

func TestGetMostRecentLogin(t *testing.T) {
	withStorage(t, func(s *Storage) {
		ctx := context.Background()

		// No users - should return nil
		user, err := s.GetMostRecentLogin(ctx)
		if err != nil {
			t.Fatalf("GetMostRecentLogin failed: %v", err)
		}
		if user != nil {
			t.Errorf("expected nil for empty database, got %v", user)
		}

		// Create users with different login times
		now := time.Now().UTC()
		alice := &User{Name: "alice", PasswordHash: "hash", Object: "obj1"}
		alice.SetLastLogin(now.Add(-2 * time.Hour))
		bob := &User{Name: "bob", PasswordHash: "hash", Object: "obj2"}
		bob.SetLastLogin(now.Add(-1 * time.Hour)) // Most recent
		charlie := &User{Name: "charlie", PasswordHash: "hash", Object: "obj3"} // Never logged in

		for _, u := range []*User{alice, bob, charlie} {
			if err := s.StoreUser(ctx, u, false, "test"); err != nil {
				t.Fatalf("StoreUser failed: %v", err)
			}
		}

		// Should return bob (most recent login)
		user, err = s.GetMostRecentLogin(ctx)
		if err != nil {
			t.Fatalf("GetMostRecentLogin failed: %v", err)
		}
		if user == nil {
			t.Fatal("expected user, got nil")
		}
		if user.Name != "bob" {
			t.Errorf("expected bob, got %s", user.Name)
		}
	})
}
