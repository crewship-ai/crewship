package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// Shared history is a deliberate additional audience. Definition-only and Run
// grants retain their shipped semantics. Inputs are not shown directly; outputs
// may contain them. Requester identities, errors,
// costs, internal provenance and transcripts are never part of this projection.
const maxGuestRoutineResultBytes = 1 << 20
const maxGuestRoutineResultContentBytes = 256 << 10

var guestRoutineResultScrubber = scrubber.New()

type guestRoutineRun struct {
	ID        string  `json:"run_id"`
	RoutineID string  `json:"routine_id"`
	Status    string  `json:"status"`
	Mode      string  `json:"mode"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at"`
}

type guestRoutineRunResult struct {
	guestRoutineRun
	Output      *string           `json:"output"`
	StepOutputs map[string]string `json:"step_outputs"`
}

func (r *Router) restrictedRoutineRunHistory(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		replyError(w, 404, "Routine results unavailable")
		return
	}
	if req.PathValue("runId") != "" && (req.URL.Query().Has("limit") || req.URL.Query().Has("offset")) {
		replyError(w, 400, "Pagination is only available when listing runs")
		return
	}
	limit, offset, ok := routineListPage(w, req.URL.Query().Get("limit"), req.URL.Query().Get("offset"), 100)
	if !ok {
		return
	}
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		replyError(w, 500, "Routine results unavailable")
		return
	}
	defer tx.Rollback()
	if _, err = guestRoutineMember(req.Context(), tx, user.ID, workspace); err != nil {
		replyError(w, 404, "Routine results unavailable")
		return
	}
	var routineID string
	err = tx.QueryRowContext(req.Context(), `SELECT id FROM pipelines WHERE workspace_id=? AND (id=? OR slug=?) AND deleted_at IS NULL`, workspace, req.PathValue("routineId"), req.PathValue("routineId")).Scan(&routineID)
	if err != nil {
		guestRoutineResultsError(w, err)
		return
	}
	store := access.Store{DB: r.db}
	for _, operation := range []string{"view", "results"} {
		if err = store.CheckIn(req.Context(), tx, user.ID, workspace, access.Right{Kind: "routine", ID: routineID, Operation: operation}); err != nil {
			guestRoutineResultsError(w, err)
			return
		}
	}
	// Every inherited origin remains an additional read ceiling. Compare the
	// run, its pinned version and the current recipe; detaching the live recipe
	// cannot erase private provenance of a historical run. Missing/unknown
	// origins (including web origins without a chat audience) fail closed.
	from := ` FROM pipeline_runs pr JOIN pipelines p ON p.id=pr.pipeline_id AND p.workspace_id=pr.workspace_id
	 LEFT JOIN pipeline_versions v ON v.pipeline_id=p.id AND v.version=pr.pipeline_version
	 WHERE pr.workspace_id=? AND p.id=? AND p.deleted_at IS NULL
	 AND (pr.pipeline_version IS NULL OR v.id IS NOT NULL)
     AND COALESCE(pr.triggered_via,'')<>'issue'
     AND (pr.chat_origin_id IS NOT NULL OR (
       (` + autonomousPipelineRunSQL + `)
       AND COALESCE(pr.is_replay,0)=0
       AND EXISTS(SELECT 1 FROM journal_entries proof
         WHERE proof.workspace_id=pr.workspace_id
         AND ((proof.entry_type='run.started' AND proof.trace_id=pr.id)
           OR (proof.entry_type='pipeline.run.started' AND proof.actor_id=pr.id))
         AND CASE WHEN json_valid(proof.payload) THEN json_type(proof.payload)='object' ELSE 0 END)
     ))
	 AND NOT EXISTS (
	   SELECT 1 FROM (SELECT pr.chat_origin_id AS origin_id UNION SELECT p.chat_origin_id UNION SELECT v.chat_origin_id) origins
	   WHERE origins.origin_id IS NOT NULL AND NOT EXISTS (
	     SELECT 1 FROM execution_origins e JOIN chats c ON c.id=e.chat_id AND c.workspace_id=e.workspace_id
	     WHERE e.id=origins.origin_id AND e.workspace_id=pr.workspace_id AND (` + chataudience.VisibleSQL + `)
	   )
	 )`
	args := []any{workspace, routineID}
	args = append(args, chataudience.Args(user.ID)...)
	runID := req.PathValue("runId")
	if runID == "" {
		var total int
		if err = tx.QueryRowContext(req.Context(), `SELECT COUNT(*)`+from, args...).Scan(&total); err != nil {
			guestRoutineResultsError(w, err)
			return
		}
		rows, queryErr := tx.QueryContext(req.Context(), `SELECT pr.id,p.id,pr.status,pr.mode,pr.started_at,pr.ended_at`+from+` ORDER BY pr.started_at DESC,pr.id LIMIT ? OFFSET ?`, append(args, limit, offset)...)
		if queryErr != nil {
			guestRoutineResultsError(w, queryErr)
			return
		}
		items := []guestRoutineRun{}
		for rows.Next() {
			var item guestRoutineRun
			if err = rows.Scan(&item.ID, &item.RoutineID, &item.Status, &item.Mode, &item.StartedAt, &item.EndedAt); err != nil {
				break
			}
			items = append(items, item)
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil || rowErr != nil {
			replyError(w, 500, "Routine results unavailable")
			return
		}
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
		setRoutineListNextOffset(w, offset, limit, len(items))
		writeJSON(w, 200, items)
		return
	}
	// Bound stored content before loading it into application memory. Include
	// per-step JSON overhead, then enforce the encoded response ceiling too.
	var contentBytes int64
	err = tx.QueryRowContext(req.Context(), `SELECT
      length(CAST(COALESCE(pr.output,'') AS BLOB)) +
      COALESCE((SELECT SUM(length(CAST(output AS BLOB))+length(CAST(step_id AS BLOB))+16)
        FROM pipeline_run_step_outputs WHERE run_id=pr.id),0)`+from+` AND pr.id=?`, append(args, runID)...).Scan(&contentBytes)
	if err != nil {
		guestRoutineResultsError(w, err)
		return
	}
	if contentBytes > maxGuestRoutineResultContentBytes {
		replyError(w, http.StatusRequestEntityTooLarge, "Routine result is too large")
		return
	}
	result := guestRoutineRunResult{StepOutputs: map[string]string{}}
	err = tx.QueryRowContext(req.Context(), `SELECT pr.id,p.id,pr.status,pr.mode,pr.started_at,pr.ended_at,pr.output`+from+` AND pr.id=?`, append(args, runID)...).Scan(&result.ID, &result.RoutineID, &result.Status, &result.Mode, &result.StartedAt, &result.EndedAt, &result.Output)
	if err != nil {
		guestRoutineResultsError(w, err)
		return
	}
	rows, err := tx.QueryContext(req.Context(), `SELECT step_id,output FROM pipeline_run_step_outputs WHERE run_id=? ORDER BY step_id`, result.ID)
	if err != nil {
		guestRoutineResultsError(w, err)
		return
	}
	for rows.Next() {
		var step, output string
		if err = rows.Scan(&step, &output); err != nil {
			break
		}
		result.StepOutputs[step] = guestRoutineResultScrubber.Scrub(output)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		replyError(w, 500, "Routine results unavailable")
		return
	}
	if result.Output != nil {
		output := guestRoutineResultScrubber.Scrub(*result.Output)
		result.Output = &output
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		guestRoutineResultsError(w, err)
		return
	}
	if len(encoded) > maxGuestRoutineResultBytes {
		replyError(w, http.StatusRequestEntityTooLarge, "Routine result is too large")
		return
	}
	writeJSON(w, 200, json.RawMessage(encoded))
}

func guestRoutineResultsError(w http.ResponseWriter, err error) {
	if errors.Is(err, access.ErrDenied) || errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "Routine results unavailable")
		return
	}
	replyError(w, 500, "Routine results unavailable")
}
