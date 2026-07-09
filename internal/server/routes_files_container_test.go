package server

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/localfs"
)

// crewSharedContainerPath maps storage keys to /crew paths — pinned as a pure
// unit so the byte-for-byte mapping the runner relies on can't drift.
func TestCrewSharedContainerPath(t *testing.T) {
	cases := []struct {
		crewID, key, want string
		ok                bool
	}{
		{"c1", "crews/c1/shared/scripts/x.sh", "/crew/shared/scripts/x.sh", true},
		{"c1", "crews/c1/shared", "/crew/shared", true},
		{"c1", "c1/report.txt", "", false},           // /output tree — not shared
		{"c1", "crews/c2/shared/x.sh", "", false},    // other crew
		{"c1", "crews/c1/notshared/x.sh", "", false}, // outside shared subtree
	}
	for _, tc := range cases {
		got, ok := crewSharedContainerPath(tc.crewID, tc.key)
		if ok != tc.ok || got != tc.want {
			t.Errorf("crewSharedContainerPath(%q,%q) = (%q,%v), want (%q,%v)",
				tc.crewID, tc.key, got, ok, tc.want, tc.ok)
		}
	}
}

// permOverwriteStorage delegates to a real localfs but forces an EACCES on
// Write for one key — reproducing the #922 ownership handoff (the bind source
// is chowned to the container UID 1001 after provisioning, so the server UID
// can no longer overwrite it host-side) without needing root to chown.
type permOverwriteStorage struct {
	provider.StorageProvider
	failKey string
}

func (p *permOverwriteStorage) Write(ctx context.Context, path string, r io.Reader) error {
	if path == p.failKey {
		return &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	}
	return p.StorageProvider.Write(ctx, path, r)
}

// recordingContainer captures the Exec the save fallback routes through it.
type recordingContainer struct {
	*mockContainer
	gotCfg   provider.ExecConfig
	gotStdin []byte
	exitCode int
	execErr  error
}

func (c *recordingContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if c.execErr != nil {
		return nil, c.execErr
	}
	c.gotCfg = cfg
	if cfg.Stdin != nil {
		c.gotStdin, _ = io.ReadAll(cfg.Stdin)
	}
	return &provider.ExecResult{ExecID: "e1", Reader: io.NopCloser(strings.NewReader(""))}, nil
}

func (c *recordingContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, c.exitCode, nil
}

func newContainerFallbackServer(t *testing.T, stor provider.StorageProvider, ctr provider.ContainerProvider) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-files-container-32ch"
	logger := logging.New("error", "json", nil)
	db := openTestDB(t)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?,?,?,?)`,
		"crewX", "ws1", "CrewX", "crewx-slug"); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	s := New(cfg, logger, &Deps{Storage: stor, Container: ctr, DB: db})
	t.Cleanup(s.StopBackground)
	s.startedAt = time.Now()
	return s
}

// TestHandleFileSave_OverwriteRoutesThroughContainer is the #922 regression:
// when the host-side overwrite fails with EACCES (bind source owned by the
// crew's UID 1001), the save must re-route through the container as 1001 and
// return 200 — not the old 500.
func TestHandleFileSave_OverwriteRoutesThroughContainer(t *testing.T) {
	base, _ := localfs.New(t.TempDir())
	key := "crews/crewX/shared/scripts/parse_check.sh"
	stor := &permOverwriteStorage{StorageProvider: base, failKey: key}
	ctr := &recordingContainer{mockContainer: &mockContainer{}}
	s := newContainerFallbackServer(t, stor, ctr)

	body := "echo updated\n"
	req := httptest.NewRequest("PUT", "/crews/crewX/files/save?path=shared/scripts/parse_check.sh",
		strings.NewReader(body))
	req.SetPathValue("id", "crewX")
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("overwrite via container: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	// The write must have gone through the container as the tree owner.
	if ctr.gotCfg.User != "1001:1001" {
		t.Errorf("exec User = %q, want 1001:1001", ctr.gotCfg.User)
	}
	if got := string(ctr.gotStdin); got != body {
		t.Errorf("exec stdin = %q, want %q", got, body)
	}
	// The container-absolute destination must be passed via env, not argv.
	var dest string
	for _, e := range ctr.gotCfg.Env {
		if strings.HasPrefix(e, "DEST=") {
			dest = strings.TrimPrefix(e, "DEST=")
		}
	}
	if dest != "/crew/shared/scripts/parse_check.sh" {
		t.Errorf("DEST env = %q, want /crew/shared/scripts/parse_check.sh", dest)
	}
}

// TestHandleFileSave_OverwriteContainerDown: when the crew container is not
// running, the overwrite can't complete — surface a clear 409, not a 500.
func TestHandleFileSave_OverwriteContainerDown(t *testing.T) {
	base, _ := localfs.New(t.TempDir())
	key := "crews/crewX/shared/scripts/parse_check.sh"
	stor := &permOverwriteStorage{StorageProvider: base, failKey: key}
	ctr := &recordingContainer{mockContainer: &mockContainer{}, execErr: errors.New("container not running")}
	s := newContainerFallbackServer(t, stor, ctr)

	req := httptest.NewRequest("PUT", "/crews/crewX/files/save?path=shared/scripts/parse_check.sh",
		strings.NewReader("x"))
	req.SetPathValue("id", "crewX")
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("container-down overwrite: status = %d, want 409, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleFileSave_OverwriteNonzeroExit: a failed container write (non-zero
// exit) must surface as an error, not a false 200.
func TestHandleFileSave_OverwriteNonzeroExit(t *testing.T) {
	base, _ := localfs.New(t.TempDir())
	key := "crews/crewX/shared/scripts/parse_check.sh"
	stor := &permOverwriteStorage{StorageProvider: base, failKey: key}
	ctr := &recordingContainer{mockContainer: &mockContainer{}, exitCode: 1}
	s := newContainerFallbackServer(t, stor, ctr)

	req := httptest.NewRequest("PUT", "/crews/crewX/files/save?path=shared/scripts/parse_check.sh",
		strings.NewReader("x"))
	req.SetPathValue("id", "crewX")
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("non-zero container exit must not report success (got 200)")
	}
}
