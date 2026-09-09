package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestIssueWorkConcurrentTransfersHaveOneWinner(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "TODO")
	start := make(chan struct{})
	results := make(chan int, 2)
	var workers sync.WaitGroup
	for _, op := range []string{"first-transfer", "second-transfer"} {
		workers.Add(1)
		go func(op string) {
			defer workers.Done()
			body, _ := json.Marshal(map[string]any{"operation_id": op, "revision": 0, "action": "take_over"})
			req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
			req.SetPathValue("crewId", crew)
			req.SetPathValue("identifier", "ENG-1")
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER"))
			rr := httptest.NewRecorder()
			<-start
			h.Work(rr, req)
			results <- rr.Code
		}(op)
	}
	close(start)
	workers.Wait()
	close(results)
	counts := map[int]int{}
	for code := range results {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent transfers: %v", counts)
	}
	var revision, receipts int
	if err := h.db.QueryRow(`SELECT revision FROM issue_work WHERE mission_id=?`, id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE mission_id=? AND source_kind='work_operation'`, id).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || receipts != 1 {
		t.Fatalf("losing transfer left effects: revision=%d receipts=%d", revision, receipts)
	}
}

func TestInboxAnswerAndTakeoverCommitOnlyWinningAction(t *testing.T) {
	r := setupNeedsHumanRig(t)
	start := make(chan struct{})
	results := make(chan int, 2)
	var workers sync.WaitGroup
	for _, body := range []string{`{"action":"answer","input":"Use staging"}`, `{"action":"take_over"}`} {
		workers.Add(1)
		go func(body string) {
			defer workers.Done()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			req.SetPathValue("id", r.cardID)
			req = withWorkspaceUser(req, r.f.userID, r.f.wsID, "OWNER")
			rr := httptest.NewRecorder()
			<-start
			r.ih.Act(rr, req)
			results <- rr.Code
		}(body)
	}
	close(start)
	workers.Wait()
	close(results)
	r.f.assign.WaitDispatches()
	counts := map[int]int{}
	for code := range results {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("competing Inbox decisions: %v", counts)
	}
	_, action, _, _ := r.card(t)
	var resumed, answers int
	if err := r.f.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE session_id=? AND id!=?`, r.sessionID, r.assignmentID).Scan(&resumed); err != nil {
		t.Fatal(err)
	}
	if err := r.f.db.QueryRow(`SELECT COUNT(*) FROM mission_comments WHERE mission_id=? AND body='Use staging'`, r.f.missionID).Scan(&answers); err != nil {
		t.Fatal(err)
	}
	want := 0
	if action == "answer" {
		want = 1
	}
	if resumed != want || answers != want {
		t.Fatalf("losing %s action leaked effects: runs=%d answers=%d want=%d", action, resumed, answers, want)
	}
}

func TestInternalStatusUpdateCannotRacePastHumanTakeover(t *testing.T) {
	h, ws, crew, agent, user := newInternalIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, agent, "ENG-1", "TODO")
	issues := NewIssueHandler(h.db, nil, nil, h.logger)
	start := make(chan struct{})
	var workers sync.WaitGroup
	work, status := httptest.NewRecorder(), httptest.NewRecorder()
	workers.Add(2)
	go func() {
		defer workers.Done()
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"operation_id":"take","revision":0,"action":"take_over"}`))
		req.SetPathValue("crewId", crew)
		req.SetPathValue("identifier", "ENG-1")
		req = withWorkspaceUser(req, user, ws, "OWNER")
		<-start
		issues.Work(work, req)
	}()
	go func() {
		defer workers.Done()
		body, _ := json.Marshal(map[string]string{"workspace_id": ws, "agent_id": agent, "status": "IN_PROGRESS"})
		req := httptest.NewRequest("PATCH", "/", bytes.NewReader(body))
		req.SetPathValue("identifier", "ENG-1")
		<-start
		h.UpdateStatus(status, req)
	}()
	close(start)
	workers.Wait()
	if work.Code != 200 || (status.Code != 200 && status.Code != 409) {
		t.Fatalf("takeover=%d %s status=%d %s", work.Code, work.Body.String(), status.Code, status.Body.String())
	}
	var mode, finalStatus string
	if err := h.db.QueryRowContext(t.Context(), `SELECT w.mode,m.status FROM missions m JOIN issue_work w ON w.mission_id=m.id WHERE m.id=?`, id).Scan(&mode, &finalStatus); err != nil {
		t.Fatal(err)
	}
	if mode != "human" || finalStatus != "TODO" {
		t.Fatalf("agent mutated human work: %s %s", mode, finalStatus)
	}
}
