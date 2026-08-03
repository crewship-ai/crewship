package backup

import (
	"bytes"
	"context"
	"io"
	"sort"
	"testing"
)

// captureDockerOps records every srcPath asked of CopyFrom so a test
// can assert which container paths CollectCrew chose for a given
// ScopeLevel. Returns an empty tar from CopyFrom — the collector
// doesn't care about the bytes at this layer.
type captureDockerOps struct {
	pausedCount   int
	unpausedCount int
	sources       []string
}

func (c *captureDockerOps) Pause(ctx context.Context, _ string) error {
	c.pausedCount++
	return nil
}
func (c *captureDockerOps) Unpause(ctx context.Context, _ string) error {
	c.unpausedCount++
	return nil
}
func (c *captureDockerOps) ContainerExists(ctx context.Context, _ string) (bool, error) {
	return true, nil
}
func (c *captureDockerOps) CopyFrom(ctx context.Context, _ string, srcPath string) (io.ReadCloser, error) {
	c.sources = append(c.sources, srcPath)
	// Empty tar so RepackTar consumes EOF cleanly without seeing any
	// entries (we only care about which paths got asked, not what was
	// inside them).
	return io.NopCloser(bytes.NewReader(emptyTar())), nil
}
func (c *captureDockerOps) CopyTo(ctx context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}
func (c *captureDockerOps) CopyToPath(ctx context.Context, _ string, _ ExtractSpec, _ io.Reader) error {
	return nil
}
func (c *captureDockerOps) Exec(ctx context.Context, _ string, _ []string) (int, []byte, error) {
	return 0, nil, nil
}
func (c *captureDockerOps) ExecAs(ctx context.Context, _ string, _ string, _ []string) (int, []byte, error) {
	return 0, nil, nil
}

// emptyTar returns the bytes of a valid empty tar archive (1024
// zero bytes — two zero blocks, the GNU end-of-archive marker).
func emptyTar() []byte {
	return make([]byte, 1024)
}

func TestCollectCrew_ScopeLevels(t *testing.T) {
	cases := []struct {
		name     string
		level    ScopeLevel
		wantSrcs []string
	}{
		{
			name:  "quick: workspace + crew memory + output only",
			level: ScopeLevelQuick,
			wantSrcs: []string{
				ContainerWorkspacePath,
				ContainerCrewPath,
				ContainerOutputPath,
			},
		},
		{
			name:  "standard: + named volumes",
			level: ScopeLevelStandard,
			wantSrcs: []string{
				ContainerWorkspacePath,
				ContainerCrewPath,
				ContainerOutputPath,
				ContainerHomePath,
				ContainerToolsPath,
			},
		},
		{
			name:  "full: + /var/lib system data",
			level: ScopeLevelFull,
			wantSrcs: []string{
				ContainerWorkspacePath,
				ContainerCrewPath,
				ContainerOutputPath,
				ContainerHomePath,
				ContainerToolsPath,
				ContainerVarLibPath,
			},
		},
		{
			name:  "empty resolves to standard (back-compat)",
			level: "",
			wantSrcs: []string{
				ContainerWorkspacePath,
				ContainerCrewPath,
				ContainerOutputPath,
				ContainerHomePath,
				ContainerToolsPath,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ops := &captureDockerOps{}
			var buf bytes.Buffer
			tw, err := NewTarZstWriter(&buf)
			if err != nil {
				t.Fatalf("writer: %v", err)
			}
			defer tw.Close()
			capture, err := CollectCrew(context.Background(), ops, tw,
				CrewTarget{ID: "c1", Slug: "research", ContainerID: "c1-container"},
				tc.level)
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			// Every source returned an empty tar, so the capture must
			// report zero entries everywhere — the manifest is derived
			// from what actually landed, not from which paths were asked.
			if capture.Slug != "research" {
				t.Errorf("capture slug = %q, want research", capture.Slug)
			}
			if capture.WorkspaceFiles+capture.CrewFiles+capture.OutputFiles+
				capture.HomeFiles+capture.ToolsFiles+capture.VarLibFiles != 0 {
				t.Errorf("empty tars must produce a zero capture, got %+v", capture)
			}
			gotSorted := append([]string(nil), ops.sources...)
			wantSorted := append([]string(nil), tc.wantSrcs...)
			sort.Strings(gotSorted)
			sort.Strings(wantSorted)
			if len(gotSorted) != len(wantSorted) {
				t.Fatalf("source count: got %d %v, want %d %v",
					len(gotSorted), gotSorted, len(wantSorted), wantSorted)
			}
			for i := range gotSorted {
				if gotSorted[i] != wantSorted[i] {
					t.Errorf("source %d: got %q, want %q", i, gotSorted[i], wantSorted[i])
				}
			}
			if ops.pausedCount != 1 || ops.unpausedCount != 1 {
				t.Errorf("pause/unpause count: paused=%d unpaused=%d (each must be 1)",
					ops.pausedCount, ops.unpausedCount)
			}
		})
	}
}

func TestScopeLevel_Valid(t *testing.T) {
	for _, ok := range []ScopeLevel{ScopeLevelQuick, ScopeLevelStandard, ScopeLevelFull} {
		if !ok.Valid() {
			t.Errorf("%q.Valid() = false, want true", ok)
		}
	}
	for _, bad := range []ScopeLevel{"", "minimal", "Full", "QUICK", "everything"} {
		if bad.Valid() {
			t.Errorf("%q.Valid() = true, want false", bad)
		}
	}
}
