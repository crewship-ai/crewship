package hooks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRegister_ValidationBranches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	cases := []struct {
		name         string
		hook         Hook
		allowedShell bool
		wantSub      string
	}{
		{
			name:    "missing workspace",
			hook:    Hook{Event: "pre_tool_call", HandlerKind: HandlerKindHTTP, HandlerConfig: map[string]any{"url": "https://x"}},
			wantSub: "workspace_id required",
		},
		{
			name:    "missing event",
			hook:    Hook{WorkspaceID: "ws1", HandlerKind: HandlerKindHTTP, HandlerConfig: map[string]any{"url": "https://x"}},
			wantSub: "event required",
		},
		{
			name:         "shell not allowed for non-owner",
			hook:         Hook{WorkspaceID: "ws1", Event: "pre_tool_call", HandlerKind: HandlerKindShell, HandlerConfig: map[string]any{"command": "echo hi"}},
			allowedShell: false,
			wantSub:      ErrShellHookNotAllowed.Error(),
		},
		{
			name:         "shell missing command",
			hook:         Hook{WorkspaceID: "ws1", Event: "pre_tool_call", HandlerKind: HandlerKindShell, HandlerConfig: map[string]any{}},
			allowedShell: true,
			wantSub:      "handler_config.command",
		},
		{
			name:    "http missing url",
			hook:    Hook{WorkspaceID: "ws1", Event: "pre_tool_call", HandlerKind: HandlerKindHTTP, HandlerConfig: map[string]any{}},
			wantSub: "handler_config.url",
		},
		{
			name:    "unknown handler kind",
			hook:    Hook{WorkspaceID: "ws1", Event: "pre_tool_call", HandlerKind: HandlerKind("carrier-pigeon")},
			wantSub: ErrUnknownHandlerKind.Error(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Register(ctx, db, tc.hook, tc.allowedShell)
			if err == nil {
				t.Fatalf("Register accepted invalid hook %q", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v, want mention of %q", err, tc.wantSub)
			}
		})
	}

	// Subagent kind enforces no extra shape — must insert cleanly.
	id, err := Register(ctx, db, Hook{
		WorkspaceID: "ws1", Event: "pre_tool_call", HandlerKind: HandlerKindSubagent,
	}, false)
	if err != nil {
		t.Fatalf("Register subagent hook: %v", err)
	}
	if !strings.HasPrefix(id, "hk_") {
		t.Errorf("id = %q, want hk_ prefix", id)
	}
}

func TestRegister_ShellHookLintsAndPersists(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Unquoted $CREWSHIP_PAYLOAD is exactly the lint gotcha — the hook
	// must still register (lint is advisory, not a gate).
	id, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws1",
		Event:         "pre_tool_call",
		HandlerKind:   HandlerKindShell,
		HandlerConfig: map[string]any{"command": "echo $CREWSHIP_PAYLOAD"},
		Blocking:      true,
		Enabled:       true,
	}, true)
	if err != nil {
		t.Fatalf("Register shell hook: %v", err)
	}
	got, err := Get(ctx, db, "ws1", id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("registered hook not found")
	}
	if got.HandlerConfig["command"] != "echo $CREWSHIP_PAYLOAD" {
		t.Errorf("command = %v, want round-tripped", got.HandlerConfig["command"])
	}
	if !got.Blocking || !got.Enabled {
		t.Errorf("flags lost: blocking=%v enabled=%v", got.Blocking, got.Enabled)
	}
}

func TestRegister_PreservesProvidedIDAndTimestamps(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	id, err := Register(ctx, db, Hook{
		ID:            "hk_explicit",
		WorkspaceID:   "ws1",
		Event:         "pre_tool_call",
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://example.com/hook"},
		CreatedAt:     created,
		CreatedBy:     "user-7",
	}, false)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if id != "hk_explicit" {
		t.Errorf("id = %q, want caller-provided hk_explicit", id)
	}
	got, err := Get(ctx, db, "ws1", id)
	if err != nil || got == nil {
		t.Fatalf("Get: %v / %v", got, err)
	}
	if !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, created)
	}
	if !got.UpdatedAt.Equal(created) {
		t.Errorf("UpdatedAt = %v, want defaulted to CreatedAt", got.UpdatedAt)
	}
	if got.CreatedBy != "user-7" {
		t.Errorf("CreatedBy = %q, want user-7", got.CreatedBy)
	}
}

func TestListByEvent_CrewScoping(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	mk := func(id, crew string) {
		t.Helper()
		_, err := Register(ctx, db, Hook{
			ID: id, WorkspaceID: "ws1", CrewID: crew, Event: "pre_tool_call",
			HandlerKind:   HandlerKindHTTP,
			HandlerConfig: map[string]any{"url": "https://x/" + id},
			Enabled:       true,
		}, false)
		if err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	mk("hk_ws_wide", "")
	mk("hk_crew1", "crew1")
	mk("hk_crew2", "crew2")

	// Crew-bound call: workspace-wide + own crew, never the other crew.
	got, err := ListByEvent(ctx, db, "ws1", "crew1", "pre_tool_call")
	if err != nil {
		t.Fatalf("ListByEvent crew1: %v", err)
	}
	ids := make([]string, len(got))
	for i, h := range got {
		ids[i] = h.ID
	}
	if len(got) != 2 {
		t.Fatalf("crew1 hooks = %v, want [hk_ws_wide hk_crew1] in some order", ids)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen["hk_ws_wide"] || !seen["hk_crew1"] || seen["hk_crew2"] {
		t.Errorf("crew1 hooks = %v", ids)
	}

	// Crew-less call: only the workspace-wide hook.
	got, err = ListByEvent(ctx, db, "ws1", "", "pre_tool_call")
	if err != nil {
		t.Fatalf("ListByEvent ws-wide: %v", err)
	}
	if len(got) != 1 || got[0].ID != "hk_ws_wide" {
		t.Errorf("ws-wide hooks = %+v, want only hk_ws_wide", got)
	}

	// Missing workspace is a hard error, not an empty result.
	if _, err := ListByEvent(ctx, db, "", "crew1", "pre_tool_call"); err == nil {
		t.Error("ListByEvent accepted empty workspace_id")
	}
}

func TestListByEvent_SkipsDisabledHooks(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	id, err := Register(ctx, db, Hook{
		WorkspaceID: "ws1", Event: "pre_tool_call",
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://x"},
		Enabled:       true,
	}, false)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := Disable(ctx, db, "ws1", id); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	got, err := ListByEvent(ctx, db, "ws1", "", "pre_tool_call")
	if err != nil {
		t.Fatalf("ListByEvent: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("disabled hook still listed: %+v", got)
	}
}

func TestGet_RoundTripsMatcherAndConfig(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	id, err := Register(ctx, db, Hook{
		WorkspaceID: "ws1",
		Event:       "pre_tool_call",
		Matcher:     Matcher{Tools: []string{"^Bash$"}, AgentIDs: []string{"ag1"}},
		HandlerKind: HandlerKindHTTP,
		HandlerConfig: map[string]any{
			"url":     "https://example.com/h",
			"timeout": float64(5),
		},
		Enabled: true,
	}, false)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := Get(ctx, db, "ws1", id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil for an existing hook")
	}
	if len(got.Matcher.Tools) != 1 || got.Matcher.Tools[0] != "^Bash$" {
		t.Errorf("Matcher.Tools = %v", got.Matcher.Tools)
	}
	if len(got.Matcher.AgentIDs) != 1 || got.Matcher.AgentIDs[0] != "ag1" {
		t.Errorf("Matcher.AgentIDs = %v", got.Matcher.AgentIDs)
	}
	if got.HandlerConfig["url"] != "https://example.com/h" || got.HandlerConfig["timeout"] != float64(5) {
		t.Errorf("HandlerConfig = %v", got.HandlerConfig)
	}
}

func TestScanHook_CorruptJSONColumns(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	insert := func(id, matcher, cfg string) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO hooks_config
			(id, workspace_id, event, matcher, handler_kind, handler_config, enabled)
			VALUES (?, 'ws1', 'pre_tool_call', ?, 'http', ?, 1)`, id, matcher, cfg)
		if err != nil {
			t.Fatalf("raw insert: %v", err)
		}
	}

	insert("hk_bad_matcher", "{not json", `{"url":"https://x"}`)
	if _, err := Get(ctx, db, "ws1", "hk_bad_matcher"); err == nil ||
		!strings.Contains(err.Error(), "unmarshal matcher") {
		t.Errorf("bad matcher: err = %v, want unmarshal-matcher error", err)
	}

	insert("hk_bad_cfg", "{}", "{not json")
	if _, err := Get(ctx, db, "ws1", "hk_bad_cfg"); err == nil ||
		!strings.Contains(err.Error(), "unmarshal handler_config") {
		t.Errorf("bad config: err = %v, want unmarshal-handler_config error", err)
	}
}

func TestParseTS_AcceptedLayoutsAndGarbage(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
	}{
		{"2026-06-11T10:20:30.123456789Z", true}, // RFC3339Nano
		{"2026-06-11T10:20:30Z", true},           // RFC3339
		{"2026-06-11 10:20:30", true},            // sqlite datetime('now')
		{"yesterday-ish", false},
	}
	for _, tc := range cases {
		got, err := parseTS(tc.in)
		if tc.wantOK {
			if err != nil {
				t.Errorf("parseTS(%q) error: %v", tc.in, err)
				continue
			}
			if got.Year() != 2026 || got.Month() != 6 || got.Day() != 11 {
				t.Errorf("parseTS(%q) = %v, wrong date", tc.in, got)
			}
			if got.Location() != time.UTC {
				t.Errorf("parseTS(%q) not normalised to UTC", tc.in)
			}
		} else if err == nil {
			t.Errorf("parseTS(%q) accepted garbage as %v", tc.in, got)
		}
	}
}

func TestStoreOps_ClosedDBSurfaceErrors(t *testing.T) {
	db := openTestDB(t)
	_ = db.Close()
	ctx := context.Background()

	_, err := Register(ctx, db, Hook{
		WorkspaceID: "ws1", Event: "pre_tool_call",
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://x"},
	}, false)
	if err == nil || !strings.Contains(err.Error(), "hooks: insert") {
		t.Errorf("Register closed-db err = %v", err)
	}
	if err := Delete(ctx, db, "ws1", "hk_x"); err == nil ||
		!strings.Contains(err.Error(), "hooks: delete") {
		t.Errorf("Delete closed-db err = %v", err)
	}
	if err := SetEnabled(ctx, db, "ws1", "hk_x", true); err == nil ||
		!strings.Contains(err.Error(), "hooks: set enabled") {
		t.Errorf("SetEnabled closed-db err = %v", err)
	}
	if _, err := ListByEvent(ctx, db, "ws1", "", "pre_tool_call"); err == nil ||
		!strings.Contains(err.Error(), "hooks: list by event") {
		t.Errorf("ListByEvent closed-db err = %v", err)
	}
}
