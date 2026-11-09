package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// Producer run records of a dashboard's datasets. Starting and finishing a
// run takes exactly the authority of a data batch (artifactDataAuthority with
// write=true: the owner, the owner's consented run, or a human Edit share, on
// the latest code version that declares the collection). Reading takes the
// authority of a data read and only lists collections that version declares.
// The failure reason is shown to the owner and editors only.

const maxDataRunRequestBytes = 8 << 10

type artifactDataRunStart struct {
	BaseVersion    int64  `json:"base_version"`
	IdempotencyKey string `json:"idempotency_key"`
}
type artifactDataRunFinish struct {
	BaseVersion int64  `json:"base_version"`
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
}
type artifactDataRunResponse struct {
	ID          string `json:"id"`
	Collection  string `json:"collection"`
	State       string `json:"state"`
	StartedBy   string `json:"started_by"`
	StartedKind string `json:"started_by_kind"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at,omitempty"`
	Reason      string `json:"failure_reason,omitempty"`
}
type artifactDataRunSummaryResponse struct {
	Collection    string                  `json:"collection"`
	Latest        artifactDataRunResponse `json:"latest"`
	LastAttemptAt string                  `json:"last_attempt_at"`
	LastSuccessAt string                  `json:"last_success_at,omitempty"`
}
type artifactDataRunsResponse struct {
	CodeVersion int64                            `json:"code_version"`
	Runs        []artifactDataRunSummaryResponse `json:"runs"`
}

// The producer's raw actor (account, agent or run ids) never leaves the server.
func projectArtifactDataRun(r *http.Request, run *artifacts.DataRun, showReason bool) artifactDataRunResponse {
	label, kind := "Contributor", "human"
	user := UserFromContext(r.Context())
	switch {
	case strings.HasPrefix(run.StartedBy, "agent:"):
		label, kind = "Agent", "agent"
	case run.StartedBy == "erased" || run.StartedBy == "":
		label, kind = "Former contributor", "unknown"
	case user != nil && (run.StartedBy == user.ID || run.StartedBy == "user:"+user.ID):
		label = "You"
	}
	out := artifactDataRunResponse{ID: run.ID, Collection: run.Collection, State: run.State, StartedBy: label, StartedKind: kind, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt}
	if showReason {
		out.Reason = run.Reason
	}
	return out
}

func decodeDataRunBody(w http.ResponseWriter, r *http.Request, target any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDataRunRequestBytes))
	if err != nil {
		artifactDataError(w, artifacts.ErrDataInvalid)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.More() {
		artifactDataError(w, artifacts.ErrDataInvalid)
		return false
	}
	return true
}

func artifactDataRunError(w http.ResponseWriter, err error) {
	if errors.Is(err, artifacts.ErrDataRunNotFound) {
		replyError(w, 404, "run not found")
		return
	}
	if errors.Is(err, artifacts.ErrDataConflict) {
		writeJSON(w, 409, map[string]string{"error": "This run already ended differently, or its key belongs to another producer. Nothing was changed.", "code": "artifact_data_run_conflict"})
		return
	}
	artifactDataError(w, err)
}

// StartArtifactDataRun handles POST /api/v1/pages/{slug}/artifact/data/{collection}/runs.
func (h *PageHandler) StartArtifactDataRun(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactDataStart(w, r)
	if !ok {
		return
	}
	if err := h.artifactProjectCheck(r, k, true); err != nil {
		artifactDataError(w, err)
		return
	}
	var body artifactDataRunStart
	if !decodeDataRunBody(w, r, &body) {
		return
	}
	if body.BaseVersion < 1 || body.BaseVersion > artifacts.MaxDataVersion {
		artifactDataError(w, artifacts.ErrDataInvalid)
		return
	}
	collection := r.PathValue("collection")
	auth, _ := h.artifactDataAuthority(r, k, collection, body.BaseVersion, true)
	run, created, err := artifacts.NewDataStore(h.db, artifacts.DataOptions{Enabled: true}).StartRun(r.Context(), k, collection, body.IdempotencyKey, auth)
	if err != nil {
		artifactDataRunError(w, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	writeJSON(w, status, projectArtifactDataRun(r, run, true))
}

// FinishArtifactDataRun handles POST /api/v1/pages/{slug}/artifact/data/{collection}/runs/{run}.
func (h *PageHandler) FinishArtifactDataRun(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactDataStart(w, r)
	if !ok {
		return
	}
	if err := h.artifactProjectCheck(r, k, true); err != nil {
		artifactDataError(w, err)
		return
	}
	var body artifactDataRunFinish
	if !decodeDataRunBody(w, r, &body) {
		return
	}
	if body.BaseVersion < 1 || body.BaseVersion > artifacts.MaxDataVersion {
		artifactDataError(w, artifacts.ErrDataInvalid)
		return
	}
	collection := r.PathValue("collection")
	auth, _ := h.artifactDataAuthority(r, k, collection, body.BaseVersion, true)
	run, err := artifacts.NewDataStore(h.db, artifacts.DataOptions{Enabled: true}).FinishRun(r.Context(), k, collection, r.PathValue("run"), body.State, body.Reason, auth)
	if err != nil {
		artifactDataRunError(w, err)
		return
	}
	writeJSON(w, 200, projectArtifactDataRun(r, run, true))
}

// ListArtifactDataRuns handles GET /api/v1/pages/{slug}/artifact/data-runs?version=N.
func (h *PageHandler) ListArtifactDataRuns(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactDataStart(w, r)
	if !ok {
		return
	}
	r = ensureDesignAdminAuditRequest(r, false)
	if err := h.artifactProjectCheck(r, k, false); err != nil {
		artifactDataError(w, err)
		return
	}
	version, ok := dataCodeVersion(w, r)
	if !ok {
		return
	}
	auth, selection := h.artifactDataAuthority(r, k, "", version, false)
	summaries, err := artifacts.NewDataStore(h.db, artifacts.DataOptions{Enabled: true}).RunSummaries(r.Context(), k, func() []string { return selection.collections }, auth)
	if err != nil {
		artifactDataError(w, err)
		return
	}
	notifyDesignAdminAudit(r)
	out := artifactDataRunsResponse{CodeVersion: version, Runs: []artifactDataRunSummaryResponse{}}
	showReason := selection.owner || selection.editor
	for _, summary := range summaries {
		out.Runs = append(out.Runs, artifactDataRunSummaryResponse{Collection: summary.Collection, Latest: projectArtifactDataRun(r, summary.Latest, showReason), LastAttemptAt: summary.LastAttemptAt, LastSuccessAt: summary.LastSuccessAt})
	}
	writeJSON(w, 200, out)
}
