package orchestrator

import (
	"bytes"
	"testing"
)

// The CLI version that answered belongs on the same line as the model that
// answered, for the same reason: the run request says what we asked for, and
// only the session-init event says what we got.
//
// Measured on dev1 while fixing #1932 — three different versions in play at
// once: the adapter was validated against 2.1.126, the container had 2.1.204,
// and the host had 2.1.226. Nothing in a run said so, which is how a --bare
// tool clamp went unnoticed. One greppable line per run closes that.
func TestLogResolvedModel_RecordsTheCLIThatAnswered(t *testing.T) {
	cases := []struct {
		name         string
		version      string
		apiKeySource string
		wantFields   map[string]any
		wantAbsent   []string
	}{
		{
			name:         "version and auth source recorded",
			version:      "2.1.204",
			apiKeySource: "none",
			wantFields:   map[string]any{"cli_version": "2.1.204", "api_key_source": "none"},
		},
		{
			// Non-Claude adapters report neither, and an older Claude Code
			// reports no apiKeySource. Absent must stay absent rather than
			// logging an empty string that reads as a real value.
			name:       "adapter reports neither",
			wantAbsent: []string{"cli_version", "api_key_source"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logResolvedModel(captureSlog(&buf), "agent-1", "claude-haiku-4-5", "claude-haiku-4-5",
				tc.version, tc.apiKeySource)

			lines := decodeLogLines(t, &buf)
			if len(lines) == 0 {
				t.Fatal("nothing logged")
			}
			rec := lines[0]
			for k, want := range tc.wantFields {
				if got := rec[k]; got != want {
					t.Errorf("%q = %v, want %v", k, got, want)
				}
			}
			for _, k := range tc.wantAbsent {
				if v, ok := rec[k]; ok {
					t.Errorf("%q = %v on a run that never reported it", k, v)
				}
			}
		})
	}
}
