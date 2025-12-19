package game

import (
	"strings"
	"testing"
)

func TestValidateUsername(t *testing.T) {
	tests := []struct {
		name     string
		username string
		wantErr  bool
	}{
		// Valid usernames
		{"single letter", "a", false},
		{"short name", "abc", false},
		{"mixed case", "UserName", false},
		{"with numbers", "user123", false},
		{"with hyphen", "test-name", false},
		{"with underscore", "test_name", false},
		{"max length 16", "abcdefghijklmnop", false},
		{"mixed valid chars", "User-Name_123", false},

		// Invalid usernames
		{"empty", "", true},
		{"starts with number", "1user", true},
		{"starts with hyphen", "-user", true},
		{"starts with underscore", "_user", true},
		{"too long 17 chars", "abcdefghijklmnopq", true},
		{"contains space", "user name", true},
		{"contains dot", "user.name", true},
		{"contains at", "user@name", true},
		{"only numbers", "123456", true},
		{"special chars", "user!name", true},
		{"unicode", "usér", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUsername(tt.username)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateUsername(%q) error = %v, wantErr %v", tt.username, err, tt.wantErr)
			}
			if err != nil {
				if _, ok := err.(InvalidUsernameError); !ok {
					t.Errorf("validateUsername(%q) returned %T, want InvalidUsernameError", tt.username, err)
				}
			}
		})
	}
}

func TestInvalidUsernameErrorMessage(t *testing.T) {
	err := InvalidUsernameError{}
	msg := err.Error()
	if !strings.Contains(msg, "1-16 characters") {
		t.Errorf("error message should mention length requirement, got: %q", msg)
	}
	if !strings.Contains(msg, "start with a letter") {
		t.Errorf("error message should mention letter requirement, got: %q", msg)
	}
}

func TestHashPassword(t *testing.T) {
	password := "testPassword123!"

	hash1, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}

	// Hash should be in PHC format
	if !strings.HasPrefix(hash1, "$argon2id$") {
		t.Errorf("hash should start with $argon2id$, got: %q", hash1)
	}

	// Hash should have 6 parts separated by $
	parts := strings.Split(hash1, "$")
	if len(parts) != 6 {
		t.Errorf("hash should have 6 parts, got %d: %q", len(parts), hash1)
	}

	// Each call should produce different hash (different salt)
	hash2, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword() second call error = %v", err)
	}
	if hash1 == hash2 {
		t.Error("hashPassword() should produce different hashes for same password (random salt)")
	}
}

func TestVerifyPassword(t *testing.T) {
	password := "testPassword123!"
	wrongPassword := "wrongPassword456!"

	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}

	tests := []struct {
		name     string
		password string
		hash     string
		want     bool
	}{
		{"correct password", password, hash, true},
		{"wrong password", wrongPassword, hash, false},
		{"empty password", "", hash, false},
		{"empty hash", password, "", false},
		{"malformed hash - wrong prefix", password, "$argon2i$v=19$m=65536,t=1,p=4$abc$def", false},
		{"malformed hash - too few parts", password, "$argon2id$v=19", false},
		{"malformed hash - invalid base64 salt", password, "$argon2id$v=19$m=65536,t=1,p=4$!!!$def", false},
		{"malformed hash - invalid base64 hash", password, "$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$!!!", false},
		{"malformed hash - invalid params", password, "$argon2id$v=19$invalid$abc$def", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verifyPassword(tt.password, tt.hash)
			if got != tt.want {
				t.Errorf("verifyPassword() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVerifyPasswordConstantTime(t *testing.T) {
	// This test verifies the function uses constant-time comparison
	// by checking it doesn't short-circuit on partial matches.
	// We can't truly verify timing, but we ensure both paths work.
	password := "testPassword123!"
	hash, _ := hashPassword(password)

	// Similar password (same prefix)
	similar := "testPassword123?"
	if verifyPassword(similar, hash) {
		t.Error("similar password should not verify")
	}

	// Completely different password
	different := "completelyDifferent"
	if verifyPassword(different, hash) {
		t.Error("different password should not verify")
	}

	// Correct password should still work
	if !verifyPassword(password, hash) {
		t.Error("correct password should verify")
	}
}

func BenchmarkHashPassword(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = hashPassword("benchmarkPassword123!")
	}
}

func BenchmarkVerifyPassword(b *testing.B) {
	hash, _ := hashPassword("benchmarkPassword123!")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		verifyPassword("benchmarkPassword123!", hash)
	}
}
