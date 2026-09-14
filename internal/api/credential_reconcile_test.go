package api

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// #814 — revoking a file-based credential must remove its /secrets file(s)
// from running crew containers, exec'd as UID 1001.

// recordingCtr captures every ExecConfig passed to Exec (reusing
// mockContainerExec from keeper_execute_test.go for the rest of the interface).
type recordingCtr struct {
	*mockContainerExec
	calls *[]provider.ExecConfig
}

func (r *recordingCtr) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	*r.calls = append(*r.calls, cfg)
	return r.mockContainerExec.Exec(ctx, cfg)
}

func newRecordingCtr(calls *[]provider.ExecConfig, execErr error) *recordingCtr {
	return &recordingCtr{mockContainerExec: &mockContainerExec{execErr: execErr}, calls: calls}
}

func TestCredSecretPaths(t *testing.T) {
	cases := []struct {
		credType string
		want     []string
	}{
		// Each credential now yields TWO paths: the flat directory a container
		// started before per-run secrets still has, and the glob that reaches
		// every live run's own directory. Naming only the first is what turned
		// revocation into a silent no-op.
		{"SECRET", []string{"/secrets/writer/GH_TOKEN", "/secrets/writer/*/GH_TOKEN"}},
		{"CLI_TOKEN", []string{"/secrets/writer/GH_TOKEN", "/secrets/writer/*/GH_TOKEN"}},
		{"GENERIC_SECRET", []string{"/secrets/writer/GH_TOKEN", "/secrets/writer/*/GH_TOKEN"}},
		{"USERPASS", []string{
			"/secrets/writer/GH_TOKEN_USERNAME", "/secrets/writer/*/GH_TOKEN_USERNAME",
			"/secrets/writer/GH_TOKEN_PASSWORD", "/secrets/writer/*/GH_TOKEN_PASSWORD",
		}},
		{"SSH_KEY", []string{"/secrets/writer/ssh/GH_TOKEN", "/secrets/writer/*/ssh/GH_TOKEN"}},
		{"CERTIFICATE", []string{"/secrets/writer/certs/GH_TOKEN.pem", "/secrets/writer/*/certs/GH_TOKEN.pem"}},
		{"API_KEY", nil},      // sidecar-injected, never on disk
		{"AI_CLI_TOKEN", nil}, // ditto
	}
	for _, c := range cases {
		got := credSecretPaths("writer", "GH_TOKEN", c.credType, "", "", nil)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: paths = %v, want %v", c.credType, got, c.want)
		}
	}
}

func TestCodexProviderRevokeRequiresSubscriptionMode(t *testing.T) {
	for _, mode := range []string{"", "api_key", "unknown", "subscription"} {
		t.Run(mode, func(t *testing.T) {
			paths := credSecretPaths("writer", "OPENAI_API_KEY", "PROVIDER_LOGIN", "OPENAI", mode, nil)
			if got, want := len(paths) != 0, mode == "subscription"; got != want {
				t.Fatalf("mode %q: removes login file = %v, want %v", mode, got, want)
			}
		})
	}
}

// TestCredSecretPaths_IncludesMultiPartFields is the revoke half of P4 delivery.
// A multi-part credential writes one file per part (exec_sidecar.go), so a
// revoke that removed only the primary would leave the secret access key on
// disk in a live container while the vault reported the credential gone — the
// operator's revoke would be a lie until the container next restarted.
//
// The names are derived with the SAME function delivery used; a second spelling
// here would remove the wrong paths and, being a best-effort `rm -f`, would say
// nothing about it.
func TestCredSecretPaths_IncludesMultiPartFields(t *testing.T) {
	got := credSecretPaths("writer", "AWS", "GENERIC_SECRET", "", "", []string{"region", "secret_access_key"})
	want := []string{
		"/secrets/writer/AWS", "/secrets/writer/*/AWS",
		"/secrets/writer/AWS_REGION", "/secrets/writer/*/AWS_REGION",
		"/secrets/writer/AWS_SECRET_ACCESS_KEY", "/secrets/writer/*/AWS_SECRET_ACCESS_KEY",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("paths = %v, want %v", got, want)
	}

	// A type with no on-disk form has no on-disk parts either: buildCredFileScript
	// skips the whole credential before it ever looks at the fields.
	if got := credSecretPaths("writer", "ANTHROPIC", "API_KEY", "", "", []string{"region"}); got != nil {
		t.Errorf("API_KEY paths = %v, want none — the credential itself never touches disk", got)
	}

	// An unsafe derived name is dropped rather than interpolated into the `rm`.
	// Delivery would have refused to write it, so there is nothing to remove,
	// and the one thing that must not happen is it reaching a shell.
	if got := credSecretPaths("writer", "AWS", "SECRET", "", "", []string{"a;rm -rf /"}); strings.Join(got, "|") != "/secrets/writer/AWS|/secrets/writer/*/AWS" {
		t.Errorf("paths = %v, want only the primary — an unsafe part name must not reach the shell", got)
	}
}

func TestBuildCredRemoveScript(t *testing.T) {
	if s := buildCredRemoveScript("writer", "GH_TOKEN", "SECRET", "", "", nil); s != "rm -f '/secrets/writer/GH_TOKEN' '/secrets/writer/'*'/GH_TOKEN'" {
		t.Errorf("SECRET script = %q", s)
	}
	if s := buildCredRemoveScript("writer", "DB", "USERPASS", "", "", nil); s != "rm -f '/secrets/writer/DB_USERNAME' '/secrets/writer/'*'/DB_USERNAME' '/secrets/writer/DB_PASSWORD' '/secrets/writer/'*'/DB_PASSWORD'" {
		t.Errorf("USERPASS script = %q", s)
	}
	if s := buildCredRemoveScript("writer", "KEY", "SSH_KEY", "", "", nil); s != "rm -f '/secrets/writer/ssh/KEY' '/secrets/writer/'*'/ssh/KEY'" {
		t.Errorf("SSH_KEY script = %q", s)
	}
	if s := buildCredRemoveScript("writer", "X", "API_KEY", "", "", nil); s != "" {
		t.Errorf("API_KEY (no disk form) script = %q, want empty", s)
	}
}

// seedFileMountCred wires ws → crew → agent → credential (of credType) →
// agent_credentials, returning the credential id. Whether the reconciler acts
// is decided by credType (file-materialized vs sidecar-injected), NOT by the
// vestigial agent_credentials.mount_type (always 'env' — nothing sets 'file').
func seedFileMountCred(t *testing.T, db *sql.DB, credType string) (wsID, credID string) {
	t.Helper()
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	seedCrew(t, db, "crew-rec", wsID, "Rec", "recrew")
	seedAgent(t, db, "agent-rec", wsID, "crew-rec", "Writer", "writer")
	credID = "cred-rec"
	// Insert directly (no encryption dep — reconcile reads type/slug/env only).
	if _, err := db.Exec(
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		 VALUES (?, ?, 'gh', 'x', ?, 'GITHUB', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		credID, wsID, credType, userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	// mount_type left at its 'env' default on purpose — it must not matter.
	if _, err := db.Exec(
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		 VALUES ('ac-rec', 'agent-rec', ?, 'GH_TOKEN', 0)`, credID); err != nil {
		t.Fatalf("seed agent_credentials: %v", err)
	}
	return wsID, credID
}

func TestReconcileRevokedCredential_FileMount_ExecsRemoveAsUID1001(t *testing.T) {
	db := setupTestDB(t)
	wsID, credID := seedFileMountCred(t, db, "SECRET")

	var calls []provider.ExecConfig
	h := NewCredentialHandler(db, newTestLogger())
	h.SetContainer(newRecordingCtr(&calls, nil))

	h.reconcileRevokedCredential(context.Background(), credID, wsID)

	if len(calls) != 1 {
		t.Fatalf("exec called %d times, want 1", len(calls))
	}
	c := calls[0]
	if c.ContainerID != "crewship-team-recrew" {
		t.Errorf("ContainerID = %q, want crewship-team-recrew (crew slug)", c.ContainerID)
	}
	if c.User != "1001:1001" {
		t.Errorf("User = %q, want 1001:1001 (agent UID)", c.User)
	}
	if len(c.Cmd) != 3 || c.Cmd[0] != "sh" || c.Cmd[1] != "-c" {
		t.Fatalf("Cmd = %v, want [sh -c <script>]", c.Cmd)
	}
	if !strings.Contains(c.Cmd[2], "rm -f '/secrets/writer/GH_TOKEN'") {
		t.Errorf("script = %q, want an rm of the secret file", c.Cmd[2])
	}
}

// A sidecar-injected type (API_KEY) never touches disk, so revoking it must
// not exec anything — the distinction is the TYPE, not the (unused) mount_type.
func TestReconcileRevokedCredential_NonFileType_NoExec(t *testing.T) {
	db := setupTestDB(t)
	wsID, credID := seedFileMountCred(t, db, "API_KEY")

	var calls []provider.ExecConfig
	h := NewCredentialHandler(db, newTestLogger())
	h.SetContainer(newRecordingCtr(&calls, nil))

	h.reconcileRevokedCredential(context.Background(), credID, wsID)
	if len(calls) != 0 {
		t.Fatalf("non-file type (API_KEY) must not exec (nothing on disk); got %d calls", len(calls))
	}
}

func TestReconcileRevokedCredential_OpenCodeAllGrantSources(t *testing.T) {
	for _, source := range []string{"direct", "crew_binding", "workspace_binding", "crew_link"} {
		t.Run(source, func(t *testing.T) {
			db := setupTestDB(t)
			wsID, credID := seedFileMountCred(t, db, "API_KEY")
			execOrFatal(t, db, `UPDATE agents SET cli_adapter = 'OPENCODE' WHERE id = 'agent-rec'`)
			execOrFatal(t, db, `UPDATE credentials SET name = 'OPENAI_API_KEY', provider = 'OPENAI' WHERE id = ?`, credID)
			execOrFatal(t, db, `UPDATE agent_credentials SET env_var_name = 'OPENAI_API_KEY' WHERE id = 'ac-rec'`)
			if source != "direct" {
				execOrFatal(t, db, `DELETE FROM agent_credentials WHERE id = 'ac-rec'`)
			}
			switch source {
			case "crew_binding":
				execOrFatal(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, crew_id, slot) VALUES ('binding-rec', ?, ?, 'CREW', 'crew-rec', 'OPENAI_API_KEY')`, wsID, credID)
			case "workspace_binding":
				execOrFatal(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, slot) VALUES ('binding-rec', ?, ?, 'WORKSPACE', 'OPENAI_API_KEY')`, wsID, credID)
			case "crew_link":
				execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES (?, 'crew-rec')`, credID)
			}
			var calls []provider.ExecConfig
			h := NewCredentialHandler(db, newTestLogger())
			h.SetContainer(newRecordingCtr(&calls, nil))
			h.reconcileRevokedCredential(context.Background(), credID, wsID)
			if len(calls) != 1 || calls[0].User != "1001:1001" || !strings.Contains(strings.Join(calls[0].Cmd, " "), "/crew/agents/writer/.local/share/opencode/auth.json") {
				t.Fatalf("revocation did not remove the OpenCode derivative: %+v", calls)
			}
		})
	}
}

func TestReconcileRevokedCredential_NilContainer_NoOp(t *testing.T) {
	db := setupTestDB(t)
	wsID, credID := seedFileMountCred(t, db, "SECRET")
	h := NewCredentialHandler(db, newTestLogger()) // no SetContainer → container nil
	// Must not panic.
	h.reconcileRevokedCredential(context.Background(), credID, wsID)
}

func TestReconcileRevokedCredential_ExecError_Tolerated(t *testing.T) {
	db := setupTestDB(t)
	wsID, credID := seedFileMountCred(t, db, "SECRET")

	var calls []provider.ExecConfig
	h := NewCredentialHandler(db, newTestLogger())
	// A stopped container makes Exec error — reconcile must swallow it.
	h.SetContainer(newRecordingCtr(&calls, context.DeadlineExceeded))

	h.reconcileRevokedCredential(context.Background(), credID, wsID)
	if len(calls) != 1 {
		t.Fatalf("exec attempted %d times, want 1 (error tolerated, not retried)", len(calls))
	}
}

// #2428: a Codex login is the one credential written outside /secrets — into
// the agent's HOME, where Codex reads it — and revoke must reach it there.
func TestCredSecretPaths_CodexLoginLivesInHome(t *testing.T) {
	// Both HOME layouts: the per-run glob and the pre-upgrade flat path.
	got := credSecretPaths("reviewer", "OPENAI_API_KEY", "AI_CLI_TOKEN", "OPENAI", "", []string{"region"})
	if strings.Join(got, "|") != "/crew/runs/reviewer/*/.codex/auth.json|/crew/agents/reviewer/.codex/auth.json" {
		t.Errorf("paths = %v", got)
	}
	// Same type, other vendor: still never on disk.
	if got := credSecretPaths("reviewer", "CLAUDE_CODE_OAUTH_TOKEN", "AI_CLI_TOKEN", "ANTHROPIC", "", nil); got != nil {
		t.Errorf("Anthropic login must not map to a file: %v", got)
	}
	if s := buildCredRemoveScript("reviewer", "OPENAI_API_KEY", "AI_CLI_TOKEN", "openai", "", nil); s != "rm -f '/crew/runs/reviewer/'*'/.codex/auth.json' '/crew/agents/reviewer/.codex/auth.json'" {
		t.Errorf("remove script = %q", s)
	}
}

// A Gemini login lives one directory over, in ~/.gemini — the AuthDelivery
// file form — and revoke must reach it there too.
func TestCredSecretPaths_GeminiLoginLivesInHome(t *testing.T) {
	cases := []struct {
		name       string
		credType   string
		provider   string
		wantPaths  string
		wantScript string
	}{
		{"google login", "AI_CLI_TOKEN", "GOOGLE",
			"/crew/runs/researcher/*/.gemini/oauth_creds.json|/crew/agents/researcher/.gemini/oauth_creds.json",
			"rm -f '/crew/runs/researcher/'*'/.gemini/oauth_creds.json' '/crew/agents/researcher/.gemini/oauth_creds.json'"},
		{"google login, lower-case provider", "AI_CLI_TOKEN", "google",
			"/crew/runs/researcher/*/.gemini/oauth_creds.json|/crew/agents/researcher/.gemini/oauth_creds.json",
			"rm -f '/crew/runs/researcher/'*'/.gemini/oauth_creds.json' '/crew/agents/researcher/.gemini/oauth_creds.json'"},
		{"google api key never touches disk", "API_KEY", "GOOGLE", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := credSecretPaths("researcher", "GEMINI_API_KEY", tc.credType, tc.provider, "", []string{"plan"})
			if strings.Join(got, "|") != tc.wantPaths {
				t.Errorf("paths = %v, want %q", got, tc.wantPaths)
			}
			if s := buildCredRemoveScript("researcher", "GEMINI_API_KEY", tc.credType, tc.provider, "", nil); s != tc.wantScript {
				t.Errorf("remove script = %q, want %q", s, tc.wantScript)
			}
		})
	}
}

// E0 gave every run its own secrets directory and its own HOME. That silently
// turned revocation into a no-op: the paths here named the old shared
// locations, `rm -f` reported success on files that were not there, and the
// revoked credential stayed on disk in every live run — while the vault showed
// it gone.
func TestCredSecretPaths_ReachEveryLiveRun(t *testing.T) {
	tests := []struct {
		name     string
		credType string
		provider string
		mode     string
		envVar   string
		fields   []string
		wantAny  []string // substrings that must appear among the paths
	}{
		{
			name: "generic secret covers both layouts", credType: "GENERIC_SECRET", envVar: "GH_TOKEN",
			wantAny: []string{"/secrets/writer/*/GH_TOKEN", "/secrets/writer/GH_TOKEN"},
		},
		{
			name: "ssh key", credType: "SSH_KEY", envVar: "DEPLOY_KEY",
			wantAny: []string{"/secrets/writer/*/ssh/DEPLOY_KEY", "/secrets/writer/ssh/DEPLOY_KEY"},
		},
		{
			name: "userpass covers both halves in both layouts", credType: "USERPASS", envVar: "DB",
			wantAny: []string{
				"/secrets/writer/*/DB_USERNAME", "/secrets/writer/*/DB_PASSWORD",
				"/secrets/writer/DB_USERNAME", "/secrets/writer/DB_PASSWORD",
			},
		},
		{
			name: "multi-part fields follow the same layouts", credType: "GENERIC_SECRET", envVar: "AWS",
			fields:  []string{"secret_access_key"},
			wantAny: []string{"/secrets/writer/*/AWS_SECRET_ACCESS_KEY", "/secrets/writer/AWS_SECRET_ACCESS_KEY"},
		},
		{
			// A Codex login is the one credential written outside /secrets, into
			// HOME — which is per run now, so it needs the glob too.
			name:     "codex login lives in HOME, which is now per run",
			credType: "AI_CLI_TOKEN", provider: "OPENAI", envVar: "OPENAI_API_KEY",
			wantAny: []string{"/crew/runs/writer/*/.codex/auth.json", "/crew/agents/writer/.codex/auth.json"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := credSecretPaths("writer", tc.envVar, tc.credType, tc.provider, tc.mode, tc.fields)
			joined := strings.Join(got, " ")
			for _, want := range tc.wantAny {
				if !strings.Contains(joined, want) {
					t.Errorf("no path contains %q; a revoked credential would survive there.\ngot: %v", want, got)
				}
			}
		})
	}
}

// The glob has to survive quoting or it is not a glob. Every other character
// must stay inside the quotes, because the shell is the one place a credential
// name could stop being data.
func TestQuoteWithGlob_ExpandsOnlyTheStarWeAdded(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/secrets/writer/GH_TOKEN", "'/secrets/writer/GH_TOKEN'"},
		{"/secrets/writer/*/GH_TOKEN", "'/secrets/writer/'*'/GH_TOKEN'"},
		{"/crew/runs/writer/*/.codex/auth.json", "'/crew/runs/writer/'*'/.codex/auth.json'"},
	}
	for _, tc := range tests {
		if got := quoteWithGlob(tc.in); got != tc.want {
			t.Errorf("quoteWithGlob(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// A name that somehow carried a quote must not break out of it. Delivery
	// refuses such a name, so this asserts the second line of defence.
	got := quoteWithGlob("/secrets/writer/EVIL'; rm -rf /; '")
	if strings.Count(got, "*") != 0 {
		t.Errorf("quoteWithGlob invented a glob: %q", got)
	}
}

// The whole script, so the quoting and the paths are checked together rather
// than each being right on its own.
func TestBuildCredRemoveScript_GlobsAreShellVisible(t *testing.T) {
	script := buildCredRemoveScript("writer", "GH_TOKEN", "GENERIC_SECRET", "", "", nil)
	if !strings.Contains(script, "'/secrets/writer/'*'/GH_TOKEN'") {
		t.Errorf("the per-run glob is not shell-visible in the script:\n%s", script)
	}
	if strings.Contains(script, "'/secrets/writer/*/GH_TOKEN'") {
		t.Errorf("the glob is inside the quotes, so the shell will treat it as a literal path:\n%s", script)
	}
}
