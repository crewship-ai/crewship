package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeDockerOps is a deterministic in-memory DockerOps implementation for
// the self-test unit. It simulates a crew container's /workspace as a
// plain map[name]content: CopyTo untars the stream and merges files in;
// CopyFrom serialises a named entry as a one-shot tar; Pause/Unpause are
// no-ops; Exec only understands `rm -f <path>` so the orchestrator can
// destroy the canary. Anything else returns an error — we want a loud
// failure if the self-test starts depending on unimplemented behaviour.
type fakeDockerOps struct {
	exists    bool
	workspace map[string][]byte // filename → content (relative to /workspace)
	paused    bool

	copyToErr   error
	copyFromErr error
	execErr     error

	copyFromCalls int
	execCalls     int
}

func newFakeDockerOps() *fakeDockerOps {
	return &fakeDockerOps{
		exists:    true,
		workspace: map[string][]byte{},
	}
}

func (f *fakeDockerOps) Pause(_ context.Context, _ string) error {
	f.paused = true
	return nil
}

func (f *fakeDockerOps) Unpause(_ context.Context, _ string) error {
	f.paused = false
	return nil
}

func (f *fakeDockerOps) ContainerExists(_ context.Context, _ string) (bool, error) {
	return f.exists, nil
}

// CopyFrom returns either a tar of the whole /workspace dir (for
// CollectCrew) or a tar containing a single named file (for readCanary).
// Other paths (/home/agent, /opt/crew-tools, /output) are reported as
// missing so CollectCrew's silent-skip branch runs.
func (f *fakeDockerOps) CopyFrom(_ context.Context, _ string, srcPath string) (io.ReadCloser, error) {
	if f.copyFromErr != nil {
		return nil, f.copyFromErr
	}
	f.copyFromCalls++

	// CollectCrew asks for /workspace, /home/agent, /opt/crew-tools,
	// /output in that order. We only have workspace; everything else
	// reports a NotFound-equivalent error that isNotFoundErr recognises.
	if srcPath == ContainerWorkspacePath {
		return io.NopCloser(bytes.NewReader(f.workspaceTar())), nil
	}
	if strings.HasPrefix(srcPath, ContainerWorkspacePath+"/") {
		// readCanary asks for a specific file — extract by basename.
		name := strings.TrimPrefix(srcPath, ContainerWorkspacePath+"/")
		content, ok := f.workspace[name]
		if !ok {
			return nil, fmt.Errorf("No such container:path: %s", srcPath)
		}
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), ModTime: time.Now()})
		_, _ = tw.Write(content)
		_ = tw.Close()
		return io.NopCloser(&buf), nil
	}
	// Any other path: NotFound (triggers collector silent-skip).
	return nil, fmt.Errorf("No such container:path: %s", srcPath)
}

// workspaceTar serialises the current workspace map as a tar archive. The
// entry names use "./" prefix to match what the real Docker API emits so
// RepackTar's TrimPrefix path is exercised.
func (f *fakeDockerOps) workspaceTar() []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// Directory entry first for robustness.
	_ = tw.WriteHeader(&tar.Header{Name: "./", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: time.Now()})
	for name, content := range f.workspace {
		_ = tw.WriteHeader(&tar.Header{
			Name: "./" + name, Mode: 0o644, Size: int64(len(content)), ModTime: time.Now(),
		})
		_, _ = tw.Write(content)
	}
	_ = tw.Close()
	return buf.Bytes()
}

// CopyToVolume mirrors CopyTo for the test fake — selftest only
// touches /workspace, so volume-targeted traffic is silently consumed
// (in production CopyToVolume execs `tar -x` inside the container,
// which we don't model here).
func (f *fakeDockerOps) CopyToVolume(ctx context.Context, containerID, dstPath string, content io.Reader) error {
	return f.CopyTo(ctx, containerID, dstPath, content)
}

// CopyToSystem is the uid-0 variant. The in-memory fake doesn't model
// uid checks, so it routes to the same merge path.
func (f *fakeDockerOps) CopyToSystem(ctx context.Context, containerID, dstPath string, content io.Reader) error {
	return f.CopyTo(ctx, containerID, dstPath, content)
}

// CopyTo merges incoming tar entries into the workspace map.
//
// Models two callers:
//  1. BackupSelfTest writes the canary via CopyTo(/workspace, tar) where
//     the tar carries a flat entry name like "CANARY-abc.txt". Resolved
//     path: /workspace/CANARY-abc.txt → workspace["CANARY-abc.txt"].
//  2. RestoreCrew (post-fix) calls CopyTo(parent, rewrappedTar) where
//     the tar's entries each start with the section basename
//     ("workspace/", "agent/", "output/", "crew-tools/"). Only the
//     "workspace/" branch is modelled; the others are silently
//     consumed because the fake CollectCrew never produces them.
//
// dstPath is the absolute container path the SDK would receive. The
// effective container path of an entry is dstPath + "/" + entryName,
// then we strip ContainerWorkspacePath to get the workspace-relative
// key.
func (f *fakeDockerOps) CopyTo(_ context.Context, _ string, dstPath string, content io.Reader) error {
	if f.copyToErr != nil {
		return f.copyToErr
	}
	tr := tar.NewReader(content)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		entry := strings.TrimPrefix(hdr.Name, "./")
		// Compose the absolute container path the entry would land at.
		full := dstPath
		if !strings.HasSuffix(full, "/") {
			full += "/"
		}
		full += entry
		// Only record entries that land inside /workspace.
		if rest, ok := strings.CutPrefix(full, ContainerWorkspacePath+"/"); ok {
			f.workspace[rest] = body
		}
	}
}

// Exec understands `rm -f <path>` and nothing else. We deliberately don't
// stub more commands — a mismatch means the orchestrator has started
// using another shell invocation we haven't sanctioned, and the unit
// test should catch that explicitly.
func (f *fakeDockerOps) Exec(_ context.Context, _ string, cmd []string) (int, []byte, error) {
	if f.execErr != nil {
		return -1, nil, f.execErr
	}
	f.execCalls++
	if len(cmd) == 3 && cmd[0] == "rm" && cmd[1] == "-f" {
		path := cmd[2]
		name := strings.TrimPrefix(path, ContainerWorkspacePath+"/")
		delete(f.workspace, name)
		return 0, nil, nil
	}
	return -1, nil, fmt.Errorf("fakeDockerOps.Exec: unsupported cmd %v", cmd)
}

// -- tests ----------------------------------------------------------------

func TestBackupSelfTest_Happy(t *testing.T) {
	ops := newFakeDockerOps()
	res, err := BackupSelfTest(context.Background(), ops, SelfTestOpts{
		ContainerID: "ctr-1",
		Crew:        CrewTarget{ID: "crew-1", Slug: "research", ContainerID: "ctr-1"},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.OK {
		t.Fatalf("want OK=true, got error: %s", res.Error)
	}
	if res.BundleBytes == 0 {
		t.Errorf("BundleBytes = 0, expected non-zero")
	}
	if res.CanaryBytes == 0 {
		t.Errorf("CanaryBytes = 0")
	}
	// Cleanup step overwrites the canary with zero bytes. The file may
	// still exist in the workspace map, but its content must be empty.
	for name, content := range ops.workspace {
		if strings.HasPrefix(name, "CANARY-") && len(content) != 0 {
			t.Errorf("canary %q left with %d bytes after cleanup", name, len(content))
		}
	}
	// We deliberately avoid Exec — /workspace bind-mount permissions make
	// `rm` flaky across runtimes. Any exec call means a regression back
	// to the destroy-canary approach.
	if ops.execCalls != 0 {
		t.Errorf("exec called %d times, want 0 (self-test should use CopyTo only)", ops.execCalls)
	}
}

func TestBackupSelfTest_ContainerMissing(t *testing.T) {
	ops := newFakeDockerOps()
	ops.exists = false
	res, err := BackupSelfTest(context.Background(), ops, SelfTestOpts{
		ContainerID: "ctr-ghost",
		Crew:        CrewTarget{ID: "crew-1", Slug: "research", ContainerID: "ctr-ghost"},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.OK {
		t.Errorf("want OK=false for missing container")
	}
	if !strings.Contains(res.Error, "container not found") {
		t.Errorf("err message = %q, want container-not-found hint", res.Error)
	}
}

func TestBackupSelfTest_RejectsEmptyInputs(t *testing.T) {
	ctx := context.Background()
	ops := newFakeDockerOps()
	cases := []struct {
		name string
		opts SelfTestOpts
	}{
		{"empty container", SelfTestOpts{Crew: CrewTarget{Slug: "x"}}},
		{"empty slug", SelfTestOpts{ContainerID: "c", Crew: CrewTarget{}}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if _, err := BackupSelfTest(ctx, ops, c.opts); err == nil {
				t.Errorf("want error, got nil")
			}
		})
	}
	if _, err := BackupSelfTest(ctx, nil, SelfTestOpts{ContainerID: "c", Crew: CrewTarget{Slug: "x"}}); err == nil {
		t.Errorf("nil DockerOps should error")
	}
}
