package backup

import (
	"context"
	"path/filepath"
	"testing"
)

type restoreStagingProbe struct {
	LocalStorageOps
	parent string
}

func (p *restoreStagingProbe) MkdirTemp(ctx context.Context, parent, pattern string) (string, error) {
	p.parent = parent
	return p.LocalStorageOps.MkdirTemp(ctx, parent, pattern)
}
func TestRestoreStagesBesideBundleInsteadOfSystemTemp(t *testing.T) {
	db := openMigratedDBCov(t)
	ws, _ := seedCovWorkspace(t, db, "staging-parent")
	result, err := CreateBackup(t.Context(), db, CreateOptions{Scope: ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Actor: covAdminActor(), Passphrase: "synthetic-staging-passphrase"})
	if err != nil {
		t.Fatal(err)
	}
	probe := &restoreStagingProbe{}
	reset := SetDefaultStorage(probe)
	defer reset()
	_, err = RestoreBackup(t.Context(), db, RestoreOptions{Path: result.Path, ResumeWorkspaceID: ws, Actor: covAdminActor(), Passphrase: "synthetic-staging-passphrase", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if probe.parent != filepath.Dir(result.Path) {
		t.Fatalf("restore stages in system tmpfs instead of bundle filesystem: %q", probe.parent)
	}
}
