//go:build linux && wordcraft_c1_integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Expected RED on 8789ef07f: the current exact-revision protocol admits only
// one of two independent intents at revision zero. This exercises native C1,
// SQL and the confirmed agent mandate, NOT a CLI or browser. No intent is
// replayed, relabelled with another base, or silently dropped by the test.
func TestC1HumanAndConfirmedAgentConcurrentIntentsBothPersist(t *testing.T) {
	rig := newCommandAgentNativeRig(t, false)
	admission, err := rig.s.Admit(rig.x.human, true)
	if err != nil {
		t.Fatal(err)
	}
	defer commandReleaseAdmission(rig.s, admission)
	actor := commandEngineActorBinding{transportParticipant: admission.participant, sourceUser: admission.source.UserID, actor: "human:" + admission.source.UserID}
	human, err := json.Marshal(map[string]any{"op_id": rig.generation + ":human-concurrent", "generation": rig.generation, "participant": actor.actor, "base_revision": 0, "command": "text.insert", "params": map[string]any{"text": "H"}, "target": map[string]any{"block_id": rig.block, "unit": "utf8_bytes", "start": 0, "end": 0}})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := rig.s.Submit(httptest.NewRequest("POST", "/human-fixture", nil).WithContext(rig.x.human), admission, actor, human)
		results <- err
	}()
	go func() {
		<-start
		_, err := rig.a.Submit(rig.request, rig.input("agent-concurrent", 0, "A"))
		results <- err
	}()
	close(start)
	errors := []error{<-results, <-results}
	projection, err := rig.p.Projection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	text := actualNativeText(t, projection)
	if errors[0] != nil || errors[1] != nil || projection.Revision != 2 || (text != "HA" && text != "AH") {
		t.Fatalf("both independent intents must persist without client replay: results=%v revision=%d text=%q; current protocol needs server conflict resolution", errors, projection.Revision, text)
	}
	var receipts int
	if err := rig.x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id=?`, rig.x.key.ArtifactID).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatalf("both intents require durable SQL receipts: count=%d error=%v", receipts, err)
	}
	snapshot, err := rig.a.ReadCheckpoint(rig.request)
	if err != nil || snapshot.Revision != 2 {
		t.Fatal("read confirmed SQL checkpoint", err)
	}
	cold, err := startCommandNativePort(t.Context(), actualCommandNativeConfig(t, "concurrent-reload"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cold.Close)
	if err := cold.RestoreConfirmed(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	reloaded, err := cold.Projection(t.Context())
	if err != nil || reloaded.Revision != 2 || actualNativeText(t, reloaded) != text {
		t.Fatal("cold restore lost a concurrent intent", err)
	}
}

// Expected RED prerequisite for a human + CLI-agent scenario: the public C1
// handler has no confirmed-agent ingress. A real CLI calls the actual handler;
// the auth boundary is a fixture using the same post-auth context as existing
// mandate tests. It does not impersonate an agent as an original human or add
// an invented endpoint. A 403 is a missing capability, NOT an auth bypass bug.
func TestC1ConfirmedAgentCanOpenWritableDocumentThroughCLI(t *testing.T) {
	binary := os.Getenv("CREWSHIP_C1_CLI_BINARY")
	if binary == "" {
		t.Fatal("set CREWSHIP_C1_CLI_BINARY to a CLI built from this checkout")
	}
	rig := newCommandAgentNativeRig(t, false)
	if _, err := rig.a.ReadCheckpoint(rig.request); err != nil {
		t.Fatal("control: confirmed agent must be authorized", err)
	}
	// A complete product factory shares the actual agent SQL, lease and port.
	_, manager, _ := commandProductTestManager(t)
	manager.pages = rig.x.f.pages
	manager.owners = rig.s.owners
	manager.fence = rig.x.fence
	manager.clock = rig.x.clock
	factory, err := newCommandProductFactory(true, manager, func(context.Context, string) (commandProductPort, error) { return rig.p, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	document := &commandProductDocument{ready: make(chan struct{}), store: rig.s, port: rig.p, owner: rig.s.owner}
	close(document.ready)
	factory.docs[rig.x.key] = document
	var calls, status atomic.Int32
	token := rig.request.Header.Get("Authorization")
	path := "/api/v1/pages/@" + rig.x.key.ArtifactID + "/mooring/command-session"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != token {
			http.Error(w, "fixture credential mismatch", 401)
			return
		}
		r.SetPathValue("slug", "@"+rig.x.key.ArtifactID)
		// Existing confirmed-agent test context includes the real mandate token
		// and ingress stamp, but deliberately no OriginalHumanCredential.
		r = r.WithContext(rig.request.Context())
		capture := &cliRedStatusWriter{ResponseWriter: w, status: &status}
		factory.serve(capture, r)
	}))
	defer server.Close()
	config := filepath.Join(t.TempDir(), "cli.json")
	if err := os.WriteFile(config, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--server", server.URL, "--workspace", rig.x.key.WorkspaceID, "api", "request", "POST", path, "--input", "-", "--yes", "--header", "Origin="+server.URL)
	cmd.Stdin = strings.NewReader(`{"action":"open","write":true}`)
	// Explicit fixture-only credentials; never log subprocess environment.
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "CREWSHIP_") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_TOKEN="+strings.TrimPrefix(token, "Bearer "))
	output, err := cmd.CombinedOutput()
	if calls.Load() != 1 {
		t.Fatalf("CLI harness did not reach C1 exactly once: calls=%d exit=%v output=%s", calls.Load(), err, output)
	}
	if status.Load() != http.StatusOK || err != nil {
		t.Fatalf("confirmed agent CLI writable-open prerequisite failed: HTTP%d exit=%v; full concurrent CLI scenario cannot start", status.Load(), err)
	}
	var opened commandProductHTTPResponse
	if json.Unmarshal(output, &opened) != nil || opened.View == "" || opened.Actor != rig.a.actor {
		t.Fatal("CLI response must contain an agent-bound view")
	}
}

type cliRedStatusWriter struct {
	http.ResponseWriter
	status *atomic.Int32
}

func (w *cliRedStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *cliRedStatusWriter) Flush()                      { w.ResponseWriter.(http.Flusher).Flush() }
func (w *cliRedStatusWriter) WriteHeader(code int) {
	w.status.Store(int32(code))
	w.ResponseWriter.WriteHeader(code)
}
func (w *cliRedStatusWriter) Write(p []byte) (int, error) {
	if w.status.Load() == 0 {
		w.status.Store(200)
	}
	return w.ResponseWriter.Write(p)
}
