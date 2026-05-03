package orchestrator

import (
	"slices"
	"strings"
	"testing"
)

// TestAdapterArgvMatchesUpstreamRef pins each adapter's command-line shape
// against the canonical reference for the upstream CLI as of 2026-05. When
// upstream renames or removes a flag, this test fails with a precise message
// pointing at the doc URL — much louder than the eventual "command not found"
// or "unknown flag" error in production.
//
// Update procedure when one of these tests fails:
//  1. Re-fetch the doc URL listed in the test case.
//  2. If the flag really did rename, update the adapter and the test together.
//  3. Bump pinnedNpmVersion below so the next reviewer sees the latest matrix
//     this code was validated against.
func TestAdapterArgvMatchesUpstreamRef(t *testing.T) {
	cases := []struct {
		adapter          string
		mustHave         []string // flags that MUST appear (canonical per upstream docs)
		mustNotHave      []string // flags that must NOT appear (deprecated / removed)
		docURL           string
		pinnedNpmVersion string
	}{
		{
			adapter:          "CLAUDE_CODE",
			docURL:           "https://code.claude.com/docs/en/cli-reference",
			mustHave:         []string{"--print", "--output-format", "stream-json", "--include-partial-messages", "--dangerously-skip-permissions", "--system-prompt"},
			pinnedNpmVersion: "@anthropic-ai/claude-code@2.1.126",
		},
		{
			adapter: "CODEX_CLI",
			docURL:  "https://developers.openai.com/codex/cli/reference",
			// `codex exec --json` is the documented non-interactive form; the
			// pre-refactor `codex --quiet` does not exist in the Rust port.
			mustHave:         []string{"exec", "--json", "--sandbox"},
			mustNotHave:      []string{"--quiet"},
			pinnedNpmVersion: "@openai/codex@0.128.0",
		},
		{
			adapter:  "GEMINI_CLI",
			docURL:   "https://geminicli.com/docs/cli/headless/",
			mustHave: []string{"-p", "--output-format", "stream-json"},
			// --system-instruction is not in the public headless reference;
			// adapter folds the system prompt into the prompt body instead.
			mustNotHave:      []string{"--system-instruction"},
			pinnedNpmVersion: "@google/gemini-cli@0.40.1",
		},
		{
			adapter: "OPENCODE",
			docURL:  "https://opencode.ai/docs/cli/",
			// Flag is --format, NOT --output-format — historical confusion.
			mustHave:         []string{"run", "--format", "json"},
			mustNotHave:      []string{"--output-format"},
			pinnedNpmVersion: "opencode-ai@1.14.33",
		},
		{
			adapter: "CURSOR_CLI",
			docURL:  "https://cursor.com/docs/cli/headless",
			// --force is required in headless mode or write tools hang on
			// permission prompts. --stream-partial-output enables incremental
			// deltas without which the UI sees one giant text block at the end.
			mustHave:         []string{"-p", "--output-format", "stream-json", "--force", "--stream-partial-output"},
			pinnedNpmVersion: "(curl https://cursor.com/install)",
		},
		{
			adapter: "FACTORY_DROID",
			docURL:  "https://docs.factory.ai/reference/cli-reference",
			// `droid exec --auto <level> -o stream-json` is the documented
			// non-interactive form. Without -o stream-json the orchestrator
			// only sees raw text — Paymaster gets nothing, Crow's Nest sees
			// no tool events.
			mustHave:         []string{"exec", "--auto", "-o", "stream-json"},
			pinnedNpmVersion: "(curl https://app.factory.ai/cli)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.adapter, func(t *testing.T) {
			req := AgentRunRequest{
				CLIAdapter:  tc.adapter,
				UserMessage: "test",
				ToolProfile: "CODING",
			}
			argv := BuildCLICommand(req)
			argvJoined := strings.Join(argv, " ")

			for _, flag := range tc.mustHave {
				if !slices.Contains(argv, flag) {
					t.Errorf("%s: argv missing required flag %q (per %s, pinned %s)\n  got: %v",
						tc.adapter, flag, tc.docURL, tc.pinnedNpmVersion, argv)
				}
			}
			for _, flag := range tc.mustNotHave {
				if slices.Contains(argv, flag) {
					t.Errorf("%s: argv contains forbidden flag %q (deprecated/non-existent per %s)\n  got: %v",
						tc.adapter, flag, tc.docURL, argv)
				}
			}
			// Sanity: the user message must always appear in argv (otherwise
			// the agent has nothing to do). Done as a contains-check on the
			// joined string because some adapters use a -- separator.
			if !strings.Contains(argvJoined, "test") {
				t.Errorf("%s: argv missing user message: %v", tc.adapter, argv)
			}
		})
	}
}

// TestAdapterParserUsesStreamJSON_MatchesAdapterCommand asserts that any
// adapter whose BuildCommand emits an --output-format / --json / --format json
// flag also reports UseStreamJSON()==true (so streamOutput will route stdout
// through ParseStreamLine instead of the raw-text fallback). A mismatch means
// either the parser or the command went out of sync — both are silent
// regressions that shipping break the chat UI.
func TestAdapterParserUsesStreamJSON_MatchesAdapterCommand(t *testing.T) {
	expectStream := map[string]bool{
		"CLAUDE_CODE":   true, // --output-format stream-json
		"CODEX_CLI":     true, // --json
		"GEMINI_CLI":    true, // --output-format stream-json
		"OPENCODE":      true, // --format json (JSONL Part stream)
		"CURSOR_CLI":    true, // --output-format stream-json
		"FACTORY_DROID": true, // -o stream-json
	}

	for name, want := range expectStream {
		t.Run(name, func(t *testing.T) {
			a := getAdapter(name)
			if a.UseStreamJSON() != want {
				t.Errorf("%s: UseStreamJSON()=%v want %v — adapter command and parser disagree on JSON mode",
					name, a.UseStreamJSON(), want)
			}
		})
	}
}

// TestAdapterEnvVarMatchesProvider keeps the env-var contract honest. Each
// non-Claude adapter has a documented auth env var; apiKeyEnvVarsForAdapter
// must list it, otherwise BuildEnvVarsSidecar will not inject the user's
// real API key and the CLI will silently fall back to the dummy value.
func TestAdapterEnvVarMatchesProvider(t *testing.T) {
	cases := []struct {
		adapter     string
		mustInclude string // env var the CLI definitely reads
	}{
		{"CODEX_CLI", "OPENAI_API_KEY"},
		{"GEMINI_CLI", "GEMINI_API_KEY"}, // canonical AI Studio var
		{"OPENCODE", "OPENAI_API_KEY"},   // BYOK includes OpenAI
		{"CURSOR_CLI", "CURSOR_API_KEY"},
		{"FACTORY_DROID", "FACTORY_API_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.adapter, func(t *testing.T) {
			allowed := apiKeyEnvVarsForAdapter(tc.adapter)
			if _, ok := allowed[tc.mustInclude]; !ok {
				t.Errorf("%s: apiKeyEnvVarsForAdapter missing %q — sidecar won't inject the real API key, CLI will see the dummy and 401",
					tc.adapter, tc.mustInclude)
			}
		})
	}
}
