//go:build linux && !restrictedruntime_live

package restrictedruntime

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAcceptanceTransportAbsentFromReleaseBuild(t *testing.T) {
	// This default-build gate prevents the synthetic TLS rerouting seam from
	// becoming a production network destination option by losing its tag.
	cmd := exec.CommandContext(t.Context(), "go", "list", "-tags=", "-f", `{{join .GoFiles " "}}`, ".")
	source, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list release inputs: %s %v", source, err)
	}
	if strings.Contains(string(source), "broker_acceptance_linux.go") {
		t.Fatal("acceptance TLS seam compiled into release")
	}
}
