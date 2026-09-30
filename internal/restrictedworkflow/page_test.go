package restrictedworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

const pageSpec = `{"apiVersion":"crewship/v1","kind":"Page","metadata":{"slug":"private-page"},"spec":{"name":"Private Page","panels":[{"id":"panel","schema":"status.v1","owner":"crew/workflow-crew","producer":"script/status.sh","sla_seconds":30,"span":8,"actions":[{"id":"do-work","kind":"call","label":"Run private work","routine":"private-work","params":{"task":"PAGE_PRIVATE_CANARY"}}]}]}}`

func seedPage(t *testing.T, s *Service) pipeline.PageActionInvocation {
	t.Helper()
	for _, q := range []string{
		`UPDATE workspace_members SET role='MANAGER' WHERE id='m1'`,
		`INSERT INTO crew_members(crew_id,user_id,role) VALUES('crew','h1','MANAGER')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('page','w','private-page','Private','owner',?)`,
		`INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds,span) VALUES('panel-row','page','panel','status.v1','crew','script','status.sh',30,8)`,
	} {
		args := []any{}
		if strings.Contains(q, "INSERT INTO pages(") {
			args = append(args, pageSpec)
		}
		if _, err := s.db.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	var doc pages.Document
	if err := json.Unmarshal([]byte(pageSpec), &doc); err != nil {
		t.Fatal(err)
	}
	panel, ok := doc.FindPanel("panel")
	if !ok {
		t.Fatal("panel missing")
	}
	action, ok := panel.FindAction("do-work")
	if !ok {
		t.Fatal("action missing")
	}
	return pipeline.PageActionInvocation{PageID: "page", PanelID: "panel", ActionID: "do-work", PipelineID: "routine", ActionDigest: pipeline.PageActionDigest(action)}
}
func TestDeclaredPageActionUsesPrivateQueueAndCannotBypassFloor(t *testing.T) {
	s, _ := fixture(t)
	action := seedPage(t, s)
	if _, err := s.AdmitPage(t.Context(), "h2", "w", action, map[string]any{"task": "private"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent run bypassed Page floor %v", err)
	}
	receipt, err := s.AdmitPage(t.Context(), "h1", "w", action, map[string]any{"task": "PAGE_PRIVATE_CANARY"}, "same-click")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.AdmitPage(t.Context(), "h1", "w", action, map[string]any{"task": "PAGE_PRIVATE_CANARY"}, "same-click")
	if err != nil || again.ID != receipt.ID || again.State != "DEDUPED" {
		t.Fatalf("Page click duplicated %+v %v", again, err)
	}
	if _, err = s.AdmitPage(t.Context(), "h1", "w", action, map[string]any{"task": "changed"}, "same-click"); !errors.Is(err, ErrDenied) {
		t.Fatalf("idempotency changed input %v", err)
	}
	if worked, err := s.DispatchNext(t.Context()); !worked || err != nil {
		t.Fatalf("Page dispatch %v %v", worked, err)
	}
	if result, err := s.Result(t.Context(), "h1", "w", receipt.ID); err != nil || result.State != "completed" {
		t.Fatalf("Page result %+v %v", result, err)
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE pages SET spec_json='{}' WHERE id='page'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE pages SET spec_json=? WHERE id='page'`, pageSpec); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Result(t.Context(), "h1", "w", receipt.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("Page regrant revived original declaration %v", err)
	}
	var shared int
	if err = s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pending_runs`).Scan(&shared); err != nil || shared != 0 {
		t.Fatalf("Page queued shared input rows %d %v", shared, err)
	}
}
func TestPageActionDeletionBehindQueueStopsBeforeBuilder(t *testing.T) {
	s, runner := fixture(t)
	action := seedPage(t, s)
	starts := 0
	base := runner.StartSession
	runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
		starts++
		return base(ctx, handle)
	}
	if _, err := s.AdmitPage(t.Context(), "h1", "w", action, map[string]any{"task": "PAGE_PRIVATE_CANARY"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE pages SET spec_json='{}' WHERE id='page'`); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.DispatchNext(t.Context()); !worked || !errors.Is(err, ErrDenied) || starts != 0 {
		t.Fatalf("deleted Page action ran worked=%v err=%v starts=%d", worked, err, starts)
	}
}
