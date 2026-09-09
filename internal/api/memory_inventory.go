package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MemoryInventory exposes current knowledge independently of the optional
// version projection. Personal profiles and instructions have separate APIs.
// Reads are bounded and never provision or start a container.
type memoryDocument struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	Content     string `json:"content,omitempty"`
	State       string `json:"state"`
	Bytes       *int64 `json:"bytes"`
	Revision    string `json:"revision,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	HistoryPath string `json:"history_path,omitempty"`
}

var knowledgeName = regexp.MustCompile(`^(AGENT|CREW|BRIEF|LEAD|pins|lessons|learned-[a-zA-Z0-9_-]+)\.md$|^daily/\d{4}-\d{2}-\d{2}\.md$`)

// Each directory is opened beneath an already anchored root. Symlinks are
// refused, including the document itself, so another crew's files cannot be
// presented as this crew's knowledge.
func knowledgeRoot(base, dir string) (*os.Root, error) {
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, os.ErrPermission
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		info, e := root.Lstat(part)
		if e != nil {
			root.Close()
			return nil, e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, os.ErrPermission
		}
		next, e := root.OpenRoot(part)
		root.Close()
		if e != nil {
			return nil, e
		}
		root = next
	}
	return root, nil
}
func currentKnowledge(base, dir, scope, prefix string) ([]memoryDocument, string) {
	docs := []memoryDocument{}
	root, err := knowledgeRoot(base, dir)
	if errors.Is(err, os.ErrNotExist) {
		return docs, "empty"
	}
	if err != nil {
		return docs, "unavailable"
	}
	defer root.Close()
	names := []string{}
	for _, sub := range []string{".", "daily"} {
		if sub != "." {
			info, e := root.Lstat(sub)
			if e != nil {
				if errors.Is(e, os.ErrNotExist) {
					continue
				}
				return docs, "unavailable"
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return docs, "unavailable"
			}
		}
		f, e := root.Open(sub)
		if e != nil {
			if sub == "." {
				return docs, "unavailable"
			}
			continue
		}
		entries, e := f.ReadDir(201)
		f.Close()
		if e != nil && e != io.EOF {
			return docs, "unavailable"
		}
		// Refuse partial discovery rather than claim this is the whole inventory.
		if len(entries) > 200 {
			return docs, "unavailable"
		}
		for _, entry := range entries {
			name := filepath.ToSlash(filepath.Join(sub, entry.Name()))
			if (knowledgeName.MatchString(name) || (scope == "workspace" && sub == "." && strings.HasSuffix(name, ".md") && name != "PERSONA.md")) && entry.Type()&os.ModeSymlink == 0 && !entry.IsDir() {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	remainingBytes := 512 * 1024
	for _, name := range names {
		doc := memoryDocument{ID: scope + ":" + name, Name: name, Scope: scope, State: "unavailable"}
		info, e := root.Lstat(name)
		if e != nil || !info.Mode().IsRegular() {
			docs = append(docs, doc)
			continue
		}
		f, e := root.Open(name)
		if e != nil {
			docs = append(docs, doc)
			continue
		}
		body, e := io.ReadAll(io.LimitReader(f, 65537))
		f.Close()
		if e != nil || len(body) > 65536 || len(body) > remainingBytes {
			docs = append(docs, doc)
			continue
		}
		remainingBytes -= len(body)
		size := int64(len(body))
		sum := sha256.Sum256(body)
		doc.Bytes = &size
		doc.Content = string(body)
		doc.State = "available"
		doc.Revision = hex.EncodeToString(sum[:])
		doc.UpdatedAt = info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
		if scope != "workspace" {
			doc.HistoryPath = prefix + name
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return docs, "empty"
	}
	return docs, "available"
}

func (h *PersonaHandler) AgentMemoryInventory(w http.ResponseWriter, r *http.Request) {
	paths, crewID, slug, _, _, err := h.resolveAgentPaths(r, r.PathValue("agentId"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "agent memory not found")
		} else {
			replyError(w, http.StatusServiceUnavailable, "agent memory unavailable")
		}
		return
	}
	h.writeMemoryInventory(w, paths.AgentDir, paths.CrewDir, slug, crewID, r)
}
func (h *PersonaHandler) CrewMemoryInventory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("crewId")
	var slug string
	if err := h.db.QueryRowContext(r.Context(), "SELECT slug FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL", id, WorkspaceIDFromContext(r.Context())).Scan(&slug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "crew not found")
		} else {
			replyError(w, http.StatusServiceUnavailable, "crew memory unavailable")
		}
		return
	}
	h.writeMemoryInventory(w, "", h.crewSharedMemoryDir(id), "", id, r)
}
func (h *PersonaHandler) writeMemoryInventory(w http.ResponseWriter, agentDir, crewDir, slug, crewID string, r *http.Request) {
	if h.outputBasePath == "" {
		replyError(w, http.StatusServiceUnavailable, "memory storage unavailable")
		return
	}
	docs := []memoryDocument{}
	states := map[string]string{}
	if agentDir != "" {
		rows, state := currentKnowledge(h.outputBasePath, agentDir, "agent", "agent:"+slug+"/")
		docs = append(docs, rows...)
		states["agent"] = state
	}
	if crewDir != "" {
		// Shared history is keyed by immutable crew ID in the audit watcher
		// and consolidator; the display slug is not its storage identity.
		rows, state := currentKnowledge(h.outputBasePath, crewDir, "crew", "crew:"+crewID+"/")
		docs = append(docs, rows...)
		states["crew"] = state
	}
	states["workspace"] = "unavailable"
	wsID := WorkspaceIDFromContext(r.Context())
	if h.workspaceMemoryRoot != "" && filepath.Base(wsID) == wsID && wsID != "." && wsID != ".." {
		rows, state := currentKnowledge(h.workspaceMemoryRoot, filepath.Join(h.workspaceMemoryRoot, wsID), "workspace", "workspace:"+wsID+"/")
		docs = append(docs, rows...)
		states["workspace"] = state
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"documents": docs, "scopes": states, "peer_generation": "unavailable", "source": "current_files"})
}
