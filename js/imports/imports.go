// Package imports provides JavaScript source import resolution.
// It implements a build-time concatenation system using `// @import` directives.
package imports

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"
)

// importPattern matches `// @import path` at the start of a line.
// Only matches if @import is at the very start (after //) to prevent
// accidental matches in comments like "// note: @import is cool".
var importPattern = regexp.MustCompile(`(?m)^// @import\s+(\S+)\s*$`)

// LoadFunc loads raw source bytes and returns (source, mtime, error).
type LoadFunc func(ctx context.Context, path string) ([]byte, int64, error)

// ResolveResult contains the resolved source and metadata.
type ResolveResult struct {
	Source   string   // Concatenated source with imports resolved
	MaxMtime int64    // Maximum mtime across all files in dependency tree
	Deps     []string // All files in the dependency tree (for mtime checking)
}

// cacheEntry stores resolved source and dependency info.
type cacheEntry struct {
	source   string
	maxMtime int64
	deps     []string
}

// Resolver handles JavaScript import resolution and caching.
type Resolver struct {
	mu    sync.RWMutex
	cache map[string]*cacheEntry
}

// NewResolver creates a new Resolver instance.
func NewResolver() *Resolver {
	return &Resolver{
		cache: make(map[string]*cacheEntry),
	}
}

// GetCachedDeps returns the cached dependency list for a path.
// Returns a single-element slice containing just the path if not cached.
func (r *Resolver) GetCachedDeps(sourcePath string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if entry, ok := r.cache[sourcePath]; ok {
		return entry.deps
	}
	return []string{sourcePath}
}

// GetCachedMaxMtime returns the cached max mtime for a path.
// Returns 0 if not cached.
func (r *Resolver) GetCachedMaxMtime(sourcePath string) int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if entry, ok := r.cache[sourcePath]; ok {
		return entry.maxMtime
	}
	return 0
}

// Resolve resolves all imports for a source file and returns concatenated source.
// Uses depth-first traversal to produce correct topological order (deps before dependents).
// Handles diamond dependencies correctly (each file appears only once).
func (r *Resolver) Resolve(ctx context.Context, sourcePath string, load LoadFunc) (*ResolveResult, error) {
	// Check if cached entry exists
	r.mu.RLock()
	entry, cached := r.cache[sourcePath]
	r.mu.RUnlock()

	if cached {
		// Return cached result
		return &ResolveResult{
			Source:   entry.source,
			MaxMtime: entry.maxMtime,
			Deps:     entry.deps,
		}, nil
	}

	// Resolve from scratch
	rctx := &resolveContext{
		inProgress: make(map[string]bool),
		included:   make(map[string]bool),
		load:       load,
	}

	source, maxMtime, deps, err := r.resolveRecursive(ctx, sourcePath, rctx)
	if err != nil {
		return nil, err
	}

	// Cache the result
	r.mu.Lock()
	r.cache[sourcePath] = &cacheEntry{
		source:   source,
		maxMtime: maxMtime,
		deps:     deps,
	}
	r.mu.Unlock()

	return &ResolveResult{
		Source:   source,
		MaxMtime: maxMtime,
		Deps:     deps,
	}, nil
}

// InvalidateCache removes a path from the cache.
// Call this when you know a source file has changed.
func (r *Resolver) InvalidateCache(sourcePath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, sourcePath)
}

// InvalidateAll clears the entire cache.
func (r *Resolver) InvalidateAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache = make(map[string]*cacheEntry)
}

// resolveContext tracks state during recursive resolution.
type resolveContext struct {
	inProgress map[string]bool // Current resolution stack (cycle detection)
	included   map[string]bool // Files already in output (deduplication)
	load       LoadFunc
}

// resolveRecursive performs depth-first resolution of imports.
func (r *Resolver) resolveRecursive(ctx context.Context, sourcePath string, rctx *resolveContext) (string, int64, []string, error) {
	// Cycle detection
	if rctx.inProgress[sourcePath] {
		return "", 0, nil, fmt.Errorf("circular import detected: %s", sourcePath)
	}

	// Already included? Skip source output but still track mtime
	if rctx.included[sourcePath] {
		// We need to get the mtime even for already-included files
		// to ensure the max mtime calculation is correct
		_, mtime, err := rctx.load(ctx, sourcePath)
		if err != nil {
			return "", 0, nil, fmt.Errorf("loading %s: %w", sourcePath, err)
		}
		return "", mtime, []string{sourcePath}, nil
	}

	rctx.inProgress[sourcePath] = true
	defer delete(rctx.inProgress, sourcePath)

	// Load the source file
	sourceBytes, mtime, err := rctx.load(ctx, sourcePath)
	if err != nil {
		return "", 0, nil, fmt.Errorf("loading %s: %w", sourcePath, err)
	}
	source := string(sourceBytes)

	// Parse imports
	imports := ParseImports(source)
	maxMtime := mtime
	deps := []string{sourcePath}

	var resolved strings.Builder

	// Process each import
	for _, imp := range imports {
		absPath := ResolvePath(sourcePath, imp)

		depSource, depMtime, depDeps, err := r.resolveRecursive(ctx, absPath, rctx)
		if err != nil {
			return "", 0, nil, fmt.Errorf("in %s: %w", sourcePath, err)
		}

		resolved.WriteString(depSource)
		if depMtime > maxMtime {
			maxMtime = depMtime
		}
		deps = append(deps, depDeps...)
	}

	// Append this file's source (with import lines removed)
	resolved.WriteString(RemoveImports(source))

	// Mark as included AFTER we've output it
	rctx.included[sourcePath] = true

	return resolved.String(), maxMtime, deps, nil
}

// ParseImports extracts all import paths from source code.
// Imports must be at the start of a line: `// @import path`
func ParseImports(source string) []string {
	matches := importPattern.FindAllStringSubmatch(source, -1)
	result := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) >= 2 {
			result = append(result, match[1])
		}
	}
	return result
}

// RemoveImports removes all `// @import` lines from source.
func RemoveImports(source string) string {
	return importPattern.ReplaceAllString(source, "")
}

// ResolvePath resolves an import path relative to the importing file.
// Absolute paths (starting with /) are returned as-is.
// Relative paths (starting with ./ or ../) are resolved relative to the importing file's directory.
func ResolvePath(fromPath, importPath string) string {
	if strings.HasPrefix(importPath, "/") {
		return importPath
	}
	dir := path.Dir(fromPath)
	return path.Clean(path.Join(dir, importPath))
}
