package main

// CLI parity tests for the hooks write commands (repo rule #3: every API
// endpoint gets a CLI command). These drive the real RunE bodies against a
// mock server and assert on the request that actually goes out — the wire
// shape is the contract, not the flag parsing.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/spf13/cobra"
)

// hooksWriteMock records the method, path, and decoded body of the last
// write so tests can assert what the CLI sent.
type hooksWriteMock struct {
	mu     sync.Mutex
	method string
	path   string
	body   map[string]any
	status int
}

func (m *hooksWriteMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/crews" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"crew-cuid-1","slug":"backend"}]`))
			return
		}
		m.mu.Lock()
		m.method = r.Method
		m.path = r.URL.Path
		m.body = nil
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &m.body)
		}
		status := m.status
		m.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"id":"hk_abc","event":"pre_tool_call","handler_kind":"http","enabled":true}`))
	})
}

// startHooksWriteMock wires a mock server and points the CLI config at it.
func startHooksWriteMock(t *testing.T) *hooksWriteMock {
	t.Helper()
	saveCLIState(t)
	m := &hooksWriteMock{}
	srv := httptest.NewServer(m.handler())
	t.Cleanup(srv.Close)
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	return m
}

// setHookFlags sets flags on a command and restores them afterwards, so the
// package-level cobra vars don't leak state between tests.
func setHookFlags(t *testing.T, cmd *cobra.Command, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatalf("set --%s: %v", k, err)
		}
	}
	t.Cleanup(func() {
		for k := range kv {
			f := cmd.Flags().Lookup(k)
			if f == nil {
				continue
			}
			_ = cmd.Flags().Set(k, f.DefValue)
			f.Changed = false
		}
	})
}

func TestHooksWriteCmdStructure(t *testing.T) {
	t.Parallel()

	have := map[string]bool{}
	for _, sub := range hooksCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "create", "update", "delete", "enable", "disable"} {
		if !have[want] {
			t.Errorf("hooks missing subcommand %q; have %v", want, have)
		}
	}
	// `crewship hook ...` must work too — the singular is what reads
	// naturally for the single-object verbs (create/update/delete).
	found := false
	for _, a := range hooksCmd.Aliases {
		if a == "hook" {
			found = true
		}
	}
	if !found {
		t.Errorf("hooks should alias 'hook'; aliases = %v", hooksCmd.Aliases)
	}
}

func TestHooksCreateRunE_RejectsAnUnknownEventBeforeCallingTheServer(t *testing.T) {
	m := startHooksWriteMock(t)

	setHookFlags(t, hooksCreateCmd, map[string]string{
		"event":   "PreToolUse",
		"handler": "http",
		"url":     "https://example.test/h",
	})
	err := hooksCreateCmd.RunE(hooksCreateCmd, nil)
	if err == nil {
		t.Fatal("expected an error for an unknown event")
	}
	// The whole value of validating locally is that the message is better
	// than a bare 400, so it has to carry the legal values.
	for _, ev := range hooks.AllEvents {
		if !strings.Contains(err.Error(), string(ev)) {
			t.Errorf("error omits valid event %q: %v", ev, err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.method != "" {
		t.Errorf("CLI hit the server (%s %s) despite a locally-invalid event", m.method, m.path)
	}
}

func TestHooksCreateRunE_RejectsAnUnknownHandlerKind(t *testing.T) {
	startHooksWriteMock(t)

	setHookFlags(t, hooksCreateCmd, map[string]string{
		"event":   string(hooks.EventPreToolCall),
		"handler": "carrier-pigeon",
	})
	err := hooksCreateCmd.RunE(hooksCreateCmd, nil)
	if err == nil {
		t.Fatal("expected an error for an unknown handler kind")
	}
	for _, kind := range []string{"shell", "http", "subagent"} {
		if !strings.Contains(err.Error(), kind) {
			t.Errorf("error omits handler kind %q: %v", kind, err)
		}
	}
}

func TestHooksCreateRunE_RequiresTheHandlerTarget(t *testing.T) {
	startHooksWriteMock(t)

	for _, tc := range []struct {
		kind string
		want string
	}{
		{"http", "--url"},
		{"shell", "--command"},
		{"subagent", "--subagent"},
	} {
		setHookFlags(t, hooksCreateCmd, map[string]string{
			"event":   string(hooks.EventPreToolCall),
			"handler": tc.kind,
		})
		err := hooksCreateCmd.RunE(hooksCreateCmd, nil)
		if err == nil {
			t.Errorf("%s with no target should error", tc.kind)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s error should name %s; got %v", tc.kind, tc.want, err)
		}
	}
}

func TestHooksCreateRunE_PostsTheFullBody(t *testing.T) {
	m := startHooksWriteMock(t)
	m.status = http.StatusCreated

	setHookFlags(t, hooksCreateCmd, map[string]string{
		"event":              string(hooks.EventPreToolCall),
		"handler":            "http",
		"url":                "https://example.test/h",
		"crew":               "backend",
		"matcher-tools":      "Bash,Write",
		"matcher-severities": "warn,error",
		"blocking":           "true",
		"disabled":           "true",
	})
	if err := hooksCreateCmd.RunE(hooksCreateCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.method != "POST" || m.path != "/api/v1/hooks" {
		t.Fatalf("request = %s %s, want POST /api/v1/hooks", m.method, m.path)
	}
	if m.body["event"] != string(hooks.EventPreToolCall) {
		t.Errorf("event = %v", m.body["event"])
	}
	if m.body["handler_kind"] != "http" {
		t.Errorf("handler_kind = %v", m.body["handler_kind"])
	}
	cfg, _ := m.body["handler_config"].(map[string]any)
	if cfg["url"] != "https://example.test/h" {
		t.Errorf("handler_config = %v", m.body["handler_config"])
	}
	// --crew takes a slug; the server wants the id (#1194's lesson on list).
	if m.body["crew_id"] != "crew-cuid-1" {
		t.Errorf("crew_id = %v, want the resolved cuid", m.body["crew_id"])
	}
	matcher, _ := m.body["matcher"].(map[string]any)
	tools, _ := matcher["tools"].([]any)
	if len(tools) != 2 || tools[0] != "Bash" {
		t.Errorf("matcher.tools = %v", matcher["tools"])
	}
	sevs, _ := matcher["severities"].([]any)
	if len(sevs) != 2 {
		t.Errorf("matcher.severities = %v", matcher["severities"])
	}
	if m.body["blocking"] != true {
		t.Errorf("blocking = %v, want true", m.body["blocking"])
	}
	if m.body["enabled"] != false {
		t.Errorf("enabled = %v, want false (--disabled)", m.body["enabled"])
	}
}

func TestHooksCreateRunE_RawJSONEscapeHatches(t *testing.T) {
	m := startHooksWriteMock(t)
	m.status = http.StatusCreated

	setHookFlags(t, hooksCreateCmd, map[string]string{
		"event":          string(hooks.EventOnBudgetExceeded),
		"handler":        "http",
		"handler-config": `{"url":"https://raw.test/h","method":"PUT","secret":"s3cr3t"}`,
		"matcher":        `{"agent_ids":["ag_1"],"when":"cost > 5"}`,
	})
	if err := hooksCreateCmd.RunE(hooksCreateCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, _ := m.body["handler_config"].(map[string]any)
	if cfg["method"] != "PUT" || cfg["secret"] != "s3cr3t" {
		t.Errorf("--handler-config not forwarded verbatim: %v", cfg)
	}
	matcher, _ := m.body["matcher"].(map[string]any)
	if matcher["when"] != "cost > 5" {
		t.Errorf("--matcher not forwarded verbatim: %v", matcher)
	}
}

func TestHooksCreateRunE_RejectsMalformedJSONFlags(t *testing.T) {
	startHooksWriteMock(t)

	setHookFlags(t, hooksCreateCmd, map[string]string{
		"event":          string(hooks.EventPreToolCall),
		"handler":        "http",
		"handler-config": `{not json`,
	})
	err := hooksCreateCmd.RunE(hooksCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--handler-config") {
		t.Fatalf("expected a --handler-config parse error; got %v", err)
	}
}

func TestHooksUpdateRunE_SendsOnlyTheFlagsThatChanged(t *testing.T) {
	m := startHooksWriteMock(t)

	setHookFlags(t, hooksUpdateCmd, map[string]string{
		"event": string(hooks.EventPostToolCall),
	})
	if err := hooksUpdateCmd.RunE(hooksUpdateCmd, []string{"hk_abc"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.method != "PATCH" || m.path != "/api/v1/hooks/hk_abc" {
		t.Fatalf("request = %s %s, want PATCH /api/v1/hooks/hk_abc", m.method, m.path)
	}
	if m.body["event"] != string(hooks.EventPostToolCall) {
		t.Errorf("event = %v", m.body["event"])
	}
	// A PATCH that ships every flag at its default would silently clear
	// blocking / enabled / matcher on the server. Absent means absent.
	for _, k := range []string{"handler_kind", "handler_config", "matcher", "blocking", "enabled", "crew_id"} {
		if _, present := m.body[k]; present {
			t.Errorf("unset flag %q was sent anyway: %v", k, m.body[k])
		}
	}
}

func TestHooksUpdateRunE_RequiresAtLeastOneFlag(t *testing.T) {
	m := startHooksWriteMock(t)

	err := hooksUpdateCmd.RunE(hooksUpdateCmd, []string{"hk_abc"})
	if err == nil {
		t.Fatal("update with no flags should error rather than send an empty patch")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.method != "" {
		t.Errorf("empty patch was sent: %s %s", m.method, m.path)
	}
}

func TestHooksUpdateRunE_RejectsAnUnknownEvent(t *testing.T) {
	startHooksWriteMock(t)

	setHookFlags(t, hooksUpdateCmd, map[string]string{"event": "post_run"})
	err := hooksUpdateCmd.RunE(hooksUpdateCmd, []string{"hk_abc"})
	if err == nil || !strings.Contains(err.Error(), string(hooks.EventPostToolCall)) {
		t.Fatalf("expected an event error listing the valid names; got %v", err)
	}
}

func TestHooksDeleteRunE_CallsDeleteOnTheID(t *testing.T) {
	m := startHooksWriteMock(t)

	setHookFlags(t, hooksDeleteCmd, map[string]string{"yes": "true"})
	if err := hooksDeleteCmd.RunE(hooksDeleteCmd, []string{"hk_abc"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.method != "DELETE" || m.path != "/api/v1/hooks/hk_abc" {
		t.Fatalf("request = %s %s, want DELETE /api/v1/hooks/hk_abc", m.method, m.path)
	}
}

func TestHooksDeleteRunE_RefusesWithoutConfirmation(t *testing.T) {
	m := startHooksWriteMock(t)

	err := hooksDeleteCmd.RunE(hooksDeleteCmd, []string{"hk_abc"})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("delete without --yes should error and name the flag; got %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.method != "" {
		t.Errorf("unconfirmed delete was sent: %s %s", m.method, m.path)
	}
}

func TestHooksArgsValidation_WriteCommands(t *testing.T) {
	t.Parallel()

	if err := hooksUpdateCmd.Args(hooksUpdateCmd, []string{}); err == nil {
		t.Error("update with no args should error")
	}
	if err := hooksDeleteCmd.Args(hooksDeleteCmd, []string{}); err == nil {
		t.Error("delete with no args should error")
	}
	if err := hooksCreateCmd.Args(hooksCreateCmd, []string{"stray"}); err == nil {
		t.Error("create takes no positional args")
	}
}

// TestHooksListRenders_TheHandlerTarget covers a live gap rather than the
// new endpoints: the list command decoded a `target` field the API has
// never emitted, so the TARGET column was always blank while the docs
// described it as showing the URL / command / subagent. The target is
// derivable from handler_config, which the API does send.
func TestHooksListRenders_TheHandlerTarget(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind string
		cfg  map[string]any
		want string
	}{
		{"http", map[string]any{"url": "https://hooks.slack.test/x"}, "https://hooks.slack.test/x"},
		{"shell", map[string]any{"command": "/usr/local/bin/after.sh"}, "/usr/local/bin/after.sh"},
		{"subagent", map[string]any{"agent_id": "oncall-router"}, "oncall-router"},
		{"subagent", map[string]any{"agent": "legacy-key"}, "legacy-key"},
		{"http", map[string]any{}, ""},
	} {
		if got := hookTarget(tc.kind, tc.cfg); got != tc.want {
			t.Errorf("hookTarget(%q, %v) = %q, want %q", tc.kind, tc.cfg, got, tc.want)
		}
	}
}
