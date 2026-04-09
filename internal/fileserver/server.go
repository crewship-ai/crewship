package fileserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileInfo describes a file or directory returned by the file listing API.
type FileInfo struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
}

// Server serves file listing and download endpoints for crew output directories.
type Server struct {
	basePath string
}

// NewServer creates a file server rooted at basePath.
func NewServer(basePath string) *Server {
	return &Server{basePath: basePath}
}

// HandleFileList returns a JSON listing of files in a crew's output directory.
func (s *Server) HandleFileList(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	subPath := r.URL.Query().Get("path")

	base := filepath.Join(s.basePath, crewID)
	dir := base
	if subPath != "" {
		dir = filepath.Join(base, filepath.Clean(subPath))
	}

	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Resolve symlinks and re-check containment (matches HandleFileDownload V-09).
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"crew_id": crewID, "files": []FileInfo{}})
		return
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"crew_id": crewID, "files": []FileInfo{}})
		return
	}
	if realDir != realBase && !strings.HasPrefix(realDir, realBase+string(os.PathSeparator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	entries, err := os.ReadDir(realDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{
				"crew_id": crewID,
				"files":   []FileInfo{},
			})
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var files []FileInfo
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(filepath.Join(s.basePath, crewID), filepath.Join(dir, e.Name()))
		files = append(files, FileInfo{
			Path:    rel,
			Name:    e.Name(),
			Size:    info.Size(),
			IsDir:   e.IsDir(),
			ModTime: info.ModTime(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"crew_id": crewID,
		"files":   files,
	})
}

// HandleFileDownload serves a file from a crew's output directory for download.
func (s *Server) HandleFileDownload(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	filePath := r.PathValue("path")

	base := filepath.Join(s.basePath, crewID)
	full := filepath.Join(base, filepath.Clean(filePath))
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// V-09: Resolve symlinks and re-check containment
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !strings.HasPrefix(realFull, realBase+string(os.PathSeparator)) && realFull != realBase {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	f, err := os.Open(realFull)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", detectMIME(filePath))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	// Sanitize filename to prevent Content-Disposition header injection via
	// quotes or control characters in filenames.
	safeName := sanitizeFilename(filepath.Base(filePath))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, safeName))
	io.Copy(w, f)
}

func detectMIME(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".md":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".html":
		return "text/html"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

// sanitizeFilename strips characters that can break Content-Disposition header
// parsing (quotes, backslashes, control chars). Keeps the name human-readable.
func sanitizeFilename(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if r < 0x20 || r == '"' || r == '\\' || r == 0x7f {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	result := b.String()
	if result == "" {
		return "download"
	}
	return result
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
