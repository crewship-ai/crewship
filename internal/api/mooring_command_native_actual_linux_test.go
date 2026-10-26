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
	"path/filepath"
	"strings"
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
	// Opt-in local qualification artifacts only; synthetic fixture DB, never
	// user data. Register after fixture cleanup so failure snapshot runs first.
	if dir := os.Getenv("CREWSHIP_C1_FAILURE_ARTIFACT_DIR"); dir != "" {
		t.Cleanup(func() {
			if !t.Failed() {
				return
			}
			if os.MkdirAll(dir, 0700) != nil {
				t.Log("fixture preservation directory failed")
				return
			}
			target := filepath.Join(dir, strings.ReplaceAll(t.Name(), "/", "-")+".sqlite")
			if _, err := f.r.db.Exec(`VACUUM INTO ?`, target); err != nil {
				t.Log("fixture DB preservation failed")
				return
			}
			if os.Chmod(target, 0600) != nil {
				t.Log("fixture DB preservation mode failed")
			}
		})
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

// Actual helper -> Rust candidate -> native RPC -> atomic SQL operation receipt.
// This is not a rendered browser/keyboard/Saved qualification.
func TestCommandNativeActualSQLSelectionInsertEnterRestore(t *testing.T) {
	r := newActualCommandNativeRig(t)
	req := httptest.NewRequest("POST", "/native-test-only", nil).WithContext(r.f.ctx)
	block, off := r.block, uint64(0)
	var last commandSQLOutcome
	for i, item := range []struct {
		command string
		params  map[string]any
	}{{"text.insert", map[string]any{"text": "a"}}, {"text.insert", map[string]any{"text": "b"}}, {"text.insert", map[string]any{"text": "č"}}, {"text.newParagraph", map[string]any{}}, {"text.insert", map[string]any{"text": "Ž"}}, {"text.backspace", map[string]any{}}} {
		raw, _ := json.Marshal(map[string]any{"op_id": r.generation + ":" + stringUint(uint64(i+1)), "generation": r.generation, "participant": r.actor.actor, "base_revision": uint64(i), "command": item.command, "params": item.params, "target": map[string]any{"block_id": block, "unit": "utf8_bytes", "start": off, "end": off}})
		out, e := r.s.Submit(req, r.a, r.actor, raw)
		if e != nil {
			t.Fatal("actual command SQL selection", i, e)
		}
		cp := r.sqlSnapshot(t)
		selection, e := commandReceiptSelection(out.Receipt, cp.Checkpoint, item.command)
		if e != nil {
			t.Fatal("missing actual selection", e)
		}
		var value struct {
			Anchor, Focus struct {
				Story string   `json:"story"`
				Path  []uint64 `json:"path"`
				Off   uint64   `json:"off"`
			}
		}
		if json.Unmarshal(selection, &value) != nil {
			t.Fatal("selection")
		}
		projection, e := r.p.Projection(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		off = value.Focus.Off
		block = projection.IDs[value.Focus.Path[0]]
		expectedOff := []uint64{1, 2, 4, 0, 2, 0}[i]
		expectedBlock := uint64(0)
		if i >= 3 {
			expectedBlock = 1
		}
		if off != expectedOff || value.Focus.Path[0] != expectedBlock {
			t.Fatalf("actual selection step%d off%d block%d", i, off, value.Focus.Path[0])
		}
		var stored string
		if e = r.f.r.db.QueryRow(`SELECT result_json FROM mooring_collab_operations WHERE page_id='admission-page' AND op_id=?`, out.OpID).Scan(&stored); e != nil {
			t.Fatal(e)
		}
		var sqlOut commandSQLOutcome
		if json.Unmarshal([]byte(stored), &sqlOut) != nil || !bytes.Equal(sqlOut.Receipt, out.Receipt) {
			t.Fatal("SQL receipt lost exact selection")
		}
		duplicate, e := r.s.Submit(req, r.a, r.actor, raw)
		if e != nil || !bytes.Equal(duplicate.Receipt, out.Receipt) {
			t.Fatal("known op changed selection", e)
		}
		last = out
	}
	cp := r.sqlSnapshot(t)
	q, e := startCommandNativePort(context.Background(), r.config)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	if e = q.RestoreConfirmed(context.Background(), cp); e != nil {
		t.Fatal(e)
	}
	projection, e := q.Projection(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	// Peer data remains pure document/IDs; own selection is in the bound receipt.
	raw, _ := json.Marshal(projection)
	var peer map[string]json.RawMessage
	_ = json.Unmarshal(raw, &peer)
	if peer["selection"] != nil {
		t.Fatal("own caret leaked into peer projection")
	}
	var saved struct {
		Seen map[string]struct {
			Result json.RawMessage `json:"result"`
		} `json:"seen"`
	}
	if json.Unmarshal(cp.Checkpoint, &saved) != nil || !bytes.Equal(saved.Seen[last.OpID].Result, last.Receipt) {
		t.Fatal("restart checkpoint lost known selection")
	}
	if r.p.stages != 6 {
		t.Fatal("known operations reexecuted engine stage")
	}
	if e = q.exchange(context.Background(), "status", map[string]any{"op_id": last.OpID, "expected_fingerprint": last.Fingerprint}, func(raw json.RawMessage) error {
		var known struct {
			State       string `json:"state"`
			ReceiptJSON string `json:"receipt_json"`
		}
		if commandNativeDecode(raw, &known, "state", "receipt_json") != nil || known.State != "committed" || !bytes.Equal([]byte(known.ReceiptJSON), last.Receipt) {
			return errCommandStore
		}
		return nil
	}); e != nil {
		t.Fatal("restored known receipt lost actual selection", e)
	}
	if actualNativeText(t, projection) != "Abč" {
		t.Fatal("sequential own offsets reversed text")
	}
}

// Actual native engine and SQL, not a browser ribbon/renderer acceptance.
func TestCommandNativeActualSQLQualified72FormattingAndEnter(t *testing.T) {
	r := newActualCommandNativeRig(t)
	req := httptest.NewRequest("POST", "/native-test-only", nil).WithContext(r.f.ctx)
	steps := []struct {
		command    string
		params     map[string]any
		start, end uint64
	}{
		{"text.insert", map[string]any{"text": "Alpha"}, 0, 0},
		{"format.font", map[string]any{"name": "Arial"}, 1, 3},
		{"format.size", map[string]any{"size": 18}, 1, 3},
		{"format.bold", map[string]any{"value": true}, 1, 3},
		{"para.align", map[string]any{"value": "center"}, 0, 5},
		{"text.newParagraph", map[string]any{}, 5, 5},
	}
	for i, step := range steps {
		raw, _ := json.Marshal(map[string]any{"op_id": r.generation + ":" + stringUint(uint64(i+1)), "generation": r.generation, "participant": r.actor.actor, "base_revision": uint64(i), "command": step.command, "params": step.params, "target": map[string]any{"block_id": r.block, "unit": "utf8_bytes", "start": step.start, "end": step.end}})
		out, e := r.s.Submit(req, r.a, r.actor, raw)
		if e != nil || out.Revision != uint64(i+1) {
			t.Fatal("actual qualified SQL command", step.command, e)
		}
		cp := r.sqlSnapshot(t)
		if _, e = commandReceiptSelection(out.Receipt, cp.Checkpoint, step.command); e != nil {
			t.Fatal("native selection not bound to SQL receipt", step.command, e)
		}
	}
	projection, e := r.p.Projection(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var native struct {
		Body []struct {
			Text  string         `json:"text"`
			Props map[string]any `json:"props"`
			Runs  []struct {
				Len   int            `json:"len"`
				Props map[string]any `json:"props"`
			} `json:"runs"`
		} `json:"body"`
	}
	if json.Unmarshal([]byte(projection.NativeJSON), &native) != nil || len(native.Body) != 2 || native.Body[0].Text != "Alpha" || native.Body[1].Text != "" || native.Body[0].Props["align"] != "center" || projection.IDs[0] != r.block || projection.IDs[1] == r.block {
		t.Fatal("native text/Enter/alignment/IDs", projection.NativeJSON)
	}
	offset := 0
	for _, run := range native.Body[0].Runs {
		for j := 0; j < run.Len; j++ {
			selected := offset >= 1 && offset < 3
			if selected {
				if run.Props["font"] != "Arial" || run.Props["fontCs"] != "Arial" || run.Props["size"] != float64(18) || run.Props["sizeCs"] != float64(18) || run.Props["bold"] != true || run.Props["boldCs"] != true {
					t.Fatal("missing actual selected font/size/bold", offset, run.Props)
				}
			} else if run.Props["font"] == "Arial" || run.Props["size"] == float64(18) || run.Props["bold"] == true {
				t.Fatal("format leaked outside selected bytes", offset, run.Props)
			}
			offset++
		}
	}
	if offset != 5 {
		t.Fatal("native runs do not cover text", offset)
	}
	cp := r.sqlSnapshot(t)
	q, e := startCommandNativePort(context.Background(), r.config)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	if e = q.RestoreConfirmed(context.Background(), cp); e != nil {
		t.Fatal(e)
	}
	restored, e := q.Projection(context.Background())
	if e != nil || restored.NativeJSON != projection.NativeJSON || restored.Revision != 6 || restored.IDs[0] != projection.IDs[0] || restored.IDs[1] != projection.IDs[1] {
		t.Fatal("actual canonical SQL reload changed formatted document", e)
	}
}

func TestCommandNativeActualSQLQualifiedListDefineSelection(t *testing.T) {
	r := newActualCommandNativeRig(t)
	req := httptest.NewRequest("POST", "/native-test-only", nil).WithContext(r.f.ctx)
	if _, e := r.s.Submit(req, r.a, r.actor, r.input("seed", 0, "Alpha")); e != nil {
		t.Fatal(e)
	}
	policy, e := os.ReadFile("../../tools/wordcraft-capabilities/engine-qualification-policy.json")
	if e != nil {
		t.Fatal(e)
	}
	var declared struct {
		Commands map[string]struct {
			Params json.RawMessage `json:"testedParameters"`
		} `json:"commands"`
	}
	if json.Unmarshal(policy, &declared) != nil || len(declared.Commands["list.define"].Params) == 0 {
		t.Fatal("missing canonical list fixture")
	}
	raw, _ := json.Marshal(map[string]any{"op_id": r.generation + ":list", "generation": r.generation, "participant": r.actor.actor, "base_revision": 1, "command": "list.define", "params": declared.Commands["list.define"].Params, "target": map[string]any{"block_id": r.block, "unit": "utf8_bytes", "start": 0, "end": 5}})
	out, e := r.s.Submit(req, r.a, r.actor, raw)
	if e != nil || out.Revision != 2 {
		t.Fatal("actual list.define SQL commit", e)
	}
	cp := r.sqlSnapshot(t)
	selection, e := commandReceiptSelection(out.Receipt, cp.Checkpoint, "list.define")
	if e != nil {
		t.Fatal("native list selection must survive strict SQL receipt validation", e)
	}
	var positions map[string]json.RawMessage
	if json.Unmarshal(selection, &positions) != nil || len(positions) != 2 || positions["num"] != nil {
		t.Fatal("command metadata leaked into selection", string(selection))
	}
	projection, e := r.p.Projection(context.Background())
	if e != nil || actualNativeText(t, projection) != "Alpha" {
		t.Fatal("native list projection", e)
	}
	if !bytes.Equal(cp.Checkpoint, r.sqlSnapshot(t).Checkpoint) {
		t.Fatal("projection changed canonical SQL")
	}
	q, e := startCommandNativePort(context.Background(), r.config)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	if e = q.RestoreConfirmed(context.Background(), cp); e != nil {
		t.Fatal(e)
	}
	restored, e := q.Projection(context.Background())
	if e != nil || restored.NativeJSON != projection.NativeJSON || restored.Revision != 2 {
		t.Fatal("committed native list changed after reload", e)
	}
}
