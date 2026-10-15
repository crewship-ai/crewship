package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/crewship-ai/crewship/internal/ws"
)

func lifecycleFixture(t *testing.T, kind string) (*PageHandler, string, string, string, string) {
	t.Helper()
	var h *PageHandler
	var workspace, user, id string
	if kind == "artifact" {
		h, workspace, user, _ = requestFixture(t)
		id = createArtifactRequestForTest(t, h, workspace, user, requestBody, 202)
	} else {
		t.Fatal("unsupported test request kind")
	}
	execOrFatal(t, h.db, `INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) SELECT 'lifecycle-run',workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at FROM artifact_run_principals WHERE run_id='artifact-run'`)
	execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('lifecycle-start',?,'artifact-agent','run.started','system','fixture','lifecycle-run')`, workspace)
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", "lifecycle-run")
	return h, workspace, user, id, token
}

func lifecycleBindDuringDispatch(t *testing.T, h *PageHandler, kind, id string, returned error) {
	t.Helper()
	h.artifactDispatch = func(ctx context.Context, _ string, _ string, _ string, opts ws.ChatMessageOption) error {
		req := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
		var err error
		if kind == "artifact" {
			err = bindArtifactRequestRun(req, h.db, id, "lifecycle-run", opts.HumanAuthority)
		} else {
			t.Fatal("unsupported test request kind")
		}
		if err != nil {
			t.Fatal(err)
		}
		var stored string
		if err := h.db.QueryRow(`SELECT deadline_at FROM `+pageRequestTable(kind)+` WHERE id=?`, id).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		deadline, ok := ctx.Deadline()
		recorded, err := parsePageRequestTime(stored)
		if !ok || err != nil || !deadline.Equal(recorded) {
			t.Fatalf("dispatch deadline=%v stored=%q err=%v", deadline, stored, err)
		}
		return returned
	}
	if kind == "artifact" {
		h.dispatchNextArtifactRequest(t.Context())
	} else {
		t.Fatal("unsupported test request kind")
	}
}

func lifecycleJobState(t *testing.T, h *PageHandler, kind, id, want string) pageRequestIdentity {
	t.Helper()
	job, err := loadPageRequestIdentity(t.Context(), h.db, kind, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != want {
		t.Fatalf("job status=%s error=%q want=%s", job.Status, job.Error, want)
	}
	return job
}

func TestPageRequestLifecycleObserverLossAndRestartPreserveBoundRun(t *testing.T) {
	for _, kind := range []string{"artifact"} {
		for _, returned := range []error{nil, fmt.Errorf("observer: %w", orchestrator.ErrHostCaptureUnconfirmed)} {
			t.Run(fmt.Sprintf("%s/%v", kind, returned), func(t *testing.T) {
				h, _, _, id, _ := lifecycleFixture(t, kind)
				lifecycleBindDuringDispatch(t, h, kind, id, returned)
				job := lifecycleJobState(t, h, kind, id, "running")
				h.initializePageRequestLifecycles(t.Context())
				lifecycleJobState(t, h, kind, id, "running")
				observed, err := readPageRequestLifecycle(t.Context(), h.db, job, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if observed.HeartbeatAt != nil || observed.RunStatus != "" || observed.DeadlineSource != "recorded" || observed.ExecutionState != "awaiting_confirmation" {
					t.Fatalf("fabricated or missing lifecycle evidence: %+v", observed)
				}
				h.artifactDispatch = func(context.Context, string, string, string, ws.ChatMessageOption) error {
					t.Error("restart re-executed bound job")
					return nil
				}
				ctx, cancel := context.WithTimeout(t.Context(), 1200*time.Millisecond)
				defer cancel()
				h.RunArtifactRequests(ctx)
				lifecycleJobState(t, h, kind, id, "running")
			})
		}
	}
}

func TestPageRequestLifecycleConfirmedTerminalNeverInventsResult(t *testing.T) {
	for _, kind := range []string{"artifact"} {
		for _, event := range []string{"run.completed", "run.failed", "run.cancelled", "run.timeout"} {
			t.Run(kind+"/"+event, func(t *testing.T) {
				h, workspace, _, id, _ := lifecycleFixture(t, kind)
				lifecycleBindDuringDispatch(t, h, kind, id, nil)
				execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('lifecycle-terminal',?,'artifact-agent',?,'system','fixture','lifecycle-run','{}')`, workspace, event)
				if err := h.settlePageRequest(t.Context(), kind, id, "", time.Now()); err != nil {
					t.Fatal(err)
				}
				want := "failed"
				if event == "run.cancelled" {
					want = "cancelled"
				}
				job := lifecycleJobState(t, h, kind, id, want)
				if event == "run.completed" && !strings.Contains(job.Error, "without committing") {
					t.Fatalf("process exit mistaken for published result: %q", job.Error)
				}
			})
		}
	}
}

func TestPageRequestLifecycleIgnoresHistoricalObserverFailure(t *testing.T) {
	h, workspace, _, id, _ := lifecycleFixture(t, "artifact")
	lifecycleBindDuringDispatch(t, h, "artifact", id, nil)
	payload, _ := json.Marshal(map[string]any{"error_message": orchestrator.ErrHostCaptureUnconfirmed.Error(), "exit_code": nil})
	execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('old-observer-failure',?,'artifact-agent','run.failed','system','fixture','lifecycle-run',?)`, workspace, string(payload))
	if err := h.settlePageRequest(t.Context(), "artifact", id, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	lifecycleJobState(t, h, "artifact", id, "running")
}

func TestPageRequestLifecycleDeadlineOnlySettlesJobAndPublicationIsFenced(t *testing.T) {
	for _, kind := range []string{"artifact"} {
		t.Run(kind, func(t *testing.T) {
			h, workspace, _, id, token := lifecycleFixture(t, kind)
			lifecycleBindDuringDispatch(t, h, kind, id, nil)
			expired := tsformat.Format(time.Now().Add(-time.Second))
			execOrFatal(t, h.db, `UPDATE `+pageRequestTable(kind)+` SET deadline_at=? WHERE id=?`, expired, id)
			// No sweep has run yet. The actual final transaction must already refuse
			// a late result, even though its persisted job status is still running.
			if kind == "artifact" {
				body := fmt.Sprintf(`{"html":"<h1>Too late</h1>","base_rev":1,"mutation_id":"late","request_id":%q}`, id)
				artifactExpect(t, artifactAgentSave(t, h, workspace, token, body), 403)
			}
			lifecycleJobState(t, h, kind, id, "running")
			if err := h.settlePageRequest(t.Context(), kind, id, "", time.Now()); err != nil {
				t.Fatal(err)
			}
			job := lifecycleJobState(t, h, kind, id, "failed")
			if job.Error != pageRequestDeadlineError {
				t.Fatal(job.Error)
			}
			var terminals int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id='lifecycle-run' AND entry_type IN('run.completed','run.failed','run.cancelled','run.timeout')`).Scan(&terminals); err != nil || terminals != 0 {
				t.Fatalf("job expiry fabricated runtime settlement: %d %v", terminals, err)
			}
		})
	}
}

func TestPageRequestLifecycleCommittedResultWinsExpiry(t *testing.T) {
	h, workspace, _, id, token := lifecycleFixture(t, "artifact")
	lifecycleBindDuringDispatch(t, h, "artifact", id, nil)
	body := fmt.Sprintf(`{"html":"<h1>Committed first</h1>","base_rev":1,"mutation_id":"committed","request_id":%q}`, id)
	artifactExpect(t, artifactAgentSave(t, h, workspace, token, body), 201)
	job := lifecycleJobState(t, h, "artifact", id, "succeeded")
	deadline, _, err := pageRequestDeadline(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.settlePageRequest(t.Context(), "artifact", id, "", deadline.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	lifecycleJobState(t, h, "artifact", id, "succeeded")
}

func seedLifecycleWork(t *testing.T, h *PageHandler, workspace, agent, result, status string) {
	t.Helper()
	now := tsformat.Format(time.Now())
	later := tsformat.Format(time.Now().Add(time.Minute))
	execOrFatal(t, h.db, `INSERT INTO work_items(id,workspace_id,source,agent_id,crew_id,state,generation,eligible_at,created_at,updated_at) VALUES('lifecycle-work',?,'chat',?,'artifact-crew','running',1,?,?,?)`, workspace, agent, now, now, now)
	execOrFatal(t, h.db, `INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,started_at,run_status,run_result_json) VALUES('lifecycle-run','lifecycle-work',1,1,'fixture',?,?,?,?,NULLIF(?,''))`, later, now, now, status, result)
}

func TestPageRequestLifecycleWorkEvidenceIsTypedScopedAndDeadlineBounded(t *testing.T) {
	for _, tc := range []struct {
		name, agent, result, status string
		invalid                     bool
	}{{"unconfirmed", "artifact-agent", "", "", false}, {"malformed-object", "artifact-agent", `"not an object"`, "COMPLETED", true}, {"null-result", "artifact-agent", `null`, "COMPLETED", true}, {"foreign-agent", "other-agent", `{"exit_code":0}`, "COMPLETED", false}} {
		t.Run(tc.name, func(t *testing.T) {
			h, workspace, _, id, _ := lifecycleFixture(t, "artifact")
			lifecycleBindDuringDispatch(t, h, "artifact", id, nil)
			if tc.agent != "artifact-agent" {
				seedAgentRow(t, h.db, tc.agent, workspace, "artifact-crew", "Other", "other-agent", "AGENT")
			}
			seedLifecycleWork(t, h, workspace, tc.agent, tc.result, tc.status)
			// A legacy terminal must not bypass an owned attempt's missing or invalid
			// result, nor may another identity's ready result settle this job.
			execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('premature-terminal',?,'artifact-agent','run.completed','system','fixture','lifecycle-run','{}')`, workspace)
			job := lifecycleJobState(t, h, "artifact", id, "running")
			observation, err := readPageRequestLifecycle(t.Context(), h.db, job, time.Now())
			if tc.invalid && !errors.Is(err, errPageRequestEvidenceInvalid) {
				t.Fatalf("invalid typed result accepted: %+v %v", observation, err)
			}
			if !tc.invalid && err != nil {
				t.Fatal(err)
			}
			if observation.RunStatus != "" {
				t.Fatalf("unconfirmed/foreign evidence became terminal: %+v", observation)
			}
			deadline, _, err := pageRequestDeadline(job)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.settlePageRequest(t.Context(), "artifact", id, "", deadline.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			lifecycleJobState(t, h, "artifact", id, "failed")
		})
	}
}

func TestPageRequestLifecycleBadEvidenceDoesNotStarveOtherJobs(t *testing.T) {
	h, workspace, user, id, _ := lifecycleFixture(t, "artifact")
	lifecycleBindDuringDispatch(t, h, "artifact", id, nil)
	seedLifecycleWork(t, h, workspace, "artifact-agent", `[]`, "COMPLETED")
	second := createArtifactRequestForTest(t, h, workspace, user, strings.Replace(requestBody, "first-request", "second-request", 1), 202)
	execOrFatal(t, h.db, `UPDATE page_artifact_requests SET status='running',run_id='unobserved-other-run',deadline_at=? WHERE id=?`, tsformat.Format(time.Now().Add(-time.Second)), second)
	_, err := h.reconcilePageRequests(t.Context(), "", time.Now())
	if !errors.Is(err, errPageRequestEvidenceInvalid) {
		t.Fatalf("invalid evidence not reported: %v", err)
	}
	lifecycleJobState(t, h, "artifact", id, "running")
	lifecycleJobState(t, h, "artifact", second, "failed")
}

func TestPageRequestLifecycleConfirmedWorkResultSettlesWithoutJournalProjection(t *testing.T) {
	h, workspace, _, id, _ := lifecycleFixture(t, "artifact")
	lifecycleBindDuringDispatch(t, h, "artifact", id, nil)
	seedLifecycleWork(t, h, workspace, "artifact-agent", `{"exit_code":0}`, "COMPLETED")
	if err := h.settlePageRequest(t.Context(), "artifact", id, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	job := lifecycleJobState(t, h, "artifact", id, "failed")
	observation, err := readPageRequestLifecycle(t.Context(), h.db, job, time.Now())
	if err != nil || observation.RunStatus != "COMPLETED" {
		t.Fatalf("confirmed projection unavailable: %+v %v", observation, err)
	}
	if !strings.Contains(job.Error, "without committing") {
		t.Fatalf("work completion invented publish success: %q", job.Error)
	}
}
