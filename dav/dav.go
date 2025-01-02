package dav

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
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
	Read(name string) (io.ReadCloser, error)   // Read file content
	Write(name string) (io.WriteCloser, error) // Create a new file
	Stat(name string) (*FileInfo, error)       // Retrieve file or directory info
	Remove(name string) error                  // Delete a file or directory
	Mkdir(name string) error                   // Create a directory
	List(name string) ([]*FileInfo, error)     // List files in a directory
	Rename(oldName, newName string) error      // Rename or move a file
}

// Handler provides WebDAV functionality over the given FileSystem.
type Handler struct {
	Fs FileSystem
}

// ServeHTTP handles HTTP requests for the WebDAV server.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
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
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleGet serves files or lists directory contents.
func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	file, err := h.Fs.Read(r.URL.Path)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	info, err := h.Fs.Stat(r.URL.Path)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	if info.IsDir {
		files, err := h.Fs.List(r.URL.Path)
		if err != nil {
			http.Error(w, "Failed to list directory", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		for _, f := range files {
			w.Write([]byte(f.Name + "\n"))
		}
	} else {
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
			_, err = io.Copy(w, file)
			if err != nil {
				http.Error(w, "Failed to read file", http.StatusInternalServerError)
			}
		}
	}
}

func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request) {
	file, err := h.Fs.Write(r.URL.Path)
	if err != nil {
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	_, err = io.Copy(file, r.Body)
	if err != nil {
		http.Error(w, "Failed to write file", http.StatusInternalServerError)
	}
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	err := h.Fs.Remove(r.URL.Path)
	if err != nil {
		http.Error(w, "Failed to delete file", http.StatusInternalServerError)
	}
}

func (h *Handler) handleMkcol(w http.ResponseWriter, r *http.Request) {
	err := h.Fs.Mkdir(r.URL.Path)
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

	err := h.Fs.Rename(r.URL.Path, destination)
	if err != nil {
		http.Error(w, "Failed to move file", http.StatusInternalServerError)
	}
}

func (h *Handler) handlePropfind(w http.ResponseWriter, r *http.Request) {
	// Minimal implementation: Just list files in the directory
	files, err := h.Fs.List(r.URL.Path)
	if err != nil {
		http.Error(w, "Failed to list properties", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.Write([]byte("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<multistatus>\n"))
	for _, file := range files {
		w.Write([]byte("<response>\n"))
		w.Write([]byte("<href>" + file.Name + "</href>\n"))
		w.Write([]byte("</response>\n"))
	}
	w.Write([]byte("</multistatus>"))
}
