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

type FileInfo struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
}

type Server struct {
	basePath string
}

func NewServer(basePath string) *Server {
	return &Server{basePath: basePath}
}

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

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{
				"crew_id": crewID,
				"files":   []FileInfo{},
			})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", detectMIME(filePath))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(filePath)))
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

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
