package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const guardedQuotaServices = `[{"name":"db","quota_enforced":true,"volumes":[{"name":"data","generation":"g1","quota_bytes":33554432}]}]`

func TestCreateBackupRejectsStandaloneServiceData(t *testing.T) {
	db := openMigratedDBCov(t)
	for _, scope := range []Scope{ScopeCrew, ScopeWorkspace} {
		for _, level := range []ScopeLevel{ScopeLevelQuick, ScopeLevelStandard, ScopeLevelFull} {
			t.Run(string(scope)+"/"+string(level), func(t *testing.T) {
				ws, crew := seedCovWorkspace(t, db, string(scope)+string(level))
				if _, err := db.Exec("UPDATE crews SET services_json = ? WHERE id = ?", guardedQuotaServices, crew); err != nil {
					t.Fatal(err)
				}
				out := filepath.Join(t.TempDir(), "not-created")
				res, err := CreateBackup(context.Background(), db, CreateOptions{Scope: scope, WorkspaceID: ws, CrewID: crew, Level: level, OutputDir: out, Actor: covAdminActor(), NoEncrypt: true})
				if res != nil || err == nil || !strings.Contains(err.Error(), "snapshot transport") {
					t.Fatalf("result=%v error=%v", res, err)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatalf("preflight created output directory: %v", err)
				}
			})
		}
	}
}

func TestServiceBackupConfigurationGuard(t *testing.T) {
	for _, raw := range []string{"", "[]", "null", `[{"name":"legacy","volumes":[{"name":"data"}]}]`, `[{"quota_enforced":true}]`} {
		if err := requireSupportedServiceConfig(raw); err != nil {
			t.Fatalf("valid declaration rejected: %v", err)
		}
	}
	for _, raw := range []string{guardedQuotaServices, "{", `{ "private-secret": "do-not-print" }`, "crewsvc:invalid-secret"} {
		err := requireSupportedServiceConfig(raw)
		if err == nil || strings.Contains(err.Error(), "do-not-print") || strings.Contains(err.Error(), "invalid-secret") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	for _, value := range []any{guardedQuotaServices, []byte(guardedQuotaServices), 123} {
		if err := requireSupportedDumpServiceBackups(&DBDump{Tables: map[string][]map[string]any{"crews": {{"services_json": value}}}}); err == nil {
			t.Fatalf("dump accepted %T", value)
		}
	}
}

type serviceMutationProbe struct {
	*fakeDockerOps
	db   *sql.DB
	crew string
}

func (p *serviceMutationProbe) ContainerExists(ctx context.Context, _ string) (bool, error) {
	_, err := p.db.ExecContext(ctx, "UPDATE crews SET services_json = ? WHERE id = ?", guardedQuotaServices, p.crew)
	return false, err
}

func TestCreateBackupRejectsServiceDeclarationChangedAfterAdmission(t *testing.T) {
	db := openMigratedDBCov(t)
	ws, crew := seedCovWorkspace(t, db, "servicechanged")
	out := t.TempDir()
	res, err := CreateBackup(context.Background(), db, CreateOptions{Scope: ScopeWorkspace, WorkspaceID: ws, OutputDir: out, Actor: covAdminActor(), NoEncrypt: true, CrewContainerName: func(_, _ string) string { return "synthetic-crew" }, DockerOps: &serviceMutationProbe{fakeDockerOps: &fakeDockerOps{}, db: db, crew: crew}})
	if res != nil || err == nil || !strings.Contains(err.Error(), "snapshot transport") {
		t.Fatalf("result=%v error=%v", res, err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed backup published files: %v", entries)
	}
}
