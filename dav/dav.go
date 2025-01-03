package dav

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
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
	Read(ctx context.Context, name string) (io.ReadCloser, error)   // Read file content
	Write(ctx context.Context, name string) (io.WriteCloser, error) // Create a new file
	Stat(ctx context.Context, name string) (*FileInfo, error)       // Retrieve file or directory info
	Remove(ctx context.Context, name string) error                  // Delete a file or directory
	Mkdir(ctx context.Context, name string) error                   // Create a directory
	List(ctx context.Context, name string) ([]*FileInfo, error)     // List files in a directory
	Rename(ctx context.Context, oldName, newName string) error      // Rename or move a file
}

// Handler provides WebDAV functionality over the given FileSystem.
type Handler struct {
	fileSystem FileSystem
	locks      map[string]*Lock // File path to lock mapping
	lockMutex  sync.Mutex       // Protects Locks map
}

func New(fs FileSystem) *Handler {
	return &Handler{
		fileSystem: fs,
		locks:      map[string]*Lock{},
	}
}

// ServeHTTP handles HTTP requests for the WebDAV server.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "OPTIONS":
		h.handleOptions(w, r)
	case "GET", "HEAD":
		h.handleGet(w, r)
	case "PUT":
		h.handlePut(w, r)
	case "DELETE":
		h.handleDelete(w, r)
	case "MKCOL":
		h.handleMkcol(w, r)
	case "MOVE":
		h.handleMove(w, r)
	case "PROPFIND":
		h.handlePropfind(w, r)
	case "LOCK":
		h.handleLock(w, r)
	case "UNLOCK":
		h.handleUnlock(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleOptions(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, MOVE, PROPFIND, LOCK, UNLOCK")
	w.Header().Set("DAV", "1, 2")          // WebDAV compliance levels
	w.Header().Set("MS-Author-Via", "DAV") // Required for macOS compatibility
	w.WriteHeader(http.StatusOK)
}

// handleGet serves files or lists directory contents.
func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	info, err := h.fileSystem.Stat(r.Context(), r.URL.Path)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	if info.IsDir {
		files, err := h.fileSystem.List(r.Context(), r.URL.Path)
		if err != nil {
			http.Error(w, "Failed to list directory", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		for _, f := range files {
			w.Write([]byte(f.Name + "\n"))
		}
	} else {
		if info.Size == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Predict content type based on file suffix
		ext := path.Ext(info.Name)
		contentType := mime.TypeByExtension(ext)
		if contentType == "" {
			contentType = "application/octet-stream" // Default for unknown types
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
		w.Header().Set("Last-Modified", info.ModTime.UTC().Format(http.TimeFormat))

		if r.Method != http.MethodHead {
			file, err := h.fileSystem.Read(r.Context(), r.URL.Path)
			if err != nil {
				http.Error(w, "File not found", http.StatusNotFound)
				return
			}
			defer file.Close()

			_, err = io.Copy(w, file)
			if err != nil {
				http.Error(w, "Failed to read file", http.StatusInternalServerError)
			}
		}
	}
}

func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request) {
	h.lockMutex.Lock()
	lock := h.locks[r.URL.Path]
	h.lockMutex.Unlock()

	// Check if the file is locked
	if lock != nil && lock.ExpiresAt.After(time.Now()) {
		token := r.Header.Get("If")
		if token == "" || !strings.Contains(token, lock.Token) {
			http.Error(w, "Resource is locked", http.StatusLocked)
			return
		}
	}

	file, err := h.fileSystem.Write(r.Context(), r.URL.Path)
	if err != nil {
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	_, err = io.Copy(file, r.Body)
	if err != nil {
		http.Error(w, "Failed to write file", http.StatusInternalServerError)
	}

	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	err := h.fileSystem.Remove(r.Context(), r.URL.Path)
	if err != nil {
		http.Error(w, "Failed to delete file", http.StatusInternalServerError)
	}
}

func (h *Handler) handleMkcol(w http.ResponseWriter, r *http.Request) {
	err := h.fileSystem.Mkdir(r.Context(), r.URL.Path)
	if err != nil {
		http.Error(w, "Failed to create directory", http.StatusInternalServerError)
	}
}

func (h *Handler) handleMove(w http.ResponseWriter, r *http.Request) {
	destination := r.Header.Get("Destination")
	if destination == "" {
		http.Error(w, "Destination header missing", http.StatusBadRequest)
		return
	}

	err := h.fileSystem.Rename(r.Context(), r.URL.Path, destination)
	if err != nil {
		http.Error(w, "Failed to move file", http.StatusInternalServerError)
	}
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

func (h *Handler) handlePropfind(w http.ResponseWriter, r *http.Request) {
	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "1" // Default to Depth: 1
	}

	ctx := r.Context() // Pass context to FileSystem methods
	info, err := h.fileSystem.Stat(ctx, r.URL.Path)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// Prepare responses
	var responses []davResponse

	// Add the root directory itself
	responses = append(responses, createDavResponse(r.URL.Path, info))

	// Add children if Depth > 0 and the root is a directory
	if depth != "0" && info.IsDir {
		children, err := h.fileSystem.List(ctx, r.URL.Path)
		if err != nil {
			http.Error(w, "Failed to list directory", http.StatusInternalServerError)
			return
		}

		for _, child := range children {
			childPath := path.Join(r.URL.Path, child.Name)
			if child.IsDir {
				childPath += "/" // Ensure trailing slash for directories
			}
			responses = append(responses, createDavResponse(childPath, child))
		}
	}

	// Prepare and send the multistatus response
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	multiStatus := multistatus{
		Xmlns:     "DAV:",
		Responses: responses,
	}

	// Encode response to XML
	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if err := encoder.Encode(multiStatus); err != nil {
		http.Error(w, "Failed to encode XML", http.StatusInternalServerError)
	}
}

// Helper function to create a davResponse
func createDavResponse(href string, info *FileInfo) davResponse {
	if info.IsDir && !strings.HasSuffix(href, "/") {
		href = href + "/"
	}

	// Set display name
	displayName := info.Name
	if displayName == "" && info.IsDir {
		displayName = "/" // Use "/" as the display name for the root
	}

	// Set resource type
	resourceType := resourceType{}
	if info.IsDir {
		resourceType.Collection = &struct{}{}
	}

	// Handle zero or invalid ModTime
	modifiedTime := info.ModTime.UTC().Format(http.TimeFormat)
	if info.ModTime.IsZero() {
		modifiedTime = "Mon, 02 Jan 2023 15:04:05 GMT" // Fallback
	}

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
	return 10 * time.Minute // Default timeout
}

// Updated handleLock function.
func (h *Handler) handleLock(w http.ResponseWriter, r *http.Request) {
	h.lockMutex.Lock()
	defer h.lockMutex.Unlock()

	// Extract the requested path and lock duration (default to 10 minutes)
	path := r.URL.Path
	duration := 10 * time.Minute
	if timeoutHeader := r.Header.Get("Timeout"); timeoutHeader != "" {
		duration = parseTimeout(timeoutHeader)
	}

	// Generate or reuse lock token
	lockTk := "opaquelocktoken:" + generateToken()

	// Create or refresh the lock
	lock := h.locks[path]
	if lock == nil {
		lock = &Lock{
			Token:     lockTk,
			Owner:     "anonymous", // In a real system, use user info
			ExpiresAt: time.Now().Add(duration),
		}
		h.locks[path] = lock
	} else {
		lock.ExpiresAt = time.Now().Add(duration) // Refresh existing lock
	}

	// Prepare the lock discovery response
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

	// Send the response
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if err := encoder.Encode(lockResponse); err != nil {
		http.Error(w, "Failed to encode XML", http.StatusInternalServerError)
		return
	}
}

func (h *Handler) handleUnlock(w http.ResponseWriter, r *http.Request) {
	h.lockMutex.Lock()
	defer h.lockMutex.Unlock()

	path := r.URL.Path
	token := r.Header.Get("Lock-Token")
	token = strings.Trim(token, "<>")

	lock := h.locks[path]
	if lock == nil || lock.Token != token {
		http.Error(w, "Lock not found or token mismatch", http.StatusConflict)
		return
	}

	// Remove the lock
	delete(h.locks, path)
	w.WriteHeader(http.StatusNoContent)
}
