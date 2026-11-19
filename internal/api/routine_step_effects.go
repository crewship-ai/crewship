package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Routine step effects (#5332).
//
// A routine `crewship` step calls an internal route over loopback, then the
// executor persists the step's output. A crash between the two re-runs the
// step on resume with the same request, and the route used to write again —
// and an approval waitpoint stays `approved`, so one approval could authorise
// two writes. Only chat-origin issue writes deduped (agent_operations), and
// that ledger cannot simply be reused: keying an autonomous call also applies
// its crew fence, which would take cross-crew updates away from standing
// automations.
//
// So the dispatcher sends the step's operation key (run, foreach path, step)
// in X-Crewship-Step-Key, and each route that a routine can reach records
// that key in the SAME transaction as its write. A resumed call finds the
// committed row and is answered with the first response; a crash before the
// commit leaves neither the write nor the row, so the resumed call writes
// once. Retries of a failed attempt keep the key too: an attempt that wrote
// but lost its answer (a transport timeout) is replayed, one that wrote
// nothing writes.

const (
	routineStepKeyHeader      = "X-Crewship-Step-Key"
	routineStepReplayedHeader = "X-Crewship-Step-Replayed"
	maxRoutineStepKeyLen      = 200
)

// routineStepEffectVerbs are the routes wrapped by withRoutineStepEffect, by
// the crewship verb that reaches them. The dispatcher sends the key only to
// these, and router wiring is tested against the same list.
var routineStepEffectVerbs = map[string]bool{
	"issue.create":      true,
	"issue.update":      true,
	"issue.comment":     true,
	"issue.link":        true,
	"assignment.create": true,
	"escalation.create": true,
}

var errRoutineStepEffectConflict = errors.New("step_key_conflict")

type routineStepEffect struct {
	runID, key, workspaceID, verb, hash string
}

type routineStepEffectCtxKey struct{}

func routineStepEffectFrom(ctx context.Context) *routineStepEffect {
	eff, _ := ctx.Value(routineStepEffectCtxKey{}).(*routineStepEffect)
	return eff
}

// withRoutineStepEffect replays a committed step write, or hands the handler
// the key it must record with its write. Must run inside withHostChatOrigin:
// the key is honoured only for the authenticated host's pipeline run.
func withRoutineStepEffect(db *sql.DB, verb string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(routineStepKeyHeader)
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		runID := hostPipelineRunID(ctx)
		if runID == "" {
			replyError(w, http.StatusForbidden, "step_key_requires_pipeline_run")
			return
		}
		if len(key) > maxRoutineStepKeyLen {
			replyError(w, http.StatusBadRequest, "step_key_invalid")
			return
		}
		raw, err := readBodyLimit(r, maxJSONBodyBytes)
		if err != nil {
			replyError(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		hash, workspaceID, err := routineStepRequestHash(verb, r.URL.Path, raw)
		if err != nil {
			replyError(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		var runWorkspace string
		err = db.QueryRowContext(ctx, `SELECT workspace_id FROM pipeline_runs WHERE id=?`, runID).Scan(&runWorkspace)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// A nested call_pipeline run and an unsaved draft run have no
			// row of their own. Neither is resumed under its run id (a
			// resumed parent re-runs the whole call with a new one), so a
			// record could never be found again: serve the call as before.
			next.ServeHTTP(w, r)
			return
		case err != nil:
			replyInternalError(w, nil, "routine step effect run", err)
			return
		case workspaceID == "" || runWorkspace != workspaceID:
			replyError(w, http.StatusForbidden, "step_effect_run_unavailable")
			return
		}
		var storedVerb, storedHash, response string
		var status int
		err = db.QueryRowContext(ctx, `SELECT verb, request_hash, status_code, response FROM routine_step_effects WHERE run_id=? AND operation_key=?`, runID, key).
			Scan(&storedVerb, &storedHash, &status, &response)
		switch {
		case err == nil:
			if storedVerb != verb || storedHash != hash {
				// Same step, different request: arguments rendered differently
				// on resume. Neither replaying the old answer nor writing again
				// is right; fail the step and let a person look.
				replyError(w, http.StatusConflict, errRoutineStepEffectConflict.Error())
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set(routineStepReplayedHeader, "true")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, response+"\n") // writeJSON's encoder framing
			return
		case !errors.Is(err, sql.ErrNoRows):
			replyInternalError(w, nil, "routine step effect lookup", err)
			return
		}
		eff := &routineStepEffect{runID: runID, key: key, workspaceID: workspaceID, verb: verb, hash: hash}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, routineStepEffectCtxKey{}, eff)))
	})
}

// routineStepRequestHash binds the key to the exact request: verb, path and
// the canonical JSON body (key order and number spelling do not count).
func routineStepRequestHash(verb, path string, raw []byte) (hash, workspaceID string, err error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var body any
	if err = dec.Decode(&body); err != nil {
		return "", "", err
	}
	if fields, ok := body.(map[string]any); ok {
		workspaceID, _ = fields["workspace_id"].(string)
	}
	canonical, err := json.Marshal(body)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verb + "\x00" + path + "\x00" + string(canonical)))
	return hex.EncodeToString(sum[:]), workspaceID, nil
}

// recordRoutineStepEffectTx stores the step's response in the caller's write
// transaction. It is a no-op for requests that carry no step key. A second
// writer racing on the same key fails here and must roll its write back.
func recordRoutineStepEffectTx(ctx context.Context, tx *sql.Tx, status int, result any) error {
	eff := routineStepEffectFrom(ctx)
	if eff == nil {
		return nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO routine_step_effects(run_id, operation_key, workspace_id, verb, request_hash, status_code, response, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, eff.runID, eff.key, eff.workspaceID, eff.verb, eff.hash, status, string(body), tsformat.Format(time.Now()))
	if err != nil && isUniqueConstraintErr(err) {
		return errRoutineStepEffectConflict
	}
	return err
}

// writeRoutineStepEffectError answers a failed record. The write it guarded
// is rolled back by the caller's deferred Rollback.
func writeRoutineStepEffectError(w http.ResponseWriter, err error) {
	if errors.Is(err, errRoutineStepEffectConflict) {
		replyError(w, http.StatusConflict, errRoutineStepEffectConflict.Error())
		return
	}
	replyError(w, http.StatusInternalServerError, "routine_step_effect_unavailable")
}
