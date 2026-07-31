package fileserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/safepath"
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

// crewRel validates the caller-supplied crew id and sub-path and returns the
// basePath-relative path to operate on.
//
// The crew id is checked first and separately, through the same
// safepath.ValidateComponent every other handler uses for an id destined for
// a filesystem path: it arrives from the request path, and every containment
// check below is relative to the *crew* directory — so a crew id of "../.."
// would make the crew directory the check's own base and pass trivially,
// handing out any file the process can read. A crew id is one path element,
// nothing else.
func crewRel(crewID, subPath string) (string, bool) {
	if _, err := safepath.ValidateComponent(crewID); err != nil {
		return "", false
	}
	rel := crewID
	if subPath != "" {
		rel = filepath.Join(crewID, filepath.Clean(subPath))
	}
	inside, err := filepath.Rel(crewID, rel)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return rel, true
}

// HandleFileList returns a JSON listing of files in a crew's output directory.
func (s *Server) HandleFileList(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	subPath := r.URL.Query().Get("path")

	empty := func() {
		writeJSON(w, http.StatusOK, map[string]any{"crew_id": crewID, "files": []FileInfo{}})
	}

	rel, ok := crewRel(crewID, subPath)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	base := filepath.Join(s.basePath, crewID)
	dir := filepath.Join(s.basePath, rel)

	// Resolve symlinks and re-check containment (matches HandleFileDownload V-09).
	// The check is against the *crew* directory, not basePath, so a symlink
	// that stays inside the storage tree but points at another crew is refused
	// as well.
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		empty()
		return
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		empty()
		return
	}
	if realDir != realBase && !strings.HasPrefix(realDir, realBase+string(os.PathSeparator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Read through an os.Root anchored at basePath: each component is
	// validated as the root walks it, so no symlink reached along the way can
	// redirect the listing outside the storage tree between the check above
	// and the open below.
	root, err := os.OpenRoot(s.basePath)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer root.Close()
	d, err := root.Open(rel)
	if err != nil {
		if os.IsNotExist(err) {
			empty()
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer d.Close()
	entries, err := d.ReadDir(-1)
	if err != nil {
		if os.IsNotExist(err) {
			empty()
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
		crewRelPath, _ := filepath.Rel(crewID, filepath.Join(rel, e.Name()))
		files = append(files, FileInfo{
			Path:    crewRelPath,
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

	rel, ok := crewRel(crewID, filePath)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	base := filepath.Join(s.basePath, crewID)
	full := filepath.Join(s.basePath, rel)

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

	// Serve through an os.Root anchored at basePath — see HandleFileList.
	root, err := os.OpenRoot(s.basePath)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer root.Close()
	f, err := root.Open(rel)
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
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
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
