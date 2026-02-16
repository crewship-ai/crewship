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
	teamID := r.PathValue("id")
	subPath := r.URL.Query().Get("path")

	dir := filepath.Join(s.basePath, teamID)
	if subPath != "" {
		dir = filepath.Join(dir, filepath.Clean(subPath))
	}

	if !strings.HasPrefix(dir, filepath.Join(s.basePath, teamID)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{
				"team_id": teamID,
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
		rel, _ := filepath.Rel(filepath.Join(s.basePath, teamID), filepath.Join(dir, e.Name()))
		files = append(files, FileInfo{
			Path:    rel,
			Name:    e.Name(),
			Size:    info.Size(),
			IsDir:   e.IsDir(),
			ModTime: info.ModTime(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"team_id": teamID,
		"files":   files,
	})
}

func (s *Server) HandleFileDownload(w http.ResponseWriter, r *http.Request) {
	teamID := r.PathValue("id")
	filePath := r.PathValue("path")

	full := filepath.Join(s.basePath, teamID, filepath.Clean(filePath))
	if !strings.HasPrefix(full, filepath.Join(s.basePath, teamID)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	f, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	info, _ := f.Stat()
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
