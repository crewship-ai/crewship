package orchestrator

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestIngressFenceGate_Orchestrator is the CI lint-gate for issue #808 M1, the
// orchestrator-package twin of the webhook gate M0 added in internal/api. It
// forbids raw interpolation of the known attacker-influenceable prompt fields
// into any non-test orchestrator source unless the value goes through the
// ingress trust fence (internal/untrusted.Wrap / a *Fence.Wrap method).
//
// The webhook payload lives in internal/api; the mission/task and crew-context
// fields live here, so each package guards its own ingress surface. A new call
// site that formats one of these fields straight into a prompt string reopens
// the prompt-injection hole the fence closes, so it must fail the build. When a
// legitimate new consumer appears, route it through untrusted.Wrap.
//
// Precision: a field token alone is not a violation (nil-guards and struct
// assignments are fine). Only a line that BOTH names the field AND carries a
// printf verb — i.e. actually interpolates it into a string — is gated, and
// only when no fence marker sits on that line.
func TestIngressFenceGate_Orchestrator(t *testing.T) {
	root := repoRootForOrchestratorGate(t)
	pkgDir := filepath.Join(root, "internal", "orchestrator")

	// Fields carrying untrusted external bytes into prompt assembly (#808 M1).
	forbiddenFields := []string{
		"task.Description", // mission task body (mission_tasks.go)
		"missionDesc",      // mission goal/description (mission_tasks.go)
		"m.Description",    // crew-member free-text bio (lead.go / peer.go)
	}
	// A line interpolates only if it also carries a printf verb.
	verbs := []string{"%s", "%v", "%q", "%+v", "%[1]s"}
	sanctionedMarkers := []string{
		"untrusted.Wrap(", // package-level fence call
		".Wrap(",          // *Fence.Wrap method (e.g. f.fence.Wrap)
	}

	var violations []string
	err := filepath.WalkDir(pkgDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(data), "\n") {
			field := ""
			for _, f := range forbiddenFields {
				if strings.Contains(line, f) {
					field = f
					break
				}
			}
			if field == "" {
				continue
			}
			interpolates := false
			for _, v := range verbs {
				if strings.Contains(line, v) {
					interpolates = true
					break
				}
			}
			if !interpolates {
				continue // nil-guard, assignment, Scan target — not a prompt sink
			}
			sanctioned := false
			for _, m := range sanctionedMarkers {
				if strings.Contains(line, m) {
					sanctioned = true
					break
				}
			}
			if !sanctioned {
				rel, _ := filepath.Rel(root, path)
				violations = append(violations, rel+":"+strconv.Itoa(i+1)+"  "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk orchestrator: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("raw interpolation of an untrusted ingress field reopens the trust fence (#808 M1).\n"+
			"Route the value through internal/untrusted.Wrap before it reaches a prompt.\n"+
			"Offending sites:\n  %s", strings.Join(violations, "\n  "))
	}
}

// repoRootForOrchestratorGate resolves the module root from this test file's
// location (internal/orchestrator/, so two levels up is the repo root).
func repoRootForOrchestratorGate(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller for repo root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}
