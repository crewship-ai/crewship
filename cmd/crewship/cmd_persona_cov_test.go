package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const (
	covAgentIDCli5 = "cagent0000000000000000a"
	covCrewIDCli5  = "ccrew00000000000000000a"
)

// covCmdOut attaches a buffer to a Cobra command's output stream and
// restores stdout binding at test end.
func covCmdOut(t *testing.T, cmd *cobra.Command) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	t.Cleanup(func() { cmd.SetOut(nil) })
	return &buf
}

// covStubAgents registers the agents list used by resolveAgentID.
func covStubAgents(stub *clitest.StubServer) {
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200,
		[]map[string]string{{"id": covAgentIDCli5, "slug": "viktor"}}))
}

// covStubCrews registers the crews list used by resolveCrewID.
func covStubCrews(stub *clitest.StubServer) {
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200,
		[]map[string]string{{"id": covCrewIDCli5, "slug": "backend"}}))
}

// covEditorScript writes an executable shell script that overwrites the
// edited file with the given content, and points $EDITOR at it.
func covEditorScript(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "editor.sh")
	body := "#!/bin/sh\nprintf '%s' '" + content + "' > \"$1\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write editor script: %v", err)
	}
	t.Setenv("EDITOR", script)
}

// ─── printPersona ────────────────────────────────────────────────────────

func TestPrintPersona(t *testing.T) {
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	printPersona(cmd, "agent", personaResponse{
		Layer: "agent", Content: "You write Go.", Bytes: 13, CapBytes: 1536,
	})
	out := buf.String()
	if !strings.Contains(out, "agent persona (source: agent, 13/1536 bytes)") {
		t.Errorf("header wrong; got:\n%s", out)
	}
	if !strings.Contains(out, "You write Go.") {
		t.Errorf("content missing; got:\n%s", out)
	}

	buf.Reset()
	printPersona(cmd, "crew", personaResponse{
		Layer: "crew", FromDefault: true, Content: "synth", Bytes: 5, CapBytes: 1536,
	})
	if !strings.Contains(buf.String(), "source: synthesized default") {
		t.Errorf("from_default should render as synthesized default; got:\n%s", buf.String())
	}
}

// ─── putJSON ─────────────────────────────────────────────────────────────

func TestPutJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPut("/api/v1/thing", clitest.JSONResponse(200, map[string]any{"ok": true}))
	client := newAPIClient()

	var out map[string]any
	if err := putJSON(client, "/api/v1/thing", map[string]string{"a": "b"}, &out); err != nil {
		t.Fatalf("putJSON: %v", err)
	}
	if out["ok"] != true {
		t.Errorf("decoded out = %v", out)
	}
	calls := stub.CallsFor("PUT", "/api/v1/thing")
	if len(calls) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["a"] != "b" {
		t.Errorf("PUT body = %v", body)
	}

	// nil out → body closed without decode.
	if err := putJSON(client, "/api/v1/thing", nil, nil); err != nil {
		t.Errorf("putJSON nil out: %v", err)
	}
}

func TestPutJSON_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPut("/api/v1/thing", clitest.ErrorResponse(422, "too big"))
	client := newAPIClient()

	err := putJSON(client, "/api/v1/thing", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "too big") {
		t.Errorf("expected 422 error; got %v", err)
	}
}

// ─── requireAPIClientWithWorkspace ───────────────────────────────────────

func TestRequireAPIClientWithWorkspace(t *testing.T) {
	covSetupCli5(t)
	c, err := requireAPIClientWithWorkspace()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected a client")
	}
}

// ─── openInEditor ────────────────────────────────────────────────────────

func TestOpenInEditor_NoopEditorReturnsSeed(t *testing.T) {
	t.Setenv("EDITOR", "/usr/bin/true")
	got, err := openInEditor("seed content", ".md")
	if err != nil {
		t.Fatalf("openInEditor: %v", err)
	}
	if got != "seed content" {
		t.Errorf("got %q, want seed unchanged", got)
	}
}

func TestOpenInEditor_EditorWithArgs(t *testing.T) {
	// $EDITOR with flags must be tokenised, not exec'd as one path.
	t.Setenv("EDITOR", "/usr/bin/true --wait")
	got, err := openInEditor("x", ".md")
	if err != nil {
		t.Fatalf("openInEditor with args: %v", err)
	}
	if got != "x" {
		t.Errorf("got %q, want x", got)
	}
}

func TestOpenInEditor_EditorRewritesFile(t *testing.T) {
	covEditorScript(t, "rewritten")
	got, err := openInEditor("old", ".md")
	if err != nil {
		t.Fatalf("openInEditor: %v", err)
	}
	if got != "rewritten" {
		t.Errorf("got %q, want rewritten", got)
	}
}

func TestOpenInEditor_EditorFails(t *testing.T) {
	t.Setenv("EDITOR", filepath.Join(t.TempDir(), "definitely-not-here"))
	_, err := openInEditor("", ".md")
	if err == nil || !strings.Contains(err.Error(), "editor failed") {
		t.Errorf("expected editor-failed error; got %v", err)
	}
}

// ─── persona view ────────────────────────────────────────────────────────

func TestPersonaViewRunE(t *testing.T) {
	stub := covSetupCli5(t)
	covStubAgents(stub)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli5+"/persona", clitest.JSONResponse(200, personaResponse{
		AgentID: covAgentIDCli5, Layer: "agent", Content: "Persona body", Bytes: 12, CapBytes: 1536,
	}))
	buf := covCmdOut(t, personaViewCmd)

	if err := personaViewCmd.RunE(personaViewCmd, []string{"viktor"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(buf.String(), "Persona body") {
		t.Errorf("output missing persona content; got:\n%s", buf.String())
	}
}

func TestPersonaViewRunE_UnknownAgent(t *testing.T) {
	stub := covSetupCli5(t)
	covStubAgents(stub)

	err := personaViewCmd.RunE(personaViewCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "agent not found") {
		t.Errorf("expected agent-not-found; got %v", err)
	}
}

// ─── persona edit ────────────────────────────────────────────────────────

func TestPersonaEditRunE_Success(t *testing.T) {
	stub := covSetupCli5(t)
	covStubAgents(stub)
	personaPath := "/api/v1/agents/" + covAgentIDCli5 + "/persona"
	stub.OnGet(personaPath, clitest.JSONResponse(200, personaResponse{
		Layer: "agent", FromDefault: false, Content: "old persona",
	}))
	stub.OnPut(personaPath, clitest.JSONResponse(200, map[string]any{"bytes": 11}))
	covEditorScript(t, "new persona")
	buf := covCmdOut(t, personaEditCmd)

	if err := personaEditCmd.RunE(personaEditCmd, []string{"viktor"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PUT", personaPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["content"] != "new persona" {
		t.Errorf("PUT content = %q, want %q", body["content"], "new persona")
	}
	if !strings.Contains(buf.String(), "persona updated") {
		t.Errorf("missing confirmation; got:\n%s", buf.String())
	}
}

func TestPersonaEditRunE_AbortOnEmpty(t *testing.T) {
	stub := covSetupCli5(t)
	covStubAgents(stub)
	// FromDefault → blank editor seed; /usr/bin/true leaves it blank.
	stub.OnGet("/api/v1/agents/"+covAgentIDCli5+"/persona", clitest.JSONResponse(200, personaResponse{
		Layer: "agent", FromDefault: true, Content: "synthesized — should not seed",
	}))
	t.Setenv("EDITOR", "/usr/bin/true")

	err := personaEditCmd.RunE(personaEditCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "aborted (empty content)") {
		t.Errorf("expected abort; got %v", err)
	}
	if calls := stub.CallsFor("PUT", "/api/v1/agents/"+covAgentIDCli5+"/persona"); len(calls) != 0 {
		t.Errorf("aborted edit must not PUT; got %d calls", len(calls))
	}
}

// ─── persona reset / history / suggest ──────────────────────────────────

func TestPersonaResetRunE(t *testing.T) {
	stub := covSetupCli5(t)
	covStubAgents(stub)
	personaPath := "/api/v1/agents/" + covAgentIDCli5 + "/persona"
	stub.OnDelete(personaPath, clitest.EmptyResponse(204))
	buf := covCmdOut(t, personaResetCmd)

	if err := personaResetCmd.RunE(personaResetCmd, []string{"viktor"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("DELETE", personaPath); len(calls) != 1 {
		t.Errorf("expected 1 DELETE, got %d", len(calls))
	}
	if !strings.Contains(buf.String(), "persona reset") {
		t.Errorf("missing confirmation; got:\n%s", buf.String())
	}
}

func TestPersonaHistoryRunE(t *testing.T) {
	stub := covSetupCli5(t)
	covStubAgents(stub)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli5+"/persona/history", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{{
			"id": "v1", "sha256": "abcdef0123456789ffff", "bytes": 42,
			"written_at": "2026-06-12T10:00:00Z", "written_by": "operator",
		}},
	}))
	buf := covCmdOut(t, personaHistoryCmd)

	if err := personaHistoryCmd.RunE(personaHistoryCmd, []string{"viktor"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "abcdef012345") || !strings.Contains(out, "by operator") {
		t.Errorf("history row malformed; got:\n%s", out)
	}
}

func TestPersonaHistoryRunE_Empty(t *testing.T) {
	stub := covSetupCli5(t)
	covStubAgents(stub)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli5+"/persona/history",
		clitest.JSONResponse(200, map[string]any{"entries": []map[string]any{}}))
	buf := covCmdOut(t, personaHistoryCmd)

	if err := personaHistoryCmd.RunE(personaHistoryCmd, []string{"viktor"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(buf.String(), "(no history)") {
		t.Errorf("expected (no history); got:\n%s", buf.String())
	}
}

func TestPersonaSuggestFromInboxRunE(t *testing.T) {
	covSetupCli5(t)
	buf := covCmdOut(t, personaSuggestFromInboxCmd)

	if err := personaSuggestFromInboxCmd.RunE(personaSuggestFromInboxCmd, []string{"audit_1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(buf.String(), "Phase 2") {
		t.Errorf("expected Phase 2 hint; got:\n%s", buf.String())
	}
}

// ─── persona crew ────────────────────────────────────────────────────────

func TestPersonaCrewRunE_View(t *testing.T) {
	stub := covSetupCli5(t)
	covStubCrews(stub)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/persona", clitest.JSONResponse(200, personaResponse{
		CrewID: covCrewIDCli5, Layer: "crew", Content: "Crew voice", Bytes: 10, CapBytes: 1536,
	}))
	buf := covCmdOut(t, personaCrewCmd)

	if err := personaCrewCmd.RunE(personaCrewCmd, []string{"backend", "view"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(buf.String(), "Crew voice") {
		t.Errorf("output missing crew persona; got:\n%s", buf.String())
	}
}

func TestPersonaCrewRunE_Edit(t *testing.T) {
	stub := covSetupCli5(t)
	covStubCrews(stub)
	personaPath := "/api/v1/crews/" + covCrewIDCli5 + "/persona"
	stub.OnGet(personaPath, clitest.JSONResponse(200, personaResponse{Content: "old crew"}))
	stub.OnPut(personaPath, clitest.JSONResponse(200, map[string]any{"ok": true}))
	covEditorScript(t, "new crew persona")
	buf := covCmdOut(t, personaCrewCmd)

	if err := personaCrewCmd.RunE(personaCrewCmd, []string{"backend", "edit"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PUT", personaPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["content"] != "new crew persona" {
		t.Errorf("PUT content = %q", body["content"])
	}
	if !strings.Contains(buf.String(), "crew persona updated") {
		t.Errorf("missing confirmation; got:\n%s", buf.String())
	}
}

func TestPersonaCrewRunE_Reset(t *testing.T) {
	stub := covSetupCli5(t)
	covStubCrews(stub)
	personaPath := "/api/v1/crews/" + covCrewIDCli5 + "/persona"
	stub.OnDelete(personaPath, clitest.EmptyResponse(204))
	buf := covCmdOut(t, personaCrewCmd)

	if err := personaCrewCmd.RunE(personaCrewCmd, []string{"backend", "reset"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("DELETE", personaPath); len(calls) != 1 {
		t.Errorf("expected 1 DELETE, got %d", len(calls))
	}
	if !strings.Contains(buf.String(), "crew persona reset") {
		t.Errorf("missing confirmation; got:\n%s", buf.String())
	}
}

func TestPersonaCrewRunE_UnknownSubcommand(t *testing.T) {
	stub := covSetupCli5(t)
	covStubCrews(stub)

	err := personaCrewCmd.RunE(personaCrewCmd, []string{"backend", "bogus"})
	if err == nil || !strings.Contains(err.Error(), `unknown subcommand "bogus"`) {
		t.Errorf("expected unknown-subcommand error; got %v", err)
	}
}

func TestPersonaCrewRunE_TooFewArgs(t *testing.T) {
	covSetupCli5(t)
	err := personaCrewCmd.RunE(personaCrewCmd, []string{"backend"})
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("expected usage error; got %v", err)
	}
}

// Sanity-check no-auth on a persona command: the shared client constructor
// does not gate on auth, so the resolve call must surface a clean API error.
func TestPersonaViewRunE_ServerError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/agents", clitest.ErrorResponse(401, "Unauthorized"))

	err := personaViewCmd.RunE(personaViewCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("expected 401 to bubble; got %v", err)
	}
}

// ─── error branches round 2 ──────────────────────────────────────────────

func TestPersonaViewRunE_PersonaFetchError(t *testing.T) {
	stub := covSetupCli5(t)
	covStubAgents(stub)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli5+"/persona", clitest.ErrorResponse(500, "persona wedged"))

	err := personaViewCmd.RunE(personaViewCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "persona wedged") {
		t.Errorf("expected fetch error; got %v", err)
	}
}

func TestPersonaEditRunE_ResolveAndFetchErrors(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "agents down"))
	err := personaEditCmd.RunE(personaEditCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "agents down") {
		t.Errorf("expected resolve error; got %v", err)
	}

	stub.Reset()
	covStubAgents(stub)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli5+"/persona", clitest.ErrorResponse(404, "agent gone"))
	err = personaEditCmd.RunE(personaEditCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "agent gone") {
		t.Errorf("expected seed-fetch error; got %v", err)
	}
}

func TestPersonaEditRunE_PutError(t *testing.T) {
	stub := covSetupCli5(t)
	covStubAgents(stub)
	personaPath := "/api/v1/agents/" + covAgentIDCli5 + "/persona"
	stub.OnGet(personaPath, clitest.JSONResponse(200, personaResponse{Content: "old"}))
	stub.OnPut(personaPath, clitest.ErrorResponse(422, "persona too large"))
	covEditorScript(t, "way too big")

	err := personaEditCmd.RunE(personaEditCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "persona too large") {
		t.Errorf("expected PUT error; got %v", err)
	}
}

func TestPersonaResetRunE_DeleteError(t *testing.T) {
	stub := covSetupCli5(t)
	covStubAgents(stub)
	stub.OnDelete("/api/v1/agents/"+covAgentIDCli5+"/persona", clitest.ErrorResponse(500, "delete failed"))

	err := personaResetCmd.RunE(personaResetCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "delete failed") {
		t.Errorf("expected delete error; got %v", err)
	}
}

func TestPersonaHistoryRunE_FetchError(t *testing.T) {
	stub := covSetupCli5(t)
	covStubAgents(stub)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli5+"/persona/history", clitest.ErrorResponse(500, "history wedged"))

	err := personaHistoryCmd.RunE(personaHistoryCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "history wedged") {
		t.Errorf("expected history error; got %v", err)
	}
}

func TestPersonaCrewRunE_ResolveError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	err := personaCrewCmd.RunE(personaCrewCmd, []string{"ghost", "view"})
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found; got %v", err)
	}
}

func TestPersonaCrewRunE_ViewFetchError(t *testing.T) {
	stub := covSetupCli5(t)
	covStubCrews(stub)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/persona", clitest.ErrorResponse(500, "crew persona wedged"))

	err := personaCrewCmd.RunE(personaCrewCmd, []string{"backend", "view"})
	if err == nil || !strings.Contains(err.Error(), "crew persona wedged") {
		t.Errorf("expected view error; got %v", err)
	}
}

func TestPersonaCrewRunE_EditErrors(t *testing.T) {
	stub := covSetupCli5(t)
	covStubCrews(stub)
	personaPath := "/api/v1/crews/" + covCrewIDCli5 + "/persona"

	// Seed fetch fails.
	stub.OnGet(personaPath, clitest.ErrorResponse(500, "seed gone"))
	err := personaCrewCmd.RunE(personaCrewCmd, []string{"backend", "edit"})
	if err == nil || !strings.Contains(err.Error(), "seed gone") {
		t.Errorf("expected seed error; got %v", err)
	}

	// Editor fails.
	stub.OnGet(personaPath, clitest.JSONResponse(200, personaResponse{Content: "x"}))
	t.Setenv("EDITOR", filepath.Join(t.TempDir(), "ghost-editor"))
	err = personaCrewCmd.RunE(personaCrewCmd, []string{"backend", "edit"})
	if err == nil || !strings.Contains(err.Error(), "editor failed") {
		t.Errorf("expected editor error; got %v", err)
	}

	// Empty edit aborts before PUT.
	stub.OnGet(personaPath, clitest.JSONResponse(200, personaResponse{Content: ""}))
	t.Setenv("EDITOR", "/usr/bin/true")
	err = personaCrewCmd.RunE(personaCrewCmd, []string{"backend", "edit"})
	if err == nil || !strings.Contains(err.Error(), "aborted (empty content)") {
		t.Errorf("expected abort; got %v", err)
	}

	// PUT fails.
	stub.OnGet(personaPath, clitest.JSONResponse(200, personaResponse{Content: "x"}))
	stub.OnPut(personaPath, clitest.ErrorResponse(500, "crew put failed"))
	covEditorScript(t, "fresh")
	err = personaCrewCmd.RunE(personaCrewCmd, []string{"backend", "edit"})
	if err == nil || !strings.Contains(err.Error(), "crew put failed") {
		t.Errorf("expected PUT error; got %v", err)
	}
}

func TestPersonaCrewRunE_ResetError(t *testing.T) {
	stub := covSetupCli5(t)
	covStubCrews(stub)
	stub.OnDelete("/api/v1/crews/"+covCrewIDCli5+"/persona", clitest.ErrorResponse(500, "reset failed"))

	err := personaCrewCmd.RunE(personaCrewCmd, []string{"backend", "reset"})
	if err == nil || !strings.Contains(err.Error(), "reset failed") {
		t.Errorf("expected reset error; got %v", err)
	}
}

func TestPersonaEditRunE_EditorFails(t *testing.T) {
	stub := covSetupCli5(t)
	covStubAgents(stub)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli5+"/persona",
		clitest.JSONResponse(200, personaResponse{Content: "x"}))
	t.Setenv("EDITOR", filepath.Join(t.TempDir(), "no-such-editor"))

	err := personaEditCmd.RunE(personaEditCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "editor failed") {
		t.Errorf("expected editor error; got %v", err)
	}
}

func TestPersonaResetRunE_ResolveError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "agents down"))

	err := personaResetCmd.RunE(personaResetCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "agents down") {
		t.Errorf("expected resolve error; got %v", err)
	}
}

func TestPersonaHistoryRunE_ResolveError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "agents down"))

	err := personaHistoryCmd.RunE(personaHistoryCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "agents down") {
		t.Errorf("expected resolve error; got %v", err)
	}
}

func TestPutJSON_TransportError(t *testing.T) {
	stub := covSetupCli5(t)
	client := newAPIClient()
	stub.Close()

	err := putJSON(client, "/api/v1/thing", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Errorf("expected transport error; got %v", err)
	}
}

func TestOpenInEditor_TempDirUnavailable(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv("EDITOR", "/usr/bin/true")

	_, err := openInEditor("seed", ".md")
	if err == nil || !strings.Contains(err.Error(), "create temp") {
		t.Errorf("expected create-temp error; got %v", err)
	}
}

func TestOpenInEditor_EditorDeletesFile(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "deleter.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nrm -f \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script)

	_, err := openInEditor("seed", ".md")
	if err == nil {
		t.Fatal("expected error when editor removes the temp file")
	}
}
