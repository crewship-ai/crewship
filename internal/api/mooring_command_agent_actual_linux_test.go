//go:build linux && wordcraft_c1_integration

package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/work"
)

type commandAgentObservedPort struct {
	*actualCommandNativeObservedPort
	rejects, restores int
}

func (p *commandAgentObservedPort) Reject(ctx context.Context, c commandSQLCandidate) error {
	p.rejects++
	return p.commandNativePort.Reject(ctx, c)
}
func (p *commandAgentObservedPort) RestoreConfirmed(ctx context.Context, c commandSQLSnapshot) error {
	p.restores++
	return p.commandNativePort.RestoreConfirmed(ctx, c)
}

type commandAgentNativeRig struct {
	x                 *collaborationAgentTestFixture
	s                 *collaborationCommandStore
	p                 *commandAgentObservedPort
	a                 *collaborationCommandAgent
	request           *http.Request
	initial           commandSQLSnapshot
	generation, block string
}

// Actual C1 child/native engine and migrated SQLite, with canonical chat,
// original credential, run-principal and work-attempt SQL fixtures. The agent
// process/LLM itself is NOT launched or qualified by this suite.
func newCommandAgentNativeRig(t *testing.T, reader bool, partial ...bool) *commandAgentNativeRig {
	t.Helper()
	return newCommandAgentNativeRigWithSetup(t, reader, nil, partial...)
}
func newCommandAgentNativeRigWithSetup(t *testing.T, reader bool, beforeTask func(*collaborationAgentTestFixture), partial ...bool) *commandAgentNativeRig {
	t.Helper()
	config := actualCommandNativeConfig(t, "command-agent-native-owner")
	level := "write"
	if reader {
		level = "read"
	}
	copy := testutil.MigratedDB(t)
	path := copy.Path()
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	full, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = full.Close() })
	f := newArtifactPrincipalFixtureWithRig(t, pagesTransferSetupWithDB(t, full))
	expiry := time.Now().Add(time.Hour).UTC()
	mustExec(t, full.DB, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('original-session',?,?)`, f.a.HumanUserID, expiry.Format(time.RFC3339Nano))
	source, err := captureOriginalHumanCredential(t.Context(), full, &AuthUser{ID: f.a.HumanUserID, SessionID: "original-session", CredentialExpiresAt: expiry.Add(-time.Minute)}, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	x := newCollaborationAgentTestFixtureForPrincipal(t, f, source, level)
	x.key.ArtifactID = "admission-page"
	x.f.rig.seedPage(t, x.key.ArtifactID, x.key.ArtifactID, "Native", x.f.a.HumanUserID, "")
	mustExec(t, x.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,?,?)`, x.key.ArtifactID, x.f.a.AgentID, level, x.f.a.HumanUserID)
	owners := mooring.NewOwnerStore(x.f.rig.db, true, x.clock, time.Minute)
	owner, err := owners.Acquire(t.Context(), x.key, config.RuntimeRef)
	if err != nil {
		t.Fatal(err)
	}
	port, err := startCommandNativePort(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(port.Close)
	initial, err := port.InitialSnapshot(t.Context(), x.key.ArtifactID, "command-agent-generation")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := port.Projection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	observed := &commandAgentObservedPort{actualCommandNativeObservedPort: &actualCommandNativeObservedPort{commandNativePort: port}}
	s, err := newCollaborationCommandStore(true, x.f.pages, owners, x.fence, x.clock, owner, observed)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Create(httptest.NewRequest("POST", "/private-command-fixture", nil).WithContext(x.human), initial); err != nil {
		t.Fatal(err)
	}
	if beforeTask != nil {
		beforeTask(x)
	}
	intent := "task"
	if reader {
		intent = "read"
	}
	var pending *collaborationAgentPending
	x.tx(t, func(tx *sql.Tx) {
		var accepted work.Receipt
		pending, accepted, err = x.store.StartTask(x.human, tx, x.key, intent, x.f.a.MessageID, x.f.receipt, partial...)
		if err != nil {
			t.Fatal(err)
		}
		x.workID = accepted.WorkID
	})
	handle := x.attach(t, pending)
	confirmed := collaborationAgentConfirmed{store: x.store, key: x.key, mandate: handle, workID: x.workID, runID: x.f.a.RunID, agentID: x.f.a.AgentID, crewID: x.f.a.CrewID, attempt: 1, generation: 1, execID: "fixture-confirmed-exec"}
	a, err := s.bindAgent(confirmed, "agent:"+x.f.a.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	return &commandAgentNativeRig{x: x, s: s, p: observed, a: a, request: x.request(), initial: initial, generation: projection.Generation, block: projection.IDs[0]}
}
func (r *commandAgentNativeRig) input(op string, revision uint64, text string) []byte {
	raw, _ := json.Marshal(map[string]any{"op_id": r.generation + ":" + op, "generation": r.generation, "participant": r.a.actor, "base_revision": revision, "target": map[string]any{"block_id": r.block, "unit": "utf8_bytes", "start": 0, "end": 0}, "command": "text.insert", "params": map[string]any{"text": text}})
	return raw
}
func (r *commandAgentNativeRig) assertInitial(t *testing.T) {
	t.Helper()
	var encoded string
	var revision, operations int
	if err := r.x.f.rig.db.QueryRow(`SELECT revision,checkpoint_base64,(SELECT count(*) FROM mooring_collab_operations WHERE page_id=?) FROM mooring_collab_documents WHERE page_id=?`, r.x.key.ArtifactID, r.x.key.ArtifactID).Scan(&revision, &encoded, &operations); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || revision != 0 || operations != 0 || !bytes.Equal(raw, r.initial.Checkpoint) {
		t.Fatal("denial changed confirmed SQL P/history", err)
	}
	projection, err := r.p.Projection(t.Context())
	if err != nil || projection.Revision != 0 || actualNativeText(t, projection) != "" {
		t.Fatal("denied K changed actual native P", err)
	}
}

func TestCommandAgentActualSQLMandateBoundary(t *testing.T) {
	// One explicit optional suite, not one default-build skip per subcase.
	_ = actualCommandNativeConfig(t, "command-agent-native-owner")
	t.Run("confirmed_original_commits_and_duplicate_does_not_reexecute", func(t *testing.T) {
		r := newCommandAgentNativeRig(t, false)
		raw := r.input("one", 0, "Agent native")
		out, err := r.a.Submit(r.request, raw)
		if err != nil || out.Revision != 1 {
			t.Fatal("original chat mandate -> native -> SQL", err)
		}
		var receipt struct{ Actor, Author string }
		if json.Unmarshal(out.Receipt, &receipt) != nil || receipt.Actor != r.a.actor || receipt.Author != "agent:"+r.x.f.a.AgentID || receipt.Author == r.x.f.a.HumanUserID {
			t.Fatal("agent was projected as a human")
		}
		var participant string
		if err = r.x.f.rig.db.QueryRow(`SELECT participant_ref FROM mooring_collab_operations WHERE page_id=? AND op_id=?`, r.x.key.ArtifactID, out.OpID).Scan(&participant); err != nil || participant != r.a.confirmed.mandate {
			t.Fatal("SQL original mandate binding lost", err)
		}
		again, err := r.a.Submit(r.request, raw)
		if err != nil || !bytes.Equal(again.Receipt, out.Receipt) || r.p.stages != 1 {
			t.Fatal("exact duplicate reexecuted", err)
		}
		if _, err = r.a.Submit(r.request, r.input("partial-without-consent", 1, "DENIED")); err == nil || r.p.stages != 1 {
			t.Fatal("default atomic task silently committed a second command", err)
		}
		if _, err = r.a.Submit(r.request, r.input("one", 0, "changed")); !errors.Is(err, errCommandConflict) || r.p.stages != 1 {
			t.Fatal("changed payload accepted", err)
		}
		if cp, err := r.a.ReadCheckpoint(r.request); err != nil || cp.Revision != 1 {
			t.Fatal("confirmed SQL checkpoint", err)
		}
		if known, err := r.a.Reconcile(r.request, out.OpID, out.Fingerprint); err != nil || known == nil || !bytes.Equal(known.Receipt, out.Receipt) || r.p.stages != 1 {
			t.Fatal("known outcome replayed", err)
		}
		projection, err := r.p.Projection(t.Context())
		if err != nil || actualNativeText(t, projection) != "Agent native" {
			t.Fatal("native result differs from SQL receipt", err)
		}
	})
	t.Run("explicit_original_partial_policy_allows_separate_commands", func(t *testing.T) {
		r := newCommandAgentNativeRig(t, false, true)
		if _, err := r.a.Submit(r.request, r.input("partial-one", 0, "One")); err != nil {
			t.Fatal(err)
		}
		if _, err := r.a.Submit(r.request, r.input("partial-two", 1, "Two")); err != nil || r.p.stages != 2 {
			t.Fatal("original explicit partial policy lost", err)
		}
	})
	t.Run("reader_reads_but_never_stages", func(t *testing.T) {
		r := newCommandAgentNativeRig(t, true)
		if _, err := r.a.ReadCheckpoint(r.request); err != nil {
			t.Fatal("original reader", err)
		}
		if _, err := r.a.Submit(r.request, r.input("reader", 0, "DENIED")); err == nil || r.p.stages != 0 {
			t.Fatal("reader reached native Stage")
		}
		r.assertInitial(t)
	})
	for _, change := range []string{"revoked", "original_source_expired", "work_binding_changed", "cancel_requested", "human_projection", "missing_ingress", "forged_bearer", "payload_actor", "payload_task"} {
		t.Run(change, func(t *testing.T) {
			r := newCommandAgentNativeRig(t, false)
			raw := r.input("denied", 0, "DENIED")
			switch change {
			case "revoked":
				mustExec(t, r.x.f.rig.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='agent'`, r.x.key.ArtifactID)
			case "original_source_expired":
				mustExec(t, r.x.f.rig.db, `UPDATE user_sessions SET expires_at='2000-01-01T00:00:00Z' WHERE id='original-session'`)
			case "work_binding_changed":
				mustExec(t, r.x.f.rig.db, `UPDATE work_items SET source_ref='other-message' WHERE id=?`, r.x.workID)
			case "cancel_requested":
				mustExec(t, r.x.f.rig.db, `UPDATE work_attempts SET cancel_requested_at=? WHERE run_id=?`, time.Now().UTC().Format(time.RFC3339Nano), r.x.f.a.RunID)
			case "human_projection":
				r.request = r.request.WithContext(context.WithValue(r.request.Context(), ctxUser, &AuthUser{ID: r.x.f.a.HumanUserID}))
			case "missing_ingress":
				r.request = r.request.WithContext(context.Background())
			case "forged_bearer":
				r.request.Header.Set("Authorization", "Bearer forged-fixture")
			case "payload_actor", "payload_task":
				var value map[string]any
				if err := json.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				if change == "payload_actor" {
					value["participant"] = "human:" + r.x.f.a.HumanUserID
				} else {
					value["task_id"] = "replacement-task"
				}
				raw, _ = json.Marshal(value)
			}
			if _, err := r.a.Submit(r.request, raw); err == nil || r.p.stages != 0 {
				t.Fatal("unqualified source reached native Stage")
			}
			r.assertInitial(t)
		})
	}
	t.Run("inflight_revoke_rejects_native_K", func(t *testing.T) {
		r := newCommandAgentNativeRig(t, false)
		r.p.afterStage = func() {
			mustExec(t, r.x.f.rig.db, `UPDATE page_grants SET level='read' WHERE page_id=? AND subject_type='agent'`, r.x.key.ArtifactID)
		}
		if _, err := r.a.Submit(r.request, r.input("inflight", 0, "DENIED")); err == nil || r.p.stages != 1 || r.p.rejects != 1 {
			t.Fatal("postStage revoke published or left K", err)
		}
		r.assertInitial(t)
	})
	t.Run("final_SQL_revoke_rolls_back_before_Confirm", func(t *testing.T) {
		r := newCommandAgentNativeRigWithSetup(t, false, func(x *collaborationAgentTestFixture) {
			// Install before StartTask captures schemaCookie. Runtime DDL would
			// correctly invalidate the original before Stage instead.
			mustExec(t, x.f.rig.db, `CREATE TRIGGER agent_final_revoke AFTER INSERT ON mooring_collab_operations BEGIN UPDATE page_grants SET level='read' WHERE page_id=NEW.page_id AND subject_type='agent'; END`)
		})
		if _, err := r.a.Submit(r.request, r.input("final-revoke", 0, "DENIED")); err == nil || r.p.stages != 1 || r.p.rejects != 1 {
			t.Fatal("final current-rights check missed trigger change", err)
		}
		r.assertInitial(t)
	})
	t.Run("schema_drift_never_refreshes_original", func(t *testing.T) {
		r := newCommandAgentNativeRig(t, false)
		mustExec(t, r.x.f.rig.db, `CREATE TABLE command_after_admission_schema(value TEXT)`)
		if _, err := r.a.Submit(r.request, r.input("schema-drift", 0, "DENIED")); err == nil || r.p.stages != 0 {
			t.Fatal("schema drift refreshed original source", err)
		}
		r.assertInitial(t)
	})
	t.Run("unknown_op_lookup_never_replays", func(t *testing.T) {
		r := newCommandAgentNativeRig(t, false)
		if out, err := r.a.ReconcileOriginal(r.request, r.input("unknown", 0, "NO REPLAY")); err != nil || out != nil || r.p.stages != 0 || r.p.restores != 0 {
			t.Fatal("unknown outcome lookup executed command", err)
		}
		r.assertInitial(t)
	})
	t.Run("unknown_ACK_recovers_SQL_without_replay", func(t *testing.T) {
		r := newCommandAgentNativeRig(t, false)
		r.p.lostACK = true
		raw := r.input("lost-ack", 0, "Committed once")
		out, err := r.a.Submit(r.request, raw)
		if !errors.Is(err, errCommandUnknown) || len(out.Receipt) != 0 {
			t.Fatal("injected lost ACK falsely delivered", err)
		}
		known, err := r.a.ReconcileOriginal(r.request, raw)
		if err != nil || known == nil || known.Revision != 1 || r.p.stages != 1 || r.p.restores != 1 {
			t.Fatal("SQL reconciliation replayed", err)
		}
	})
	t.Run("same_actor_cannot_ACK_another_task_original", func(t *testing.T) {
		r := newCommandAgentNativeRig(t, false)
		raw := r.input("first-task", 0, "First")
		out, err := r.a.Submit(r.request, raw)
		if err != nil {
			t.Fatal(err)
		}
		// A second real StartTask/confirmed-attempt mandate, same stable actor.
		const run = "command-second-run"
		if err = captureArtifactChatPrincipal(httptest.NewRequest("POST", "/fixture", nil), r.x.f.rig.db, run, r.x.f.a.WorkspaceID, r.x.f.a.AgentID, r.x.f.a.CrewID, r.x.f.a.ChatID, "principal-next-message", r.x.f.receipt); err != nil {
			t.Fatal(err)
		}
		mustExec(t, r.x.f.rig.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('command-second-start',?,?,?,'run.started','system','test',?)`, r.x.f.a.WorkspaceID, r.x.f.a.CrewID, r.x.f.a.AgentID, run)
		var pending *collaborationAgentPending
		var accepted work.Receipt
		r.x.tx(t, func(tx *sql.Tx) {
			pending, accepted, err = r.x.store.StartTask(r.x.human, tx, r.x.key, "task", "principal-next-message", r.x.f.receipt)
			if err != nil {
				t.Fatal(err)
			}
		})
		r.x.workID = accepted.WorkID
		r.x.f.a.RunID = run
		handle := r.x.attach(t, pending)
		confirmed := r.a.confirmed
		confirmed.mandate, confirmed.workID, confirmed.runID, confirmed.execID = handle, accepted.WorkID, run, "fixture-second-exec"
		other, err := r.s.bindAgent(confirmed, r.a.actor)
		if err != nil {
			t.Fatal(err)
		}
		token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(r.x.f.pages.artifactMasterToken, confirmed.key.WorkspaceID, confirmed.crewID), confirmed.key.WorkspaceID, confirmed.agentID, run)
		req := r.x.request()
		req.Header.Set("Authorization", "Bearer "+token)
		if _, err = other.Submit(req, raw); !errors.Is(err, errCommandConflict) || r.p.stages != 1 {
			t.Fatal("stable actor borrowed another task duplicate", err)
		}
		if _, err = other.Reconcile(req, out.OpID, out.Fingerprint); !errors.Is(err, errCommandConflict) || r.p.stages != 1 {
			t.Fatal("stable actor borrowed another task outcome", err)
		}
		if _, err = other.Submit(req, r.input("second-task", 1, "Second")); err != nil || r.p.stages != 2 {
			t.Fatal("new explicit mandate cannot author its own operation", err)
		}
	})
}
