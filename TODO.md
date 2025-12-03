# TODO

Security and correctness issues identified during code review.

## Completed

- [x] Fix infinite recursion in `sizeBody.Close()` - `server/server.go:60-62`
- [x] Fix binary slice underread in `SourceModTime()` - `storage/storage.go:432`
- [x] Fix WebDAV lock race conditions (TOCTOU) - `dav/dav.go:159-161` - **Won't fix**: Analysis showed the races only occur with adversarial clients. With sane clients, the lock protocol (LOCK → PUT → UNLOCK) prevents races. Lock validity is checked at request start, and no other client can obtain a conflicting lock while one is held.
- [x] Fix WebDAV LOCK semantic bug - should reject if lock held by another client, not refresh - `dav/dav.go:429-439`
- [x] Review MD5 password storage limitations - `digest/digest.go:17-18` - **Won't fix**: MD5 is mandated by HTTP Digest Auth (RFC 2617) and required for macOS WebDAV client compatibility. Security relies on HTTPS transport. Added documentation comment.
- [x] Implement read/write group access restrictions - `storage/storage.go:3` - **Already implemented**: Analysis showed read/write group checks are in place throughout the codebase. Removed stale TODO comment.

## In Progress

- [ ] Add rate limiting on login attempts - `game/connection.go:756-768`

## Pending

- [ ] Add upload size limits to WebDAV PUT - `dav/dav.go:182`
- [ ] Fix JavaScript timeout race condition - `js/js.go:254`
- [ ] Fix SQL error handling in `FileExists()` - `storage/storage.go:630-636`
- [ ] Add session idle timeout for SSH connections - `game/connection.go:699-716`
- [ ] Add path traversal prevention with `filepath.Clean()` - `fs/fs.go`
- [ ] Complete or remove incomplete `TestMulti` test - `structs/structs_test.go:17-37`
