package api

import (
	"strings"
	"testing"
)

// validateStdioServer must no longer reject a bare executable at a spaced path
// as long as it is quoted (so it shlex-parses to a single token), while still
// catching the classic mistake of the whole launch line stuffed into the
// command field — whether or not the crammed line happens to carry a flag
// token. See issue #1132, and the crammed-command hardening in #1140 (a
// command like "uvx some-pkg" or "python script.py" has no leading dash but
// is still two tokens, and must be rejected the same way).
func TestValidateStdioServer(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		argsJSON   string
		wantStatus string
	}{
		{"empty command", "", "", "error"},
		{"bare executable", "npx", `["-y","@scope/pkg"]`, "ok"},
		{"unquoted spaced path is rejected as crammed", "/opt/my app/bin/server", "", "error"},
		{"quoted spaced path is allowed", `"/opt/my app/bin/server"`, "", "ok"},
		{"crammed launch line with flag is rejected", "npx -y @scope/pkg", "", "error"},
		{"crammed launch line without a flag is rejected", "uvx some-pkg", "", "error"},
		{"crammed launch line with script arg is rejected", "python script.py", "", "error"},
		{"invalid args_json", "npx", `{not an array}`, "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateStdioServer(tt.command, tt.argsJSON)
			if got.Status != tt.wantStatus {
				t.Errorf("status = %q (%s), want %q", got.Status, strings.TrimSpace(got.Message), tt.wantStatus)
			}
		})
	}
}
