package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestSystemInfo_ResourceCapacity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		want  string
	}{
		{"unlimited", 0, "Unlimited"}, {"future finite license", 15, "15"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := covSetupCli5(t)
			stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{"available": false}))
			stub.OnGet("/api/v1/system/license", clitest.JSONResponse(200, map[string]any{
				"edition": "community", "max_crews": tc.limit, "max_members": tc.limit, "max_agents_per_crew": tc.limit,
			}))
			flagFormat = "table"
			var err error
			out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
			if err != nil {
				t.Fatal(err)
			}
			for _, label := range []string{"Max crews:        ", "Max agents/crew:  ", "Max members:      "} {
				if !strings.Contains(out, fmt.Sprintf("%s%s", label, tc.want)) {
					t.Errorf("missing %q capacity %q in output:\n%s", label, tc.want, out)
				}
			}
		})
	}
}
