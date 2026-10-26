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
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Explicit engine fixture. These tests prove real SQL/current Page policy and
// ordering only; they are not C1 engine, RPC, native editor or agent evidence.
type commandSQLFixturePort struct {
	done                  chan struct{}
	p                     []byte
	k                     bool
	stages, acks, rejects int
	failACK               bool
	refuse                bool
	stageError            bool
	afterStage            func()
	afterRestore          func()
	mutateCandidate       func(*commandSQLCandidate)
}

func (p *commandSQLFixturePort) Done() <-chan struct{} { return p.done }
func (p *commandSQLFixturePort) RuntimeRef() string    { return "admission-test-owner" }
func (p *commandSQLFixturePort) EngineSHA256() string  { return "sha256:" + strings.Repeat("a", 64) }
func (p *commandSQLFixturePort) Stage(_ context.Context, a commandHostAdmission, base commandSQLSnapshot, raw []byte) (commandSQLCandidate, error) {
	p.stages++
	if p.refuse {
		return commandSQLCandidate{}, errCommandRefused
	}
	p.k = true
	if p.stageError {
		return commandSQLCandidate{}, errors.New("fixture transport uncertainty")
	}
	var input struct {
		OpID       string `json:"op_id"`
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return commandSQLCandidate{}, err
	}
	receipt, _ := json.Marshal(map[string]any{"state": "host_committed", "op_id": input.OpID, "revision": base.Revision + 1, "actor": a.Participant, "author": a.Author, "command_time": a.Time, "fingerprint": commandDigest(raw)})
	var cp map[string]any
	if err := json.Unmarshal(base.Checkpoint, &cp); err != nil {
		return commandSQLCandidate{}, err
	}
	cp["revision"] = base.Revision + 1
	seen := cp["seen"].(map[string]any)
	seen[input.OpID] = map[string]any{"input": []byte(raw), "result": json.RawMessage(receipt)}
	cp["p"] = map[string]any{"fixture_revision": base.Revision + 1}
	next, _ := json.Marshal(cp)
	candidate := commandSQLCandidate{OpID: input.OpID, Document: "admission-page", Generation: input.Generation, Format: commandFormat, EngineRevision: commandEngineRevision, EngineSHA256: p.EngineSHA256(), BaseRevision: base.Revision, Revision: base.Revision + 1, BaseDigest: commandDigest(base.Checkpoint), Digest: commandDigest(next), Fingerprint: commandDigest(raw), Checkpoint: next, Receipt: receipt}
	if p.afterStage != nil {
		p.afterStage()
	}
	if p.mutateCandidate != nil {
		p.mutateCandidate(&candidate)
	}
	return candidate, nil
}
func (p *commandSQLFixturePort) Confirm(_ context.Context, c commandSQLCandidate, _ commandHostAdmission) error {
	p.acks++
	if p.failACK {
		return errors.New("fixture lost confirmation")
	}
	p.p = append([]byte(nil), c.Checkpoint...)
	p.k = false
	return nil
}
func (p *commandSQLFixturePort) Reject(_ context.Context, _ commandSQLCandidate) error {
	p.rejects++
	p.k = false
	return nil
}
func (p *commandSQLFixturePort) RestoreConfirmed(_ context.Context, s commandSQLSnapshot) error {
	p.p = append([]byte(nil), s.Checkpoint...)
	p.k = false
	if p.afterRestore != nil {
		p.afterRestore()
	}
	return nil
}

type commandSQLRig struct {
	f *continuityRig
	s *collaborationCommandStore
	p *commandSQLFixturePort
	a collaborationHumanAdmission
}

func newCommandSQLRig(t *testing.T, writer bool) *commandSQLRig {
	t.Helper()
	f := newContinuityRig(t, true)
	mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
	p := &commandSQLFixturePort{done: make(chan struct{})}
	s, err := newCollaborationCommandStore(true, f.s.pages, f.s.owners, f.s.fence, f.s.clock, f.owner, p)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"format": commandFormat, "document": "admission-page", "generation": "fixture-generation", "generations": []string{"fixture-generation"}, "revision": 0, "p": map[string]any{}, "history": []any{}, "seen": map[string]any{}, "engine_identity": map[string]any{"revision": commandEngineRevision, "patch_sha256": commandClockPatch, "catalog": "wordcraft-c1-10-v1"}})
	p.p = append([]byte(nil), raw...)
	req := httptest.NewRequest("POST", "/not-a-public-route", nil).WithContext(f.ctx)
	if err = s.Create(req, commandSQLSnapshot{Checkpoint: raw}); err != nil {
		t.Fatal("create", err)
	}
	if !writer {
		mustExec(t, f.r.db, `UPDATE workspace_members SET role='VIEWER',access_mode='restricted' WHERE workspace_id=? AND user_id=?`, f.r.wsID, f.r.userID)
		// Existing restricted-user policy allows reading one's own explicit
		// private audience, never writing it through workspace role snapshots.
		mustExec(t, f.r.db, `UPDATE pages SET private_audience=1 WHERE id='admission-page'`)
		// Membership changes invalidate sessions; model a new normal login,
		// not a forged role snapshot attached to the old credential.
		mustExec(t, f.r.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('command-reader-source',?,?)`, f.r.userID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
		user := &AuthUser{ID: f.r.userID, SessionID: "command-reader-source", CredentialExpiresAt: time.Now().Add(time.Hour)}
		source, e := captureOriginalHumanCredential(f.ctx, f.r.db, user, AuthKindSession, "")
		if e != nil {
			t.Fatal(e)
		}
		user.OriginalCredential = source
		f.ctx = auth.WithOriginalCredential(context.WithValue(f.ctx, ctxUser, user), *source)
	}
	a, err := s.Admit(f.ctx, writer)
	if err != nil {
		t.Fatal("admit", err)
	}
	return &commandSQLRig{f, s, p, a}
}
func (r *commandSQLRig) request() *http.Request {
	return httptest.NewRequest("POST", "/unit-command-submit", nil).WithContext(r.f.ctx)
}
func (r *commandSQLRig) actor() commandEngineActorBinding {
	return commandEngineActorBinding{transportParticipant: r.a.participant, sourceUser: r.a.source.UserID, actor: "fixture-logical-human"}
}
func (r *commandSQLRig) input(id string, base uint64, text string) []byte {
	raw, _ := json.Marshal(map[string]any{"op_id": "fixture-generation:" + id, "generation": "fixture-generation", "participant": r.actor().actor, "base_revision": base, "target": nil, "command": "text.insert", "params": map[string]any{"text": text}})
	return raw
}
func (r *commandSQLRig) state(t *testing.T) (string, []byte, int) {
	t.Helper()
	var revision, encoded string
	var n int
	if err := r.f.r.db.QueryRow(`SELECT revision,checkpoint_base64 FROM mooring_collab_documents WHERE page_id='admission-page'`).Scan(&revision, &encoded); err != nil {
		t.Fatal(err)
	}
	if err := r.f.r.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id='admission-page'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return revision, raw, n
}

func TestCommandSQLCommitOrdersCheckpointReceiptAndACK(t *testing.T) {
	r := newCommandSQLRig(t, true)
	old := append([]byte(nil), r.p.p...)
	r.p.afterStage = func() {
		rev, cp, n := r.state(t)
		if rev != "0" || n != 0 || !bytes.Equal(cp, old) || !bytes.Equal(r.p.p, old) {
			t.Fatal("prepare published candidate before SQL commit")
		}
	}
	out, err := r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 0, "hello"))
	if err != nil || len(out.Receipt) == 0 || out.Revision != 1 {
		t.Fatal(out, err)
	}
	rev, cp, n := r.state(t)
	if rev != "1" || n != 1 || !bytes.Equal(cp, r.p.p) || r.p.acks != 1 || r.p.rejects != 0 || r.p.k {
		t.Fatal("SQL/ACK order", rev, n, r.p.acks, r.p.rejects)
	}
	duplicate, err := r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 0, "hello"))
	if err != nil || duplicate.Fingerprint != out.Fingerprint || r.p.stages != 1 || r.p.acks != 1 {
		t.Fatal("duplicate re-executed", err)
	}
	if _, err = r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 0, "changed")); !errors.Is(err, errCommandConflict) {
		t.Fatal("changed payload", err)
	}
	var transport string
	if err = r.f.r.db.QueryRow(`SELECT participant_ref FROM mooring_collab_operations WHERE page_id='admission-page'`).Scan(&transport); err != nil || transport != r.a.participant || transport == r.actor().actor {
		t.Fatal("logical engine actor replaced original transport source", err)
	}
	fresh, err := r.s.Admit(r.f.ctx, true)
	if err != nil || fresh.participant == r.a.participant {
		t.Fatal("new transport fixture failed", err)
	}
	r.a = fresh
	if _, err = r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 0, "hello")); err != nil || r.p.stages != 1 {
		t.Fatal("logical actor retry changed provenance or executed", err)
	}
}

func TestCommandSQLDeniedAndStaleNeverReachEngine(t *testing.T) {
	t.Run("reader", func(t *testing.T) {
		r := newCommandSQLRig(t, false)
		before := append([]byte(nil), r.p.p...)
		for _, command := range []string{"text.insert", "text.delete", "text.backspace", "text.newParagraph", "format.bold", "format.font", "format.size", "para.align", "participant.undo"} {
			raw := bytes.ReplaceAll(r.input("one", 0, "no"), []byte("text.insert"), []byte(command))
			if _, err := r.s.Submit(r.request(), r.a, r.actor(), raw); err == nil {
				t.Fatal("reader write", command)
			}
		}
		rev, cp, n := r.state(t)
		if rev != "0" || n != 0 || r.p.stages != 0 || !bytes.Equal(cp, before) {
			t.Fatal("reader touched candidate/SQL")
		}
		read, err := r.s.ReadCheckpoint(r.request(), r.a)
		if err != nil || read.Revision != 0 || !bytes.Equal(read.Checkpoint, before) {
			t.Fatal("reader lost authorized confirmed read", err)
		}
		if _, err = r.s.Admit(r.f.ctx, true); err == nil {
			t.Fatal("canonical reader obtained writer admission")
		}
		if err = r.s.Create(r.request(), commandSQLSnapshot{Checkpoint: before}); err == nil {
			t.Fatal("canonical reader initialized document")
		}
	})
	t.Run("stale", func(t *testing.T) {
		r := newCommandSQLRig(t, true)
		if _, err := r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 3, "no")); !errors.Is(err, errCommandConflict) || r.p.stages != 0 {
			t.Fatal("stale reached engine", err)
		}
	})
	t.Run("agent", func(t *testing.T) {
		r := newCommandSQLRig(t, true)
		ctx := context.WithValue(r.f.ctx, artifactRunKey{}, &artifactRunAuthority{RunID: "fixture-agent-run"})
		req := r.request().WithContext(ctx)
		if _, err := r.s.Submit(req, r.a, r.actor(), r.input("one", 0, "no")); err == nil || r.p.stages != 0 {
			t.Fatal("agent gained interactive human path", err)
		}
	})
}

func TestCommandSQLStageUnknownAndMalformedPortNeverPublish(t *testing.T) {
	t.Run("stage transport unknown", func(t *testing.T) {
		r := newCommandSQLRig(t, true)
		r.p.stageError = true
		raw := r.input("one", 0, "K")
		if _, err := r.s.Submit(r.request(), r.a, r.actor(), raw); !errors.Is(err, errCommandUnknown) {
			t.Fatal(err)
		}
		if !r.p.k || r.p.rejects != 0 || r.p.acks != 0 {
			t.Fatal("uncertain stage converted to denial")
		}
		if out, err := r.s.ReconcileOriginal(r.request(), r.a, r.actor(), raw); err != nil || out != nil || r.p.stages != 1 || r.p.k {
			t.Fatal("stage recovery replayed", out, err)
		}
	})
	for _, kind := range []string{"engine", "generation", "digest", "receipt"} {
		t.Run(kind, func(t *testing.T) {
			r := newCommandSQLRig(t, true)
			before := append([]byte(nil), r.p.p...)
			r.p.mutateCandidate = func(c *commandSQLCandidate) {
				switch kind {
				case "engine":
					c.EngineSHA256 = "sha256:" + strings.Repeat("b", 64)
				case "generation":
					c.Generation = "different-generation"
				case "digest":
					c.BaseDigest = "sha256:" + strings.Repeat("b", 64)
				case "receipt":
					c.Receipt = []byte(`{"state":"host_committed"}`)
				}
			}
			if _, err := r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 0, "K")); err == nil {
				t.Fatal("unqualified candidate stored")
			}
			rev, cp, n := r.state(t)
			if rev != "0" || n != 0 || !bytes.Equal(cp, before) || r.p.acks != 0 {
				t.Fatal("malformed candidate changed confirmed state")
			}
		})
	}
}

func TestCommandSQLFinalRevocationRollsBackKWithoutPublishing(t *testing.T) {
	for _, mode := range []string{"revoke", "downgrade", "rollback", "base-CAS", "head-trigger", "receipt-trigger"} {
		t.Run(mode, func(t *testing.T) {
			r := newCommandSQLRig(t, true)
			before := append([]byte(nil), r.p.p...)
			if mode == "rollback" || mode == "head-trigger" || mode == "receipt-trigger" {
				// Install before admission so this tests an actual SQL insertion
				// rollback, not the separate schema-cookie preflight refusal.
				switch mode {
				case "rollback":
					mustExec(t, r.f.r.db, `CREATE TRIGGER command_unit_reject BEFORE INSERT ON mooring_collab_operations BEGIN SELECT RAISE(ABORT,'unit denial'); END`)
				case "head-trigger":
					mustExec(t, r.f.r.db, `CREATE TRIGGER command_unit_corrupt_head AFTER UPDATE OF revision ON mooring_collab_documents BEGIN UPDATE mooring_collab_documents SET checkpoint_base64='e30=' WHERE page_id=NEW.page_id; END`)
				case "receipt-trigger":
					mustExec(t, r.f.r.db, `CREATE TRIGGER command_unit_corrupt_receipt AFTER INSERT ON mooring_collab_operations BEGIN UPDATE mooring_collab_operations SET result_json='{}' WHERE page_id=NEW.page_id AND op_id=NEW.op_id; END`)
				}
				var err error
				r.a, err = r.s.Admit(r.f.ctx, true)
				if err != nil {
					t.Fatal(err)
				}
			}
			r.p.afterStage = func() {
				switch mode {
				case "revoke":
					mustExec(t, r.f.r.db, `UPDATE user_sessions SET revoked_at='2026-10-26T00:00:00Z' WHERE id='admission-source'`)
				case "downgrade":
					mustExec(t, r.f.r.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, r.f.r.wsID, r.f.r.userID)
				case "base-CAS":
					mustExec(t, r.f.r.db, `UPDATE mooring_collab_documents SET checkpoint_base64=? WHERE page_id='admission-page'`, base64.StdEncoding.EncodeToString(append(before, ' ')))
				}
			}
			if _, err := r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 0, "K")); err == nil {
				t.Fatal("final SQL guard accepted")
			}
			rev, _, n := r.state(t)
			if rev != "0" || n != 0 || r.p.acks != 0 || r.p.rejects != 1 || r.p.k || !bytes.Equal(r.p.p, before) {
				t.Fatal("rejected commit polluted P", rev, n, r.p.acks, r.p.rejects)
			}
			var changes int
			if err := r.f.r.db.QueryRow(`SELECT count(*) FROM mooring_collab_changes WHERE page_id='admission-page'`).Scan(&changes); err != nil || changes != 0 {
				t.Fatal("partial receipt survived rollback", changes, err)
			}
		})
	}
}

func TestCommandSQLCommitErrorSuspendsAndQueriesCanonicalOutcome(t *testing.T) {
	r := newCommandSQLRig(t, true)
	before := append([]byte(nil), r.p.p...)
	// Real deferred SQLite constraint: statements succeed, COMMIT returns an
	// error. The bridge conservatively refuses to infer an operation outcome.
	mustExec(t, r.f.r.db, `CREATE TABLE command_unit_deferred_fault(page TEXT REFERENCES pages(id) DEFERRABLE INITIALLY DEFERRED)`)
	mustExec(t, r.f.r.db, `CREATE TRIGGER command_unit_commit_error AFTER INSERT ON mooring_collab_operations BEGIN INSERT INTO command_unit_deferred_fault(page) VALUES('nonexistent-page'); END`)
	var err error
	r.a, err = r.s.Admit(r.f.ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.s.Submit(r.request(), r.a, r.actor(), r.input("commit-error", 0, "K"))
	if !errors.Is(err, errCommandUnknown) || out.Fingerprint == "" || r.p.rejects != 0 || r.p.acks != 0 || !r.p.k {
		t.Fatal("COMMIT error inferred rollback or success", out, err)
	}
	recovered, err := r.s.Reconcile(r.request(), r.a, out.OpID, out.Fingerprint)
	if err != nil || recovered != nil || r.p.stages != 1 || r.p.rejects != 0 || r.p.k || !bytes.Equal(r.p.p, before) {
		t.Fatal("unknown recovery replayed or failed", recovered, err)
	}
	rev, cp, n := r.state(t)
	if rev != "0" || n != 0 || !bytes.Equal(cp, before) {
		t.Fatal("failed COMMIT published SQL P")
	}
}

func TestCommandSQLReconcileFinalRightsAndTrustedActor(t *testing.T) {
	t.Run("revoked during restore", func(t *testing.T) {
		r := newCommandSQLRig(t, true)
		r.p.failACK = true
		out, err := r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 0, "committed"))
		if !errors.Is(err, errCommandUnknown) {
			t.Fatal(err)
		}
		r.p.afterRestore = func() {
			mustExec(t, r.f.r.db, `UPDATE user_sessions SET revoked_at='2026-10-26T00:00:00Z' WHERE id='admission-source'`)
		}
		if value, err := r.s.Reconcile(r.request(), r.a, out.OpID, out.Fingerprint); err == nil || value != nil {
			t.Fatal("reconcile disclosed revoked result", value, err)
		}
	})
	t.Run("actor source binding", func(t *testing.T) {
		r := newCommandSQLRig(t, true)
		actor := r.actor()
		actor.sourceUser = "different-user"
		if _, err := r.s.Submit(r.request(), r.a, actor, r.input("one", 0, "no")); err == nil || r.p.stages != 0 {
			t.Fatal("unbound engine actor")
		}
		copy := r.a
		copy.write = false
		if _, err := r.s.Submit(r.request(), copy, r.actor(), r.input("one", 0, "no")); err == nil || r.p.stages != 0 {
			t.Fatal("caller substituted admission")
		}
	})
	t.Run("client authority fields", func(t *testing.T) {
		r := newCommandSQLRig(t, true)
		for _, key := range []string{"author", "time", "role", "writer", "engine_revision"} {
			var input map[string]any
			json.Unmarshal(r.input("one", 0, "no"), &input)
			input[key] = "forged"
			raw, _ := json.Marshal(input)
			if _, err := r.s.Submit(r.request(), r.a, r.actor(), raw); err == nil || r.p.stages != 0 {
				t.Fatal("client authority reached engine", key)
			}
		}
	})
	t.Run("qualified refusal is not transport uncertainty", func(t *testing.T) {
		r := newCommandSQLRig(t, true)
		r.p.refuse = true
		if _, err := r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 0, "no")); !errors.Is(err, errCommandRefused) {
			t.Fatal(err)
		}
		r.p.refuse = false
		if _, err := r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 0, "valid")); err != nil {
			t.Fatal("clean refusal poisoned future write", err)
		}
	})
}

func TestCommandSQLUnknownACKReconcilesCommittedOutcomeWithoutReplay(t *testing.T) {
	r := newCommandSQLRig(t, true)
	r.p.failACK = true
	out, err := r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 0, "committed"))
	if !errors.Is(err, errCommandUnknown) || out.Fingerprint == "" || len(out.Receipt) != 0 || r.p.rejects != 0 || !r.p.k {
		t.Fatal("uncertainty treated as rejection", out, err)
	}
	if _, err = r.s.Submit(r.request(), r.a, r.actor(), r.input("two", 1, "do not replay")); !errors.Is(err, errCommandBusy) {
		t.Fatal("unknown did not suspend", err)
	}
	if _, err = r.s.Reconcile(r.request(), r.a, out.OpID, "sha256:"+strings.Repeat("b", 64)); !errors.Is(err, errCommandConflict) {
		t.Fatal("fingerprint mismatch", err)
	}
	recovered, err := r.s.Reconcile(r.request(), r.a, out.OpID, out.Fingerprint)
	if err != nil || recovered == nil || recovered.Revision != 1 || r.p.k || r.p.stages != 1 || r.p.rejects != 0 {
		t.Fatal("unknown recovery replayed/rejected", recovered, err)
	}
	rev, cp, n := r.state(t)
	if rev != "1" || n != 1 || !bytes.Equal(cp, r.p.p) {
		t.Fatal("restore not canonical")
	}
}

type commandLegacySQLFixture struct{ called bool }

func (a *commandLegacySQLFixture) ValidateCheckpoint(context.Context, []byte) error {
	a.called = true
	return nil
}
func (a *commandLegacySQLFixture) Apply(context.Context, []byte, mooring.BatchChange) ([]byte, *mooring.PreparedFailure, error) {
	a.called = true
	return []byte("{}"), nil, nil
}

func TestCommandSQLFormatGuardsApplyWhileCommandFeatureDisabled(t *testing.T) {
	r := newCommandSQLRig(t, true)
	before := append([]byte(nil), r.p.p...)
	if _, err := newCollaborationCommandStore(false, r.s.pages, r.s.owners, r.s.humans.fence, r.s.humans.clock, r.s.owner, r.p); err == nil {
		t.Fatal("default-off accepted")
	}
	a := &commandLegacySQLFixture{}
	legacy := mooring.NewBatchStore(r.f.r.db, mooring.BatchStoreConfig{Enabled: true, Owners: r.s.owners, Authority: func(context.Context, *sql.Tx, mooring.BatchAccess) error { return nil }, Adapter: a, Limits: mooring.DefaultBatchLimits()})
	if _, _, err := legacy.ReadCheckpoint(r.f.ctx, r.s.owner.Key); err == nil {
		t.Fatal("legacy decoded command bytes")
	}
	if err := legacy.RegisterTask(r.f.ctx, r.s.owner, "legacy-task", "legacy-participant", false); err == nil {
		t.Fatal("legacy write accepted")
	}
	if _, err := legacy.Status(r.f.ctx, r.s.owner.Key, "old-operation", "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("legacy outcome decoded")
	}
	if err := legacy.Initialize(r.f.ctx, r.s.owner, []byte("{}")); err == nil {
		t.Fatal("legacy replacement accepted")
	}
	if a.called {
		t.Fatal("legacy adapter received command payload")
	}
	if _, err := r.f.r.db.Exec(`UPDATE mooring_collab_documents SET format_profile='mooring-causal-v1' WHERE page_id='admission-page'`); err == nil {
		t.Fatal("in-place format conversion accepted")
	}
	if _, err := r.f.r.db.Exec(`UPDATE mooring_collab_documents SET engine_sha256=? WHERE page_id='admission-page'`, "sha256:"+strings.Repeat("b", 64)); err == nil {
		t.Fatal("engine pin changed")
	}
	rev, cp, n := r.state(t)
	if rev != "0" || n != 0 || !bytes.Equal(cp, before) {
		t.Fatal("disabled feature lost command data")
	}
}
