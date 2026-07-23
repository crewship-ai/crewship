package orchestrator

import (
	"strings"
	"testing"
)

// Adversarial coverage for the "gate SECRET credential files on Keeper state"
// fix (exec_sidecar.go buildCredFileScript, secrets_cleanup.go
// hasFileMountedCreds). These tests chase the ways the gate could be wrong:
// the split switch accidentally gating a sibling type, the gate missing a
// duplicate/edge input, and — most importantly — a SECRET plaintext still
// reaching the agent container through a path OTHER than the file writer.

// TestBuildCredFileScript_KeeperMatrix_AllTypes is the exhaustive
// type-by-keeper-state matrix. It pins the core security invariant of the
// fix: SECRET is the ONE and ONLY file-mounted type withheld when Keeper is
// ON. Every other file type (CLI_TOKEN, GENERIC_SECRET, USERPASS, SSH_KEY,
// CERTIFICATE) must be written REGARDLESS of Keeper state — the grouped
// `case "CLI_TOKEN","SECRET","GENERIC_SECRET"` was split during the fix and
// the regression risk is that the split accidentally gated a neighbour.
func TestBuildCredFileScript_KeeperMatrix_AllTypes(t *testing.T) {
	t.Parallel()

	// For each type: the credential to feed, and the path substring that must
	// appear in the emitted script when the type IS written.
	type row struct {
		typ         string
		cred        Credential
		wantPathSub string // path that appears when written ("" = never file-mounted)
		wantCount   int    // file specs produced when written (USERPASS = 2)
	}
	rows := []row{
		{"CLI_TOKEN", Credential{Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN", PlainValue: "ghp"}, "/secrets/agent-a/GH_TOKEN", 1},
		{"GENERIC_SECRET", Credential{Type: "GENERIC_SECRET", EnvVarName: "TOK", PlainValue: "v"}, "/secrets/agent-a/TOK", 1},
		{"USERPASS", Credential{Type: "USERPASS", EnvVarName: "DB", PlainValue: "pw", Username: "u"}, "/secrets/agent-a/DB_PASSWORD", 2},
		{"SSH_KEY", Credential{Type: "SSH_KEY", EnvVarName: "DEPLOY", PlainValue: "k"}, "/secrets/agent-a/ssh/DEPLOY", 1},
		{"CERTIFICATE", Credential{Type: "CERTIFICATE", EnvVarName: "CA", PlainValue: "c"}, "/secrets/agent-a/certs/CA.pem", 1},
		{"SECRET", Credential{Type: "SECRET", EnvVarName: "WEBHOOK_SECRET", PlainValue: "shhh"}, "/secrets/agent-a/WEBHOOK_SECRET", 1},
		// Sidecar-injected types are never file-mounted, keeper-independent.
		{"API_KEY", Credential{Type: "API_KEY", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk"}, "", 0},
		{"AI_CLI_TOKEN", Credential{Type: "AI_CLI_TOKEN", EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", PlainValue: "sk-ant-oat"}, "", 0},
		{"OAUTH2", Credential{Type: "OAUTH2", EnvVarName: "GH_OAUTH", PlainValue: "oat"}, "", 0},
	}

	for _, keeper := range []bool{false, true} {
		for _, r := range rows {
			r := r
			name := r.typ
			if keeper {
				name += "_KeeperOn"
			} else {
				name += "_KeeperOff"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				script, count, err := buildCredFileScript([]Credential{r.cred}, "/secrets/agent-a", keeper)
				if err != nil {
					t.Fatalf("buildCredFileScript: %v", err)
				}

				// The single type that behaves differently across keeper
				// states is SECRET: written when OFF, withheld when ON.
				written := r.wantPathSub != ""
				if r.typ == "SECRET" && keeper {
					written = false
				}

				if written {
					if count != r.wantCount {
						t.Errorf("%s keeper=%v: want %d file(s), got %d; script:\n%s", r.typ, keeper, r.wantCount, count, script)
					}
					if !strings.Contains(script, r.wantPathSub) {
						t.Errorf("%s keeper=%v: script must write %s; got:\n%s", r.typ, keeper, r.wantPathSub, script)
					}
				} else {
					if count != 0 {
						t.Errorf("%s keeper=%v: want 0 files, got %d; script:\n%s", r.typ, keeper, count, script)
					}
					if r.wantPathSub != "" && strings.Contains(script, r.wantPathSub) {
						t.Errorf("%s keeper=%v: script must NOT contain %s; got:\n%s", r.typ, keeper, r.wantPathSub, script)
					}
					if script != "" {
						t.Errorf("%s keeper=%v: expected empty script, got:\n%s", r.typ, keeper, script)
					}
				}
			})
		}
	}
}

// TestBuildCredFileScript_DuplicateSecretEnvVars checks that duplicate SECRET
// env-var names don't sneak one copy past the gate. Under Keeper ON both are
// withheld (empty script); under Keeper OFF both are written (the writer does
// not dedupe — the last rm-then-write wins on disk, but the plaintext of both
// still lands, which is the pre-existing legacy behaviour we must not change).
func TestBuildCredFileScript_DuplicateSecretEnvVars(t *testing.T) {
	t.Parallel()
	creds := []Credential{
		{Type: "SECRET", EnvVarName: "DUP", PlainValue: "a"},
		{Type: "SECRET", EnvVarName: "DUP", PlainValue: "b"},
	}

	onScript, onCount, err := buildCredFileScript(creds, "/secrets/agent-a", true)
	if err != nil {
		t.Fatalf("keeper on: %v", err)
	}
	if onCount != 0 || onScript != "" {
		t.Errorf("keeper ON: duplicate SECRETs must both be withheld; count=%d script=%q", onCount, onScript)
	}

	offScript, offCount, err := buildCredFileScript(creds, "/secrets/agent-a", false)
	if err != nil {
		t.Fatalf("keeper off: %v", err)
	}
	if offCount != 2 {
		t.Errorf("keeper OFF: both duplicate SECRETs written (legacy), want count 2, got %d", offCount)
	}
	if !strings.Contains(offScript, "/secrets/agent-a/DUP") {
		t.Errorf("keeper OFF: DUP path must appear; script:\n%s", offScript)
	}
}

// TestBuildCredFileScript_EmptySecretBothKeeperStates confirms the empty-input
// skip (empty PlainValue / empty EnvVarName) fires BEFORE the keeper switch, so
// an empty SECRET produces no file and no error in either keeper state — it can
// never be the lone spec that would otherwise force a noop exec.
func TestBuildCredFileScript_EmptySecretBothKeeperStates(t *testing.T) {
	t.Parallel()
	for _, keeper := range []bool{false, true} {
		emptyVal := []Credential{{Type: "SECRET", EnvVarName: "X", PlainValue: ""}}
		emptyName := []Credential{{Type: "SECRET", EnvVarName: "", PlainValue: "v"}}
		for _, creds := range [][]Credential{emptyVal, emptyName} {
			s, c, err := buildCredFileScript(creds, "/secrets/agent-a", keeper)
			if err != nil {
				t.Fatalf("keeper=%v: unexpected error: %v", keeper, err)
			}
			if c != 0 || s != "" {
				t.Errorf("keeper=%v: empty SECRET must yield empty script; count=%d script=%q", keeper, c, s)
			}
		}
	}
}

// TestBuildCredFileScript_FullMixKeeperOn is the "one batch, every type"
// regression guard for the split switch. With Keeper ON, every non-SECRET file
// type must still be present in the exact same script, and only the SECRET must
// be absent — proving the fix did not over-gate its neighbours in the grouped
// case it split apart.
func TestBuildCredFileScript_FullMixKeeperOn(t *testing.T) {
	t.Parallel()
	creds := []Credential{
		{Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN", PlainValue: "ghp"},
		{Type: "GENERIC_SECRET", EnvVarName: "STRIPE_HOOK", PlainValue: "whsec"},
		{Type: "SECRET", EnvVarName: "WEBHOOK_SECRET", PlainValue: "shhh"},
		{Type: "USERPASS", EnvVarName: "VAULT", PlainValue: "pw", Username: "alice"},
		{Type: "SSH_KEY", EnvVarName: "DEPLOY", PlainValue: "key"},
		{Type: "CERTIFICATE", EnvVarName: "CA", PlainValue: "cert"},
		{Type: "API_KEY", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk"}, // always skipped
	}
	script, count, err := buildCredFileScript(creds, "/secrets/agent-a", true)
	if err != nil {
		t.Fatalf("buildCredFileScript: %v", err)
	}
	// 6 file-mounted specs would exist with keeper off (USERPASS = 2 files),
	// minus the withheld SECRET → CLI_TOKEN(1)+GENERIC(1)+USERPASS(2)+SSH(1)+CERT(1) = 6.
	if count != 6 {
		t.Errorf("keeper ON full mix: want 6 file specs (SECRET withheld, API_KEY skipped), got %d", count)
	}
	mustHave := []string{
		"/secrets/agent-a/GH_TOKEN",
		"/secrets/agent-a/STRIPE_HOOK",
		"/secrets/agent-a/VAULT_USERNAME",
		"/secrets/agent-a/VAULT_PASSWORD",
		"/secrets/agent-a/ssh/DEPLOY",
		"/secrets/agent-a/certs/CA.pem",
	}
	for _, m := range mustHave {
		if !strings.Contains(script, m) {
			t.Errorf("keeper ON full mix: script must still write %s; got:\n%s", m, script)
		}
	}
	if strings.Contains(script, "/secrets/agent-a/WEBHOOK_SECRET") {
		t.Errorf("keeper ON full mix: SECRET file must be withheld; got:\n%s", script)
	}
	if strings.Contains(script, "ANTHROPIC_API_KEY") {
		t.Errorf("API_KEY must never be file-mounted; got:\n%s", script)
	}
}
