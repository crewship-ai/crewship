package docker

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/safepath"
)

// prepareCrewDirs reaches os.RemoveAll / os.MkdirAll with paths built from the
// crew ID. EnsureCrewRuntime validates that ID before calling in, but the
// helper is the thing holding the filesystem sinks, so the barrier is pinned
// here: a caller that skips the outer check must not be able to write outside
// OutputBasePath.
func TestPrepareCrewDirs_RejectsUnsafeCrewID(t *testing.T) {
	base := t.TempDir()
	// A sibling of the base that a traversing ID would land in. If the guard
	// ever regresses, RemoveAll would delete this.
	outside := filepath.Join(filepath.Dir(base), "outside-"+filepath.Base(base))
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("seed outside dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })

	p := &Provider{
		cfg:    Config{OutputBasePath: base},
		logger: slog.New(slog.DiscardHandler),
	}

	for _, id := range []string{
		"..",
		"../escape",
		"../" + filepath.Base(outside),
		"nested/child",
		"with\x00nul",
		"",
	} {
		t.Run(strings.ReplaceAll(id, "\x00", "NUL"), func(t *testing.T) {
			_, err := p.prepareCrewDirs(provider.CrewConfig{ID: id, Slug: "slug"})
			if err == nil {
				t.Fatalf("prepareCrewDirs(%q) = nil error, want rejection", id)
			}
			if !errors.Is(err, safepath.ErrUnsafe) {
				t.Errorf("prepareCrewDirs(%q) error = %v, want safepath.ErrUnsafe", id, err)
			}
		})
	}

	if _, err := os.Stat(outside); err != nil {
		t.Errorf("sibling dir outside the base was touched: %v", err)
	}
}

// The happy path still produces the documented tree, all of it under the base.
func TestPrepareCrewDirs_CreatesTreeUnderBase(t *testing.T) {
	base := t.TempDir()
	p := &Provider{
		cfg:    Config{OutputBasePath: base},
		logger: slog.New(slog.DiscardHandler),
	}

	dirs, err := p.prepareCrewDirs(provider.CrewConfig{ID: "crew-abc123", Slug: "slug"})
	if err != nil {
		t.Fatalf("prepareCrewDirs: %v", err)
	}

	want := []string{
		filepath.Join(base, "crew-abc123"),
		filepath.Join(base, "workspaces", "crew-abc123"),
		filepath.Join(base, "crews", "crew-abc123"),
		filepath.Join(base, "crews", "crew-abc123", "shared"),
		filepath.Join(base, "crews", "crew-abc123", "agents"),
	}
	if len(dirs.all) != len(want) {
		t.Fatalf("dirs.all = %v, want %d entries", dirs.all, len(want))
	}
	for i, w := range want {
		if dirs.all[i] != w {
			t.Errorf("dirs.all[%d] = %q, want %q", i, dirs.all[i], w)
		}
		if err := safepath.EnsureInside(base, dirs.all[i]); err != nil {
			t.Errorf("dir %q escapes base: %v", dirs.all[i], err)
		}
		if st, err := os.Stat(w); err != nil || !st.IsDir() {
			t.Errorf("dir %q not created: %v", w, err)
		}
	}
	if dirs.output != want[0] || dirs.workspace != want[1] || dirs.crew != want[2] {
		t.Errorf("crewDirs fields mismatched: %+v", dirs)
	}
}
