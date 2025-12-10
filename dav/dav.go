package dav

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zond/juicemud"
)

// FileInfo represents a file or directory in the virtual file system.
type FileInfo struct {
	Name    string
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
	IsDir   bool
}

// FileSystem defines the minimal interface for a WebDAV file system.
type FileSystem interface {
	Read(ctx context.Context, path string) (io.ReadCloser, error)
	Write(ctx context.Context, path string) (io.WriteCloser, error)
	Stat(ctx context.Context, path string) (*FileInfo, error)
	Remove(ctx context.Context, path string) error
	Mkdir(ctx context.Context, path string) error
	List(ctx context.Context, path string) ([]*FileInfo, error)
	Rename(ctx context.Context, oldPath string, newURL *url.URL) error
}

// Handler provides WebDAV functionality over the given FileSystem.
type Handler struct {
	fileSystem FileSystem
	locks      map[string]*Lock
	lockMutex  sync.Mutex
	closeCh    chan struct{}
}

func New(fs FileSystem) *Handler {
	h := &Handler{
		fileSystem: fs,
		locks:      map[string]*Lock{},
		closeCh:    make(chan struct{}),
	}
	go h.cleanupExpiredLocks()
	return h
}

// Close shuts down the handler's background goroutines.
func (h *Handler) Close() {
	close(h.closeCh)
}

// cleanupExpiredLocks periodically removes expired locks to prevent memory exhaustion.
func (h *Handler) cleanupExpiredLocks() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-h.closeCh:
			return
		case <-ticker.C:
			h.lockMutex.Lock()
			now := time.Now()
			for path, lock := range h.locks {
				if lock.ExpiresAt.Before(now) {
					delete(h.locks, path)
				}
			}
			h.lockMutex.Unlock()
		}
	}
}

// ServeHTTP handles HTTP requests for the WebDAV server.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var err error

	switch r.Method {
	case "OPTIONS":
		h.handleOptions(w, r)
	case "GET", "HEAD":
		err = h.handleGet(w, r)
	case "PUT":
		err = h.handlePut(w, r)
	case "DELETE":
		err = h.handleDelete(w, r)
	case "MKCOL":
		err = h.handleMkcol(w, r)
	case "MOVE":
		err = h.handleMove(w, r)
	case "PROPFIND":
		err = h.handlePropfind(w, r)
	case "LOCK":
		err = h.handleLock(w, r)
	case "UNLOCK":
		err = h.handleUnlock(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}

	// If there was an error, you might want to log it here:
	if err != nil {
		log.Println(err)
		log.Println(juicemud.StackTrace(err))
	}
}

func (h *Handler) handleOptions(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, MOVE, PROPFIND, LOCK, UNLOCK")
	w.Header().Set("DAV", "1, 2")
	w.Header().Set("MS-Author-Via", "DAV")
	w.WriteHeader(http.StatusOK)
}

// handleGet serves files or lists directory contents.
func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) error {
	info, err := h.fileSystem.Stat(r.Context(), r.URL.Path)
	if err != nil {
		return handlePrettyError(w, err, "Failed to get file")
	}

	if info.IsDir {
		files, err := h.fileSystem.List(r.Context(), r.URL.Path)
		if err != nil {
			http.Error(w, "Failed to list directory", http.StatusInternalServerError)
			return juicemud.WithStack(err)
		}

		w.Header().Set("Content-Type", "text/html")
		writer := bufio.NewWriter(w)
		writer.WriteString(fmt.Sprintf("<html><head><title>%s</title></head><body>", html.EscapeString(r.URL.Path)))
		for _, f := range files {
			writer.WriteString(fmt.Sprintf("<a href=\"%s\">%s</a></br>", html.EscapeString(f.Name), html.EscapeString(f.Name)))
		}
		writer.WriteString("</body></html>")
		writer.Flush()

	} else {
		if info.Size == 0 {
			w.WriteHeader(http.StatusOK)
			return nil
		}

		ext := path.Ext(info.Name)
		contentType := mime.TypeByExtension(ext)
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
		w.Header().Set("Last-Modified", info.ModTime.UTC().Format(http.TimeFormat))

		if r.Method != http.MethodHead {
			file, err := h.fileSystem.Read(r.Context(), r.URL.Path)
			if err != nil {
				http.Error(w, "File not found", http.StatusNotFound)
				return juicemud.WithStack(err)
			}
			defer file.Close()

			_, err = io.Copy(w, file)
			if err != nil {
				http.Error(w, "Failed to read file", http.StatusInternalServerError)
				return juicemud.WithStack(err)
			}
		}
	}
	return nil
}

const maxUploadSize = 5 * 1024 * 1024 // 5 MB

// checkLock verifies the resource is not locked, or that a valid lock token is provided.
// Returns nil if the operation can proceed, or an error if the resource is locked.
func (h *Handler) checkLock(w http.ResponseWriter, r *http.Request) error {
	h.lockMutex.Lock()
	lock := h.locks[r.URL.Path]
	h.lockMutex.Unlock()

	if lock != nil && lock.ExpiresAt.After(time.Now()) {
		token := r.Header.Get("If")
		// Use exact token matching with angle brackets per RFC 4918 Section 10.4
		if token == "" || !strings.Contains(token, "<"+lock.Token+">") {
			http.Error(w, "Resource is locked", http.StatusLocked)
			return errors.New("resource is locked")
		}
	}
	return nil
}

func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request) error {
	// Check Content-Length header for early rejection
	if r.ContentLength > maxUploadSize {
		http.Error(w, "File too large", http.StatusRequestEntityTooLarge)
		return nil
	}

	if err := h.checkLock(w, r); err != nil {
		return err
	}

	file, err := h.fileSystem.Write(r.Context(), r.URL.Path)
	if errors.Is(err, os.ErrPermission) {
		http.Error(w, "Unauthorized", http.StatusForbidden)
		return nil
	} else if err != nil {
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return juicemud.WithStack(err)
	}
	defer file.Close()

	// Use LimitReader as safety net in case Content-Length is missing or wrong
	limited := io.LimitReader(r.Body, maxUploadSize+1)
	n, err := io.Copy(file, limited)
	if err != nil {
		http.Error(w, "Failed to write file", http.StatusInternalServerError)
		return juicemud.WithStack(err)
	}
	if n > maxUploadSize {
		http.Error(w, "File too large", http.StatusRequestEntityTooLarge)
		return nil
	}

	w.WriteHeader(http.StatusCreated)
	return nil
}

type httpErrorable interface {
	HTTPError() (int, string)
}

func handlePrettyError(w http.ResponseWriter, err error, def string) error {
	var herr httpErrorable
	if errors.Is(err, os.ErrPermission) {
		http.Error(w, "Unauthorized", http.StatusForbidden)
		return nil
	} else if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "File not found", http.StatusNotFound)
		return nil
	} else if errors.As(err, &herr) {
		code, message := herr.HTTPError()
		http.Error(w, message, code)
		return nil
	} else if err != nil {
		http.Error(w, def, http.StatusInternalServerError)
		return juicemud.WithStack(err)
	}
	return nil
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) error {
	if err := h.checkLock(w, r); err != nil {
		return err
	}
	if err := h.fileSystem.Remove(r.Context(), r.URL.Path); err != nil {
		return handlePrettyError(w, err, "Failed to delete file")
	}
	return nil
}

func (h *Handler) handleMkcol(w http.ResponseWriter, r *http.Request) error {
	if err := h.fileSystem.Mkdir(r.Context(), r.URL.Path); err != nil {
		return handlePrettyError(w, err, "Failed to create directory")
	}
	return nil
}

func (h *Handler) handleMove(w http.ResponseWriter, r *http.Request) error {
	if err := h.checkLock(w, r); err != nil {
		return err
	}

	destination := r.Header.Get("Destination")
	if destination == "" {
		http.Error(w, "Destination header missing", http.StatusBadRequest)
		return errors.New("destination header missing")
	}

	destURL, err := url.Parse(destination)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to parse destination %q: %v", destination, err), http.StatusBadRequest)
		return juicemud.WithStack(err)
	}

	if err = h.fileSystem.Rename(r.Context(), r.URL.Path, destURL); err != nil {
		return handlePrettyError(w, err, "Failed to move file")
	}

	return nil
}

type multistatus struct {
	XMLName   xml.Name      `xml:"D:multistatus"`
	Xmlns     string        `xml:"xmlns:D,attr"`
	Responses []davResponse `xml:"D:response"`
}

type davResponse struct {
	Href     string     `xml:"D:href"`
	Propstat []propstat `xml:"D:propstat"`
}

type propstat struct {
	Prop   prop   `xml:"D:prop"`
	Status string `xml:"D:status"`
}

type prop struct {
	DisplayName      string       `xml:"D:displayname"`
	ResourceType     resourceType `xml:"D:resourcetype"`
	GetContentLength string       `xml:"D:getcontentlength,omitempty"`
	GetLastModified  string       `xml:"D:getlastmodified,omitempty"`
}

type resourceType struct {
	Collection *struct{} `xml:"D:collection,omitempty"`
}

func (h *Handler) handlePropfind(w http.ResponseWriter, r *http.Request) error {
	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "1"
	}

	ctx := r.Context()
	info, err := h.fileSystem.Stat(ctx, r.URL.Path)
	if err != nil {
		return handlePrettyError(w, err, "Failed to stat file")
	}

	var responses []davResponse
	responses = append(responses, createDavResponse(r.URL.Path, info))

	if depth != "0" && info.IsDir {
		children, err := h.fileSystem.List(ctx, r.URL.Path)
		if err != nil {
			http.Error(w, "Failed to list directory", http.StatusInternalServerError)
			return juicemud.WithStack(err)
		}

		for _, child := range children {
			childPath := path.Join(r.URL.Path, child.Name)
			if child.IsDir {
				childPath += "/"
			}
			responses = append(responses, createDavResponse(childPath, child))
		}
	}

	multiStatus := multistatus{
		Xmlns:     "DAV:",
		Responses: responses,
	}

	// Buffer the XML response before writing headers to handle encoding errors properly
	var buf bytes.Buffer
	encoder := xml.NewEncoder(&buf)
	encoder.Indent("", "  ")
	if err := encoder.Encode(multiStatus); err != nil {
		http.Error(w, "Failed to encode XML", http.StatusInternalServerError)
		return juicemud.WithStack(err)
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	buf.WriteTo(w)

	return nil
}

// createDavResponse is a helper function to build a WebDAV response entry.
func createDavResponse(href string, info *FileInfo) davResponse {
	if info.IsDir && !strings.HasSuffix(href, "/") {
		href = href + "/"
	}
	// URL-encode the path for proper RFC 4918 compliance.
	// Split into segments to preserve path structure while encoding each segment.
	href = encodePath(href)

	displayName := info.Name
	if displayName == "" && info.IsDir {
		displayName = "/"
	}

	resourceType := resourceType{}
	if info.IsDir {
		resourceType.Collection = &struct{}{}
	}

	modifiedTime := info.ModTime.UTC().Format(http.TimeFormat)

	prop := prop{
		DisplayName:      displayName,
		ResourceType:     resourceType,
		GetContentLength: strconv.FormatInt(info.Size, 10),
		GetLastModified:  modifiedTime,
	}

	propStat := propstat{
		Prop:   prop,
		Status: "HTTP/1.1 200 OK",
	}

	return davResponse{
		Href:     href,
		Propstat: []propstat{propStat},
	}
}

// Lock and related types for handling LOCK/UNLOCK requests below.

// lockDiscovery represents the WebDAV lock discovery response.
type lockDiscovery struct {
	XMLName    xml.Name   `xml:"D:prop"`
	LockActive activeLock `xml:"D:lockdiscovery>D:activelock"`
}

// activeLock represents an active lock in the lock discovery response.
type activeLock struct {
	LockType  lockType  `xml:"D:locktype"`
	LockScope lockScope `xml:"D:lockscope"`
	Depth     string    `xml:"D:depth"`
	Owner     string    `xml:"D:owner"`
	Timeout   string    `xml:"D:timeout"`
	LockToken lockToken `xml:"D:locktoken"`
}

// lockType represents the type of lock (write).
type lockType struct {
	Write string `xml:"D:write"`
}

// lockScope represents the scope of the lock (exclusive).
type lockScope struct {
	Exclusive string `xml:"D:exclusive"`
}

// lockToken represents the lock token.
type lockToken struct {
	Href string `xml:"D:href"`
}

// Lock represents a WebDAV lock.
type Lock struct {
	Token     string
	Owner     string
	ExpiresAt time.Time
}

func generateToken() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(bytes)
}

func parseTimeout(timeoutHeader string) time.Duration {
	parts := strings.Split(timeoutHeader, "-")
	if len(parts) == 2 && parts[0] == "Second" {
		if seconds, err := strconv.Atoi(parts[1]); err == nil {
			return time.Duration(seconds) * time.Second
		}
	}
	return 10 * time.Minute
}

func (h *Handler) handleLock(w http.ResponseWriter, r *http.Request) error {
	h.lockMutex.Lock()
	defer h.lockMutex.Unlock()

	path := r.URL.Path
	duration := 10 * time.Minute
	if timeoutHeader := r.Header.Get("Timeout"); timeoutHeader != "" {
		duration = parseTimeout(timeoutHeader)
	}

	lockTk := "opaquelocktoken:" + generateToken()

	lock := h.locks[path]
	if lock != nil && lock.ExpiresAt.After(time.Now()) {
		// Lock is held by another client and hasn't expired
		http.Error(w, "Resource is already locked", http.StatusLocked)
		return nil
	}
	// No lock or lock expired - create new lock
	lock = &Lock{
		Token:     lockTk,
		Owner:     "anonymous",
		ExpiresAt: time.Now().Add(duration),
	}
	h.locks[path] = lock

	lockResponse := lockDiscovery{
		LockActive: activeLock{
			LockType:  lockType{Write: ""},
			LockScope: lockScope{Exclusive: ""},
			Depth:     "infinity",
			Owner:     lock.Owner,
			Timeout:   "Second-" + strconv.Itoa(int(duration.Seconds())),
			LockToken: lockToken{Href: lock.Token},
		},
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if err := encoder.Encode(lockResponse); err != nil {
		http.Error(w, "Failed to encode XML", http.StatusInternalServerError)
		return juicemud.WithStack(err)
	}

	return nil
}

func (h *Handler) handleUnlock(w http.ResponseWriter, r *http.Request) error {
	h.lockMutex.Lock()
	defer h.lockMutex.Unlock()

	path := r.URL.Path
	token := r.Header.Get("Lock-Token")
	token = strings.Trim(token, "<>")

	lock := h.locks[path]
	if lock == nil || subtle.ConstantTimeCompare([]byte(lock.Token), []byte(token)) != 1 {
		http.Error(w, "Lock not found or token mismatch", http.StatusConflict)
		return errors.New("lock not found or token mismatch")
	}

	delete(h.locks, path)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// encodePath URL-encodes each segment of a path while preserving the "/" separators.
func encodePath(p string) string {
	segments := strings.Split(p, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}
