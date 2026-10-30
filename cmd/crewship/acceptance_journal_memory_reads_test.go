package main

// Acceptance for the journal and memory READ commands that had no test
// driving the built binary (Swarm 2 CLI matrix, part of #4519):
//
//	crewship journal lookup            GET  /api/v1/journal/lookup
//	crewship journal get <id>          GET  /api/v1/journal/{id}
//	crewship journal priority <id>     POST /api/v1/journal/{id}/priority
//	crewship journal --follow          GET  /api/v1/journal/stream
//	crewship memory health             GET  /api/v1/memory/health
//	crewship memory hybrid <query>     POST /api/v1/memory/search/hybrid
//	crewship memory export             GET  /api/v1/memory/export
//	crewship memory versions show      GET  /api/v1/memory/versions/{sha}
//
// A real CLI process → the real api.Router → a migrated SQLite, a real
// storage root and a real content-addressed blob root. Each command is
// exercised on its success path, its machine format, a miss that must read
// as a sentence, and a caller the server must refuse: a token whose user
// is not in the workspace at all, and — where the route is OWNER/ADMIN
// only — a plain MEMBER.
//
// Memory import and versions restore are deliberately out of scope: both
// write storage and belong with their own review.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const (
	jmrWorkspaceID = "cjmreadsws000000001a"
	jmrOtherWSID   = "cjmreadsws000000002b"
	jmrOwnerToken  = "crewship_cli_jmrowner0000000000000000000"
	jmrMemberToken = "crewship_cli_jmrmember000000000000000000"
	jmrStrangerTok = "crewship_cli_jmrstranger0000000000000000"
	jmrEntryID     = "jmr-entry-deploy"
	jmrVersionPath = "crew:jmr-crew/CREW.md"
	jmrVersionBody = "# Backend\nDeploys go out on Tuesdays.\n"
)

type jmrEnv struct {
	binary     string
	serverURL  string
	versionSha string
	cfgDir     string
}

// startJournalMemoryReadsServer seeds one workspace with an OWNER and a
// MEMBER, a second workspace holding a STRANGER (so their token is valid
// but carries no right to the first), one crew + agent, one journal entry,
// one crew-shared memory file on disk and one recorded memory version.
func startJournalMemoryReadsServer(t *testing.T) *jmrEnv {
	t.Helper()
	db := testutil.MigratedDB(t).DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Reads', 'jmr-reads')`, jmrWorkspaceID)
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Elsewhere', 'jmr-elsewhere')`, jmrOtherWSID)
	for _, u := range []struct{ id, email, ws, role, token string }{
		{"jmr-owner", "owner@jmr.invalid", jmrWorkspaceID, "OWNER", jmrOwnerToken},
		{"jmr-member", "member@jmr.invalid", jmrWorkspaceID, "MEMBER", jmrMemberToken},
		{"jmr-stranger", "stranger@jmr.invalid", jmrOtherWSID, "OWNER", jmrStrangerTok},
	} {
		mustExec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, u.id, u.email, u.id)
		mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, ?)`,
			"m-"+u.id, u.ws, u.id, u.role)
		mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, 't', ?, datetime('now'))`,
			"t-"+u.id, u.id, sha256HexToken(u.token))
	}
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
		VALUES ('jmr-crew', ?, 'Backend', 'backend', 'free', 4096, 2.0)`, jmrWorkspaceID)
	mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('jmr-agent', ?, 'jmr-crew', 'Viktor', 'viktor', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`, jmrWorkspaceID)
	// The member is on the crew roster so the CLI can resolve the crew and
	// the refusal under test is the server's role gate, not a lookup miss.
	mustExec(`INSERT INTO crew_members (id, crew_id, user_id) VALUES ('jmr-cm-member', 'jmr-crew', 'jmr-member')`)
	mustExec(`INSERT INTO journal_entries
			(id, workspace_id, crew_id, agent_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs)
		VALUES (?, ?, 'jmr-crew', 'jmr-agent', '2026-09-15T10:00:00.000Z', 'agent.status', 'info', 'normal', 'agent', 'jmr-agent',
			'JMR-ROW Viktor finished the backend deploy', '{"build":"914ae2f"}', '{}')`, jmrEntryID, jmrWorkspaceID)

	storageRoot := t.TempDir()
	crewMem := filepath.Join(storageRoot, "crews", "jmr-crew", "shared", ".memory", "CREW.md")
	if err := os.MkdirAll(filepath.Dir(crewMem), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(crewMem, []byte(jmrVersionBody), 0o644); err != nil {
		t.Fatal(err)
	}
	blobRoot := t.TempDir()
	rec, err := memory.RecordVersion(context.Background(), db, memory.VersionRecord{
		WorkspaceID: jmrWorkspaceID, Path: jmrVersionPath, Tier: memory.Tier("crew"),
		Content: []byte(jmrVersionBody), WrittenBy: "jmr-owner", BlobRoot: blobRoot,
	})
	if err != nil {
		t.Fatalf("record memory version: %v", err)
	}

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger,
		api.WithOutputBasePath(storageRoot), api.WithMemoryVersionsBlobRoot(blobRoot))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &jmrEnv{binary: buildCrewshipBinary(t), serverURL: srv.URL, versionSha: rec.Sha256, cfgDir: t.TempDir()}
}

// config writes a CLI config for one caller and output format. The
// workspace is always the seeded one, including for the stranger: their
// token is genuine, the workspace is simply not theirs.
func (e *jmrEnv) config(t *testing.T, token, format string) string {
	t.Helper()
	p := filepath.Join(e.cfgDir, strings.TrimPrefix(token, "crewship_cli_")+"-"+format+".yaml")
	body := "server: " + e.serverURL + "\nworkspace: " + jmrWorkspaceID + "\ntoken: " + token + "\nformat: " + format + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *jmrEnv) command(cfg string, args ...string) *exec.Cmd {
	cmd := exec.Command(e.binary, args...)
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	return cmd
}

func (e *jmrEnv) run(cfg string, args ...string) (string, error) {
	out, err := e.command(cfg, args...).CombinedOutput()
	return string(out), err
}

func (e *jmrEnv) must(t *testing.T, cfg string, args ...string) string {
	t.Helper()
	out, err := e.run(cfg, args...)
	if err != nil {
		t.Fatalf("CLI %v: %v\n%s", args, err, out)
	}
	return out
}

// refuse asserts a non-zero exit whose output carries want.
func (e *jmrEnv) refuse(t *testing.T, label, cfg, want string, args ...string) {
	t.Helper()
	out, err := e.run(cfg, args...)
	if err == nil {
		t.Errorf("%s: %v exited 0, want a refusal\n%s", label, args, out)
		return
	}
	t.Logf("%s: %v → %s", label, args, strings.TrimSpace(out))
	if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
		t.Errorf("%s: %v output lacks %q:\n%s", label, args, want, out)
	}
	if strings.Contains(out, "goroutine ") || strings.Contains(out, "panic:") {
		t.Errorf("%s: %v printed a stack, not a sentence:\n%s", label, args, out)
	}
}

func decodeJSONObject(t *testing.T, label, out string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%s: output is not one JSON object: %v\n%s", label, err, out)
	}
	return v
}

func TestAcceptanceJournalMemoryReads_JournalLookup(t *testing.T) {
	e := startJournalMemoryReadsServer(t)

	table := e.must(t, e.config(t, jmrOwnerToken, "table"), "journal", "lookup")
	for _, want := range []string{"Crews (1)", "jmr-crew", "backend", "Agents (1)", "jmr-agent", "Viktor"} {
		if !strings.Contains(table, want) {
			t.Errorf("lookup table lacks %q:\n%s", want, table)
		}
	}

	body := decodeJSONObject(t, "lookup json", e.must(t, e.config(t, jmrOwnerToken, "json"), "journal", "lookup"))
	crews, _ := body["crews"].([]any)
	if len(crews) != 1 || crews[0].(map[string]any)["slug"] != "backend" {
		t.Errorf("lookup json crews = %v", body["crews"])
	}
	agents, _ := body["agents"].([]any)
	if len(agents) != 1 || agents[0].(map[string]any)["crew_id"] != "jmr-crew" {
		t.Errorf("lookup json agents = %v", body["agents"])
	}

	e.refuse(t, "stranger", e.config(t, jmrStrangerTok, "table"), "workspace", "journal", "lookup")
}

func TestAcceptanceJournalMemoryReads_JournalGet(t *testing.T) {
	e := startJournalMemoryReadsServer(t)
	owner := e.config(t, jmrOwnerToken, "table")

	text := e.must(t, owner, "journal", "get", jmrEntryID)
	for _, want := range []string{"JMR-ROW Viktor finished the backend deploy", "agent.status", "payload", "914ae2f"} {
		if !strings.Contains(text, want) {
			t.Errorf("journal get lacks %q:\n%s", want, text)
		}
	}

	entry := decodeJSONObject(t, "get json", e.must(t, e.config(t, jmrOwnerToken, "json"), "journal", "get", jmrEntryID))
	if entry["id"] != jmrEntryID || entry["entry_type"] != "agent.status" {
		t.Errorf("journal get json = %v", entry)
	}

	e.refuse(t, "unknown id", owner, "not found", "journal", "get", "jmr-no-such-entry")
	e.refuse(t, "stranger", e.config(t, jmrStrangerTok, "table"), "workspace", "journal", "get", jmrEntryID)
}

func TestAcceptanceJournalMemoryReads_JournalPriority(t *testing.T) {
	e := startJournalMemoryReadsServer(t)
	owner := e.config(t, jmrOwnerToken, "table")

	out := e.must(t, owner, "journal", "priority", jmrEntryID, "--mark", "pin", "--reason", "team playbook")
	if !strings.Contains(out, "Entry "+jmrEntryID+": priority normal → pin") {
		t.Errorf("priority text output = %q", out)
	}

	// --format json must be the server's answer, not the prose line.
	res := decodeJSONObject(t, "priority json",
		e.must(t, e.config(t, jmrOwnerToken, "json"), "journal", "priority", jmrEntryID, "--mark", "high"))
	if res["id"] != jmrEntryID || res["priority"] != "high" || res["previous"] != "pin" {
		t.Errorf("priority json = %v", res)
	}

	// The change is real: `journal get` now reports it.
	got := decodeJSONObject(t, "get after priority", e.must(t, e.config(t, jmrOwnerToken, "json"), "journal", "get", jmrEntryID))
	if got["priority"] != "high" {
		t.Errorf("priority after mark = %v, want high", got["priority"])
	}

	e.refuse(t, "bad mark", owner, "invalid --mark", "journal", "priority", jmrEntryID, "--mark", "urgent")
	e.refuse(t, "unknown id", owner, "not found", "journal", "priority", "jmr-no-such-entry", "--mark", "pin")
	e.refuse(t, "member", e.config(t, jmrMemberToken, "table"), "403", "journal", "priority", jmrEntryID, "--mark", "pin")
	e.refuse(t, "stranger", e.config(t, jmrStrangerTok, "table"), "workspace", "journal", "priority", jmrEntryID, "--mark", "pin")
}

// TestAcceptanceJournalMemoryReads_JournalFollow drives the SSE tail. The
// stream never ends on its own, so the test waits — bounded — for the
// seeded entry to be replayed, then interrupts the process the way a
// person does (Ctrl-C) and expects a clean exit.
func TestAcceptanceJournalMemoryReads_JournalFollow(t *testing.T) {
	e := startJournalMemoryReadsServer(t)

	cmd := e.command(e.config(t, jmrOwnerToken, "table"), "journal", "--follow")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	seen := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "JMR-ROW") {
				seen <- sc.Text()
				break
			}
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	select {
	case line := <-seen:
		if !strings.Contains(line, "agent.status") {
			t.Errorf("follow line does not render like the list view: %q", line)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("journal --follow did not replay the seeded entry within 20s")
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("journal --follow after Ctrl-C exited with %v, want 0", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("journal --follow did not exit within 10s of Ctrl-C")
	}

	// A refused stream is permanent: the CLI must stop, not reconnect forever.
	strangerCmd := e.command(e.config(t, jmrStrangerTok, "table"), "journal", "--follow")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	strangerCmd = exec.CommandContext(ctx, strangerCmd.Path, strangerCmd.Args[1:]...)
	strangerCmd.Env = e.command(e.config(t, jmrStrangerTok, "table")).Env
	out, err := strangerCmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("stranger's journal --follow kept retrying instead of stopping:\n%s", out)
	}
	if err == nil {
		t.Errorf("stranger's journal --follow exited 0:\n%s", out)
	}
}

func TestAcceptanceJournalMemoryReads_MemoryHealth(t *testing.T) {
	e := startJournalMemoryReadsServer(t)

	text := e.must(t, e.config(t, jmrOwnerToken, "table"), "memory", "health")
	if !strings.Contains(text, "Memory health (workspace-wide):") || !strings.Contains(text, "/100") {
		t.Errorf("memory health text = %q", text)
	}

	body := decodeJSONObject(t, "health json", e.must(t, e.config(t, jmrOwnerToken, "json"), "memory", "health", "--crew", "backend"))
	if body["crew_id"] != "jmr-crew" {
		t.Errorf("memory health --crew backend scoped to %v, want jmr-crew", body["crew_id"])
	}
	if _, ok := body["overall"].(float64); !ok {
		t.Errorf("memory health json has no numeric overall: %v", body)
	}
	if _, ok := body["metrics"].(map[string]any); !ok {
		t.Errorf("memory health json has no metrics object: %v", body)
	}

	// Members read health: it is aggregate counts, no content.
	e.must(t, e.config(t, jmrMemberToken, "json"), "memory", "health")

	e.refuse(t, "unknown crew", e.config(t, jmrOwnerToken, "table"), "crew not found", "memory", "health", "--crew", "nowhere")
	e.refuse(t, "stranger", e.config(t, jmrStrangerTok, "table"), "workspace", "memory", "health")
}

func TestAcceptanceJournalMemoryReads_MemoryHybrid(t *testing.T) {
	e := startJournalMemoryReadsServer(t)

	// No FTS engine and no embedder are wired, so the honest answer is an
	// empty result — the contract under test is the envelope and the
	// flags reaching the route, not ranking.
	table := e.must(t, e.config(t, jmrOwnerToken, "table"), "memory", "hybrid", "deploy runbook")
	if !strings.Contains(table, "no results") && !strings.Contains(table, "SOURCE") {
		t.Errorf("memory hybrid table = %q", table)
	}
	body := decodeJSONObject(t, "hybrid json", e.must(t, e.config(t, jmrOwnerToken, "json"), "memory", "hybrid", "deploy runbook", "--limit", "3"))
	if body["query"] != "deploy runbook" {
		t.Errorf("hybrid json query = %v", body["query"])
	}
	if _, ok := body["count"].(float64); !ok {
		t.Errorf("hybrid json has no count: %v", body)
	}

	owner := e.config(t, jmrOwnerToken, "table")
	e.refuse(t, "bad scope", owner, "invalid scope", "memory", "hybrid", "x", "--scope", "everything")
	e.refuse(t, "unknown crew", owner, "crew not found", "memory", "hybrid", "x", "--scope", "crew_shared", "--crew", "nowhere")
	// The owner runs the workspace but is not on the crew roster: the
	// crew's shared episodic memory stays closed (#1049).
	e.refuse(t, "not on crew", owner, "not a member of the requested crew", "memory", "hybrid", "x", "--scope", "crew_shared", "--crew", "backend")
	e.refuse(t, "stranger", e.config(t, jmrStrangerTok, "table"), "workspace", "memory", "hybrid", "x")
}

func TestAcceptanceJournalMemoryReads_MemoryExport(t *testing.T) {
	e := startJournalMemoryReadsServer(t)
	owner := e.config(t, jmrOwnerToken, "table")

	outDir := filepath.Join(t.TempDir(), "bundle")
	text := e.must(t, owner, "memory", "export", "--crew", "backend", "--out", outDir)
	if !strings.Contains(text, "exported 1 documents to "+outDir) || !strings.Contains(text, "CREW.md") {
		t.Errorf("memory export text = %q", text)
	}
	written, err := os.ReadFile(filepath.Join(outDir, "CREW.md"))
	if err != nil {
		entries, _ := os.ReadDir(outDir)
		t.Fatalf("bundle lacks CREW.md (%v); has %v", err, entries)
	}
	if !strings.Contains(string(written), "Deploys go out on Tuesdays.") {
		t.Errorf("exported CREW.md lost its body:\n%s", written)
	}

	jsonDir := filepath.Join(t.TempDir(), "bundle-json")
	res := decodeJSONObject(t, "export json", e.must(t, e.config(t, jmrOwnerToken, "json"), "memory", "export", "--crew", "backend", "--out", jsonDir))
	if res["out"] != jsonDir {
		t.Errorf("export json out = %v, want %s", res["out"], jsonDir)
	}
	docs, _ := res["documents"].([]any)
	if len(docs) != 1 || docs[0] != "CREW.md" {
		t.Errorf("export json documents = %v", res["documents"])
	}
	if _, err := os.Stat(filepath.Join(jsonDir, "CREW.md")); err != nil {
		t.Errorf("--format json still has to write the bundle: %v", err)
	}

	e.refuse(t, "no --out", owner, "--out is required", "memory", "export", "--crew", "backend")
	e.refuse(t, "unknown crew", owner, "crew not found", "memory", "export", "--crew", "nowhere", "--out", t.TempDir())
	e.refuse(t, "empty agent scope", owner, "nothing to export", "memory", "export", "--crew", "backend", "--agent", "viktor", "--out", t.TempDir())
	e.refuse(t, "member", e.config(t, jmrMemberToken, "table"), "403", "memory", "export", "--crew", "backend", "--out", t.TempDir())
	e.refuse(t, "stranger", e.config(t, jmrStrangerTok, "table"), "workspace", "memory", "export", "--crew", "jmr-crew", "--out", t.TempDir())
}

func TestAcceptanceJournalMemoryReads_MemoryVersionsShow(t *testing.T) {
	e := startJournalMemoryReadsServer(t)
	owner := e.config(t, jmrOwnerToken, "table")

	// Raw bytes on stdout, whatever the format: the command exists to be piped.
	for _, cfg := range []string{owner, e.config(t, jmrOwnerToken, "json")} {
		cmd := e.command(cfg, "memory", "versions", "show", jmrVersionPath, e.versionSha)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("versions show: %v\n%s", err, out)
		}
		if string(out) != jmrVersionBody {
			t.Errorf("versions show stdout = %q, want the recorded bytes", out)
		}
	}

	// A member reads the audit chain too.
	if out := e.must(t, e.config(t, jmrMemberToken, "table"), "memory", "versions", "show", jmrVersionPath, e.versionSha); out != jmrVersionBody {
		t.Errorf("member versions show = %q", out)
	}

	e.refuse(t, "unknown sha", owner, "memory version not found", "memory", "versions", "show", jmrVersionPath, strings.Repeat("0", 64))
	e.refuse(t, "wrong path", owner, "memory version not found", "memory", "versions", "show", "crew:jmr-crew/OTHER.md", e.versionSha)
	e.refuse(t, "stranger", e.config(t, jmrStrangerTok, "table"), "workspace", "memory", "versions", "show", jmrVersionPath, e.versionSha)
}
