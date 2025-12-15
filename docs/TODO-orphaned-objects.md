# TODO: Orphaned Object Detection

## Problem

Objects store a `SourcePath` field that references a JavaScript source file. If the source file is deleted or moved without updating the objects that reference it, those objects become "orphaned" - they will fail at runtime when the game tries to load their source.

Currently, this failure happens silently in the background. The error is logged but there's no proactive way for administrators to detect or fix these problems.

## Historical Context

The previous WebDAV-based source storage had built-in protection against this. When deleting a file via WebDAV, the `delFileIfExists` function would check `sourceObjects.SubCount(path)` and return HTTP 422 if any objects referenced that source:

```go
if count, err := s.sourceObjects.SubCount(path); err != nil {
    return juicemud.WithStack(err)
} else if count > 0 {
    return httpError{code: 422, err: errors.Errorf("%q is used by %v objects", path, count)}
}
```

With the move to filesystem-native storage, this protection was intentionally removed to enable standard filesystem tools, Git, and agentic coding assistants to manage sources directly. The tradeoff is that users can now accidentally delete source files that are in use.

## Current Behavior

When an object is processed (`game/processing.go:569-581`):
1. `SourceModTime()` is called - fails if source doesn't exist
2. `LoadSource()` is called - fails if source doesn't exist
3. Error is logged via `log.Printf` in the event worker

The `sourceObjects` index maps source paths to object IDs, but there's no validation that the referenced paths actually exist.

## Proposed Solution

Add a wizard command (e.g., `/orphans` or `/validate`) that:

1. Iterates through all objects in the database
2. For each object, checks if `SourcePath` exists on the filesystem
3. Reports any objects with missing sources, including:
   - Object ID
   - Missing source path
   - Object location (for context)

Optional enhancements:
- Add a startup validation that logs warnings for orphaned objects
- Add a `/fixorphan <objectid> <newsourcepath>` command to repair them
- Consider adding a periodic background check that warns via console

## Implementation Notes

The `storage.SourceExists()` function already exists and can be used for validation.

The `storage.EachObject()` iterator (if it exists) or similar would be needed to enumerate all objects.
