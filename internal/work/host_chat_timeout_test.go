package work

import (
	"strings"
	"testing"
)

func TestHostChatTimeout_PreservesDiagnosisWithinCanonicalStatusSchema(t *testing.T) {
	store, db, _ := newTestStore(t)
	request := hostChatRequest("msg_timeout")
	if _, _, err := store.BeginHostChat(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	exit := 137
	if err := store.FinishHostChat(t.Context(), request.RunID, HostChatResult{Status: "TIMEOUT", ObservedExit: &exit}); err != nil {
		t.Fatalf("timeout cannot settle against the production run_status CHECK: %v", err)
	}
	projection, owned, err := store.RunProjection(t.Context(), request.RunID)
	if err != nil || !owned || !projection.Ready || projection.Status != "FAILED" {
		t.Fatalf("canonical timeout projection: %+v owned=%v err=%v", projection, owned, err)
	}
	if projection.Result.Metadata["runtime_status"] != "TIMEOUT" || projection.Result.ErrorMessage == nil || !strings.Contains(*projection.Result.ErrorMessage, "timed out") {
		t.Fatalf("timeout diagnosis lost: %+v", projection.Result)
	}
	var status, reason, state string
	if err := db.QueryRow(`SELECT a.run_status,w.state_reason,w.state FROM work_attempts a JOIN work_items w ON w.id=a.work_id WHERE a.run_id=?`, request.RunID).Scan(&status, &reason, &state); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || state != "failed" || !strings.Contains(reason, "timed out") {
		t.Fatalf("timeout persisted as status=%s state=%s reason=%s", status, state, reason)
	}
}
