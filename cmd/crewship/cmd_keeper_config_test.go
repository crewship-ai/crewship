package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestKeeperConfigCmdStructure(t *testing.T) {
	t.Parallel()

	have := map[string]bool{}
	for _, sub := range keeperCmd.Commands() {
		have[sub.Name()] = true
	}
	if !have["config"] {
		t.Fatalf("keeper missing subcommand %q; have %v", "config", have)
	}
	haveCfg := map[string]bool{}
	for _, sub := range keeperConfigCmd.Commands() {
		haveCfg[sub.Name()] = true
	}
	for _, want := range []string{"get", "set", "reset"} {
		if !haveCfg[want] {
			t.Errorf("keeper config missing subcommand %q; have %v", want, haveCfg)
		}
	}
}

func (m *keeperMock) decodeConfigPut(t *testing.T) map[string]any {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.cfgBody) == 0 {
		t.Fatal("no PUT /admin/keeper/config was issued")
	}
	var body map[string]any
	if err := json.Unmarshal(m.cfgBody, &body); err != nil {
		t.Fatalf("decode PUT body %q: %v", m.cfgBody, err)
	}
	return body
}

// resetKeeperConfigFlags puts the shared cobra flags back to their zero state.
// The command values are package-level (cobra binds them at init), so a test
// that set --model would otherwise leak into the next one.
func resetKeeperConfigFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		flagKeeperCfgEnabled, flagKeeperCfgEndpoint, flagKeeperCfgModel = "", "", ""
		for _, name := range []string{"enabled", "endpoint", "model"} {
			if f := keeperConfigSetCmd.Flags().Lookup(name); f != nil {
				f.Changed = false
			}
		}
	})
}

func TestKeeperConfigGetRunE(t *testing.T) {
	startKeeperMock(t)
	if err := keeperConfigGetCmd.RunE(keeperConfigGetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
}

// The flow this command exists for: point the judge at a LAN Ollama and turn
// Keeper on, in one call, without a restart.
func TestKeeperConfigSetRunE_SendsOnlyPassedFlags(t *testing.T) {
	m := startKeeperMock(t)
	resetKeeperConfigFlags(t)

	if err := keeperConfigSetCmd.Flags().Set("endpoint", "http://192.168.1.40:11434"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if err := keeperConfigSetCmd.Flags().Set("enabled", "on"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if err := keeperConfigSetCmd.RunE(keeperConfigSetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	body := m.decodeConfigPut(t)
	if body["judge_endpoint_url"] != "http://192.168.1.40:11434" {
		t.Errorf("endpoint = %v", body["judge_endpoint_url"])
	}
	if body["enabled"] != true {
		t.Errorf("enabled = %v, want true", body["enabled"])
	}
	// A field the operator did not mention must be absent, not empty: sending
	// "judge_model": "" would clear an override they never touched.
	if _, present := body["judge_model"]; present {
		t.Errorf("body carried judge_model although --model was not passed: %v", body)
	}
}

// An empty value is the documented way to clear an override, so it has to reach
// the wire as an explicit empty string rather than being dropped as "unset".
func TestKeeperConfigSetRunE_EmptyValueClears(t *testing.T) {
	m := startKeeperMock(t)
	resetKeeperConfigFlags(t)

	if err := keeperConfigSetCmd.Flags().Set("model", ""); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if err := keeperConfigSetCmd.RunE(keeperConfigSetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := m.decodeConfigPut(t)
	v, present := body["judge_model"]
	if !present {
		t.Fatalf("--model \"\" did not reach the wire: %v", body)
	}
	if v != "" {
		t.Errorf("judge_model = %v, want an empty string", v)
	}
}

// inherit is the third state and must go out as JSON null — false would store
// "the operator turned it off", which is a different thing.
func TestKeeperConfigSetRunE_InheritSendsNull(t *testing.T) {
	m := startKeeperMock(t)
	resetKeeperConfigFlags(t)

	if err := keeperConfigSetCmd.Flags().Set("enabled", "inherit"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if err := keeperConfigSetCmd.RunE(keeperConfigSetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(string(m.cfgBody), `"enabled":null`) {
		t.Errorf("body %q does not carry an explicit null", m.cfgBody)
	}
}

func TestKeeperConfigSetRunE_RejectsBadEnabled(t *testing.T) {
	startKeeperMock(t)
	resetKeeperConfigFlags(t)

	if err := keeperConfigSetCmd.Flags().Set("enabled", "maybe"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	err := keeperConfigSetCmd.RunE(keeperConfigSetCmd, nil)
	if err == nil {
		t.Fatal("accepted --enabled maybe")
	}
	if !strings.Contains(err.Error(), "on, off, or inherit") {
		t.Errorf("message does not name the valid values: %v", err)
	}
}

// A bare `config set` must not issue an empty PUT that reports success while
// changing nothing.
func TestKeeperConfigSetRunE_RefusesEmptyUpdate(t *testing.T) {
	m := startKeeperMock(t)
	resetKeeperConfigFlags(t)

	err := keeperConfigSetCmd.RunE(keeperConfigSetCmd, nil)
	if err == nil {
		t.Fatal("an argument-less set was accepted")
	}
	if !strings.Contains(err.Error(), "nothing to change") {
		t.Errorf("unexpected error: %v", err)
	}
	if len(m.cfgBody) != 0 {
		t.Errorf("an empty update still hit the API: %q", m.cfgBody)
	}
}

// Reset deletes and then re-reads, so what it prints is what is now in force
// rather than an optimistic guess.
func TestKeeperConfigResetRunE(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperConfigResetCmd.RunE(keeperConfigResetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	m.mu.Lock()
	deletes := m.cfgDeletes
	m.mu.Unlock()
	if deletes != 1 {
		t.Errorf("issued %d DELETEs, want 1", deletes)
	}
}

func TestKeeperConfigCmds_NoAuth(t *testing.T) {
	saveCLIState(t)
	resetKeeperConfigFlags(t)
	cliCfg = &cli.CLIConfig{}

	if err := keeperConfigSetCmd.Flags().Set("model", "qwen2.5:7b"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	for name, run := range map[string]func() error{
		"get":   func() error { return keeperConfigGetCmd.RunE(keeperConfigGetCmd, nil) },
		"set":   func() error { return keeperConfigSetCmd.RunE(keeperConfigSetCmd, nil) },
		"reset": func() error { return keeperConfigResetCmd.RunE(keeperConfigResetCmd, nil) },
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected 'not logged in', got %v", name, err)
		}
	}
}

// #1558: `config get` is the CLI answer to "which model judges credential
// access?", and that question has two answers at two scopes. The instance row
// only ever accepts native Ollama (keepercfg.validate rejects the rest), so the
// output has to say so AND name the command that configures the other case —
// otherwise the CLI teaches less than the console does, and the first thing
// that teaches an operator is a 400 from `keeper config set`.
// Deliberately NOT t.Parallel(): covCaptureStdoutCli3 reaches
// guardCLIState, whose cleanup rewrites the process-wide CLI globals.
// A parallel test's cleanup fires at an arbitrary point relative to
// every other parallel test, so this one raced shellPromptString's read
// of cli.Bold (#1610). See TestCLIStateGuard_NoParallelWriter, which
// keeps the pairing from coming back.
func TestPrintKeeperInstanceConfig_NamesTheOtherScope(t *testing.T) {
	out := covCaptureStdoutCli3(t, func() {
		printKeeperInstanceConfig(keeperInstanceConfig{
			Enabled:     keeperConfigBoolField{Value: true, Source: "instance"},
			Provider:    keeperConfigStrField{Value: "ollama", Source: "default"},
			EndpointURL: keeperConfigStrField{Value: "http://127.0.0.1:11434", Source: "instance"},
			Wire:        keeperConfigStrField{Value: "ollama", Source: "default"},
			Model:       keeperConfigStrField{Value: "qwen2.5:7b", Source: "instance"},
			TimeoutMS:   keeperConfigIntField{Value: 20000, Source: "default"},

			JudgeConfigured: true,
		})
	})

	for _, want := range []string{
		// The scope this row applies at.
		"instance-wide",
		// The constraint the server enforces on write.
		"native Ollama only",
		// The other card/command, by name, so it is one copy-paste away.
		"crewship keeper model",
		"per workspace",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config get output does not mention %q:\n%s", want, out)
		}
	}
}
