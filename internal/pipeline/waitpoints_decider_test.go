package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// PRD §18 scenario 10 (B14, #2388): a peer agent's "GO" cannot satisfy a
// waitpoint. The resolve door refuses an agent decider — and an
// unidentified one — before touching the row, leaves the waitpoint
// pending, does not wake the parked step, and records the refusal in
// audit_logs. A person and the external token holder still decide.
func TestWaitpointDecide_RefusesAgentAndUnidentified_AllowsPersonAndExternal(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	const ws = "ws-b14"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B14', 'b14')`, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	store := NewSQLWaitpointStore(db)
	t.Cleanup(store.Close)

	newPending := func(t *testing.T, run string) string {
		t.Helper()
		tok, err := store.CreateApproval(context.Background(), WaitpointApprovalRequest{
			WorkspaceID: ws, PipelineRunID: run, StepID: "gate", Prompt: "May I?", TimeoutSec: 3600,
		})
		if err != nil {
			t.Fatalf("CreateApproval: %v", err)
		}
		return tok
	}
	rowStatus := func(t *testing.T, tok string) (status string, decidedBy string) {
		t.Helper()
		var by *string
		if err := db.QueryRow(`SELECT status, decided_by_user_id FROM pipeline_waitpoints WHERE token = ?`, tok).Scan(&status, &by); err != nil {
			t.Fatalf("read waitpoint: %v", err)
		}
		if by != nil {
			decidedBy = *by
		}
		return status, decidedBy
	}
	refusals := func(t *testing.T, tok string) []string {
		t.Helper()
		rows, err := db.Query(`SELECT metadata FROM audit_logs WHERE workspace_id = ? AND action = ? AND entity_type = 'waitpoint' AND entity_id = ?`,
			ws, AuditActionWaitpointDecisionRefused, tok)
		if err != nil {
			t.Fatalf("read audit: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var m string
			if err := rows.Scan(&m); err != nil {
				t.Fatalf("scan audit: %v", err)
			}
			out = append(out, m)
		}
		return out
	}

	for _, tc := range []struct {
		name        string
		decider     WaitpointDecider
		wantRefused bool
		wantStatus  string
		wantBy      string
	}{
		{"peer agent GO is refused", WaitpointDecider{Kind: DeciderAgent, ID: "crew:crew-peer"}, true, "pending", ""},
		{"unidentified caller is refused (fail closed)", WaitpointDecider{}, true, "pending", ""},
		{"a person decides", WaitpointDecider{Kind: DeciderUser, ID: "usr-manager"}, false, "approved", "usr-manager"},
		{"the external token holder decides", WaitpointDecider{Kind: DeciderExternal, ID: "external-callback"}, false, "approved", "external-callback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tok := newPending(t, "run-"+tc.name)
			// A parked step listening on the token must not be woken by a
			// refused attempt.
			woke := make(chan bool, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
				defer cancel()
				ok, err := store.WaitFor(ctx, tok)
				woke <- err == nil && ok
			}()
			time.Sleep(20 * time.Millisecond)

			err := store.Decide(context.Background(), ws, tok, true, tc.decider, "GO")
			if tc.wantRefused {
				if !errors.Is(err, ErrDeciderNotAllowed) {
					t.Fatalf("err = %v, want ErrDeciderNotAllowed", err)
				}
			} else if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			status, by := rowStatus(t, tok)
			if status != tc.wantStatus || by != tc.wantBy {
				t.Fatalf("row = (%s, %q), want (%s, %q)", status, by, tc.wantStatus, tc.wantBy)
			}
			got := refusals(t, tok)
			if tc.wantRefused {
				if len(got) != 1 {
					t.Fatalf("refusal records = %d, want 1", len(got))
				}
				for _, want := range []string{`"actor_kind":"` + string(tc.decider.Kind) + `"`, `"attempted":"approve"`, `"waitpoint_status":"pending"`, `"pipeline_run_id":"run-` + tc.name + `"`} {
					if !containsStr(got[0], want) {
						t.Errorf("refusal metadata %s lacks %s", got[0], want)
					}
				}
				if <-woke {
					t.Fatal("a refused decision woke the parked step")
				}
			} else {
				if len(got) != 0 {
					t.Fatalf("refusal records = %d for an allowed decider, want 0", len(got))
				}
				if !<-woke {
					t.Fatal("an allowed decision did not wake the parked step")
				}
			}
		})
	}
}

// After a refusal the waitpoint is still answerable by a person — the
// refusal changed nothing about the row.
func TestWaitpointDecide_RefusalLeavesWaitpointAnswerable(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	const ws = "ws-b14b"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B14b', 'b14b')`, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	store := NewSQLWaitpointStore(db)
	t.Cleanup(store.Close)
	tok, err := store.CreateApproval(context.Background(), WaitpointApprovalRequest{
		WorkspaceID: ws, PipelineRunID: "run-1", StepID: "gate", Prompt: "May I?", TimeoutSec: 3600,
	})
	if err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	if err := store.Decide(context.Background(), ws, tok, true, WaitpointDecider{Kind: DeciderAgent, ID: "crew:c1"}, "GO"); !errors.Is(err, ErrDeciderNotAllowed) {
		t.Fatalf("agent: err = %v", err)
	}
	if err := store.Decide(context.Background(), ws, tok, false, WaitpointDecider{Kind: DeciderUser, ID: "usr-1"}, "no"); err != nil {
		t.Fatalf("person after refusal: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM pipeline_waitpoints WHERE token = ?`, tok).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "denied" {
		t.Fatalf("status = %s, want denied", status)
	}
	// And the refusal of a now-decided waitpoint is still recorded, naming
	// the status it found.
	if err := store.Decide(context.Background(), ws, tok, true, WaitpointDecider{Kind: DeciderAgent, ID: "crew:c1"}, "GO"); !errors.Is(err, ErrDeciderNotAllowed) {
		t.Fatalf("agent on decided: err = %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = ? AND entity_id = ? AND metadata LIKE '%"waitpoint_status":"denied"%'`,
		AuditActionWaitpointDecisionRefused, tok).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("refusals on the decided row = %d, want 1", n)
	}
}

func containsStr(s, sub string) bool { return strings.Contains(s, sub) }
