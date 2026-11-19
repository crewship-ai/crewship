package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type acceptanceLockProvider struct {
	provider.ContainerProvider
	calls atomic.Int64
	image string
}

func (*acceptanceLockProvider) CrewContainerName(slug, _ string) string { return "unused-test-" + slug }
func (p *acceptanceLockProvider) ResolveCrewMiseLock(_ context.Context, image string, cfg *devcontainer.MiseConfig, bump bool) (*devcontainer.MiseResolution, error) {
	p.calls.Add(1)
	if image != p.image || len(cfg.Env) != 0 {
		return nil, io.ErrUnexpectedEOF
	}
	return &devcontainer.MiseResolution{ImageID: image, Lock: &devcontainer.MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": "native proposal"}}}, nil
}

// Actual CLI subprocess -> real router/auth/workspace/RBAC -> DB-selected image
// -> optional provider capability. Only the Docker builder is a synthetic peer.
func TestAcceptanceRemoteCrewLockResolveRealRouter(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	image := "sha256:" + strings.Repeat("a", 64)
	const token = "crewship_cli_lock_resolver_acceptance_fixture"
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('lock-owner','lock-owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('lock-workspace','Lock','lock-workspace')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('lock-member','lock-workspace','lock-owner','OWNER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('lock-crew','lock-workspace','Lock','lock-crew')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('lock-token','lock-owner','fixture',?,'STANDARD','["crews:*"]')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE crews SET cached_image=? WHERE id='lock-crew'`, image); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO environment_revisions(id,workspace_id,crew_id,definition_hash,build_hash,image_id,toolchain_json,created_at) VALUES('lock-revision','lock-workspace','lock-crew','definition','build',?,'null',CURRENT_TIMESTAMP)`, image); err != nil {
		t.Fatal(err)
	}
	p := &acceptanceLockProvider{image: image}
	router, err := api.NewRouter(db, "lock-resolver-synthetic-test-secret", slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithKeeperContainer(p))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	file := filepath.Join(dir, "mise.json")
	config := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(file, []byte(`{"tools":{"node":"22"},"env":{"PRIVATE":"excluded"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("server: "+srv.URL+"\nworkspace: lock-workspace\ntoken: "+token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func() (string, error) {
		cmd := exec.CommandContext(t.Context(), buildCrewshipBinary(t), "crew", "provision", "lock-resolve", file, "--crew", "lock-crew")
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(); err != nil || !strings.Contains(out, "native proposal") || p.calls.Load() != 1 {
		t.Fatalf("real route: %s %v calls=%d", out, err, p.calls.Load())
	}
	if _, err := db.Exec(`UPDATE workspace_members SET role='MANAGER' WHERE id='lock-member'`); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err == nil {
		t.Fatalf("manager accepted: %s", out)
	}
	if _, err := db.Exec(`UPDATE workspace_members SET role='OWNER' WHERE id='lock-member'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE cli_tokens SET scopes='["crews:read"]' WHERE id='lock-token'`); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err == nil {
		t.Fatalf("read-only token accepted: %s", out)
	}
	if p.calls.Load() != 1 {
		t.Fatal("denied CLI request reached provider")
	}
}
