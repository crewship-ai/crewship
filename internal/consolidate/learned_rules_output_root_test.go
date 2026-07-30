package consolidate

import (
	"path/filepath"
	"strings"
	"testing"
)

// Sentinel: the runner's default output root for learned-*.md is a
// CONTAINER-absolute path used by a HOST process, so the canonical file
// the approve path writes never appears inside the crew container.
//
// The chain:
//
//   - internal/server/server_lifecycle.go:277 calls StartBackground with
//     RunnerOptions{BlobRoot: …} and leaves CrewMemoryRoot empty, so
//     applyDefaults (runner.go:179-181) sets it to "/crew/shared/.memory".
//     internal/server/server.go:704 hardcodes the same string for the
//     manual /api/v1/consolidate/run path.
//   - runner.go:250 (and consolidate_handler.go:300,
//     post_run_trigger.go:146) then derive
//     OutputDir = {CrewMemoryRoot}/{crewSLUG}/topics, and
//     ApproveProposal appends {OutputDir}/learned-YYYY-MM-DD.md
//     (approve.go:179).
//   - But "/crew" is only a container path: the docker provider binds
//     host {Storage.BasePath}/crews/{crewID} at /crew
//     (internal/provider/docker/docker.go:702 via noexecBindMount,
//     host dir created at docker_container.go:834). The crewship server
//     is a host process, so its writes to "/crew/shared/.memory/…" land
//     at the host filesystem root, outside every bind source — and keyed
//     by crew SLUG where the bind source is keyed by crew ID.
//
// The same package already does this correctly for user models:
// userModelPathsFor (user_model_worker.go:154-164) resolves
// {basePath}/crews/{crewID}/shared/.memory from the configured storage
// base. This test contrasts the two so the divergence is impossible to
// miss.
//
// A fix means resolving CrewMemoryRoot from cfg.Storage.BasePath the way
// userModelPathsFor does (and keying by crew ID, not slug) — a change
// that relocates every existing proposal/canonical file, hence out of
// scope for a documenting test. When it lands, this sentinel trips.
func TestDefaultCrewMemoryRoot_IsContainerPathOutsideEveryBindSource(t *testing.T) {
	got := applyDefaults(RunnerOptions{}).CrewMemoryRoot

	// Control: the value this sentinel reasons about.
	const containerRoot = "/crew/shared/.memory"
	if got != containerRoot {
		t.Fatalf(`default CrewMemoryRoot changed: got %q, want %q.
If it is now derived from Storage.BasePath, GAP 0 is closed — replace this sentinel
with a positive test that the canonical learned-*.md lands inside the /crew bind source.`, got, containerRoot)
	}

	// Stand in for a real deployment: basePath is cfg.Storage.BasePath
	// (~/.crewship/output in production), crewID the crew's row id.
	basePath := t.TempDir()
	const crewID = "ckcrew_123"
	const crewSlug = "alpha-crew"

	// What /crew/shared/.memory actually maps to on the host, per the
	// docker provider's bind source. Derived here the same way the
	// package's own correct resolver does it.
	bindSource := userModelPathsFor(basePath, crewID).SharedDir
	if want := filepath.Join(basePath, "crews", crewID, "shared", ".memory"); bindSource != want {
		t.Fatalf("control failed: userModelPathsFor no longer resolves the crew bind source (got %q, want %q)", bindSource, want)
	}

	// Where the runner actually writes learned-*.md (runner.go:250).
	outputDir := filepath.Join(got, crewSlug, "topics")

	rel, err := filepath.Rel(bindSource, outputDir)
	if err != nil {
		t.Fatalf("filepath.Rel(%q, %q): %v", bindSource, outputDir, err)
	}
	escapes := rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
	if !escapes {
		t.Fatalf(`SENTINEL TRIPPED (GAP 0 closed): the consolidator's output dir now resolves INSIDE the
crew bind source (%q is %q relative to %q). Learned rules can now appear in the container —
update internal/orchestrator/learned_rules_not_delivered_test.go's doc comment and re-check
whether anything reads them.`, outputDir, rel, bindSource)
	}

	// The keying asymmetry is the second half of the mismatch: the
	// output dir is keyed by slug, the bind source by crew id.
	if strings.Contains(outputDir, crewID) {
		t.Errorf("output dir now keyed by crew id (%q) — half of the mismatch is fixed; re-derive this sentinel", outputDir)
	}
}
