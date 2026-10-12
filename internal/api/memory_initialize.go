package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/safepath"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// Initialize serves administrative first-write provisioning. Unlike import it
// never replaces existing memory, and may initialize learned.md before the
// consolidator owns it. Ordinary imports retain their narrower allowlist.
func (h *MemoryPortabilityHandler) Initialize(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), roleManage) {
		replyError(w, http.StatusForbidden, "memory initialization requires OWNER or ADMIN")
		return
	}
	var body memoryImportRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid memory initialization body")
		return
	}
	if len(body.Documents) == 0 || len(body.Documents) > 8 {
		replyError(w, http.StatusBadRequest, "provide between 1 and 8 memory documents")
		return
	}
	dir, ok := h.resolveMemoryScope(w, r, body.CrewID, body.AgentSlug)
	if !ok {
		return
	}
	// Validate the entire batch before any file or directory is created.
	seen := map[string]bool{}
	for _, doc := range body.Documents {
		cap := initialMemoryCap(doc.Path, body.AgentSlug != "")
		if cap == 0 || seen[doc.Path] || len(doc.Body) > cap || strings.TrimSpace(doc.Body) == "" {
			replyError(w, http.StatusBadRequest, "invalid, duplicate or oversized initial memory document")
			return
		}
		seen[doc.Path] = true
		if memory.ScanContent(doc.Body) != nil {
			replyError(w, http.StatusUnprocessableEntity, "initial memory rejected by content policy")
			return
		}
		if result := scrubber.New().Validate(doc.Body, scrubber.ModeBlock); result.Decision == scrubber.DecisionReject {
			replyError(w, http.StatusUnprocessableEntity, "initial memory rejected by credential policy")
			return
		}
	}
	written, existing := 0, 0
	for _, doc := range body.Documents {
		target, err := safepath.JoinRel(dir, filepath.FromSlash(doc.Path))
		if err != nil {
			replyError(w, http.StatusBadRequest, "invalid memory path")
			return
		}
		created, err := memory.InitializeFile(r.Context(), h.outputBasePath, target, []byte(doc.Body))
		if err != nil {
			h.logger.Error("memory initialization failed", "err", err, "crew_id", body.CrewID, "agent_slug", body.AgentSlug, "document", doc.Path)
			replyError(w, http.StatusConflict, fmt.Sprintf("could not initialize memory document %s; existing memory was preserved", doc.Path))
			return
		}
		if created {
			written++
		} else {
			existing++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"written": written, "existing": existing})
}

func initialMemoryCap(path string, agent bool) int {
	if !agent {
		switch path {
		case "CREW.md":
			return 4 << 10
		case "learned.md":
			return 8 << 10
		}
		return 0
	}
	switch path {
	case "AGENT.md":
		return 4 << 10
	case "PERSONA.md":
		return 1536
	case "pins.md":
		return 8 << 10
	}
	if strings.HasPrefix(path, "daily/") && strings.HasSuffix(path, ".md") {
		day := strings.TrimSuffix(strings.TrimPrefix(path, "daily/"), ".md")
		if date, err := time.Parse("2006-01-02", day); err == nil && date.Format("2006-01-02") == day {
			return 32 << 10
		}
	}
	return 0
}
