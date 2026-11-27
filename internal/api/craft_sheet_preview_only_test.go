package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestSheetITUnqualifiedChatWorkbookRejectsHumanAndAgentWrites(t *testing.T) {
	s := newSheetITScenario(t)
	testUnqualifiedChatWorkbook(t, s, func(cells sitCells) []byte { return sitWorkbook(cells, sitInitialOther()) })
}

func testUnqualifiedChatWorkbook(t *testing.T, s *sitScenario, workbook func(sitCells) []byte) {
	t.Helper()
	s.send("Mark item 7 done", true)
	call := s.agent()
	oldAgentGeneration := s.acquire(call)
	res, refused := s.toolCall(call, "craft_sheet_apply", sitApply(oldAgentGeneration, "before-preview"))
	var applied struct {
		Operation string `json:"operation_id"`
	}
	if refused || json.Unmarshal([]byte(res), &applied) != nil || applied.Operation == "" || s.head() != 2 {
		t.Fatalf("seed a real Undo target: %s", res)
	}
	s.tool(call, "craft_sheet_release", map[string]any{"generation": oldAgentGeneration})
	legacy := s.humanSession()
	legacyGeneration := s.humanLease(legacy)
	agentGeneration := s.acquire(call)

	// Use the release fallback with both kinds of earlier admissions live,
	// including the legacy editor's lease and the agent's co-write grant.
	documents := s.m.documents
	documents.mu.Lock()
	profile := documents.applications["gridcraft"]
	profile.ChatSheetPreviewOnly = true
	documents.applications["gridcraft"] = profile
	documents.mu.Unlock()
	before := s.stored(2)
	w := s.humanRequest(http.MethodPost, `{"chat_id":`+strconv.Quote(s.f.a.ChatID)+`}`, "", nil, s.m.human.sessions)
	var preview craftNativeSessionResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || !preview.RealtimeSheet || preview.CanWrite || preview.Generation != "" {
		t.Fatalf("preview admission: %d %s", w.Code, w.Body)
	}
	w = s.humanRequest(http.MethodGet, "", preview.SessionToken, nil, s.m.human.lease)
	var lease craftNativeLeaseResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &lease) != nil || lease.CanEdit || lease.CanWrite || lease.Generation != nil {
		t.Fatalf("preview proof: %d %s", w.Code, w.Body)
	}
	w = s.humanRequest(http.MethodGet, "", preview.SessionToken, nil, documents.readSnapshot)
	if w.Code != 200 || !bytes.Equal(workbookRead(t, w).Bytes, before) {
		t.Fatalf("preview read: %d", w.Code)
	}
	cells := s.headSheets(preview.SessionToken)
	cells["L"]["B8"] = "must not publish"
	candidate := workbook(cells["L"])
	for _, editor := range []struct {
		token, generation string
	}{
		{preview.SessionToken, "1"},
		{legacy, legacyGeneration},
	} {
		w := s.humanSaveWith(editor.token, editor.generation, "2", candidate, "preview-only-save", "", nil)
		if w.Code != 403 {
			t.Fatalf("save from a preview-only workbook: %d %s", w.Code, w.Body)
		}
		w = s.undo(editor.token, editor.generation, applied.Operation)
		if w.Code != 403 {
			t.Fatalf("undo of a valid operation in preview-only mode: %d %s", w.Code, w.Body)
		}
	}
	for _, action := range []string{"craft_sheet_acquire", "craft_sheet_apply"} {
		args := map[string]any{}
		if action == "craft_sheet_apply" {
			args = sitApply(agentGeneration, "preview-only-agent")
			args["base_version"] = "2"
		}
		if got, refused := s.toolCall(call, action, args); !refused || !strings.Contains(got, CraftSheetReasonPreviewOnly) ||
			!strings.Contains(got, craftSheetReasonText[CraftSheetReasonPreviewOnly]) {
			t.Fatalf("%s did not report preview-only access: %s", action, got)
		}
	}
	if w := s.humanRequest(http.MethodGet, "", legacy, nil, s.m.human.lease); w.Code != 200 {
		t.Fatalf("legacy observer: %d %s", w.Code, w.Body)
	}
	if s.head() != 2 || len(s.opsRows()) != 1 || !bytes.Equal(s.stored(2), before) {
		t.Fatal("a refused operation changed the workbook or operations journal")
	}
}
