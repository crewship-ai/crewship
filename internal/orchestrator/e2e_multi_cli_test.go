package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// e2e_multi_cli_test.go is the in-CI parity test for all six CLI adapters.
//
// Why fixture-replay instead of calling the real CLIs:
//   - The five non-Claude CLIs are upstream binaries with monthly breaking
//     changes (different schemas, different flags, different install paths).
//     Pinning their npm versions in CI flake budget would be destroyed.
//   - Each real run consumes paid API quota.
//   - Network egress + provisioning would slow the PR loop from seconds to
//     minutes per build.
//
// Instead this test replays canned NDJSON fixtures captured from real upstream
// runs (testdata/cli-fixtures/*.ndjson) and asserts each adapter's parser
// produces the minimum contract: a system bootstrap event, at least one text
// chunk, and a terminal envelope with usage data Paymaster can read.
//
// Real-CLI smoke tests live behind `make smoke-cli` (separate Make target,
// run nightly on dev2 or manually) — not in the per-PR loop.
//
// To refresh a fixture when an upstream CLI changes its schema:
//   1. ssh dev2; make sure all 6 CLIs are installed (./dev.sh start does this)
//   2. Run the CLI with stream-json output:
//        claude --print --output-format stream-json "say hello" \
//          > testdata/cli-fixtures/claude.ndjson
//        codex exec --json --sandbox read-only -- "say hello" \
//          > testdata/cli-fixtures/codex.ndjson
//        gemini -p "say hello" --output-format stream-json \
//          > testdata/cli-fixtures/gemini.ndjson
//        opencode run --format json -- "say hello" \
//          > testdata/cli-fixtures/opencode.ndjson
//        cursor-agent -p --output-format stream-json --force -- "say hello" \
//          > testdata/cli-fixtures/cursor.ndjson
//        droid exec --auto low -o stream-json -- "say hello" \
//          > testdata/cli-fixtures/droid.ndjson
//   3. Commit the updated fixtures + bump pinnedNpmVersion in
//      cli_adapter_versions_test.go.

// adapterFixtureContract pins the minimum AgentEvent set every CLI adapter
// must emit when given a canonical "say hello" prompt. If a future CLI
// version changes its output schema and breaks this contract, the test fails
// with a precise message naming which event went missing.
type adapterFixtureContract struct {
	adapter        string
	fixtureFile    string
	mustHaveTypes  []string // event types that MUST appear (bootstrap, text, terminal)
	mustHaveModel  bool     // bootstrap event must include model name in metadata
	mustHaveResult bool     // a "result" or equivalent terminal event must appear
}

func TestE2E_AllAdaptersFixtureReplay(t *testing.T) {
	contracts := []adapterFixtureContract{
		{
			adapter:        "CLAUDE_CODE",
			fixtureFile:    "claude.ndjson",
			mustHaveTypes:  []string{"system", "text", "result"},
			mustHaveModel:  true,
			mustHaveResult: true,
		},
		{
			adapter:        "CODEX_CLI",
			fixtureFile:    "codex.ndjson",
			mustHaveTypes:  []string{"system", "text", "result"},
			mustHaveModel:  true,
			mustHaveResult: true,
		},
		{
			adapter:        "GEMINI_CLI",
			fixtureFile:    "gemini.ndjson",
			mustHaveTypes:  []string{"system", "text", "result"},
			mustHaveModel:  true,
			mustHaveResult: true,
		},
		{
			adapter:        "OPENCODE",
			fixtureFile:    "opencode.ndjson",
			mustHaveTypes:  []string{"text", "result"},
			mustHaveModel:  false, // opencode does not emit a system bootstrap
			mustHaveResult: true,
		},
		{
			adapter:        "CURSOR_CLI",
			fixtureFile:    "cursor.ndjson",
			mustHaveTypes:  []string{"system", "text", "result"},
			mustHaveModel:  true,
			mustHaveResult: true,
		},
		{
			adapter:        "FACTORY_DROID",
			fixtureFile:    "droid.ndjson",
			mustHaveTypes:  []string{"system", "text", "result"},
			mustHaveModel:  true,
			mustHaveResult: true,
		},
	}

	for _, c := range contracts {
		t.Run(c.adapter, func(t *testing.T) {
			fixturePath := filepath.Join("testdata", "cli-fixtures", c.fixtureFile)
			data, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Fatalf("cannot read fixture %s: %v (run the smoke harness on dev2 to regenerate)", fixturePath, err)
			}

			a := getAdapter(c.adapter)
			if a == nil {
				t.Fatalf("getAdapter(%s) returned nil — adapter not registered", c.adapter)
			}
			if a.Name() != c.adapter {
				t.Errorf("adapter name mismatch: got %s, want %s", a.Name(), c.adapter)
			}

			seenTypes := make(map[string]int)
			seenModel := false
			seenResult := false

			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if line == "" {
					continue
				}
				a.ParseStreamLine([]byte(line), func(e AgentEvent) {
					seenTypes[e.Type]++
					// Look for the model field in any system or result event metadata.
					if meta, ok := e.Metadata.(map[string]interface{}); ok {
						if model, ok := meta["model"].(string); ok && model != "" {
							seenModel = true
						}
					}
					if e.Type == "result" {
						seenResult = true
					}
				})
			}

			for _, want := range c.mustHaveTypes {
				if seenTypes[want] == 0 {
					t.Errorf("%s: fixture replay missing event type %q (saw: %v)", c.adapter, want, seenTypes)
				}
			}
			if c.mustHaveModel && !seenModel {
				t.Errorf("%s: no model name surfaced in any event metadata — Crow's Nest 'agent runs on X' display will be blank", c.adapter)
			}
			if c.mustHaveResult && !seenResult {
				t.Errorf("%s: no terminal result event — Paymaster cannot record run cost", c.adapter)
			}
		})
	}
}

// TestE2E_AllAdaptersExposeMinimumContract verifies the CLIAdapter interface
// is consistently implemented across every registered adapter. Catches an
// adapter shipped with one method left as a stub (e.g. UseStreamJSON returning
// true but ParseStreamLine being a no-op).
func TestE2E_AllAdaptersExposeMinimumContract(t *testing.T) {
	wantAdapters := []string{
		"CLAUDE_CODE", "CODEX_CLI", "GEMINI_CLI",
		"OPENCODE", "CURSOR_CLI", "FACTORY_DROID",
	}
	for _, name := range wantAdapters {
		t.Run(name, func(t *testing.T) {
			a := getAdapter(name)
			if a.Name() != name {
				t.Errorf("Name() = %q, want %q", a.Name(), name)
			}
			if !a.SupportsMCP() {
				t.Errorf("SupportsMCP() = false; expected true for all 6 first-class adapters after the multi-CLI wave")
			}
			// BuildCommand must produce a non-empty argv with the adapter's
			// canonical binary name as argv[0].
			argv := a.BuildCommand(AgentRunRequest{
				CLIAdapter:  name,
				UserMessage: "test",
				ToolProfile: "CODING",
			})
			if len(argv) == 0 {
				t.Fatalf("BuildCommand returned empty argv")
			}
			expectedBin := map[string]string{
				"CLAUDE_CODE":   "claude",
				"CODEX_CLI":     "codex",
				"GEMINI_CLI":    "gemini",
				"OPENCODE":      "opencode",
				"CURSOR_CLI":    "cursor-agent",
				"FACTORY_DROID": "droid",
			}[name]
			if argv[0] != expectedBin {
				t.Errorf("argv[0] = %q, want %q", argv[0], expectedBin)
			}
			// ParseStreamLine must accept arbitrary input without panicking.
			a.ParseStreamLine([]byte(`{"type":"unknown_future_event","data":1}`), func(AgentEvent) {})
			a.ParseStreamLine([]byte("not json"), func(AgentEvent) {})
			// Nil handler must not panic either.
			a.ParseStreamLine([]byte(`{"type":"text"}`), nil)
		})
	}
}

// TestE2E_UnknownAdapterFallback covers the unknown-adapter safety net so a
// malformed agent record (e.g. typo, schema migration mismatch) doesn't crash
// the orchestrator.
func TestE2E_UnknownAdapterFallback(t *testing.T) {
	a := getAdapter("WHO_KNOWS_FUTURE_CLI")
	// Unknown returns the unknownAdapter which produces a minimal claude --print
	// fallback (preserves pre-multi-CLI default arm behaviour).
	argv := a.BuildCommand(AgentRunRequest{UserMessage: "test"})
	if argv[0] != "claude" {
		t.Errorf("unknown fallback should produce a 'claude' command, got %q", argv[0])
	}
	if a.SupportsMCP() {
		t.Errorf("unknown adapter must NOT advertise MCP support — would write configs the CLI never reads")
	}
	// Must not panic with valid+invalid inputs.
	a.ParseStreamLine([]byte(`{"type":"x"}`), func(AgentEvent) {})
	a.ParseStreamLine(nil, nil)
}
