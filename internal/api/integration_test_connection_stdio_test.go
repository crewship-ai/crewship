package api

import (
	"strings"
	"testing"
)

// validateStdioServer must no longer reject a bare executable at a spaced path,
// while still catching the classic mistake of the whole launch line stuffed
// into the command field. See issue #1132.
func TestValidateStdioServer(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		argsJSON   string
		wantStatus string
	}{
		{"empty command", "", "", "error"},
		{"bare executable", "npx", `["-y","@scope/pkg"]`, "ok"},
		{"spaced bare path is allowed", "/opt/my app/bin/server", "", "ok"},
		{"quoted spaced path is allowed", `"/opt/my app/bin/server"`, "", "ok"},
		{"crammed launch line with flag is rejected", "npx -y @scope/pkg", "", "error"},
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
