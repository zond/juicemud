package imports

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestParseImports(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		expected []string
	}{
		{
			name:     "no imports",
			source:   "var x = 1;",
			expected: []string{},
		},
		{
			name:     "single import",
			source:   "// @import /lib/util.js\nvar x = 1;",
			expected: []string{"/lib/util.js"},
		},
		{
			name:     "multiple imports",
			source:   "// @import /lib/util.js\n// @import /lib/math.js\nvar x = 1;",
			expected: []string{"/lib/util.js", "/lib/math.js"},
		},
		{
			name:     "relative import",
			source:   "// @import ./util.js\nvar x = 1;",
			expected: []string{"./util.js"},
		},
		{
			name:     "parent relative import",
			source:   "// @import ../lib/util.js\nvar x = 1;",
			expected: []string{"../lib/util.js"},
		},
		{
			name:     "import in middle of file",
			source:   "var x = 1;\n// @import /lib/util.js\nvar y = 2;",
			expected: []string{"/lib/util.js"},
		},
		{
			name:     "not an import - in comment",
			source:   "// This is a note: @import is cool\nvar x = 1;",
			expected: []string{},
		},
		{
			name:     "not an import - indented",
			source:   "  // @import /lib/util.js\nvar x = 1;",
			expected: []string{},
		},
		{
			name:     "import with trailing whitespace",
			source:   "// @import /lib/util.js   \nvar x = 1;",
			expected: []string{"/lib/util.js"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseImports(tt.source)
			if len(result) != len(tt.expected) {
				t.Errorf("ParseImports() = %v, want %v", result, tt.expected)
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("ParseImports()[%d] = %v, want %v", i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestRemoveImports(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		expected string
	}{
		{
			name:     "no imports",
			source:   "var x = 1;",
			expected: "var x = 1;",
		},
		{
			name:     "single import",
			source:   "// @import /lib/util.js\nvar x = 1;",
			expected: "\nvar x = 1;",
		},
		{
			name:     "multiple imports",
			source:   "// @import /lib/util.js\n// @import /lib/math.js\nvar x = 1;",
			expected: "\n\nvar x = 1;",
		},
		{
			name:     "keeps non-import comments",
			source:   "// @import /lib/util.js\n// This is a regular comment\nvar x = 1;",
			expected: "\n// This is a regular comment\nvar x = 1;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RemoveImports(tt.source)
			if result != tt.expected {
				t.Errorf("RemoveImports() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	tests := []struct {
		name       string
		fromPath   string
		importPath string
		expected   string
	}{
		{
			name:       "absolute import",
			fromPath:   "/mobs/dog.js",
			importPath: "/lib/util.js",
			expected:   "/lib/util.js",
		},
		{
			name:       "relative same directory",
			fromPath:   "/mobs/dog.js",
			importPath: "./util.js",
			expected:   "/mobs/util.js",
		},
		{
			name:       "relative parent directory",
			fromPath:   "/mobs/dog.js",
			importPath: "../lib/util.js",
			expected:   "/lib/util.js",
		},
		{
			name:       "relative without dot prefix",
			fromPath:   "/mobs/dog.js",
			importPath: "util.js",
			expected:   "/mobs/util.js",
		},
		{
			name:       "deeply nested relative",
			fromPath:   "/a/b/c/d.js",
			importPath: "../../lib/util.js",
			expected:   "/a/lib/util.js",
		},
		{
			name:       "from root",
			fromPath:   "/main.js",
			importPath: "./lib/util.js",
			expected:   "/lib/util.js",
		},
		{
			name:       "cleans paths with dotdot",
			fromPath:   "/mobs/dog.js",
			importPath: "/../../../etc/passwd",
			expected:   "/etc/passwd", // Cleaned but still within virtual root
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolvePath(tt.fromPath, tt.importPath)
			if result != tt.expected {
				t.Errorf("ResolvePath(%q, %q) = %q, want %q",
					tt.fromPath, tt.importPath, result, tt.expected)
			}
		})
	}
}

// mockLoader creates a LoadFunc that returns predefined sources.
func mockLoader(sources map[string]string) LoadFunc {
	mtime := int64(1000)
	return func(ctx context.Context, path string) ([]byte, int64, error) {
		source, ok := sources[path]
		if !ok {
			return nil, 0, fmt.Errorf("file not found: %s", path)
		}
		mtime++ // Increment to simulate different mtimes
		return []byte(source), mtime, nil
	}
}

func TestResolve_SimpleImport(t *testing.T) {
	sources := map[string]string{
		"/lib/util.js": "var util = {};\nutil.greet = function() { return 'hi'; };",
		"/main.js":     "// @import /lib/util.js\nlog(util.greet());",
	}

	r := NewResolver()
	result, err := r.Resolve(context.Background(), "/main.js", mockLoader(sources))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	// util.js should come before main.js (with import line removed)
	expected := "var util = {};\nutil.greet = function() { return 'hi'; };\nlog(util.greet());"
	if result.Source != expected {
		t.Errorf("Resolve().Source = %q, want %q", result.Source, expected)
	}

	// Should have 2 deps
	if len(result.Deps) != 2 {
		t.Errorf("Resolve().Deps = %v, want 2 elements", result.Deps)
	}
}

func TestResolve_ChainedImports(t *testing.T) {
	sources := map[string]string{
		"/lib/base.js": "var base = 'base';",
		"/lib/util.js": "// @import /lib/base.js\nvar util = base + '-util';",
		"/main.js":     "// @import /lib/util.js\nlog(util);",
	}

	r := NewResolver()
	result, err := r.Resolve(context.Background(), "/main.js", mockLoader(sources))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	// Order should be: base.js, util.js, main.js
	expected := "var base = 'base';\nvar util = base + '-util';\nlog(util);"
	if result.Source != expected {
		t.Errorf("Resolve().Source = %q, want %q", result.Source, expected)
	}

	if len(result.Deps) != 3 {
		t.Errorf("Resolve().Deps = %v, want 3 elements", result.Deps)
	}
}

func TestResolve_DiamondDependency(t *testing.T) {
	// Diamond: A imports B and C, both B and C import D
	sources := map[string]string{
		"/d.js": "var d = 'd';",
		"/b.js": "// @import /d.js\nvar b = d + '-b';",
		"/c.js": "// @import /d.js\nvar c = d + '-c';",
		"/a.js": "// @import /b.js\n// @import /c.js\nlog(b, c);",
	}

	r := NewResolver()
	result, err := r.Resolve(context.Background(), "/a.js", mockLoader(sources))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	// D should appear only once, order: d, b, c, a
	// Note: two import lines removed from a.js leave two empty lines (replaced with \n each)
	expected := "var d = 'd';\nvar b = d + '-b';\nvar c = d + '-c';\n\nlog(b, c);"
	if result.Source != expected {
		t.Errorf("Resolve().Source = %q, want %q", result.Source, expected)
	}

	// Verify deps are deduplicated (D should appear only once)
	depCounts := make(map[string]int)
	for _, dep := range result.Deps {
		depCounts[dep]++
	}
	for dep, count := range depCounts {
		if count > 1 {
			t.Errorf("Dep %q appears %d times, want 1", dep, count)
		}
	}

	// Should have exactly 4 unique deps: a, b, c, d
	if len(depCounts) != 4 {
		t.Errorf("Got %d unique deps, want 4: %v", len(depCounts), result.Deps)
	}
}

func TestResolve_CircularDependency(t *testing.T) {
	sources := map[string]string{
		"/a.js": "// @import /b.js\nvar a = 'a';",
		"/b.js": "// @import /a.js\nvar b = 'b';",
	}

	r := NewResolver()
	_, err := r.Resolve(context.Background(), "/a.js", mockLoader(sources))
	if err == nil {
		t.Fatal("Resolve() expected error for circular dependency, got nil")
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Errorf("Resolve() error = %v, want error containing 'circular'", err)
	}
}

func TestResolve_MissingImport(t *testing.T) {
	sources := map[string]string{
		"/main.js": "// @import /missing.js\nvar x = 1;",
	}

	r := NewResolver()
	_, err := r.Resolve(context.Background(), "/main.js", mockLoader(sources))
	if err == nil {
		t.Fatal("Resolve() expected error for missing import, got nil")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "missing") {
		t.Errorf("Resolve() error = %v, want error about missing file", err)
	}
}

func TestResolve_RelativeImports(t *testing.T) {
	sources := map[string]string{
		"/lib/util.js":  "var util = 'util';",
		"/mobs/dog.js":  "// @import ../lib/util.js\nvar dog = util + '-dog';",
		"/mobs/cat.js":  "// @import ./dog.js\nvar cat = dog + '-cat';",
	}

	r := NewResolver()
	result, err := r.Resolve(context.Background(), "/mobs/cat.js", mockLoader(sources))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	// Order: util.js, dog.js, cat.js
	expected := "var util = 'util';\nvar dog = util + '-dog';\nvar cat = dog + '-cat';"
	if result.Source != expected {
		t.Errorf("Resolve().Source = %q, want %q", result.Source, expected)
	}
}

func TestResolve_Caching(t *testing.T) {
	callCount := 0
	sources := map[string]string{
		"/lib/util.js": "var util = 'util';",
		"/main.js":     "// @import /lib/util.js\nvar x = util;",
	}

	loader := func(ctx context.Context, path string) ([]byte, int64, error) {
		callCount++
		source, ok := sources[path]
		if !ok {
			return nil, 0, fmt.Errorf("file not found: %s", path)
		}
		return []byte(source), 1000, nil
	}

	r := NewResolver()

	// First resolve
	_, err := r.Resolve(context.Background(), "/main.js", loader)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	firstCallCount := callCount

	// Second resolve should use cache
	_, err = r.Resolve(context.Background(), "/main.js", loader)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if callCount != firstCallCount {
		t.Errorf("Second Resolve() called loader %d more times, want 0 (cached)",
			callCount-firstCallCount)
	}
}

func TestGetCachedDeps(t *testing.T) {
	r := NewResolver()

	// Before resolving, should return single-element slice
	deps := r.GetCachedDeps("/main.js")
	if len(deps) != 1 || deps[0] != "/main.js" {
		t.Errorf("GetCachedDeps() before resolve = %v, want [/main.js]", deps)
	}

	// After resolving
	sources := map[string]string{
		"/lib/util.js": "var util = 'util';",
		"/main.js":     "// @import /lib/util.js\nvar x = util;",
	}
	_, err := r.Resolve(context.Background(), "/main.js", mockLoader(sources))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	deps = r.GetCachedDeps("/main.js")
	if len(deps) != 2 {
		t.Errorf("GetCachedDeps() after resolve = %v, want 2 elements", deps)
	}
}

func TestInvalidateCache(t *testing.T) {
	sources := map[string]string{
		"/main.js": "var x = 1;",
	}

	r := NewResolver()
	_, err := r.Resolve(context.Background(), "/main.js", mockLoader(sources))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	// Should have cached mtime
	if r.GetCachedMaxMtime("/main.js") == 0 {
		t.Error("GetCachedMaxMtime() = 0 after resolve, want non-zero")
	}

	// Invalidate
	r.InvalidateCache("/main.js")

	// Should be gone
	if r.GetCachedMaxMtime("/main.js") != 0 {
		t.Error("GetCachedMaxMtime() != 0 after invalidate, want 0")
	}
}

func TestInvalidateCacheIfStale(t *testing.T) {
	sources := map[string]string{
		"/main.js": "var x = 1;",
	}

	// Create a loader that returns mtime 1000
	loaderOld := func(ctx context.Context, path string) ([]byte, int64, error) {
		source, ok := sources[path]
		if !ok {
			return nil, 0, fmt.Errorf("file not found: %s", path)
		}
		return []byte(source), 1000, nil
	}

	// Create a loader that returns mtime 2000
	loaderNew := func(ctx context.Context, path string) ([]byte, int64, error) {
		source, ok := sources[path]
		if !ok {
			return nil, 0, fmt.Errorf("file not found: %s", path)
		}
		return []byte(source), 2000, nil
	}

	r := NewResolver()
	_, err := r.Resolve(context.Background(), "/main.js", loaderOld)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	oldMtime := r.GetCachedMaxMtime("/main.js")
	if oldMtime != 1000 {
		t.Fatalf("GetCachedMaxMtime() = %d, want 1000", oldMtime)
	}

	// Simulate another goroutine refreshing the cache:
	// invalidate and re-resolve with a loader that returns a newer mtime
	r.InvalidateCache("/main.js")
	_, err = r.Resolve(context.Background(), "/main.js", loaderNew)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	newMtime := r.GetCachedMaxMtime("/main.js")
	if newMtime != 2000 {
		t.Fatalf("GetCachedMaxMtime() = %d, want 2000", newMtime)
	}

	// Try to invalidate with the OLD stale mtime we observed earlier.
	// Since the cache now has a newer mtime, this should NOT invalidate.
	r.InvalidateCacheIfStale("/main.js", oldMtime)
	if r.GetCachedMaxMtime("/main.js") == 0 {
		t.Error("InvalidateCacheIfStale with old stale mtime invalidated fresh cache, should not")
	}

	// Try to invalidate with the current mtime - should invalidate
	r.InvalidateCacheIfStale("/main.js", newMtime)
	if r.GetCachedMaxMtime("/main.js") != 0 {
		t.Error("InvalidateCacheIfStale with current mtime did not invalidate cache")
	}
}

func TestResolve_DepthLimit(t *testing.T) {
	// Create a chain that exceeds maxImportDepth (100)
	sources := make(map[string]string)
	for i := 0; i <= 101; i++ {
		path := fmt.Sprintf("/chain%d.js", i)
		if i == 0 {
			sources[path] = "var x = 0;"
		} else {
			prevPath := fmt.Sprintf("/chain%d.js", i-1)
			sources[path] = fmt.Sprintf("// @import %s\nvar x = %d;", prevPath, i)
		}
	}

	r := NewResolver()
	_, err := r.Resolve(context.Background(), "/chain101.js", mockLoader(sources))
	if err == nil {
		t.Fatal("Resolve() expected error for depth limit exceeded, got nil")
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Errorf("Resolve() error = %v, want error containing 'depth'", err)
	}
}

func TestResolve_FileCountLimit(t *testing.T) {
	// Create a flat structure that exceeds maxImportFiles (1000)
	sources := make(map[string]string)
	var mainImports string
	for i := 0; i < 1001; i++ {
		path := fmt.Sprintf("/file%d.js", i)
		sources[path] = "var x = 1;"
		mainImports += fmt.Sprintf("// @import %s\n", path)
	}
	sources["/main.js"] = mainImports + "var main = 1;"

	r := NewResolver()
	_, err := r.Resolve(context.Background(), "/main.js", mockLoader(sources))
	if err == nil {
		t.Fatal("Resolve() expected error for file count limit exceeded, got nil")
	}
	if !strings.Contains(err.Error(), "file count") {
		t.Errorf("Resolve() error = %v, want error containing 'file count'", err)
	}
}

func TestResolve_CancelledContext(t *testing.T) {
	sources := map[string]string{
		"/lib/util.js": "var util = 'util';",
		"/main.js":     "// @import /lib/util.js\nvar x = util;",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	r := NewResolver()
	_, err := r.Resolve(ctx, "/main.js", mockLoader(sources))
	if err == nil {
		t.Fatal("Resolve() expected error for cancelled context, got nil")
	}
}

