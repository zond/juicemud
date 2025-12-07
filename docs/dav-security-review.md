# DAV Subsystem Security and Correctness Review

**Date**: 2025-12-06
**Reviewer**: Claude Code
**Scope**: `/dav/` directory and related files (`/fs/fs.go`, `/digest/digest.go`)

## Overview

The DAV subsystem implements a WebDAV (RFC 4918) server for HTTP-based file access. The architecture consists of:
- **dav/dav.go**: Core WebDAV protocol handler (OPTIONS, GET, PUT, DELETE, MKCOL, MOVE, PROPFIND, LOCK, UNLOCK)
- **fs/fs.go**: FileSystem implementation bridging DAV to the storage layer
- **digest/digest.go**: HTTP Digest Authentication wrapper

The handler is deployed with digest authentication on both HTTP and HTTPS endpoints.

---

## Security Findings

### 1. Lock Token Verification Uses Substring Match - MEDIUM

**Location**: `dav/dav.go:174`

```go
if token == "" || !strings.Contains(token, lock.Token) {
```

The `If` header is checked using `strings.Contains()`, which could match partial tokens. For example, if a lock token is `abc123`, a forged header containing `xyzabc123xyz` would pass the check.

**Impact**: An attacker who can guess or leak partial lock tokens could bypass lock protection.

**Recommendation**: Use exact token matching or proper parsing of the `If` header format:
```go
if token == "" || !strings.Contains(token, "<"+lock.Token+">") {
```
Or better, parse the `If` header according to RFC 4918 Section 10.4.

### 2. In-Memory Lock Storage - Memory Exhaustion - MEDIUM

**Location**: `dav/dav.go:49`

```go
locks      map[string]*Lock
```

Locks are stored in an unbounded in-memory map. Expired locks are never cleaned up. An attacker could:
1. Create thousands of locks on different paths
2. Let them expire naturally
3. The map entries remain forever, consuming memory

**Impact**: Denial of service through memory exhaustion.

**Recommendation**:
- Implement a background goroutine to periodically clean expired locks
- Consider a maximum lock count per user or globally
- Use a data structure that supports automatic expiration (e.g., a time-based cache)

### 3. Locks Not Persistent - LOW

**Location**: `dav/dav.go:49`

Locks are stored only in memory. Server restarts clear all locks, which could:
- Allow conflicting edits if clients assume their locks are still held
- Cause data loss in collaborative editing scenarios

**Impact**: Data integrity risk in multi-client scenarios.

**Recommendation**: For production use, consider persisting locks to the database.

### 4. Fixed 5MB Upload Limit - Appropriate

**Location**: `dav/dav.go:158`

```go
const maxUploadSize = 5 * 1024 * 1024 // 5 MB
```

The upload limit is checked twice:
1. Via `Content-Length` header (early rejection)
2. Via `io.LimitReader` (safety net for missing/spoofed headers)

**Assessment**: Good defense-in-depth approach.

### 5. Path Traversal - Protected

**Location**: `fs/fs.go:20-28`

```go
func pathify(s *string) {
    *s = filepath.Clean(*s)
    if *s != "/" && (*s)[len(*s)-1] == '/' {
        *s = (*s)[:len(*s)-1]
    }
    if (*s)[0] != '/' {
        *s = "/" + *s
    }
}
```

All file operations in `fs.Fs` pass through `pathify()`, which:
- Uses `filepath.Clean()` to normalize and collapse `../` sequences
- Ensures paths start with `/`
- Removes trailing slashes (except for root)

**Assessment**: Properly protected against path traversal attacks.

### 6. Directory Listing HTML Generation - XSS Risk - MEDIUM

**Location**: `dav/dav.go:117-119`

```go
writer.WriteString(fmt.Sprintf("<html><head><title>%s</title></head><body>", r.URL.Path))
for _, f := range files {
    writer.WriteString(fmt.Sprintf("<a href=\"%s\">%s</a></br>", f.Name, f.Name))
}
```

File names are inserted directly into HTML without escaping. If an attacker creates a file named `<script>alert('xss')</script>`, the script will execute when viewing the directory listing.

**Impact**: Cross-site scripting (XSS) attacks.

**Recommendation**: Use `html.EscapeString()` or `template/html`:
```go
writer.WriteString(fmt.Sprintf("<a href=\"%s\">%s</a></br>",
    html.EscapeString(f.Name), html.EscapeString(f.Name)))
```

### 7. Owner Field Always "anonymous" - LOW

**Location**: `dav/dav.go:452`

```go
Owner:     "anonymous",
```

Lock owner is hardcoded despite authentication being present. The authenticated user should be recorded as the lock owner for audit purposes.

**Recommendation**: Extract user from context (set by digest auth) and use it as the lock owner.

### 8. Digest Authentication - Acceptable with Caveats

**Location**: `digest/digest.go`

**Strengths**:
- Uses `crypto/subtle.ConstantTimeCompare()` for response comparison (timing attack resistant)
- Uses `crypto/rand` for nonce and opaque generation
- Properly implements RFC 2617

**Weaknesses**:
- MD5-based (required by protocol for macOS compatibility, documented in code)
- Nonces are not validated for replay - any nonce is accepted if the hash is correct
- No nonce count tracking to prevent replay attacks

**Assessment**: Acceptable for trusted network environments with HTTPS. The code comment acknowledges MD5 is required for compatibility and security relies on TLS.

**Recommendation**: For higher security environments, consider:
- Adding nonce expiration checking
- Tracking nonce counts to detect replay attacks

---

## Correctness Findings

### 1. Typo in Error Message - LOW

**Location**: `dav/dav.go:213`

```go
http.Error(w, "Unathorized", http.StatusForbidden)
```

Should be "Unauthorized".

### 2. PROPFIND Depth Header Handling - Non-Standard Behavior - LOW

**Location**: `dav/dav.go:305`

```go
if depth != "0" && info.IsDir {
```

Any depth value other than "0" is treated as infinity. RFC 4918 specifies only "0", "1", and "infinity". Non-standard values should arguably return 400 Bad Request.

**Assessment**: Permissive but functional. Low risk.

### 3. Error After Headers Written - LOW

**Location**: `dav/dav.go:331-333`

```go
w.WriteHeader(http.StatusMultiStatus)
// ...
if err := encoder.Encode(multiStatus); err != nil {
    http.Error(w, "Failed to encode XML", http.StatusInternalServerError)
```

If XML encoding fails after `WriteHeader` is called, `http.Error()` cannot change the status code (headers already sent). The error message may not be properly delivered.

**Recommendation**: Build the response in a buffer first, then write status and body together:
```go
var buf bytes.Buffer
if err := xml.NewEncoder(&buf).Encode(multiStatus); err != nil {
    http.Error(w, "Failed to encode XML", http.StatusInternalServerError)
    return juicemud.WithStack(err)
}
w.Header().Set("Content-Type", "application/xml; charset=utf-8")
w.WriteHeader(http.StatusMultiStatus)
buf.WriteTo(w)
```

### 4. Missing DELETE/MOVE Lock Checks - MEDIUM

**Location**: `dav/dav.go:229-234` (handleDelete), `dav/dav.go:243-261` (handleMove)

These operations don't check if the resource is locked before proceeding. A locked file can be deleted or moved without providing the lock token.

**Impact**: Lock semantics violated; clients expecting lock protection may lose data.

**Recommendation**: Add lock validation similar to `handlePut`:
```go
h.lockMutex.Lock()
lock := h.locks[r.URL.Path]
h.lockMutex.Unlock()

if lock != nil && lock.ExpiresAt.After(time.Now()) {
    token := r.Header.Get("If")
    if token == "" || !strings.Contains(token, lock.Token) {
        http.Error(w, "Resource is locked", http.StatusLocked)
        return errors.New("resource is locked")
    }
}
```

### 5. File Read in Stat Inefficient - LOW

**Location**: `fs/fs.go:87-88`

```go
content, modTime, err := f.Storage.LoadSource(ctx, file.Path)
```

`Stat` loads the entire file content just to get its size. For large files, this is wasteful.

**Recommendation**: Store file size as metadata or add a `Size()` method to avoid loading content.

### 6. generateToken Ignores Error - LOW

**Location**: `dav/dav.go:417`

```go
_, _ = rand.Read(bytes)
```

The error from `crypto/rand.Read()` is explicitly ignored. While `rand.Read()` essentially never fails on modern systems, best practice is to handle it:
```go
if _, err := rand.Read(bytes); err != nil {
    panic("crypto/rand failed: " + err.Error())
}
```

---

## Summary of Issues

| Issue | Severity | Location | Type | Status |
|-------|----------|----------|------|--------|
| Lock token substring match | Medium | `dav.go:174` | Security | ✅ Fixed |
| In-memory lock memory exhaustion | Medium | `dav.go:49` | Security | ✅ Fixed |
| XSS in directory listing | Medium | `dav.go:117-119` | Security | ✅ Fixed |
| Missing DELETE/MOVE lock checks | Medium | `dav.go:229,243` | Correctness | ✅ Fixed |
| Locks not persistent | Low | `dav.go:49` | Correctness | Open |
| Owner always "anonymous" | Low | `dav.go:452` | Correctness | Open |
| No nonce replay protection | Low | `digest.go` | Security | Open |
| Typo "Unathorized" | Low | `dav.go:213` | Correctness | ✅ Fixed |
| Non-standard depth handling | Low | `dav.go:305` | Correctness | Open |
| Error after headers written | Low | `dav.go:331-333` | Correctness | ✅ Fixed |
| Stat loads full file content | Low | `fs.go:87-88` | Performance | Open |
| generateToken ignores error | Low | `dav.go:417` | Correctness | ✅ Fixed |

---

## Positive Findings

1. **Path traversal protection**: The `pathify()` function properly normalizes all paths
2. **Upload size limiting**: Defense-in-depth with both header and reader limits
3. **Context-aware operations**: All filesystem operations accept context for cancellation
4. **Digest auth timing safety**: Uses constant-time comparison
5. **HTTPS deployment**: Server configured with TLS support
6. **Proper HTTP status codes**: Correct mapping of errors to HTTP statuses
7. **MIME type detection**: Proper Content-Type headers for served files

---

## Recommendations

### Immediate (Security) - ✅ All Fixed
1. ~~Fix XSS vulnerability in directory listing HTML generation~~ ✅
2. ~~Add lock validation to DELETE and MOVE handlers~~ ✅
3. ~~Improve lock token verification to use exact matching~~ ✅

### Short-term (Correctness) - ✅ All Fixed
1. ~~Fix "Unathorized" typo~~ ✅
2. ~~Add expired lock cleanup goroutine~~ ✅
3. ~~Buffer XML responses before writing headers~~ ✅

### Medium-term (Robustness) - Open
1. Consider persisting locks for production use
2. Add nonce expiration/replay protection to digest auth
3. Record authenticated user as lock owner
4. Optimize Stat to avoid loading file content

---

## Testing Recommendations

To validate fixes and prevent regressions:

1. **XSS test**: Create file with `<script>` in name, verify escaped in listing
2. **Lock tests**:
   - Verify DELETE/MOVE respect locks
   - Verify exact token matching
   - Test expired lock cleanup
3. **Path traversal tests**: Attempt `../` sequences, verify normalization
4. **Load tests**: Create many locks, verify memory bounded
5. **Concurrent tests**: Multiple clients locking/editing same file
