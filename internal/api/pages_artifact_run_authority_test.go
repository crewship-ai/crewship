package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// The principal regressions use the source admission and live-authority cases
// without constructing an artifact publisher or enabling run delegation.
type artifactPrincipalFixture struct {
	rig     *pagesTransferRig
	pages   *PageHandler
	receipt *chataudience.Receipt
	a       artifactRunAuthority
}

func newArtifactPrincipalFixture(t *testing.T) *artifactPrincipalFixture {
	t.Helper()
	r := pagesTransferSetup(t)
	r.seedCrew(t, "principal-crew", "Crew", "principal-crew")
	seedAgentRow(t, r.db, "principal-agent", r.wsID, "principal-crew", "Agent", "principal-agent", "AGENT")
	mustExec(t, r.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('principal-chat',?,'principal-agent',?,'group')`, r.wsID, r.userID)
	mustExec(t, r.db, `INSERT INTO chat_participants(chat_id,user_id,role) VALUES('principal-chat',?,'member')`, r.adminID)
	for _, message := range []struct{ id, user string }{{"principal-message", r.userID}, {"principal-next-message", r.userID}, {"principal-other-human-message", r.adminID}} {
		mustExec(t, r.db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES(?,'principal-chat','principal-agent','user','make report',?,?)`, message.id, tsformat.Format(time.Now()), message.user)
	}
	receipt, err := chataudience.CaptureTrusted(t.Context(), r.db, "principal-chat", r.userID, nil)
	if err != nil || receipt == nil {
		t.Fatalf("trusted receipt: %+v %v", receipt, err)
	}
	return &artifactPrincipalFixture{rig: r, pages: NewPageHandler(r.db, nil, newTestLogger()), receipt: receipt, a: artifactRunAuthority{WorkspaceID: r.wsID, AgentID: "principal-agent", CrewID: "principal-crew", RunID: "principal-run", HumanUserID: r.userID, ChatID: "principal-chat", MessageID: "principal-message"}}
}

func (f *artifactPrincipalFixture) capture(r *http.Request, message string, receipt *chataudience.Receipt) error {
	return captureArtifactChatPrincipal(r, f.rig.db, f.a.RunID, f.a.WorkspaceID, f.a.AgentID, f.a.CrewID, f.a.ChatID, message, receipt)
}

func (f *artifactPrincipalFixture) admit(t *testing.T) {
	t.Helper()
	if err := f.capture(httptest.NewRequest("POST", "/", nil), f.a.MessageID, f.receipt); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('principal-start',?,'principal-crew','principal-agent','run.started','system','test','principal-run')`, f.rig.wsID)
}

func TestArtifactPrincipalAdmissionAndExactRetry(t *testing.T) {
	f := newArtifactPrincipalFixture(t)
	f.admit(t)
	var before, after string
	if err := f.rig.db.QueryRow(`SELECT expires_at FROM artifact_run_principals WHERE run_id='principal-run'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := f.capture(httptest.NewRequest("POST", "/", nil), f.a.MessageID, f.receipt); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if err := f.rig.db.QueryRow(`SELECT expires_at FROM artifact_run_principals WHERE run_id='principal-run'`).Scan(&after); err != nil || after != before {
		t.Fatalf("exact retry refreshed principal expiry: %q -> %q: %v", before, after, err)
	}
	if err := f.pages.checkArtifactRunPrincipal(t.Context(), &f.a); err != nil {
		t.Fatalf("live admitted human run: %v", err)
	}
}

func TestArtifactPrincipalDuplicateBindingIsImmutable(t *testing.T) {
	for _, change := range []string{"message", "human", "member-epoch", "chat-epoch"} {
		t.Run(change, func(t *testing.T) {
			f := newArtifactPrincipalFixture(t)
			f.admit(t)
			var originalExpiry string
			if err := f.rig.db.QueryRow(`SELECT expires_at FROM artifact_run_principals WHERE run_id='principal-run'`).Scan(&originalExpiry); err != nil {
				t.Fatal(err)
			}
			message, receipt := "principal-next-message", f.receipt
			if change == "human" {
				message = "principal-other-human-message"
				var err error
				receipt, err = chataudience.CaptureTrusted(t.Context(), f.rig.db, f.a.ChatID, f.rig.adminID, nil)
				if err != nil || receipt == nil {
					t.Fatalf("second valid human receipt: %+v %v", receipt, err)
				}
			}
			if change == "member-epoch" || change == "chat-epoch" {
				message = f.a.MessageID
				if change == "member-epoch" {
					mustExec(t, f.rig.db, `UPDATE workspace_members SET access_revision=access_revision+1 WHERE user_id=?`, f.a.HumanUserID)
				} else {
					mustExec(t, f.rig.db, `UPDATE chats SET authority_revision=authority_revision+1 WHERE id=?`, f.a.ChatID)
				}
				var err error
				receipt, err = chataudience.CaptureTrusted(t.Context(), f.rig.db, f.a.ChatID, f.a.HumanUserID, nil)
				if err != nil || receipt == nil {
					t.Fatalf("renewed valid receipt: %+v %v", receipt, err)
				}
			}
			if err := f.capture(httptest.NewRequest("POST", "/", nil), message, receipt); err == nil {
				t.Errorf("changed valid %s binding reused an existing principal run without conflict", change)
			}
			var human, storedMessage, member, generation, expiry string
			var memberRevision, chatRevision int64
			if err := f.rig.db.QueryRow(`SELECT human_user_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at FROM artifact_run_principals WHERE run_id='principal-run'`).Scan(&human, &storedMessage, &member, &memberRevision, &generation, &chatRevision, &expiry); err != nil {
				t.Fatal(err)
			}
			if human != f.a.HumanUserID || storedMessage != f.a.MessageID || member != f.receipt.MemberID || memberRevision != f.receipt.MemberRevision || generation != f.receipt.ChatGeneration || chatRevision != f.receipt.ChatRevision || expiry != originalExpiry {
				t.Fatalf("immutable binding or expiry changed: human %s message %s member %s/%d chat %s/%d expiry %s", human, storedMessage, member, memberRevision, generation, chatRevision, expiry)
			}
		})
	}
}

func TestArtifactPrincipalRejectsUntrustedAdmission(t *testing.T) {
	for _, change := range []string{"crew-transport", "workspace-transport", "missing-receipt", "missing-message", "forged-human", "stale-receipt", "different-message-author"} {
		t.Run(change, func(t *testing.T) {
			f := newArtifactPrincipalFixture(t)
			r := httptest.NewRequest("POST", "/", nil)
			receipt := *f.receipt
			provided, message := &receipt, f.a.MessageID
			switch change {
			case "crew-transport":
				r = r.WithContext(context.WithValue(r.Context(), ctxInternalTokenCrew, f.a.CrewID))
			case "workspace-transport":
				r = r.WithContext(context.WithValue(r.Context(), ctxInternalTokenWS, f.a.WorkspaceID))
			case "missing-receipt":
				provided = nil
			case "missing-message":
				message = ""
			case "forged-human":
				receipt.UserID = "unknown"
			case "stale-receipt":
				receipt.MemberRevision++
			case "different-message-author":
				message = "principal-other-human-message"
			}
			if err := f.capture(r, message, provided); err == nil {
				t.Error("untrusted admission created publication principal")
			}
			var n int
			if err := f.rig.db.QueryRow(`SELECT COUNT(*) FROM artifact_run_principals`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("untrusted admission left principal: count %d error %v", n, err)
			}
		})
	}
}

func TestArtifactPrincipalLiveAuthority(t *testing.T) {
	for _, mutation := range []struct{ name, sql string }{
		{"expired admission", `UPDATE artifact_run_principals SET expires_at='2000-01-01T00:00:00Z'`},
		{"malformed expiry", `UPDATE artifact_run_principals SET expires_at='not-a-time'`},
		{"membership epoch", `UPDATE workspace_members SET access_revision=access_revision+1 WHERE user_id='ptdeparting'`},
		{"chat epoch", `UPDATE chats SET authority_revision=authority_revision+1 WHERE id='principal-chat'`},
		{"removed human", `DELETE FROM workspace_members WHERE user_id='ptdeparting'`},
		{"suspended human", `UPDATE users SET suspended_at='2000-01-01T00:00:00Z' WHERE id='ptdeparting'`},
		{"deleted workspace", `UPDATE workspaces SET deleted_at='2000-01-01T00:00:00Z' WHERE id='ptws1'`},
		{"deleted crew", `UPDATE crews SET deleted_at='2000-01-01T00:00:00Z' WHERE id='principal-crew'`},
		{"deleted agent", `UPDATE agents SET deleted_at='2000-01-01T00:00:00Z' WHERE id='principal-agent'`},
		{"expired agent", `UPDATE agents SET expired_at='2000-01-01T00:00:00Z' WHERE id='principal-agent'`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			f := newArtifactPrincipalFixture(t)
			f.admit(t)
			mustExec(t, f.rig.db, mutation.sql)
			if err := f.pages.checkArtifactRunPrincipal(t.Context(), &f.a); err == nil {
				t.Fatal("stale principal retained live run authority")
			}
		})
	}
}

func TestArtifactPrincipalTerminalStates(t *testing.T) {
	for _, state := range []string{"run.completed", "run.failed", "run.cancelled", "run.timeout", "run.recovered_stop"} {
		t.Run(state, func(t *testing.T) {
			f := newArtifactPrincipalFixture(t)
			f.admit(t)
			mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('principal-end',?,'principal-agent',?,'system','test','principal-run')`, f.rig.wsID, state)
			if err := f.pages.checkArtifactRunPrincipal(t.Context(), &f.a); err == nil {
				t.Fatal("terminal run retained principal authority")
			}
		})
	}
}

func TestArtifactPrincipalRequiresStartedRunAndMatchingIdentity(t *testing.T) {
	f := newArtifactPrincipalFixture(t)
	if err := f.capture(httptest.NewRequest("POST", "/", nil), f.a.MessageID, f.receipt); err != nil {
		t.Fatal(err)
	}
	if err := f.pages.checkArtifactRunPrincipal(t.Context(), &f.a); err == nil {
		t.Fatal("principal without committed run.started was live")
	}
	mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('principal-start',?,'principal-crew','principal-agent','run.started','system','test','principal-run')`, f.rig.wsID)
	for _, field := range []string{"workspace", "agent", "crew", "human", "run"} {
		t.Run(field, func(t *testing.T) {
			a := f.a
			switch field {
			case "workspace":
				a.WorkspaceID = "another-workspace"
			case "agent":
				a.AgentID = "another-agent"
			case "crew":
				a.CrewID = "another-crew"
			case "human":
				a.HumanUserID = f.rig.adminID
			case "run":
				a.RunID = "another-run"
			}
			if err := f.pages.checkArtifactRunPrincipal(t.Context(), &a); err == nil {
				t.Fatal("misbound identity retained principal authority")
			}
		})
	}
}

func TestArtifactPrincipalWorkLedgerFence(t *testing.T) {
	for _, tc := range []struct {
		name, mutation string
		allowed        bool
	}{
		{"running", "", true},
		{"starting", `UPDATE work_items SET state='starting'`, true},
		{"waiting", `UPDATE work_items SET state='waiting'`, true},
		{"assignment", `UPDATE work_items SET source='assignment'`, true},
		{"legacy ledger human", `UPDATE work_items SET authorized_by_user_id=''`, true},
		{"generation", `UPDATE work_items SET generation=2`, false},
		{"ended attempt", `UPDATE work_attempts SET ended_at='2000-01-01T00:00:00Z'`, false},
		{"expired deadline", `UPDATE work_items SET deadline_at='2000-01-01T00:00:00Z'`, false},
		{"malformed deadline", `UPDATE work_items SET deadline_at='not-a-time'`, false},
		{"autonomous source", `UPDATE work_items SET source='schedule'`, false},
		{"different human", `UPDATE work_items SET authorized_by_user_id='ptadmin'`, false},
		{"different workspace", `UPDATE work_items SET workspace_id='another-workspace'`, false},
		{"different agent", `UPDATE work_items SET agent_id='another-agent'`, false},
		{"different crew", `UPDATE work_items SET crew_id='another-crew'`, false},
		{"queued", `UPDATE work_items SET state='queued'`, false},
		{"retry wait", `UPDATE work_items SET state='retry_wait'`, false},
		{"terminal", `UPDATE work_items SET state='succeeded'`, false},
		{"uncertain runtime", `UPDATE work_items SET state='needs_reconciliation'`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newArtifactPrincipalFixture(t)
			f.admit(t)
			mustExec(t, f.rig.db, `INSERT INTO work_items(id,workspace_id,source,agent_id,crew_id,state,generation,authorized_by_user_id,eligible_at,created_at,updated_at) VALUES('principal-work',?,'chat','principal-agent','principal-crew','running',1,?,'2000-01-01T00:00:00Z','2000-01-01T00:00:00Z','2000-01-01T00:00:00Z')`, f.rig.wsID, f.rig.userID)
			mustExec(t, f.rig.db, `INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,started_at) VALUES('principal-run','principal-work',1,1,'worker','2100-01-01T00:00:00Z','2000-01-01T00:00:00Z','2000-01-01T00:00:00Z')`)
			if tc.mutation != "" {
				mustExec(t, f.rig.db, tc.mutation)
			}
			err := f.pages.checkArtifactRunPrincipal(t.Context(), &f.a)
			if (err == nil) != tc.allowed {
				t.Fatalf("ledger authority: error %v allowed %v", err, tc.allowed)
			}
		})
	}
}

func TestArtifactPrincipalReadsFencedAuthority(t *testing.T) {
	f := newArtifactPrincipalFixture(t)
	f.admit(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	tx, err := f.rig.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_members SET role=role WHERE user_id=?`, f.a.HumanUserID); err != nil {
		t.Fatal(err)
	}
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if err := f.pages.checkArtifactRunPrincipal(ctx, &f.a); err != nil {
		t.Fatalf("current authority through writer fence: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_members SET access_revision=access_revision+1 WHERE user_id=?`, f.a.HumanUserID); err != nil {
		t.Fatal(err)
	}
	if err := f.pages.checkArtifactRunPrincipal(ctx, &f.a); err == nil {
		t.Fatal("fenced authority ignored uncommitted membership revocation")
	}
}

func TestArtifactPrincipalRestrictedElsewhereAndCrewMove(t *testing.T) {
	for _, change := range []string{"restricted elsewhere", "agent moved crew"} {
		t.Run(change, func(t *testing.T) {
			f := newArtifactPrincipalFixture(t)
			f.admit(t)
			if change == "restricted elsewhere" {
				mustExec(t, f.rig.db, `INSERT INTO workspaces(id,name,slug) VALUES('restricted-elsewhere','Restricted','restricted-elsewhere')`)
				mustExec(t, f.rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('restricted-member','restricted-elsewhere',?,'MEMBER','restricted')`, f.a.HumanUserID)
			} else {
				f.rig.seedCrew(t, "another-crew", "Another", "another-crew")
				mustExec(t, f.rig.db, `UPDATE agents SET crew_id='another-crew' WHERE id=?`, f.a.AgentID)
			}
			if err := f.pages.checkArtifactRunPrincipal(t.Context(), &f.a); err == nil {
				t.Fatal("changed live authority retained principal")
			}
		})
	}
}
