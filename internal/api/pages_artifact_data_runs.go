package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// Producer run records of a dashboard's datasets. Starting and finishing a
// run takes exactly the authority of a data batch (artifactDataAuthority with
// write=true: the owner or a human Edit share on REST; agent runs have no run
// operation on the artifact tool path yet), on the latest code version that
// declares the collection. Only the producer that started a run may finish it
// as succeeded, naming the data generation it wrote (see artifacts.FinishRun);
// any writer may finish a run as failed, and the finisher is recorded. Reading
// takes the authority of a data read and only lists collections that version
// declares. The failure reason is shown to the owner and editors only.

const maxDataRunRequestBytes = 8 << 10

type artifactDataRunStart struct {
	BaseVersion    int64  `json:"base_version"`
	IdempotencyKey string `json:"idempotency_key"`
}
type artifactDataRunFinish struct {
	BaseVersion    int64  `json:"base_version"`
	State          string `json:"state"`
	Reason         string `json:"reason,omitempty"`
	DataGeneration *int64 `json:"data_generation,omitempty"`
	Unchanged      bool   `json:"unchanged,omitempty"`
}
type artifactDataRunResponse struct {
	ID              string `json:"id"`
	Collection      string `json:"collection"`
	State           string `json:"state"`
	StartedBy       string `json:"started_by"`
	StartedKind     string `json:"started_by_kind"`
	StartedAt       string `json:"started_at"`
	CodeVersion     int64  `json:"code_version"`
	StartGeneration int64  `json:"start_generation"`
	FinishedAt      string `json:"finished_at,omitempty"`
	FinishedBy      string `json:"finished_by,omitempty"`
	FinishedKind    string `json:"finished_by_kind,omitempty"`
	DataGeneration  *int64 `json:"data_generation,omitempty"`
	Partial         bool   `json:"partial,omitempty"`
	Reason          string `json:"failure_reason,omitempty"`
}
type artifactDataRunSummaryResponse struct {
	Collection    string                  `json:"collection"`
	Latest        artifactDataRunResponse `json:"latest"`
	LastAttemptAt string                  `json:"last_attempt_at"`
	LastSuccessAt string                  `json:"last_success_at,omitempty"`
	// Active is every running run of the collection, for the owner and editors only.
	Active []artifactDataRunResponse `json:"active_runs,omitempty"`
}
type artifactDataRunOverlapResponse struct {
	Error    string                    `json:"error"`
	Code     string                    `json:"code,omitempty"`
	Blocking []artifactDataRunResponse `json:"blocking_runs,omitempty"`
}
type artifactDataRunsResponse struct {
	CodeVersion int64 `json:"code_version"`
	// StalledAfterSeconds is the configured threshold after which clients show
	// a running run as stalled; a display default, not an end of the run.
	StalledAfterSeconds int64                            `json:"stalled_after_seconds"`
	Runs                []artifactDataRunSummaryResponse `json:"runs"`
}

// The producer's raw actor (account, agent or run ids) never leaves the server.
// The "agent:" branch is for future agent-run producers and restored rows; the
// artifact tool path has no run operations yet, so live requests record humans.
func projectRunActor(r *http.Request, actor string) (label, kind string) {
	label, kind = "Contributor", "human"
	user := UserFromContext(r.Context())
	switch {
	case strings.HasPrefix(actor, "agent:"):
		label, kind = "Agent", "agent"
	case actor == "erased" || actor == "":
		label, kind = "Former contributor", "unknown"
	case user != nil && (actor == user.ID || actor == "user:"+user.ID):
		label = "You"
	}
	return label, kind
}

func projectArtifactDataRun(r *http.Request, run *artifacts.DataRun, showReason bool) artifactDataRunResponse {
	label, kind := projectRunActor(r, run.StartedBy)
	out := artifactDataRunResponse{ID: run.ID, Collection: run.Collection, State: run.State, StartedBy: label, StartedKind: kind, StartedAt: run.StartedAt, CodeVersion: run.CodeVersion, StartGeneration: run.StartGeneration, FinishedAt: run.FinishedAt, DataGeneration: run.EndGeneration, Partial: run.Partial()}
	if run.State != "running" {
		out.FinishedBy, out.FinishedKind = projectRunActor(r, run.FinishedBy)
	}
	if showReason {
		out.Reason = run.Reason
	}
	return out
}

func decodeDataRunBody(w http.ResponseWriter, r *http.Request, target any, fields ...string) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDataRunRequestBytes))
	if err != nil {
		artifactDataError(w, artifacts.ErrDataInvalid)
		return false
	}
	if err := artifacts.DecodeDataJSON(raw, target, fields...); err != nil {
		artifactDataError(w, artifacts.ErrDataInvalid)
		return false
	}
	return true
}

func artifactDataRunError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, artifacts.ErrDataRunNotFound) {
		replyError(w, 404, "run not found")
		return
	}
	if errors.Is(err, artifacts.ErrDataRunForbidden) {
		writeJSON(w, 403, map[string]string{"error": "Only the producer that started this run can mark it succeeded. You can finish it as failed. Nothing was changed.", "code": "artifact_data_run_forbidden"})
		return
	}
	if errors.Is(err, artifacts.ErrDataRunBusy) {
		writeJSON(w, 409, map[string]string{"error": "This dataset already has the maximum number of running refreshes. Finish one, then start again.", "code": "artifact_data_run_busy"})
		return
	}
	var overlap *artifacts.DataRunOverlapError
	if errors.As(err, &overlap) {
		// Only writers reach a finish, so the blocking runs are theirs to see and abandon.
		blocking := make([]artifactDataRunResponse, 0, len(overlap.Runs))
		for _, run := range overlap.Runs {
			blocking = append(blocking, projectArtifactDataRun(r, run, true))
		}
		writeJSON(w, 409, artifactDataRunOverlapResponse{Error: "Another refresh of this dataset is still running, or failed after changing data while this one ran. Finish the runs in blocking_runs (any writer can finish one as failed), then start a new run. Nothing was changed.", Code: "artifact_data_run_overlap", Blocking: blocking})
		return
	}
	if errors.Is(err, artifacts.ErrDataRunCodeVersion) {
		writeJSON(w, 409, map[string]string{"error": "The run started on a different code version. Finish it as failed and start a new run. Nothing was changed.", "code": "artifact_data_run_code_version"})
		return
	}
	if errors.Is(err, artifacts.ErrDataRunGeneration) {
		writeJSON(w, 409, map[string]string{"error": "The collection changed outside this run, or the claimed generation does not match a linked batch or a verified unchanged run. Nothing was changed.", "code": "artifact_data_run_generation"})
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
	if !decodeDataRunBody(w, r, &body, "base_version", "idempotency_key") {
		return
	}
	if body.BaseVersion < 1 || body.BaseVersion > artifacts.MaxDataVersion {
		artifactDataError(w, artifacts.ErrDataInvalid)
		return
	}
	collection := r.PathValue("collection")
	auth, _ := h.artifactDataAuthority(r, k, collection, body.BaseVersion, true)
	run, created, err := artifacts.NewDataStore(h.db, artifacts.DataOptionsFromEnv()).StartRun(r.Context(), k, collection, body.IdempotencyKey, body.BaseVersion, auth)
	if err != nil {
		artifactDataRunError(w, r, err)
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
	if !decodeDataRunBody(w, r, &body, "base_version", "state", "reason", "data_generation", "unchanged") {
		return
	}
	if body.BaseVersion < 1 || body.BaseVersion > artifacts.MaxDataVersion {
		artifactDataError(w, artifacts.ErrDataInvalid)
		return
	}
	collection := r.PathValue("collection")
	auth, _ := h.artifactDataAuthority(r, k, collection, body.BaseVersion, true)
	run, err := artifacts.NewDataStore(h.db, artifacts.DataOptionsFromEnv()).FinishRun(r.Context(), k, collection, r.PathValue("run"), body.BaseVersion, artifacts.DataRunFinish{State: body.State, Reason: body.Reason, DataGeneration: body.DataGeneration, Unchanged: body.Unchanged}, auth)
	if err != nil {
		artifactDataRunError(w, r, err)
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
	store := artifacts.NewDataStore(h.db, artifacts.DataOptionsFromEnv())
	summaries, err := store.RunSummaries(r.Context(), k, func() []string { return selection.collections }, auth)
	if err != nil {
		artifactDataError(w, err)
		return
	}
	notifyDesignAdminAudit(r)
	out := artifactDataRunsResponse{CodeVersion: version, StalledAfterSeconds: int64(store.StalledAfter() / time.Second), Runs: []artifactDataRunSummaryResponse{}}
	showReason := selection.owner || selection.editorLatest
	for _, summary := range summaries {
		entry := artifactDataRunSummaryResponse{Collection: summary.Collection, Latest: projectArtifactDataRun(r, summary.Latest, showReason), LastAttemptAt: summary.LastAttemptAt, LastSuccessAt: summary.LastSuccessAt}
		if showReason {
			for _, run := range summary.Active {
				entry.Active = append(entry.Active, projectArtifactDataRun(r, run, true))
			}
		}
		out.Runs = append(out.Runs, entry)
	}
	writeJSON(w, 200, out)
}
