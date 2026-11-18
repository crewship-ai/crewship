package devcontainer

import "testing"

func TestAdapterCLIForAdapterAndBinaryNames(t *testing.T) {
	for _, tc := range []struct {
		adapter, binary string
		names           []string
	}{
		{"CLAUDE_CODE", "claude", []string{"claude"}},
		{"CODEX_CLI", "codex", []string{"codex", "codex-cli"}},
		{"GEMINI_CLI", "gemini", []string{"gemini", "gemini-cli"}},
		{"OPENCODE", "opencode", []string{"opencode"}},
		{"CURSOR_CLI", "cursor-agent", []string{"cursor", "cursor-agent"}},
		{"FACTORY_DROID", "droid", []string{"factory", "factory-droid", "droid"}},
	} {
		for _, name := range append(tc.names, tc.adapter) {
			cli, ok := AdapterCLIFor(name)
			if !ok || cli.Adapter != tc.adapter || cli.Binary != tc.binary {
				t.Errorf("AdapterCLIFor(%q) = %+v, %t, want %s/%s", name, cli, ok, tc.adapter, tc.binary)
			}
		}
	}
}
