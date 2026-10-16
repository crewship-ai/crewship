package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestBoardRequestInsertTamperedPayloadIsAtomicAndHumanAttributed(t *testing.T) {
	h, workspace, user, page, token, id := boardJobFixture(t)
	original := boardAgentElement(t, "existing", nil)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "batch", boardAgentBatch(t, "original", 0, 0, original)), 201)
	execOrFatal(t, h.db, `UPDATE page_board_requests SET status='running',run_id='artifact-run' WHERE id=?`, id)
	artifactExpect(t, boardJobAgentCall(t, h, workspace, token, id, `{"mutation_id":"source","source":"flowchart TD\nA[Agent source]","dialect":"flowchart-v1","converter_version":"2.2.2"}`, h.PublishBoardRequestMermaid), 201)
	var sourceHash string
	if err := h.db.QueryRow(`SELECT source_hash FROM page_board_requests WHERE id=?`, id).Scan(&sourceHash); err != nil {
		t.Fatal(err)
	}
	upsert := func(element json.RawMessage, expected any) map[string]any {
		return map[string]any{"element": element, "expected_rev": expected}
	}
	for _, test := range []struct {
		name   string
		status int
		change func(map[string]any)
	}{
		{"source hash", 409, func(req map[string]any) { req["source_hash"] = strings.Repeat("f", 64) }},
		{"converter version", 422, func(req map[string]any) { req["converter_version"] = "untrusted-converter" }},
		{"forged actor", 400, func(req map[string]any) { req["author_agent_id"] = "artifact-agent" }},
		{"missing revision", 422, func(req map[string]any) { delete(req["upserts"].([]map[string]any)[0], "expected_rev") }},
		{"null revision", 422, func(req map[string]any) { req["upserts"].([]map[string]any)[0]["expected_rev"] = nil }},
		{"overwrite revision", 422, func(req map[string]any) { req["upserts"].([]map[string]any)[0]["expected_rev"] = 1 }},
		{"existing identity", 409, func(req map[string]any) {
			req["upserts"] = []map[string]any{upsert(boardAgentElement(t, "existing", map[string]any{"x": 4321}), 0)}
		}},
		{"mixed unsafe link", 422, func(req map[string]any) {
			req["upserts"] = append(req["upserts"].([]map[string]any), upsert(boardAgentElement(t, "unsafe", map[string]any{"link": "https://untrusted.invalid/"}), 0))
		}},
		// The bounded JSON grammar rejects unknown provenance keys before geometry validation.
		{"mixed client provenance", 400, func(req map[string]any) {
			req["upserts"] = append(req["upserts"].([]map[string]any), upsert(boardAgentElement(t, "unsafe", map[string]any{"author_agent_id": "artifact-agent"}), 0))
		}},
		{"mixed invalid graph", 422, func(req map[string]any) {
			req["upserts"] = append(req["upserts"].([]map[string]any), upsert(boardAgentElement(t, "unsafe", map[string]any{"frameId": "absent-frame"}), 0))
		}},
		{"image output", 422, func(req map[string]any) {
			req["upserts"] = []map[string]any{upsert(boardAgentElement(t, "image", map[string]any{"type": "image", "fileId": "opaque-file", "status": "saved", "scale": []int{1, 1}, "crop": nil}), 0)}
		}},
		{"duplicate identity", 422, func(req map[string]any) {
			req["upserts"] = append(req["upserts"].([]map[string]any), upsert(boardAgentElement(t, "converted", nil), 0))
		}},
		{"element count", 422, func(req map[string]any) {
			items := []map[string]any{}
			for i := 0; i < 101; i++ {
				items = append(items, upsert(boardAgentElement(t, fmt.Sprint("converted-", i), nil), 0))
			}
			req["upserts"] = items
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := map[string]any{"mutation_id": "tampered-" + test.name, "base_seq": 1, "source_hash": sourceHash, "converter_version": "2.2.2", "upserts": []map[string]any{upsert(boardAgentElement(t, "converted", nil), 0)}}
			test.change(req)
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			artifactExpect(t, boardJobCall(t, h, workspace, user, id, string(raw), h.InsertBoardRequest), test.status)
			var seq, records, receipts, audits int
			var status, saved, hashes string
			var result sql.NullInt64
			if err = h.db.QueryRow(`SELECT seq,(SELECT COUNT(*) FROM page_board_elements WHERE board_page_id=?),(SELECT COUNT(*) FROM page_board_mutations WHERE board_page_id=? AND mutation_id LIKE 'tampered-%'),(SELECT COUNT(*) FROM page_board_write_audit WHERE board_page_id=? AND mutation_id LIKE 'tampered-%') FROM page_boards WHERE page_id=?`, page, page, page, page).Scan(&seq, &records, &receipts, &audits); err != nil || seq != 1 || records != 1 || receipts != 0 || audits != 0 {
				t.Fatalf("rejected conversion changed Board: seq=%d records=%d receipts=%d audits=%d err=%v", seq, records, receipts, audits, err)
			}
			if err = h.db.QueryRow(`SELECT element_json FROM page_board_elements WHERE board_page_id=? AND element_id='existing'`, page).Scan(&saved); err != nil || saved != string(original) {
				t.Fatalf("existing drawing changed: %s %v", saved, err)
			}
			if err = h.db.QueryRow(`SELECT status,result_seq,inserted_element_hashes_json FROM page_board_requests WHERE id=?`, id).Scan(&status, &result, &hashes); err != nil || status != "ready_to_insert" || result.Valid || hashes != "{}" {
				t.Fatalf("rejection consumed job: %s %v %s %v", status, result, hashes, err)
			}
		})
	}
	// The server validates safe client geometry; it does not attest that a human
	// converter reproduced the agent's source. Attribute accepted geometry to
	// the confirming human in both content and the permanent write audit.
	raw, _ := json.Marshal(map[string]any{"mutation_id": "confirmed-human-output", "base_seq": 1, "source_hash": sourceHash, "converter_version": "2.2.2", "upserts": []map[string]any{upsert(boardAgentElement(t, "converted", map[string]any{"x": 4321}), 0)}})
	artifactExpect(t, boardJobCall(t, h, workspace, user, id, string(raw), h.InsertBoardRequest), 201)
	var human, origin, auditHuman string
	var agent, run, auditAgent, auditRun sql.NullString
	if err := h.db.QueryRow(`SELECT author_user_id,author_agent_id,author_run_id,origin_request_id FROM page_board_elements WHERE board_page_id=? AND element_id='converted'`, page).Scan(&human, &agent, &run, &origin); err != nil || human != user || origin != id || agent.Valid || run.Valid {
		t.Fatalf("client conversion acquired agent authorship: %s %s %v %v %v", human, origin, agent, run, err)
	}
	if err := h.db.QueryRow(`SELECT actor_user_id,actor_agent_id,actor_run_id FROM page_board_write_audit WHERE board_page_id=? AND mutation_id='confirmed-human-output'`, page).Scan(&auditHuman, &auditAgent, &auditRun); err != nil || auditHuman != user || auditAgent.Valid || auditRun.Valid {
		t.Fatalf("audit overstates conversion authority: %s %v %v %v", auditHuman, auditAgent, auditRun, err)
	}
}

func boardRequestAssetCall(t *testing.T, h *PageHandler, workspace, token, job, file, source string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.SetPathValue("requestId", job)
	r.SetPathValue("fileId", file)
	r.SetPathValue("sourceId", source)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	rr := httptest.NewRecorder()
	h.GetBoardRequestAsset(rr, r.WithContext(ctx))
	return rr
}

func TestBoardRequestAssetsRequireExactJobSelectionAndCurrentSourceGrant(t *testing.T) {
	h, workspace, user, page, token, _ := boardJobFixture(t)
	files := []string{}
	for i := 0; i < 2; i++ {
		rr := boardFileUpload(t, h, workspace, user, "report", fmt.Sprint("image-", i), int64(i), boardRaster(t, i == 1))
		artifactExpect(t, rr, 201)
		files = append(files, boardFileID(t, rr))
	}
	images := []map[string]any{}
	for i, file := range files {
		images = append(images, map[string]any{"expected_rev": 0, "element": boardAgentElement(t, fmt.Sprint("image-", i), map[string]any{"type": "image", "fileId": file, "status": "saved", "scale": []int{1, 1}, "crop": nil})})
	}
	raw, _ := json.Marshal(map[string]any{"mutation_id": "images", "base_seq": 2, "upserts": images})
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "batch", string(raw)), 201)
	pins := []string{}
	sources := []string{}
	for i := 0; i < 2; i++ {
		slug := fmt.Sprint("selected-source-", i)
		create := httptest.NewRecorder()
		h.Create(create, pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", fmt.Sprintf(`{"slug":%q,"name":%q,"panels":[],"allow_empty":true}`, slug, slug)))
		artifactExpect(t, create, 201)
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil || created.ID == "" {
			t.Fatalf("source page: %s %v", create.Body.String(), err)
		}
		save := func(html string, base int) {
			body, _ := json.Marshal(map[string]any{"html": html, "mutation_id": fmt.Sprint("source-", base), "base_rev": base})
			r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", string(body))
			r.SetPathValue("slug", slug)
			rr := httptest.NewRecorder()
			h.PublishArtifact(rr, r)
			artifactExpect(t, rr, 201)
		}
		save(fmt.Sprintf("<h1>Private source %d revision 1</h1>", i), 0)
		execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent','artifact-agent','read',?)`, created.ID, user)
		pin := boardCall(t, h, workspace, user, "OWNER", "report", "placement", fmt.Sprintf(`{"mutation_id":"pin-%d","base_seq":%d,"source_page_slug":%q,"source_revision":1,"x":0,"y":0,"width":100,"height":100}`, i, 3+i, slug))
		artifactExpect(t, pin, 201)
		var result struct {
			ID string `json:"placement_id"`
		}
		if err := json.Unmarshal(pin.Body.Bytes(), &result); err != nil || result.ID == "" {
			t.Fatalf("pin: %s %v", pin.Body.String(), err)
		}
		pins = append(pins, result.ID)
		sources = append(sources, created.ID)
		// The job must read the retained selected revision, not this later head.
		save(fmt.Sprintf("<h1>Private source %d revision 2</h1>", i), 1)
	}
	jobs, refs := []string{}, []string{}
	for i := 0; i < 2; i++ {
		var req map[string]any
		_ = json.Unmarshal([]byte(boardDiagramRequest), &req)
		req["submission_id"], req["base_seq"] = fmt.Sprint("asset-job-", i), 5
		req["selected_element_ids"], req["selected_placement_ids"] = []string{fmt.Sprint("image-", i)}, []string{pins[i]}
		raw, _ := json.Marshal(req)
		rr := boardJobCall(t, h, workspace, user, "", string(raw), h.CreateBoardRequest)
		artifactExpect(t, rr, 202)
		var result struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil || result.ID == "" {
			t.Fatalf("job: %s %v", rr.Body.String(), err)
		}
		jobs = append(jobs, result.ID)
		var sourceID string
		if err := h.db.QueryRow(`SELECT id FROM page_board_request_sources WHERE request_id=?`, result.ID).Scan(&sourceID); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, sourceID)
		execOrFatal(t, h.db, `UPDATE page_board_requests SET status='running',run_id=? WHERE id=?`, []string{"artifact-run", "other-job-run"}[i], result.ID)
	}
	selected := boardRequestAssetCall(t, h, workspace, token, jobs[0], files[0], "")
	artifactExpect(t, selected, 200)
	var raster struct {
		ID            string `json:"id"`
		Data          []byte `json:"data"`
		Width, Height int
	}
	if err := json.Unmarshal(selected.Body.Bytes(), &raster); err != nil || raster.ID != files[0] || raster.Width != 4 || raster.Height != 3 {
		t.Fatalf("selected raster: %+v %v", raster, err)
	}
	ownerRead := boardFileRead(t, h, workspace, user, "report", files[0])
	artifactExpect(t, ownerRead, 200)
	if !bytes.Equal(raster.Data, ownerRead.Body.Bytes()) {
		t.Fatal("selected file returned different bytes")
	}
	selected = boardRequestAssetCall(t, h, workspace, token, jobs[0], "", refs[0])
	artifactExpect(t, selected, 200)
	var source struct {
		ID, HTML string
		Revision int
	}
	if err := json.Unmarshal(selected.Body.Bytes(), &source); err != nil || source.ID != refs[0] || source.Revision != 1 || source.HTML != "<h1>Private source 0 revision 1</h1>" {
		t.Fatalf("selected source: %+v %v", source, err)
	}
	for _, test := range []struct {
		name, job, file, source string
		status                  int
	}{
		{"unselected file from another job", jobs[0], files[1], "", 404},
		{"source reference from another job", jobs[0], "", refs[1], 404},
		{"Page ID is not a selected source reference", jobs[0], "", sources[0], 404},
		{"foreign job file path", jobs[1], files[1], "", 403},
		{"foreign job source path", jobs[1], "", refs[1], 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			rr := boardRequestAssetCall(t, h, workspace, token, test.job, test.file, test.source)
			artifactExpect(t, rr, test.status)
			var body map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"data", "html", "title", "revision", "width", "height"} {
				if _, exists := body[field]; exists {
					t.Fatalf("denied asset leaked %s: %s", field, rr.Body.String())
				}
			}
		})
	}
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='agent'`, sources[0])
	denied := boardRequestAssetCall(t, h, workspace, token, jobs[0], "", refs[0])
	artifactExpect(t, denied, 404)
	if strings.Contains(denied.Body.String(), "Private source") {
		t.Fatal("revoked source leaked contents")
	}
	artifactExpect(t, boardRequestAssetCall(t, h, workspace, token, jobs[0], files[0], ""), 200)
	var seq int
	if err := h.db.QueryRow(`SELECT seq FROM page_boards WHERE page_id=?`, page).Scan(&seq); err != nil || seq != 5 {
		t.Fatalf("asset reads mutated Board: %d %v", seq, err)
	}
}

func TestBoardRequestCompletionAfterWriteGrantRevocationRetainsPrivateUnpinnedArtifact(t *testing.T) {
	h, workspace, user, page, _, _ := boardJobFixture(t)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "batch", boardAgentBatch(t, "wireframe", 0, 0, boardAgentElement(t, "wire", nil))), 201)
	pagesSeedUser(t, h, workspace, "board-owner", "board-owner@example.test", "MEMBER")
	execOrFatal(t, h.db, `UPDATE pages SET owner_user_id='board-owner' WHERE id=?`, page)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MANAGER' WHERE workspace_id=? AND user_id=?`, workspace, user)
	pagesGrant(t, h, workspace, "board-owner", "report", fmt.Sprintf(`{"subject_type":"user","subject":%q,"level":"write"}`, user))
	body := strings.NewReplacer(`"kind":"diagram"`, `"kind":"artifact"`, `"base_seq":0`, `"base_seq":1`, `"submission_id":"diagram-request"`, `"submission_id":"revoked-output"`, `"selected_element_ids":[]`, `"selected_element_ids":["wire"]`).Replace(boardDiagramRequest)
	request := pagesGrantRequest(t, "POST", "/", workspace, user, "MANAGER", body)
	request.SetPathValue("slug", "report")
	rr := httptest.NewRecorder()
	h.CreateBoardRequest(rr, request)
	artifactExpect(t, rr, 202)
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &job); err != nil || job.ID == "" {
		t.Fatalf("job: %s %v", rr.Body.String(), err)
	}
	execOrFatal(t, h.db, `UPDATE page_board_requests SET status='cancelled' WHERE kind='diagram'`)
	const run = "board-output-run"
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", run)
	called := false
	h.artifactDispatch = func(_ context.Context, actualUser, chat, prompt string, opts ws.ChatMessageOption) error {
		called = true
		if actualUser != user || opts.BoardRequestID != job.ID {
			t.Fatal("wrong admitted job")
		}
		if err := captureArtifactChatPrincipal(httptest.NewRequest("POST", "/", nil), h.db, run, workspace, "artifact-agent", "artifact-crew", "artifact-chat", "artifact-message", opts.HumanAuthority); err != nil {
			t.Fatal(err)
		}
		execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('output-start',?,'artifact-crew','artifact-agent','run.started','system','test',?)`, workspace, run)
		if err := bindBoardRequestRun(httptest.NewRequest("POST", "/", nil), h.db, job.ID, run, opts.HumanAuthority); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Body = &artifactRevokingBody{reader: strings.NewReader(creationBody), revoke: func() {
			// Revoke through the human HTTP grant boundary while creation is
			// reading its body, after trusted run admission but before commit.
			revoke := pagesGrantCall(t, h, "DELETE", "/?subject_type=user&subject="+user+"&level=write", workspace, "board-owner", "MEMBER", "report", "")
			artifactExpect(t, revoke, 200)
		}}
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
		created := httptest.NewRecorder()
		h.CreateArtifactAgent(created, r.WithContext(ctx))
		artifactExpect(t, created, 201)
		artifactExpect(t, artifactCreateAgent(t, h, workspace, token, creationBody), 200)
		return nil
	}
	h.dispatchNextBoardRequest(t.Context())
	if !called {
		t.Fatal("job was not dispatched")
	}
	var status, pin, result, board string
	var placement sql.NullString
	if err := h.db.QueryRow(`SELECT status,pin_status,result_page_id,board_page_id,result_placement_id FROM page_board_requests WHERE id=?`, job.ID).Scan(&status, &pin, &result, &board, &placement); err != nil || status != "succeeded" || pin != "created_not_pinned" || result == "" || board != page || placement.Valid {
		t.Fatalf("result lost or pinned after revocation: %s %s %s %s %v %v", status, pin, result, board, placement, err)
	}
	var seq, pins, revisions, writes int
	if err := h.db.QueryRow(`SELECT seq,(SELECT COUNT(*) FROM page_board_artifact_placements WHERE board_page_id=?),(SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?),(SELECT COUNT(*) FROM page_board_mutations WHERE board_page_id=? AND mutation_id=?) FROM page_boards WHERE page_id=?`, page, result, page, "board-job:"+job.ID, page).Scan(&seq, &pins, &revisions, &writes); err != nil || seq != 1 || pins != 0 || revisions != 1 || writes != 0 {
		t.Fatalf("completion changed Board or duplicated artifact: seq=%d pins=%d revisions=%d writes=%d err=%v", seq, pins, revisions, writes, err)
	}
	r := pagesGrantRequest(t, "GET", "/", workspace, user, "MANAGER", "")
	r.SetPathValue("slug", "agent-created")
	rr = httptest.NewRecorder()
	h.GetArtifact(rr, r)
	artifactExpect(t, rr, 200)
	r = pagesGrantRequest(t, "GET", "/", workspace, "board-owner", "MEMBER", "")
	r.SetPathValue("slug", "agent-created")
	rr = httptest.NewRecorder()
	h.GetArtifact(rr, r)
	artifactExpect(t, rr, 404)
}
