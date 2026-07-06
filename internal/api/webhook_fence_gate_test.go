package api

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestIngressFenceGate is the CI lint-gate for issue #808 M0. It forbids raw
// interpolation of the attacker-controlled webhook field payload.Data into any
// non-test source file unless the site is one of the two sanctioned contexts:
//
//   - the ingress trust fence (internal/untrusted.Wrap), which neutralizes the
//     content before it reaches the agent prompt, or
//   - a non-prompt sink that only hashes the field for a dedup key (marked by
//     the \x00 separators the idempotency hash uses).
//
// Any new caller that formats payload.Data straight into a string reopens the
// prompt-injection hole this fence closes, so it must fail the build. When you
// add a legitimate new consumer, route it through untrusted.Wrap (prompt path)
// or extend the sanctioned-marker list here with review.
func TestIngressFenceGate(t *testing.T) {
	root := repoRootForFenceGate(t)

	// Fields that carry untrusted external bytes into prompt assembly. M0
	// gates the webhook payload; M1 (mission Description, crew-context member
	// fields) extends this list once those sites are fenced.
	const forbiddenField = "payload.Data"
	sanctionedMarkers := []string{
		"untrusted.Wrap(", // fenced before reaching the model
		`\x00`,            // hash-only sink (idempotency key), not a prompt
	}

	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip stale worktree copies and vendored trees.
			if d.Name() == ".claude" || d.Name() == "vendor" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if !strings.Contains(line, forbiddenField) {
				continue
			}
			// Build a small statement window (this line plus a few preceding
			// lines) so a fence call whose Wrap( sits on the previous line is
			// still recognized as sanctioned.
			start := i - 3
			if start < 0 {
				start = 0
			}
			ctx := strings.Join(lines[start:i+1], "\n")
			sanctioned := false
			for _, m := range sanctionedMarkers {
				if strings.Contains(ctx, m) {
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
		t.Fatalf("walk repo: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("raw interpolation of %s reopens the ingress trust fence (#808).\n"+
			"Route untrusted external content through internal/untrusted.Wrap before it reaches a prompt.\n"+
			"Offending sites:\n  %s", forbiddenField, strings.Join(violations, "\n  "))
	}
}

// repoRootForFenceGate resolves the module root from this test file's location
// (this file lives at internal/api/, so two levels up is the repo root).
func repoRootForFenceGate(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller for repo root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}
