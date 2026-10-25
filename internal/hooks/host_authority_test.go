package hooks

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestHostAuthorizationBindsExactConfiguration(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	id, err := Register(t.Context(), db, Hook{WorkspaceID: "ws_test", Event: EventPreAgentStart,
		HandlerKind: HandlerKindShell, HandlerConfig: map[string]any{"command": "true"}, Enabled: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Get(t.Context(), db, "ws_test", id)
	if err != nil || h == nil || !HostAuthorized(*h) {
		t.Fatalf("registered approval: %v / %+v", err, h)
	}
	h.HandlerConfig["command"] = "different command"
	if HostAuthorized(*h) {
		t.Fatal("changed command retained approval")
	}
	if _, err := runHandler(t.Context(), db, *h, EventContext{}); !errors.Is(err, ErrShellHookNotAllowed) {
		t.Fatalf("unapproved command reached handler: %v", err)
	}
	if err := Disable(t.Context(), db, "ws_test", id); err != nil {
		t.Fatal(err)
	}
	if err := Enable(t.Context(), db, "ws_test", id); !errors.Is(err, ErrShellHookNotAllowed) {
		t.Fatalf("unprivileged enable: %v", err)
	}
	if err := SetEnabledAuthorized(t.Context(), db, "ws_test", id, true, true); err != nil {
		t.Fatal(err)
	}
	h, err = Get(t.Context(), db, "ws_test", id)
	if err != nil || !HostAuthorized(*h) {
		t.Fatalf("review did not approve current state: %v", err)
	}
}

func TestHostKindRaceCannotLaunderWorkspaceUpdate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	id, err := Register(t.Context(), db, Hook{WorkspaceID: "ws_test", Event: EventPreAgentStart, HandlerKind: HandlerKindHTTP, HandlerConfig: map[string]any{"url": "https://example.test"}, Enabled: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := Get(t.Context(), db, "ws_test", id)
	updateEventRaceHookForTest = func() {
		if _, err := db.Exec(`UPDATE hooks_config SET handler_kind='shell',handler_config='{"command":"true"}' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { updateEventRaceHookForTest = nil }()
	if err := Update(t.Context(), db, "ws_test", *h, false); err == nil {
		t.Fatal("stale HTTP writer changed a concurrently installed host hook")
	}
}

func TestApprovedLegacyHostObservationRetainsApprovalDuringDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
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
			payload, _ = rec.entries[0].Payload.(map[string]any)
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
