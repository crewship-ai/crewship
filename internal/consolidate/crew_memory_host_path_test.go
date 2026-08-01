package consolidate

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
)

// hostPathForContainerPath translates an absolute path as seen INSIDE a
// crew container into the host path it actually resolves to, modelling
// the one bind mount that matters here: the docker provider mounts host
// {OutputBasePath}/crews/{crewID} at /crew (buildMounts in
// internal/provider/docker/docker.go, host dir from prepareCrewDirs in
// docker_container.go).
//
// The tests below deliberately go the long way round — container path
// first, then translate — because that is the direction the bug ran:
// something computed a container path and handed it to a host-side
// os.MkdirAll.
func hostPathForContainerPath(t *testing.T, basePath, crewID, containerPath string) string {
	t.Helper()
	if !strings.HasPrefix(containerPath, "/crew/") {
		t.Fatalf("hostPathForContainerPath: %q is not under the /crew bind", containerPath)
	}
	rel := strings.TrimPrefix(containerPath, "/crew/")
	return filepath.Join(basePath, "crews", crewID, filepath.FromSlash(rel))
}

// TestConsolidatorPins_LandWhereThePromptBuilderReadsThem is the
// assertion #1663 turns on: a pin snapshotted by the consolidator (a
// HOST process) must be readable at the path the boot-prompt builder
// actually cats inside the container.
//
// Before the fix the runner joined its output dir onto the container
// literal "/crew/shared/.memory", so pins.md was created at the host
// filesystem root — inside no bind source, and therefore invisible to
// every container. The [PINS] block documented at
// internal/orchestrator/memory.go as "an operator-pinned fact is always
// in context" was empty in every host-run crewshipd, which is every
// normal deployment.
func TestConsolidatorPins_LandWhereThePromptBuilderReadsThem(t *testing.T) {
	t.Setenv("CREWSHIP_CONSOLIDATE_HITL", "")
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	// openDB seeds crew_test / crew-test in ws_test.
	const (
		crewID   = "crew_test"
		crewSlug = "crew-test"
		needle   = "pins-host-path-canary"
	)
	seedPriorityEntry(t, db, "j_pin_host", crewID, journal.PriorityPin, needle)

	basePath := t.TempDir()
	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: "[]"}, Logger: quietLogger()}

	if err := consolidateAllCrews(context.Background(), db, c, applyDefaults(RunnerOptions{
		StorageBasePath:    basePath,
		ConsolidationSince: time.Hour,
		Logger:             quietLogger(),
	})); err != nil {
		t.Fatalf("consolidateAllCrews: %v", err)
	}

	// The container path orchestrator.buildPinsBlock reads, derived from
	// the shared constant rather than re-typed, so a change on the read
	// side moves this assertion with it.
	containerPins := path.Join(memory.ContainerCrewTopicsDir(crewSlug), "pins.md")
	hostPins := hostPathForContainerPath(t, basePath, crewID, containerPins)

	body, err := os.ReadFile(hostPins)
	if err != nil {
		t.Fatalf(`pins.md is not readable at the path the prompt builder reads.
  container path (buildPinsBlock cats this): %s
  host path it resolves to via the /crew bind: %s
  err: %v
This is #1663: the consolidator runs on the host but joined its output onto a
container-absolute root, so the [PINS] block reads a file nobody wrote.`,
			containerPins, hostPins, err)
	}
	if !strings.Contains(string(body), needle) {
		t.Errorf("pins.md at %s does not carry the pinned entry:\n%s", hostPins, body)
	}
}

// TestConsolidatorLearnedRules_LandInsideTheCrewBindSource is the same
// guarantee for the consolidator's other two outputs — the canonical
// learned-*.md and the .proposed/ staging dir — which share the output
// dir with pins.md and so shared its escape.
func TestConsolidatorLearnedRules_LandInsideTheCrewBindSource(t *testing.T) {
	t.Setenv("CREWSHIP_CONSOLIDATE_HITL", "")
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	const (
		crewID   = "crew_test"
		crewSlug = "crew-test"
	)
	ids := seedEntries(t, db, w, "ws_test", crewID, 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"host-rooted pattern","action":"host-rooted action","evidence":["` +
		ids[0] + `","` + ids[1] + `"],"confidence":0.9}]`
	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: reply}, Logger: quietLogger()}

	basePath := t.TempDir()
	if err := consolidateAllCrews(context.Background(), db, c, applyDefaults(RunnerOptions{
		StorageBasePath:    basePath,
		ConsolidationSince: time.Hour,
		Logger:             quietLogger(),
	})); err != nil {
		t.Fatalf("consolidateAllCrews: %v", err)
	}

	hostTopics := hostPathForContainerPath(t, basePath, crewID, memory.ContainerCrewTopicsDir(crewSlug))
	entries, err := os.ReadDir(hostTopics)
	if err != nil {
		t.Fatalf("learned output dir %s (the host side of %s) does not exist: %v",
			hostTopics, memory.ContainerCrewTopicsDir(crewSlug), err)
	}
	var learned string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "learned-") && strings.HasSuffix(e.Name(), ".md") {
			learned = filepath.Join(hostTopics, e.Name())
		}
	}
	if learned == "" {
		t.Fatalf("no learned-*.md under %s, only %v", hostTopics, entries)
	}
	body, _ := os.ReadFile(learned)
	if !strings.Contains(string(body), "host-rooted pattern") {
		t.Errorf("learned file %s missing the rule:\n%s", learned, body)
	}
}

// TestRunnerOutputDir_ResolvesInsideTheCrewBindSource replaces the
// sentinel that used to live in learned_rules_output_root_test.go. That
// test asserted the broken state — that the consolidator's output dir
// escaped every bind source — so it can no longer stand; this is its
// positive twin, keeping the same contrast against userModelPathsFor,
// which is the resolver the package already got right.
func TestRunnerOutputDir_ResolvesInsideTheCrewBindSource(t *testing.T) {
	basePath := t.TempDir()
	const (
		crewID   = "ckcrew_123"
		crewSlug = "alpha-crew"
	)

	// The bind source, derived by the package's own long-correct helper.
	bindSource := userModelPathsFor(basePath, crewID).SharedDir
	if want := filepath.Join(basePath, "crews", crewID, "shared", ".memory"); bindSource != want {
		t.Fatalf("control failed: userModelPathsFor no longer resolves the crew bind source (got %q, want %q)", bindSource, want)
	}

	outputDir, err := memory.HostCrewTopicsDir(basePath, crewID, crewSlug)
	if err != nil {
		t.Fatalf("HostCrewTopicsDir: %v", err)
	}

	rel, err := filepath.Rel(bindSource, outputDir)
	if err != nil {
		t.Fatalf("filepath.Rel(%q, %q): %v", bindSource, outputDir, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf(`consolidator output dir escapes the crew bind source again (#1663):
  output %q is %q relative to bind source %q`, outputDir, rel, bindSource)
	}
	if want := filepath.Join(crewSlug, "topics"); rel != want {
		t.Errorf("output dir offset inside the bind source = %q, want %q — buildPinsBlock reads /crew/shared/.memory/{slug}/topics/pins.md", rel, want)
	}

	// The keying asymmetry the old sentinel flagged: the bind source is
	// keyed by crew ID, so the output dir must be too.
	if !strings.Contains(outputDir, crewID) {
		t.Errorf("output dir %q is not keyed by crew id — a per-crew tree cannot be derived from a global root", outputDir)
	}
}

// TestRunnerDefaults_NoContainerAbsoluteMemoryRoot: applyDefaults must
// not invent a root. There is no safe default for a path that only
// exists relative to configured storage — an unconfigured runner has to
// skip, not guess.
func TestRunnerDefaults_NoContainerAbsoluteMemoryRoot(t *testing.T) {
	got := applyDefaults(RunnerOptions{})
	if got.StorageBasePath != "" {
		t.Errorf("applyDefaults invented StorageBasePath = %q; it must come from cfg.Storage.BasePath", got.StorageBasePath)
	}
	if strings.HasPrefix(got.StorageBasePath, "/crew") {
		t.Errorf("StorageBasePath defaulted to a container path %q — that is #1663", got.StorageBasePath)
	}
}

// TestConsolidateAllCrews_SkipsWhenStorageUnconfigured: with no base
// path there is nowhere legitimate to write, and the pre-fix behaviour
// (fall back to a container literal and MkdirAll at the host root) is
// exactly what must not happen. Skipping is the correct failure.
func TestConsolidateAllCrews_SkipsWhenStorageUnconfigured(t *testing.T) {
	t.Setenv("CREWSHIP_CONSOLIDATE_HITL", "")
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	seedPriorityEntry(t, db, "j_pin_nobase", "crew_test", journal.PriorityPin, "nowhere to go")

	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: "[]"}, Logger: quietLogger()}
	err := consolidateAllCrews(context.Background(), db, c, applyDefaults(RunnerOptions{
		ConsolidationSince: time.Hour,
		Logger:             quietLogger(),
	}))
	if err == nil {
		t.Fatal("expected an error when no storage base path is configured — silently writing to a container-absolute path is the bug")
	}
	if !strings.Contains(err.Error(), "crew_test") {
		t.Errorf("error should name the crew it skipped, got: %v", err)
	}
	// Nothing was created at the host root.
	if _, statErr := os.Stat("/crew/shared/.memory"); statErr == nil {
		t.Errorf("/crew/shared/.memory exists on this host — a consolidator run created it")
	}
}
