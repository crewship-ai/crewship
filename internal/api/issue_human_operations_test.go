package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestHumanIssueLabelsRejectStaleEdit(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
	for _, label := range []string{"label-a", "label-b"} {
		execOrFatal(t, h.db, `INSERT INTO labels(id,workspace_id,name,color) VALUES(?,?,?,'#abcdef')`, label, ws, label)
	}
	var revision int64
	if err := h.db.QueryRow(`SELECT revision FROM missions WHERE id=?`, id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	for i, label := range []string{"label-a", "label-b"} {
		req := httptest.NewRequest("PATCH", "/", strings.NewReader(fmt.Sprintf(`{"labels":[%q],"expected_revision":%d}`, label, revision)))
		req.SetPathValue("crewId", crew)
		req.SetPathValue("identifier", "ENG-1")
		req.Header.Set("Idempotency-Key", "labels-"+label)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER"))
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		want := http.StatusOK
		if i == 1 {
			want = http.StatusConflict
		}
		if rr.Code != want {
			t.Fatalf("labels edit %d: status %d, want %d: %s", i, rr.Code, want, rr.Body.String())
		}
	}
	var label string
	if err := h.db.QueryRow(`SELECT label_id FROM mission_labels WHERE mission_id=?`, id).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if label != "label-a" {
		t.Fatalf("stale edit overwrote labels: %q", label)
	}
}

func TestHumanIssueRevisionAndTimeoutReplay(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
	call := func(method, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/", strings.NewReader(body))
		req.SetPathValue("crewId", crew)
		req.SetPathValue("identifier", "ENG-1")
		req.Header.Set("Idempotency-Key", key)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user, Name: "Test"}), ws, "OWNER"))
		rr := httptest.NewRecorder()
		if method == "PATCH" {
			h.Update(rr, req)
		} else {
			h.CreateComment(rr, req)
		}
		return rr
	}
	var revision int64
	if err := h.db.QueryRow(`SELECT revision FROM missions WHERE id=?`, id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"title":"New title","expected_revision":%d}`, revision)
	first := call("PATCH", "patch-once", body)
	if first.Code != 200 {
		t.Fatalf("first %d %s", first.Code, first.Body.String())
	}
	var committedRevision int64
	if err := h.db.QueryRow(`SELECT revision FROM missions WHERE id=?`, id).Scan(&committedRevision); err != nil {
		t.Fatal(err)
	}
	replay := call("PATCH", "patch-once", body)
	if replay.Code != 200 || replay.Body.String() != first.Body.String() || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay %d %s", replay.Code, replay.Body.String())
	}
	var after int64
	if err := h.db.QueryRow(`SELECT revision FROM missions WHERE id=?`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != committedRevision || after <= revision {
		t.Fatalf("retry mutated revision %d -> %d", revision, after)
	}
	if rr := call("PATCH", "different-operation", body); rr.Code != 409 {
		t.Fatalf("stale %d %s", rr.Code, rr.Body.String())
	} else {
		assertEntityHandlerFixture(t, "edit-conflict.json", rr.Body.Bytes())
	}
	if rr := call("PATCH", "patch-once", `{"title":"Other"}`); rr.Code != 409 {
		t.Fatalf("key conflict %d", rr.Code)
	}
	var replies [2]*httptest.ResponseRecorder
	var wg sync.WaitGroup
	for i := range replies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replies[i] = call("POST", "comment-once", `{"body":"Please keep this exact result"}`)
		}()
	}
	wg.Wait()
	for _, rr := range replies {
		if rr.Code != 201 {
			t.Fatalf("comment %d %s", rr.Code, rr.Body.String())
		}
	}
	if replies[0].Body.String() != replies[1].Body.String() {
		t.Fatal("timeout retry duplicated comment identity")
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_comments WHERE mission_id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("duplicate comments: %d", count)
	}
	var comment map[string]any
	if err := json.Unmarshal(replies[0].Body.Bytes(), &comment); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "mission_id", "author_id", "created_at", "updated_at"} {
		comment[field] = "<" + field + ">"
	}
	assertEntityHandlerFixture(t, "comment-replay.json", comment)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, ws, user)
	if rr := call("POST", "comment-once", `{"body":"Please keep this exact result"}`); rr.Code != 403 {
		t.Fatalf("revoked retry %d %s", rr.Code, rr.Body.String())
	}
}

func TestHumanIssueOperationFailureRollsBackMutation(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
	execOrFatal(t, h.db, `CREATE TRIGGER reject_human_operation BEFORE UPDATE ON agent_operations WHEN NEW.status='completed' AND NEW.actor_type='human' BEGIN SELECT RAISE(ABORT,'synthetic failed commit'); END`)
	var old string
	if err := h.db.QueryRow(`SELECT title FROM missions WHERE id=?`, id).Scan(&old); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"PATCH", "POST"} {
		body := `{"title":"Must roll back"}`
		if method == "POST" {
			body = `{"body":"Must roll back"}`
		}
		req := httptest.NewRequest(method, "/", strings.NewReader(body))
		req.SetPathValue("crewId", crew)
		req.SetPathValue("identifier", "ENG-1")
		req.Header.Set("Idempotency-Key", method)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER"))
		rr := httptest.NewRecorder()
		if method == "PATCH" {
			h.Update(rr, req)
		} else {
			h.CreateComment(rr, req)
		}
		if rr.Code != 500 {
			t.Fatalf("expected failure %d %s", rr.Code, rr.Body.String())
		}
	}
	var title string
	var count int
	if err := h.db.QueryRow(`SELECT title FROM missions WHERE id=?`, id).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_comments WHERE mission_id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if title != old || count != 0 {
		t.Fatalf("partial writes: title=%q comments=%d", title, count)
	}
}

func TestIssueCardActionsFollowState(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER"))
	for _, status := range []string{"BACKLOG", "TODO", "IN_PROGRESS", "DONE", "CANCELLED"} {
		execOrFatal(t, h.db, `UPDATE missions SET status=? WHERE id=?`, status, id)
		issue := issueResponse{ID: id, WorkspaceID: ws, CrewID: crew, Status: status}
		if err := h.loadChatMetadata(req, &issue); err != nil {
			t.Fatal(err)
		}
		has := func(s string) bool {
			for _, a := range issue.AllowedActions {
				if a == s {
					return true
				}
			}
			return false
		}
		if has("delete") != (status == "BACKLOG" || status == "CANCELLED") {
			t.Fatalf("delete in %s: %v", status, issue.AllowedActions)
		}
		if has("start_work") != (status == "BACKLOG" || status == "TODO") {
			t.Fatalf("start in %s: %v", status, issue.AllowedActions)
		}
	}
	execOrFatal(t, h.db, `UPDATE missions SET status='BACKLOG' WHERE id=?`, id)
	execOrFatal(t, h.db, `UPDATE issue_work SET mode='human' WHERE mission_id=?`, id)
	issue := issueResponse{ID: id, WorkspaceID: ws, CrewID: crew, Status: "BACKLOG"}
	if err := h.loadChatMetadata(req, &issue); err != nil {
		t.Fatal(err)
	}
	for _, a := range issue.AllowedActions {
		if a == "start_work" {
			t.Fatal("human-held issue advertised start")
		}
	}
}

func TestChatIssueRequestHashSemanticLabels(t *testing.T) {
	hash := func(verb string, payload any) string {
		t.Helper()
		h, err := chatIssueRequestHash(verb, "issue", payload)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	if hash("created", map[string]any{"labels": nil}) != hash("created", map[string]any{"labels": []string{}}) {
		t.Fatal("nil/empty creation labels differ")
	}
	if hash("updated", map[string]any{"labels": []string{"b", "a", "a"}}) != hash("updated", map[string]any{"labels": []string{"a", "b"}}) {
		t.Fatal("label sets differ")
	}
	if hash("updated", map[string]any{"labels": nil}) == hash("updated", map[string]any{"labels": []string{}}) {
		t.Fatal("omitted update and clearing labels conflated")
	}
}

func TestHumanIssueConcurrentEditorsAndIfMatch(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
	var revision int64
	if err := h.db.QueryRow(`SELECT revision FROM missions WHERE id=?`, id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	call := func(key, body, match string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PATCH", "/", strings.NewReader(body))
		req.SetPathValue("crewId", crew)
		req.SetPathValue("identifier", "ENG-1")
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("If-Match", match)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER"))
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		return rr
	}
	if rr := call("invalid", fmt.Sprintf(`{"title":"No","expected_revision":%d}`, revision), fmt.Sprint(revision+1)); rr.Code != 400 {
		t.Fatalf("inconsistent preconditions %d", rr.Code)
	}
	var rr [2]*httptest.ResponseRecorder
	var wg sync.WaitGroup
	for i := range rr {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr[i] = call(fmt.Sprintf("editor-%d", i), fmt.Sprintf(`{"title":"Editor %d"}`, i), fmt.Sprintf(`"%d"`, revision))
		}()
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, r := range rr {
		switch r.Code {
		case 200:
			ok++
		case 409:
			conflict++
		default:
			t.Fatalf("editor %d %s", r.Code, r.Body.String())
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("lost update: successful=%d conflicts=%d", ok, conflict)
	}
}
