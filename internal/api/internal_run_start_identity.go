package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/crewship-ai/crewship/internal/journal"
)

var errRunStartIdentityConflict = errors.New("run_start_identity_conflict")
var errRunAlreadyTerminal = errors.New("run_already_terminal")

// matchRunStart never accepts a conflicting or ambiguous historical identity.
// Metadata participates in the immutable admission intent, not just the actor.
func (h *InternalHandler) matchRunStart(ctx context.Context, want journal.Entry) (bool, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT workspace_id,COALESCE(crew_id,''),COALESCE(agent_id,''),payload FROM journal_entries WHERE trace_id=? AND entry_type='run.started'`, want.TraceID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		var ws, crew, agent, raw string
		if err = rows.Scan(&ws, &crew, &agent, &raw); err != nil {
			return false, err
		}
		var actual map[string]any
		encoded, marshalErr := json.Marshal(want.Payload)
		if marshalErr != nil {
			return false, marshalErr
		}
		var expected map[string]any
		actualDecoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
		actualDecoder.UseNumber()
		expectedDecoder := json.NewDecoder(bytes.NewReader(encoded))
		expectedDecoder.UseNumber()
		if actualDecoder.Decode(&actual) != nil || expectedDecoder.Decode(&expected) != nil || ws != want.WorkspaceID || crew != want.CrewID || agent != want.AgentID || !reflect.DeepEqual(actual, expected) {
			return false, errRunStartIdentityConflict
		}
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	if n > 1 {
		return false, errRunStartIdentityConflict
	}
	// An old admission ACK must never launch an already settled execution again.
	var ended bool
	if err = h.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM journal_entries WHERE trace_id=? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop'))`, want.TraceID).Scan(&ended); err != nil {
		return false, err
	}
	if ended {
		return false, errRunAlreadyTerminal
	}
	return n == 1, nil
}
