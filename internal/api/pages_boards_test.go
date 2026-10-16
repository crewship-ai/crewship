package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func boardCall(t *testing.T, h *PageHandler, ws, user, role, slug, kind, body string) *httptest.ResponseRecorder {
	t.Helper()
	method := "POST"
	if kind == "get" {
		method = "GET"
	}
	if kind == "delete-placement" {
		method = "DELETE"
	}
	r := pagesGrantRequest(t, method, "/api/v1/pages/"+slug+"/board", ws, user, role, body)
	r.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	switch kind {
	case "init":
		h.InitBoard(rr, r)
	case "get":
		h.GetBoard(rr, r)
	case "batch":
		h.ApplyBoardBatch(rr, r)
	case "placement":
		h.AddBoardPlacement(rr, r)
	default:
		t.Fatal("unknown board call")
	}
	return rr
}
func boardAPIElement(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../pages/testdata/board-excalidraw-0.18.1.json")
	if err != nil {
		t.Fatal(err)
	}
	var elements []json.RawMessage
	if err = json.Unmarshal(raw, &elements); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(elements[2], &fields); err != nil {
		t.Fatal(err)
	}
	fields["boundElements"] = nil
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestPageBoards_InitCASRetryAndCrossKind(t *testing.T) {
	h, _, _, ws, user, pageID := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"other"}`), 409)
	raw := boardAPIElement(t)
	body := fmt.Sprintf(`{"mutation_id":"first","base_seq":0,"upserts":[{"element":%s,"expected_rev":0}],"deletes":[]}`, raw)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", body), 201)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", body), 200)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", strings.Replace(body, `"first"`, `"new"`, 1)), 409)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Wrong kind</h1>", "artifact", 0), 409)
	for _, statement := range []string{`INSERT INTO page_artifacts(page_id,head_rev) VALUES(?,1)`, `UPDATE pages SET owner_user_id=NULL,owner_crew_id='crew-engine' WHERE id=?`} {
		if _, err := h.db.Exec(statement, pageID); err == nil {
			t.Fatalf("crosskind/ownership DB guard absent: %s", statement)
		}
	}
	rr := boardCall(t, h, ws, user, "OWNER", "report", "get", "")
	artifactExpect(t, rr, 200)
	if !strings.Contains(rr.Body.String(), `"seq":1`) || !strings.Contains(rr.Body.String(), `"placements":[]`) {
		t.Fatal(rr.Body.String())
	}
	patch := pagesGrantRequest(t, "PATCH", "/", ws, user, "OWNER", `{"name":"Renamed Board"}`)
	patch.SetPathValue("slug", "report")
	rr = httptest.NewRecorder()
	h.Update(rr, patch)
	artifactExpect(t, rr, 200)
}
func TestPageBoards_ACLRetryAndConcurrentCAS(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	pagesSeedUser(t, h, ws, "writer", "writer@example.com", "MEMBER")
	pagesGrant(t, h, ws, user, "report", `{"subject_type":"user","subject":"writer@example.com","level":"write"}`)
	raw := boardAPIElement(t)
	body := fmt.Sprintf(`{"mutation_id":"first","base_seq":0,"upserts":[{"element":%s,"expected_rev":0}]}`, raw)
	artifactExpect(t, boardCall(t, h, ws, "writer", "MEMBER", "report", "batch", body), 201)
	if _, err := h.db.Exec(`DELETE FROM page_grants WHERE subject_id='writer'`); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, boardCall(t, h, ws, "writer", "MEMBER", "report", "batch", body), 404)
	artifactExpect(t, boardCall(t, h, ws, "writer", "OWNER", "report", "get", ""), 404) // stale role cannot widen access
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["x"] = 999
	changed, _ := json.Marshal(fields)
	body = fmt.Sprintf(`{"mutation_id":"%%s","base_seq":1,"upserts":[{"element":%s,"expected_rev":1}]}`, changed)
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			statuses <- boardCall(t, h, ws, user, "OWNER", "report", "batch", fmt.Sprintf(body, id)).Code
		}(id)
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatalf("CAS statuses %v", counts)
	}
}
func TestPageBoards_PlacementACLAndAtomicBatch(t *testing.T) {
	h, _, _, ws, user, sourceID := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Secret source</h1>", "source", 0), 201)
	create := pagesGrantRequest(t, "POST", "/", ws, user, "OWNER", `{"slug":"board","name":"Board","panels":[],"allow_empty":true}`)
	rr := httptest.NewRecorder()
	h.Create(rr, create)
	artifactExpect(t, rr, 201)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "init", `{"mutation_id":"init"}`), 200)
	placement := `{"mutation_id":"pin","base_seq":0,"source_page_slug":"report","source_revision":1,"x":10,"y":20,"width":300,"height":200}`
	rr = boardCall(t, h, ws, user, "OWNER", "board", "placement", placement)
	artifactExpect(t, rr, 201)
	var receipt struct {
		ID string `json:"placement_id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &receipt)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "placement", placement), 200)
	pagesSeedUser(t, h, ws, "reader", "reader@example.com", "MEMBER")
	pagesGrant(t, h, ws, user, "board", `{"subject_type":"user","subject":"reader@example.com","level":"read"}`)
	rr = boardCall(t, h, ws, "reader", "MEMBER", "board", "get", "")
	artifactExpect(t, rr, 200)
	if strings.Contains(rr.Body.String(), "report") || strings.Contains(rr.Body.String(), "Secret") || strings.Contains(rr.Body.String(), sourceID) || strings.Contains(rr.Body.String(), `"source"`) {
		t.Fatal("inaccessible source leaked: " + rr.Body.String())
	}
	artifactExpect(t, boardCall(t, h, ws, "reader", "MEMBER", "board", "batch", `{"mutation_id":"no","base_seq":1,"placement_deletes":[{"id":"`+receipt.ID+`"}]}`), 403)
	// Geometry and drawing edits fail atomically if any placement vanished.
	raw := boardAPIElement(t)
	bad := fmt.Sprintf(`{"mutation_id":"mixed-bad","base_seq":1,"upserts":[{"element":%s,"expected_rev":0}],"placement_deletes":[{"id":"missing"}]}`, raw)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", bad), 409)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_elements`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial drawing committed: %d %v", count, err)
	}
	good := fmt.Sprintf(`{"mutation_id":"mixed","base_seq":1,"upserts":[{"element":%s,"expected_rev":0}],"placement_updates":[{"id":"%s","x":50,"y":60,"width":100,"height":100}]}`, raw, receipt.ID)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", good), 201)
	if _, err := h.db.Exec(`DELETE FROM pages WHERE id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	rr = boardCall(t, h, ws, user, "OWNER", "board", "get", "")
	artifactExpect(t, rr, 200)
	if strings.Contains(rr.Body.String(), `"source"`) || !strings.Contains(rr.Body.String(), `"x":50`) {
		t.Fatal("deleted source did not preserve generic geometry: " + rr.Body.String())
	}
}
func TestPageBoards_RejectAgentAndMalformed(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	for _, body := range []string{`null`, `{}`, `{"mutation_id":"i","unknown":true}`, `{"Mutation_ID":"i"}`, `{"mutation_id":"i","Mutation_ID":"j"}`, `{"mutation_id":"i","mutation_id":"j"}`, `{"mutation_id":"i"} {}`} {
		artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", body), 400)
	}
	r := pagesGrantRequest(t, "POST", "/", ws, user, "OWNER", `{"mutation_id":"i"}`)
	r.SetPathValue("slug", "report")
	r = r.WithContext(context.WithValue(r.Context(), ctxInternalTokenWS, ws))
	rr := httptest.NewRecorder()
	h.InitBoard(rr, r)
	artifactExpect(t, rr, 401)
}
