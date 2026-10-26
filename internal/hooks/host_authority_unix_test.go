//go:build !windows

package hooks

import (
	"testing"
	"time"
)

func TestApprovedLegacyHostObservationRetainsApprovalDuringDispatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO hooks_config(id,workspace_id,event,handler_kind,handler_config,blocking,enabled) VALUES('legacy-host-observation','ws_test','post_llm_call','shell','{"command":"printf observed"}',1,0)`); err != nil {
		t.Fatal(err)
	}
	if err := SetEnabledAuthorized(t.Context(), db, "ws_test", "legacy-host-observation", true, true); err != nil {
		t.Fatal(err)
	}
	rec := &recordingEmitter{}
	if err := Dispatch(t.Context(), db, rec, EventPostLLMCall, EventContext{WorkspaceID: "ws_test", Event: EventPostLLMCall}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		var payload map[string]any
		if len(rec.entries) > 0 {
			payload = rec.entries[0].Payload
		}
		rec.mu.Unlock()
		if payload != nil {
			if payload["outcome"] != "pass" || payload["blocking"] != false {
				t.Fatalf("approved legacy observation lost approval or became blocking: %+v", payload)
			}
			h, err := Get(t.Context(), db, "ws_test", "legacy-host-observation")
			if err != nil || !h.Blocking || !HostAuthorized(*h) {
				t.Fatalf("persisted approval/config changed: %+v %v", h, err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("approved legacy observation did not execute")
}
