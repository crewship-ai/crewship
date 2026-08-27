package server

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/localfs"
)

// The READ half of the #922 container replay, at the handler.
//
// The save path has re-routed through the container on EACCES since #922; the
// read path never did, so `List` succeeded on the 0755 directory while every
// entry inside it answered "file not found". The unit that maps storage keys to
// container paths is pinned in routes_files_container_test.go — what was
// untested is the handler branch that decides between 200, 403, 404 and 409,
// and that distinction is the entire point of the change: "the bytes are not
// there" and "the bytes are there and nobody could hand them over" must not be
// the same answer.

// permReadStorage delegates to a real localfs but forces an EACCES on Read for
// one key — the mirror of permOverwriteStorage, reproducing a file the
// container created at 0600 owned by UID 1001 without needing root to chown.
type permReadStorage struct {
	provider.StorageProvider
	failKey string
}

func (p *permReadStorage) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	if path == p.failKey {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	}
	return p.StorageProvider.Read(ctx, path)
}

// replayContainer answers the read exec with a fixed body and exit code. The
// script signals everything through the exit code — nothing may be printed,
// because the docker provider demuxes stdout and stderr into one pipe and a
// diagnostic would land in the middle of the file.
type replayContainer struct {
	*mockContainer
	stdout   string
	exitCode int
	execErr  error
	gotCfg   provider.ExecConfig
	execs    int
}

func (c *replayContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if c.execErr != nil {
		return nil, c.execErr
	}
	c.execs++
	c.gotCfg = cfg
	return &provider.ExecResult{ExecID: "e1", Reader: io.NopCloser(strings.NewReader(c.stdout))}, nil
}

func (c *replayContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, c.exitCode, nil
}

const replayKey = "crews/crewX/shared/scripts/parse_check.sh"

func downloadShared(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/crews/crewX/files/download?path=shared/scripts/parse_check.sh", nil)
	req.SetPathValue("id", "crewX")
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	return rec
}

// A readable file never reaches the container at all.
func TestHandleFileDownload_HostReadNeverReplays(t *testing.T) {
	base, _ := localfs.New(t.TempDir())
	if err := base.Write(context.Background(), replayKey, strings.NewReader("echo hi\n")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctr := &replayContainer{mockContainer: &mockContainer{}}
	s := newContainerFallbackServer(t, base, ctr)

	rec := downloadShared(t, s)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "echo hi\n" {
		t.Errorf("body = %q, want the host bytes", rec.Body.String())
	}
	if ctr.execs != 0 {
		t.Errorf("replayed through the container %d times; the host read succeeded", ctr.execs)
	}
}

// The regression: EACCES host-side must come back as the file, via the
// container, as UID 1001 — not as 404.
func TestHandleFileDownload_PermissionDeniedReplaysThroughContainer(t *testing.T) {
	base, _ := localfs.New(t.TempDir())
	stor := &permReadStorage{StorageProvider: base, failKey: replayKey}
	ctr := &replayContainer{mockContainer: &mockContainer{}, stdout: "echo replayed\n", exitCode: 0}
	s := newContainerFallbackServer(t, stor, ctr)

	rec := downloadShared(t, s)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "echo replayed\n" {
		t.Errorf("body = %q, want the container bytes", rec.Body.String())
	}
	if got := ctr.gotCfg.User; got != "1001:1001" {
		t.Errorf("exec user = %q, want 1001:1001 — the UID that owns the tree", got)
	}
	// The fence is what keeps the replay inside the crew's own subtree.
	if !containsEnv(ctr.gotCfg.Env, "FENCE=/crew/shared") {
		t.Errorf("exec env = %v, want a FENCE inside the shared tree", ctr.gotCfg.Env)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q — the replay must set the download headers too", ct)
	}
}

// Exit 4 is the script's "no such file". That one IS a 404.
func TestHandleFileDownload_ContainerSaysMissingIs404(t *testing.T) {
	base, _ := localfs.New(t.TempDir())
	stor := &permReadStorage{StorageProvider: base, failKey: replayKey}
	ctr := &replayContainer{mockContainer: &mockContainer{}, exitCode: 4}
	s := newContainerFallbackServer(t, stor, ctr)

	if rec := downloadShared(t, s); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a file the container cannot find", rec.Code)
	}
}

// Exit 3 is the fence refusing a path that resolved outside the subtree. It is
// emphatically not a 404 — the caller asked for something they may not have.
func TestHandleFileDownload_FenceRefusalIsNot404(t *testing.T) {
	base, _ := localfs.New(t.TempDir())
	stor := &permReadStorage{StorageProvider: base, failKey: replayKey}
	ctr := &replayContainer{mockContainer: &mockContainer{}, exitCode: 3}
	s := newContainerFallbackServer(t, stor, ctr)

	rec := downloadShared(t, s)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("a fence refusal came back as 404 — that is the lie this change removed")
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

// A container that cannot be reached is 409, not 404: the bytes are there and
// nobody could hand them over.
func TestHandleFileDownload_ContainerUnavailableIs409(t *testing.T) {
	base, _ := localfs.New(t.TempDir())
	stor := &permReadStorage{StorageProvider: base, failKey: replayKey}
	ctr := &replayContainer{mockContainer: &mockContainer{}, execErr: context.DeadlineExceeded}
	s := newContainerFallbackServer(t, stor, ctr)

	rec := downloadShared(t, s)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unavailable") {
		t.Errorf("body = %q, want it to say the container is unavailable", rec.Body.String())
	}
}

// A nil container provider is a supported state (handleContainerStatus reports
// it as "not_configured"). resolveCrewContainer dereferences s.container, so
// without a guard this panics the request goroutine.
func TestHandleFileDownload_NilContainerProviderDoesNotPanic(t *testing.T) {
	base, _ := localfs.New(t.TempDir())
	stor := &permReadStorage{StorageProvider: base, failKey: replayKey}
	s := newContainerFallbackServer(t, stor, nil)

	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("nil container provider panicked: %v", p)
		}
	}()

	if rec := downloadShared(t, s); rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 with no container configured", rec.Code)
	}
}

// A file past the buffer ceiling must not be served as if it were whole. The
// replay buffers (the exit code is only knowable after the stream ends), so the
// cap is real and truncation would be silent corruption.
func TestHandleFileDownload_OversizedReplayIsRefused(t *testing.T) {
	base, _ := localfs.New(t.TempDir())
	stor := &permReadStorage{StorageProvider: base, failKey: replayKey}
	ctr := &replayContainer{
		mockContainer: &mockContainer{},
		stdout:        strings.Repeat("A", maxCrewFileReadBytes+1),
		exitCode:      0,
	}
	s := newContainerFallbackServer(t, stor, ctr)

	rec := downloadShared(t, s)
	if rec.Code == http.StatusOK {
		t.Fatalf("an oversized replay was served as 200 — truncated content would be silent corruption")
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

// A host read that failed for any reason OTHER than permission keeps the
// historical 404 and never spends a container exec on it.
func TestHandleFileDownload_MissingHostFileDoesNotReplay(t *testing.T) {
	base, _ := localfs.New(t.TempDir())
	ctr := &replayContainer{mockContainer: &mockContainer{}}
	s := newContainerFallbackServer(t, base, ctr)

	if rec := downloadShared(t, s); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a genuinely absent file", rec.Code)
	}
	if ctr.execs != 0 {
		t.Errorf("spent %d container execs on an absent file", ctr.execs)
	}
}

func containsEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}
