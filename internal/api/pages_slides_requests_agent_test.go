package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/sidecar"
	"github.com/crewship-ai/crewship/internal/slides"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestSlidesSelectedActualSidecarRouterFrozenSnapshotReplacementReplayAndRevocation(t *testing.T) {
	f, base, input := selectedSlidesHTTPFixture(t)
	f.r.Shutdown()
	writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	const master = "synthetic-selected-internal-master"
	router, err := NewRouter(f.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithInternalToken(master), WithJournal(writer), WithStoragePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	f.r = router
	t.Cleanup(router.Shutdown)
	router.pages.artifactDispatch = func(context.Context, string, string, string, ws.ChatMessageOption) error { return nil }
	// A valid admitted raw wire snapshot becomes >128KiB when the host JSON
	// encoder escapes its32KiB original text. Exercise the actual full path.
	stateResponse := f.slideDo(f.boss, "GET", base, "")
	wantCode(t, stateResponse, 200, "read original")
	var state slides.State
	if err = json.Unmarshal(stateResponse.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	var original slides.Element
	for _, slide := range state.Deck.Slides {
		for _, element := range slide.Elements {
			if element.ID == input.Selection.ElementID {
				original = element
			}
		}
	}
	original.Text.Value = strings.Repeat("<", slides.MaxTextBytes)
	hash, _ := slides.ElementHash(original)
	prepare := slides.PatchRequest{PageID: input.PageID, SchemaVersion: 1, BaseRev: 1, MutationID: "prepare_escaped", Operations: []slides.Operation{{Op: "set_element", SlideID: input.Selection.SlideID, ElementID: original.ID, IfHash: input.Selection.ElementHash, Element: &original}}}
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", slideJSON(t, prepare)), 200, "prepare valid large original")
	input.BaseRev, input.Instruction = 2, strings.Repeat("<", slides.MaxSelectedInstructionBytes)
	input.Selection.ElementHash, input.Selection.From, input.Selection.To, input.Selection.Quote = hash, 0, 4096, strings.Repeat("<", 4096)
	var wire bytes.Buffer
	encoder := json.NewEncoder(&wire)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(input); err != nil || wire.Len() > slides.MaxSelectedRequestBytes {
		t.Fatal("invalid wire fixture", wire.Len(), err)
	}
	created := f.slideDo(f.boss, "POST", base+"/requests", wire.String())
	wantCode(t, created, 202, "actual selected admission")
	var admitted SlidesSelectedAdmissionResponse
	if err = json.Unmarshal(created.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	key := slides.Key{WorkspaceID: "ws-old", PageID: input.PageID}
	store := router.pages.newSlidesStore()
	// Claim is a trusted worker operation; its authorization uses the same
	// actual admission fence as the later host run binding.
	claimAuth := func(ctx context.Context, tx *sql.Tx) (string, error) {
		record, e := slides.ReadSelectedAuthorityTx(ctx, tx, key, admitted.ID)
		if e != nil {
			return "", e
		}
		if _, e = (&slideHTTP{pages: router.pages}).selectedAdmissionLive(ctx, tx, key, record.Admission); e != nil {
			return "", e
		}
		return record.Summary.HumanActor, nil
	}
	if _, err = store.ClaimSelectedRequest(t.Context(), key, admitted.ID, claimAuth); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,author_user_id) VALUES('selected-trigger','selected-chat','selected-agent','user','Selected request','boss')`)
	receipt, err := chataudience.CaptureTrusted(t.Context(), f.db, "selected-chat", "boss", nil)
	if err != nil || receipt == nil {
		t.Fatal(receipt, err)
	}
	start := map[string]any{"id": "selected-run", "agent_id": "selected-agent", "workspace_id": "ws-old", "chat_id": "selected-chat", "artifact_human_receipt": receipt, "artifact_human_message_id": "selected-trigger", "human_authority": receipt, "trigger_message_id": "selected-trigger", "slides_request_id": admitted.ID}
	request := httptest.NewRequest("POST", "/api/v1/internal/runs", strings.NewReader(slideJSON(t, start)))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Internal-Token", master)
	started := httptest.NewRecorder()
	router.ServeHTTP(started, request)
	wantCode(t, started, 201, "actual trusted run start and selected binding")
	// Constructor-only restart fixtures deliberately share the test DB, while
	// real daemons are excluded by the kernel writer lease. Neither a captured
	// pre-start handler nor an alternate zero-value handler may re-admit this run.
	for _, candidate := range []*PageHandler{NewPageHandler(f.db, nil, newTestLogger()), {db: f.db}} {
		if err := candidate.bindSelectedSlidesRun(httptest.NewRequest("POST", "/", nil), admitted.ID, "selected-run", receipt); err == nil {
			t.Fatal("pre-start/alternate handler bypassed run admission fence")
		}
		tx, err := f.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.WithValue(t.Context(), artifactRunKey{}, &artifactRunAuthority{WorkspaceID: key.WorkspaceID, AgentID: "selected-agent", CrewID: "selected-crew", RunID: "selected-run", HumanUserID: "boss", ChatID: "selected-chat"})
		if _, err := (&slideHTTP{pages: candidate}).selectedRunAuthority(key, admitted.ID)(ctx, tx); err == nil {
			t.Fatal("pre-start/alternate handler admitted callback before recovery tick")
		}
		tx.Rollback()
	}
	host := httptest.NewServer(router)
	defer host.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	runKey := internaltoken.DeriveAgentRunKey(master, "ws-old", "selected-crew")
	bearer := internaltoken.DeriveAgentRunToken(runKey, "ws-old", "selected-agent", "selected-run")
	proxy := sidecar.NewServer(sidecar.ServerConfig{Addr: address, Logger: newTestLogger(), IPC: &sidecar.IPCConfig{BaseURL: host.URL, Token: internaltoken.DeriveCrewToken(master, "ws-old", "selected-crew"), AgentID: "selected-agent", AgentSlug: "selected", CrewID: "selected-crew", WorkspaceID: "ws-old", RunID: "selected-run", RunChatID: "selected-chat", AgentRunKey: runKey}})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- proxy.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("owned sidecar did not stop")
		}
	})
	select {
	case <-proxy.Ready():
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("owned sidecar did not bind")
	}
	call := func(method, suffix, body string, want int) []byte {
		t.Helper()
		r, e := http.NewRequest(method, "http://"+address+base+"/requests/"+admitted.ID+suffix, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Authorization", "Bearer "+bearer)
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
		response, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(response.Body, 512<<10))
		if response.StatusCode != want {
			t.Fatalf("actual sidecar %s %s=%d want%d body%s", method, suffix, response.StatusCode, want, data)
		}
		return data
	}
	snapshot := call("GET", "", "", 200)
	var frozen SlidesSelectedSnapshotResponse
	if len(snapshot) <= 128<<10 || json.Unmarshal(snapshot, &frozen) != nil || frozen.OriginalElement.Text.Value != original.Text.Value || frozen.Request.Selection != input.Selection {
		t.Fatal("valid escaped snapshot truncated or refreshed", len(snapshot))
	}
	replacement := slides.SelectedReplacementInput{PageID: key.PageID, RequestSchemaVersion: 1, RequestID: admitted.ID, Replacement: "Upraveno"}
	result := call("POST", "/replace", slideJSON(t, replacement), 200)
	var saved SlidesSelectedReplacementResponse
	if json.Unmarshal(result, &saved) != nil || saved.Revision != 3 {
		t.Fatal(string(result))
	}
	if err = store.SettleSelectedRequest(t.Context(), key, admitted.ID, "succeeded", "", claimAuth); err != nil {
		t.Fatal(err)
	}
	read := f.slideDo(f.boss, "GET", base, "")
	if err = json.Unmarshal(read.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	notes := "Later human edit"
	slideHash, _ := slides.SlideHash(state.Deck.Slides[0])
	later := slides.PatchRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: state.Revision, MutationID: "human_after_result", Operations: []slides.Operation{{Op: "set_notes", SlideID: state.Deck.Slides[0].ID, IfHash: slideHash, Notes: &notes}}}
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", slideJSON(t, later)), 200, "later human head")
	if json.Unmarshal(call("POST", "/replace", slideJSON(t, replacement), 200), &saved) != nil || saved.Revision != 3 || !saved.Duplicate {
		t.Fatal("receipt moved to later head", saved)
	}
	replacement.Replacement = "Different second edit"
	call("POST", "/replace", slideJSON(t, replacement), 409)
	mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(f.boss))
	replacement.Replacement = "Upraveno"
	call("POST", "/replace", slideJSON(t, replacement), 403)
	call("GET", "", "", 403)
	assertSelectedDeletedOriginRejectsOtherGenericCapabilities(t, f, bearer, master)
}

func assertSelectedDeletedOriginRejectsOtherGenericCapabilities(t *testing.T, f *instanceFixture, bearer, master string) {
	t.Helper()
	fresh := "crewship_cli_fresh-human-after-original-revoke"
	mustExec(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash) VALUES('fresh-selected-human','boss','fresh',?)`, sha256Hex(fresh))
	wantCode(t, f.slideDo(fresh, "DELETE", "/api/v1/pages/selected-page", ""), 204, "actual source Page cascade")
	var requests, principal int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM slides_requests`).Scan(&requests); err != nil || requests != 0 {
		t.Fatal("request graph survived source deletion", requests, err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM artifact_run_principals WHERE run_id='selected-run' AND slides_request_origin IS NOT NULL`).Scan(&principal); err != nil || principal != 1 {
		t.Fatal("origin tombstone disappeared", principal, err)
	}
	if _, err := f.db.Exec(`UPDATE artifact_run_principals SET slides_request_origin=NULL WHERE run_id='selected-run'`); err == nil {
		t.Fatal("retained principal origin could be cleared")
	}
	for _, page := range []string{"generic-other", "board-other"} {
		wmSeedPage(t, f.db, "ws-old", page, page, "Other authorized Page", "boss", "")
		mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent','selected-agent','write','boss')`, page)
		// Deliberately retain another otherwise-valid generic delegation, as can
		// exist in a corrupt/older capability bundle. It must never widen origin.
		mustExec(t, f.db, `INSERT INTO page_artifact_run_delegations(page_id,run_id,granted_by_user_id,expires_at,granted_at) SELECT ?,'selected-run','boss',expires_at,strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM artifact_run_principals WHERE run_id='selected-run'`, page)
	}
	wantCode(t, f.slideDo(fresh, "POST", "/api/v1/pages/board-other/board", `{"mutation_id":"init_board"}`), 200, "initialize unrelated real Board")
	mustExec(t, f.db, `INSERT INTO page_board_run_delegations(board_page_id,run_id,granted_by_user_id,expires_at,granted_at) SELECT 'board-other','selected-run','boss',expires_at,strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM artifact_run_principals WHERE run_id='selected-run'`)
	crewToken := internaltoken.DeriveCrewToken(master, "ws-old", "selected-crew")
	for range 2 {
		for _, path := range []string{"/api/v1/internal/artifacts/generic-other", "/api/v1/internal/boards/board-other", "/api/v1/internal/artifacts"} {
			r := httptest.NewRequest("GET", path, nil)
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("X-Internal-Token", crewToken)
			r.Header.Set("Authorization", "Bearer "+bearer)
			r.Header.Set("X-Slides-Request-ID", "client-cannot-set-marker")
			response := httptest.NewRecorder()
			f.r.ServeHTTP(response, r)
			wantCode(t, response, 403, "deleted selected origin cannot enter legacy transport after fresh scope load")
		}
		// Exercise the actual Go writer authorities without requiring a second
		// Docs Node service. These empty Pages are compatible with both formats;
		// denial must come from the retained run boundary, not a format conflict.
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", "Bearer "+bearer)
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, "ws-old")
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, "selected-crew")
		checked, ok := f.r.pages.artifactAgentRequest(httptest.NewRecorder(), r.WithContext(ctx))
		if !ok {
			t.Fatal("fixture must retain a valid principal/run bearer")
		}
		a := checked.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
		if err := f.r.pages.checkArtifactRunPrincipal(checked.Context(), a); err != nil {
			t.Fatal("fixture lost live principal", err)
		}
		tx, err := f.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = (&documentAuthority{pages: f.r.pages}).authorize(checked, documents.Key{WorkspaceID: "ws-old", PageID: "generic-other"}, false, false)(checked.Context(), tx); err == nil {
			t.Fatal("selected origin downgraded into Docs")
		}
		if _, err = (&slideHTTP{pages: f.r.pages}).authorize(checked, slides.Key{WorkspaceID: "ws-old", PageID: "generic-other"}, false, false)(checked.Context(), tx); err == nil {
			t.Fatal("selected origin downgraded into generic Slides")
		}
		tx.Rollback()
	}
}
