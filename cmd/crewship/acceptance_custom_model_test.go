package main

// #5376 acceptance — a model id that no catalog lists can be set by hand on
// agent create and agent update, driven through the BUILT BINARY against the
// real api router. The catalog is a list of suggestions; an operator who does
// not want to wait for a refresh types the exact id.

import (
	"strings"
	"testing"
)

func TestAcceptance_CustomModelID_CreateAndUpdate(t *testing.T) {
	env := startAvatarStyleServer(t)
	model := func(where string) string {
		t.Helper()
		var m *string
		if err := env.db.QueryRow(`SELECT llm_model FROM agents WHERE ` + where).Scan(&m); err != nil {
			t.Fatalf("read llm_model where %s: %v", where, err)
		}
		if m == nil {
			return "<null>"
		}
		return *m
	}

	out, err := runAvatarStyleCLI(t, env.cfg, "agent", "create", "--name", "Pinned", "--slug", "pinned", "--crew", "avatars",
		"--cli-adapter", "CODEX_CLI", "--llm-provider", "OPENAI", "--llm-model", "gpt-7-preview")
	if err != nil {
		t.Fatalf("agent create with a custom model: %v\n%s", err, out)
	}
	if got := model("slug = 'pinned'"); got != "gpt-7-preview" {
		t.Errorf("created agent llm_model = %q, want gpt-7-preview", got)
	}

	out, err = runAvatarStyleCLI(t, env.cfg, "agent", "update", "existing", "--llm-provider", "ANTHROPIC", "--llm-model", "claude-opus-5-5[1m]")
	if err != nil {
		t.Fatalf("agent update with a custom model: %v\n%s", err, out)
	}
	if got := model("id = 'agent-av'"); got != "claude-opus-5-5[1m]" {
		t.Errorf("updated agent llm_model = %q, want claude-opus-5-5[1m]", got)
	}

	out, err = runAvatarStyleCLI(t, env.cfg, "agent", "update", "existing", "--llm-model", "claude sonnet; id")
	if err == nil {
		t.Fatalf("a malformed model id was accepted:\n%s", out)
	}
	if !strings.Contains(out, "llm_model may contain only") {
		t.Errorf("the refusal should explain the allowed characters, got:\n%s", out)
	}
	if got := model("id = 'agent-av'"); got != "claude-opus-5-5[1m]" {
		t.Errorf("a refused update changed llm_model to %q", got)
	}
}
