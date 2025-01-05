package dav

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
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

	"github.com/pkg/errors"
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
}

func New(fs FileSystem) *Handler {
	return &Handler{
		fileSystem: fs,
		locks:      map[string]*Lock{},
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
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "File not found", http.StatusNotFound)
		return nil
	} else if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return juicemud.WithStack(err)
	}

	if info.IsDir {
		files, err := h.fileSystem.List(r.Context(), r.URL.Path)
		if err != nil {
			http.Error(w, "Failed to list directory", http.StatusInternalServerError)
			return juicemud.WithStack(err)
		}

		w.Header().Set("Content-Type", "text/html")
		writer := bufio.NewWriter(w)
		writer.WriteString(fmt.Sprintf("<html><head><title>%s</title></head><body>", r.URL.Path))
		for _, f := range files {
			writer.WriteString(fmt.Sprintf("<a href=\"%s\">%s</a></br>", f.Name, f.Name))
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

func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request) error {
	h.lockMutex.Lock()
	lock := h.locks[r.URL.Path]
	h.lockMutex.Unlock()

	// Check if the file is locked
	if lock != nil && lock.ExpiresAt.After(time.Now()) {
		token := r.Header.Get("If")
		if token == "" || !strings.Contains(token, lock.Token) {
			http.Error(w, "Resource is locked", http.StatusLocked)
			return errors.New("resource is locked")
		}
	}

	file, err := h.fileSystem.Write(r.Context(), r.URL.Path)
	if err != nil {
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return juicemud.WithStack(err)
	}
	defer file.Close()

	_, err = io.Copy(file, r.Body)
	if err != nil {
		http.Error(w, "Failed to write file", http.StatusInternalServerError)
		return juicemud.WithStack(err)
	}

	w.WriteHeader(http.StatusCreated)
	return nil
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) error {
	err := h.fileSystem.Remove(r.Context(), r.URL.Path)
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "File not found", http.StatusNotFound)
		return nil
	} else if err != nil {
		http.Error(w, "Failed to delete file", http.StatusInternalServerError)
		return juicemud.WithStack(err)
	}
	return nil
}

func (h *Handler) handleMkcol(w http.ResponseWriter, r *http.Request) error {
	err := h.fileSystem.Mkdir(r.Context(), r.URL.Path)
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "Parent not found", http.StatusNotFound)
		return nil
	} else if err != nil {
		http.Error(w, "Failed to create directory", http.StatusInternalServerError)
		return juicemud.WithStack(err)
	}
	return nil
}

func (h *Handler) handleMove(w http.ResponseWriter, r *http.Request) error {
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

	err = h.fileSystem.Rename(r.Context(), r.URL.Path, destURL)
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "File or destination directory not found", http.StatusNotFound)
		return nil
	} else if err != nil {
		http.Error(w, "Failed to move file", http.StatusInternalServerError)
		return juicemud.WithStack(err)
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
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "Not found", http.StatusNotFound)
		return nil
	} else if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return juicemud.WithStack(err)
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

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	multiStatus := multistatus{
		Xmlns:     "DAV:",
		Responses: responses,
	}

	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if err := encoder.Encode(multiStatus); err != nil {
		http.Error(w, "Failed to encode XML", http.StatusInternalServerError)
		return juicemud.WithStack(err)
	}

	return nil
}

// createDavResponse is a helper function to build a WebDAV response entry.
func createDavResponse(href string, info *FileInfo) davResponse {
	if info.IsDir && !strings.HasSuffix(href, "/") {
		href = href + "/"
	}

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
	_, _ = rand.Read(bytes)
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
	if lock == nil {
		lock = &Lock{
			Token:     lockTk,
			Owner:     "anonymous",
			ExpiresAt: time.Now().Add(duration),
		}
		h.locks[path] = lock
	} else {
		lock.ExpiresAt = time.Now().Add(duration)
	}

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
	if lock == nil || lock.Token != token {
		http.Error(w, "Lock not found or token mismatch", http.StatusConflict)
		return errors.New("lock not found or token mismatch")
	}

	delete(h.locks, path)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
