//go:build linux

package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"testing"
)

// Optional explicit actual binaries, never a private build dependency. Ordinary
// Go tests skip this suite; qualification requires running it with BOTH pinned
// public-source build outputs supplied by the integration job.
func actualCommandNativeConfig(t *testing.T, runtime string) commandNativeConfig {
	t.Helper()
	host, engine := os.Getenv("CREWSHIP_C1_HOST_BINARY"), os.Getenv("CREWSHIP_C1_ENGINE_BINARY")
	if host == "" || engine == "" {
		t.Skip("actual C1 host/engine binaries not supplied; fake-process tests are not native qualification")
	}
	hash, e := commandNativeFileDigest(engine)
	if e != nil {
		t.Fatal("engine artifact", e)
	}
	return commandNativeConfig{Enabled: true, HostBinary: host, EngineBinary: engine, EngineSHA256: hash, RuntimeRef: runtime}
}

type actualCommandNativeObservedPort struct {
	*commandNativePort
	stages     int
	lostACK    bool
	afterStage func()
}

func (p *actualCommandNativeObservedPort) Stage(ctx context.Context, a commandHostAdmission, s commandSQLSnapshot, raw []byte) (commandSQLCandidate, error) {
	p.stages++
	c, e := p.commandNativePort.Stage(ctx, a, s, raw)
	if e == nil && p.afterStage != nil {
		p.afterStage()
	}
	return c, e
}
func (p *actualCommandNativeObservedPort) Confirm(ctx context.Context, c commandSQLCandidate, a commandHostAdmission) error {
	e := p.commandNativePort.Confirm(ctx, c, a)
	if e == nil && p.lostACK {
		return errCommandUnknown
	}
	return e
}

type actualCommandNativeRig struct {
	f                 *continuityRig
	s                 *collaborationCommandStore
	p                 *actualCommandNativeObservedPort
	a                 collaborationHumanAdmission
	actor             commandEngineActorBinding
	generation, block string
	initial           commandSQLSnapshot
	config            commandNativeConfig
}

func newActualCommandNativeRig(t *testing.T) *actualCommandNativeRig {
	t.Helper()
	f := newContinuityRig(t, true)
	config := actualCommandNativeConfig(t, f.owner.OwnerID)
	p, e := startCommandNativePort(context.Background(), config)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	initial, e := p.InitialSnapshot(context.Background(), "admission-page", "native-generation-one")
	if e != nil {
		t.Fatal("actual init", e)
	}
	projection, e := p.Projection(context.Background())
	if e != nil {
		t.Fatal("actual projection", e)
	}
	mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
	observed := &actualCommandNativeObservedPort{commandNativePort: p}
	s, e := newCollaborationCommandStore(true, f.s.pages, f.s.owners, f.s.fence, f.s.clock, f.owner, observed)
	if e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("POST", "/native-test-only", nil).WithContext(f.ctx)
	if e = s.Create(req, initial); e != nil {
		t.Fatal("real SQL creation", e)
	}
	a, e := s.Admit(f.ctx, true)
	if e != nil {
		t.Fatal(e)
	}
	return &actualCommandNativeRig{f: f, s: s, p: observed, a: a, actor: commandEngineActorBinding{transportParticipant: a.participant, sourceUser: a.source.UserID, actor: "logical-native-human"}, generation: projection.Generation, block: projection.IDs[0], initial: initial, config: config}
}
func (r *actualCommandNativeRig) input(op string, revision uint64, text string) []byte {
	raw, _ := json.Marshal(map[string]any{"op_id": r.generation + ":" + op, "generation": r.generation, "participant": r.actor.actor, "base_revision": revision, "target": map[string]any{"block_id": r.block, "unit": "utf8_bytes", "start": 0, "end": 0}, "command": "text.insert", "params": map[string]any{"text": text}})
	return raw
}
func (r *actualCommandNativeRig) sqlSnapshot(t *testing.T) commandSQLSnapshot {
	t.Helper()
	var revision uint64
	var raw string
	if e := r.f.r.db.QueryRow(`SELECT revision,checkpoint_base64 FROM mooring_collab_documents WHERE page_id='admission-page'`).Scan(&revision, &raw); e != nil {
		t.Fatal(e)
	}
	checkpoint, e := base64.StdEncoding.DecodeString(raw)
	if e != nil {
		t.Fatal(e)
	}
	return commandSQLSnapshot{revision, checkpoint}
}
func actualNativeText(t *testing.T, v commandNativeProjection) string {
	t.Helper()
	var native struct {
		Body []struct {
			Text string `json:"text"`
		} `json:"body"`
	}
	if json.Unmarshal([]byte(v.NativeJSON), &native) != nil || len(native.Body) == 0 {
		t.Fatal("missing actual native paragraph")
	}
	return native.Body[0].Text
}
func TestCommandNativeActualSQLCommitDuplicateReload(t *testing.T) {
	r := newActualCommandNativeRig(t)
	req := httptest.NewRequest("POST", "/native-test-only", nil).WithContext(r.f.ctx)
	raw := r.input("one", 0, "Native SQL")
	out, e := r.s.Submit(req, r.a, r.actor, raw)
	if e != nil {
		t.Fatal("actual stage->SQL->confirm", e)
	}
	if out.Revision != 1 || r.p.stages != 1 {
		t.Fatal(out)
	}
	cp := r.sqlSnapshot(t)
	if cp.Revision != 1 || bytes.Equal(cp.Checkpoint, r.initial.Checkpoint) {
		t.Fatal("SQL P not committed")
	}
	projection, e := r.p.Projection(context.Background())
	if e != nil || actualNativeText(t, projection) != "Native SQL" {
		t.Fatal("actual P not confirmed", e)
	}
	duplicate, e := r.s.Submit(req, r.a, r.actor, raw)
	if e != nil || !bytes.Equal(duplicate.Receipt, out.Receipt) || r.p.stages != 1 {
		t.Fatal("duplicate replay", e)
	}
	q, e := startCommandNativePort(context.Background(), r.config)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	if e = q.RestoreConfirmed(context.Background(), cp); e != nil {
		t.Fatal("new process canonical reload", e)
	}
	restored, e := q.Projection(context.Background())
	if e != nil || actualNativeText(t, restored) != "Native SQL" || restored.Revision != 1 || restored.Generation == r.generation || restored.IDs[0] != r.block {
		t.Fatal("reload changed P/identity", e)
	}
	if commandDigest(cp.Checkpoint) != commandDigest(r.sqlSnapshot(t).Checkpoint) {
		t.Fatal("restore rewrote SQL")
	}
}
func TestCommandNativeActualSQLRevocationRejectsCandidate(t *testing.T) {
	r := newActualCommandNativeRig(t)
	r.p.afterStage = func() {
		mustExec(t, r.f.r.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, r.f.r.wsID, r.f.r.userID)
	}
	req := httptest.NewRequest("POST", "/native-test-only", nil).WithContext(r.f.ctx)
	if _, e := r.s.Submit(req, r.a, r.actor, r.input("denied", 0, "MUST NOT COMMIT")); e == nil {
		t.Fatal("inflight revoked command committed")
	}
	cp := r.sqlSnapshot(t)
	if cp.Revision != 0 || !bytes.Equal(cp.Checkpoint, r.initial.Checkpoint) {
		t.Fatal("rejected candidate polluted SQL P")
	}
	projection, e := r.p.Projection(context.Background())
	if e != nil || actualNativeText(t, projection) != "" || projection.Revision != 0 {
		t.Fatal("rejected K polluted actual engine P", e)
	}
}
func TestCommandNativeActualSQLUnknownACKReconcileNoReplay(t *testing.T) {
	r := newActualCommandNativeRig(t)
	r.p.lostACK = true
	req := httptest.NewRequest("POST", "/native-test-only", nil).WithContext(r.f.ctx)
	raw := r.input("unknown", 0, "Committed once")
	out, e := r.s.Submit(req, r.a, r.actor, raw)
	if !errors.Is(e, errCommandUnknown) || len(out.Receipt) != 0 {
		t.Fatal("injected lost post-SQL ACK falsely delivered", e)
	}
	if _, e = r.s.Submit(req, r.a, r.actor, r.input("blocked", 0, "NO")); !errors.Is(e, errCommandBusy) {
		t.Fatal("unknown did not suspend", e)
	}
	known, e := r.s.ReconcileOriginal(req, r.a, r.actor, raw)
	if e != nil || known == nil || known.Revision != 1 {
		t.Fatal("SQL-authoritative reconciliation", e)
	}
	if r.p.stages != 1 {
		t.Fatal("reconciliation replayed command")
	}
	projection, e := r.p.Projection(context.Background())
	if e != nil || actualNativeText(t, projection) != "Committed once" || projection.Revision != 1 {
		t.Fatal("canonical restore lost P", e)
	}
	r.p.lostACK = false
	if _, e = r.s.Submit(req, r.a, r.actor, raw); e != nil || r.p.stages != 1 {
		t.Fatal("committed old-generation retry replayed", e)
	}
}

func TestCommandNativeActualSQLCommitErrorReconcileCanonicalP(t *testing.T) {
	r := newActualCommandNativeRig(t)
	mustExec(t, r.f.r.db, `CREATE TABLE native_deferred_fault(page TEXT REFERENCES pages(id) DEFERRABLE INITIALLY DEFERRED)`)
	mustExec(t, r.f.r.db, `CREATE TRIGGER native_commit_error AFTER INSERT ON mooring_collab_operations BEGIN INSERT INTO native_deferred_fault(page) VALUES('nonexistent-page'); END`)
	var e error
	r.a, e = r.s.Admit(r.f.ctx, true)
	if e != nil {
		t.Fatal(e)
	}
	r.actor.transportParticipant = r.a.participant
	req := httptest.NewRequest("POST", "/native-test-only", nil).WithContext(r.f.ctx)
	raw := r.input("commit-unknown", 0, "Not durable")
	out, e := r.s.Submit(req, r.a, r.actor, raw)
	if !errors.Is(e, errCommandUnknown) || len(out.Receipt) != 0 || out.Fingerprint == "" {
		t.Fatal("COMMIT error inferred outcome", e)
	}
	projection, e := r.p.Projection(context.Background())
	if e != nil || actualNativeText(t, projection) != "" || projection.Revision != 0 {
		t.Fatal("unknown published K", e)
	}
	known, e := r.s.Reconcile(req, r.a, out.OpID, out.Fingerprint)
	if e != nil || known != nil || r.p.stages != 1 {
		t.Fatal("unknown COMMIT replayed", e)
	}
	cp := r.sqlSnapshot(t)
	if cp.Revision != 0 || !bytes.Equal(cp.Checkpoint, r.initial.Checkpoint) {
		t.Fatal("COMMIT error changed canonical P")
	}
	projection, e = r.p.Projection(context.Background())
	if e != nil || actualNativeText(t, projection) != "" {
		t.Fatal("recovery failed", e)
	}
}
