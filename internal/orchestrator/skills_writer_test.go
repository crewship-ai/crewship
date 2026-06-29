package orchestrator

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// fakeContainer captures every writeFileViaContainer call as a virtual
// filesystem so the test can assert which files landed. The skills
// writer only ever calls Exec to issue a base64-decoded write, so we
// parse that command back into the (path) it targeted.
type fakeContainer struct {
	mu     sync.Mutex
	writes map[string]string
	failOn map[string]bool
}

func newFakeContainer() *fakeContainer {
	return &fakeContainer{writes: map[string]string{}, failOn: map[string]bool{}}
}

func (f *fakeContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if len(cfg.Cmd) < 3 {
		return nil, errors.New("unexpected cmd")
	}
	script := cfg.Cmd[2]
	pathStart := strings.Index(script, "> ")
	if pathStart < 0 {
		return nil, errors.New("script missing redirect")
	}
	rest := script[pathStart+2:]
	relPath := strings.TrimSpace(strings.SplitN(rest, " ", 2)[0])
	relPath = strings.Trim(relPath, "'\"")

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn[relPath] {
		return nil, errors.New("simulated failure on " + relPath)
	}
	f.writes[relPath] = "<written>"
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(""))}, nil
}

// Stub out the rest of provider.ContainerProvider — only Exec is
// invoked by the skills writer; everything else can be a no-op.
func (f *fakeContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "", nil
}
func (f *fakeContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (f *fakeContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (f *fakeContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (f *fakeContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (f *fakeContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return true, 0, nil
}
func (f *fakeContainer) CrewContainerName(_ string, slug string) string { return "test-" + slug }
func (f *fakeContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

func TestWriteAgentSkills_NoOpWhenEmpty(t *testing.T) {
	t.Parallel()
	fc := newFakeContainer()
	if err := writeAgentSkills(context.Background(), fc, "cid", "/work", nil, nil); err != nil {
		t.Fatalf("expected nil for empty input, got %v", err)
	}
	if len(fc.writes) != 0 {
		t.Errorf("expected no writes, got %d", len(fc.writes))
	}
}

func TestWriteAgentSkills_MaterialisesAllFiveTargets(t *testing.T) {
	t.Parallel()
	fc := newFakeContainer()
	skills := []SkillBundle{{
		Slug:    "pdf-extract",
		Vendor:  "anthropic",
		Content: "---\nname: pdf-extract\n---\n# Body",
	}}
	if err := writeAgentSkills(context.Background(), fc, "cid", "/work", skills, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	wantPaths := []string{
		".claude/skills/pdf-extract/SKILL.md",
		".agents/skills/pdf-extract/SKILL.md",
		".opencode/skills/pdf-extract/SKILL.md",
		".factory/skills/pdf-extract/SKILL.md",
		".cursor/rules/pdf-extract.mdc",
	}
	for _, p := range wantPaths {
		if _, ok := fc.writes[p]; !ok {
			t.Errorf("expected write at %q, did not happen. writes: %v", p, fc.writes)
		}
	}
}

func TestWriteAgentSkills_SkipsBundlesWithEmptySlugOrContent(t *testing.T) {
	t.Parallel()
	fc := newFakeContainer()
	skills := []SkillBundle{
		{Slug: "", Content: "x"},
		{Slug: "y", Content: ""},
		{Slug: "good", Content: "ok"},
	}
	if err := writeAgentSkills(context.Background(), fc, "cid", "/work", skills, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	for path := range fc.writes {
		if !strings.Contains(path, "good") {
			t.Errorf("unexpected write at %q — only 'good' should land", path)
		}
	}
}

func TestWriteAgentSkills_PartialFailureIsNonFatal(t *testing.T) {
	t.Parallel()
	fc := newFakeContainer()
	fc.failOn[".cursor/rules/sk.mdc"] = true
	skills := []SkillBundle{{Slug: "sk", Content: "ok"}}
	if err := writeAgentSkills(context.Background(), fc, "cid", "/work", skills, nil); err != nil {
		t.Fatalf("partial failure should not be fatal: %v", err)
	}
	if len(fc.writes) != 4 {
		t.Errorf("expected 4 successful writes, got %d (%v)", len(fc.writes), fc.writes)
	}
}

func TestWriteAgentSkills_ReturnsErrorWhenAllPathsFail(t *testing.T) {
	t.Parallel()
	fc := newFakeContainer()
	fc.failOn[".claude/skills/sk/SKILL.md"] = true
	fc.failOn[".agents/skills/sk/SKILL.md"] = true
	fc.failOn[".opencode/skills/sk/SKILL.md"] = true
	fc.failOn[".factory/skills/sk/SKILL.md"] = true
	fc.failOn[".cursor/rules/sk.mdc"] = true
	skills := []SkillBundle{{Slug: "sk", Content: "ok"}}
	err := writeAgentSkills(context.Background(), fc, "cid", "/work", skills, nil)
	if err == nil {
		t.Fatal("expected error when every path fails")
	}
}
