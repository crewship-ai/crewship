package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"
)

const guardedQuotaServices = `[{"name":"db","quota_enforced":true,"volumes":[{"name":"data","generation":7,"mount":"/data","quota_bytes":33554432}]}]`

func TestCreateInstanceBackupRejectsStandaloneServiceData(t *testing.T) {
	for _, changedAfterAdmission := range []bool{false, true} {
		t.Run(fmt.Sprint(changedAfterAdmission), func(t *testing.T) {
			db := openMigratedDBCov(t)
			_, crew := seedCovWorkspace(t, db, "instanceserviceguard")
			identity, err := age.GenerateX25519Identity()
			if err != nil {
				t.Fatal(err)
			}
			opts := InstanceOptions{OutputDir: t.TempDir(), Actor: covAdminActor(), Recipients: []age.Recipient{identity.Recipient()}}
			change := func() {
				if _, err := db.Exec("UPDATE crews SET services_json=? WHERE id=?", guardedQuotaServices, crew); err != nil {
					t.Fatal(err)
				}
			}
			if changedAfterAdmission {
				var once sync.Once
				opts.Busy = func(context.Context) (int, string, error) { once.Do(change); return 0, "", nil }
			} else {
				change()
			}
			res, err := CreateInstanceBackup(t.Context(), db, opts)
			if res != nil || err == nil || !strings.Contains(err.Error(), "snapshot transport") {
				t.Fatalf("instance backup accepted unsupported service data: result present=%v err=%v", res != nil, err)
			}
			entries, err := os.ReadDir(opts.OutputDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("rejected instance backup published files: %v", entries)
			}
		})
	}
}

func TestCreateBackupRejectsStandaloneServiceData(t *testing.T) {
	db := openMigratedDBCov(t)
	for _, scope := range []Scope{ScopeCrew, ScopeWorkspace} {
		for _, level := range []ScopeLevel{ScopeLevelQuick, ScopeLevelStandard, ScopeLevelFull} {
			t.Run(string(scope)+"/"+string(level), func(t *testing.T) {
				ws, crew := seedCovWorkspace(t, db, string(scope)+string(level))
				if _, err := db.ExecContext(t.Context(), "UPDATE crews SET services_json = ? WHERE id = ?", guardedQuotaServices, crew); err != nil {
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
		if err := requireSupportedServiceConfig(raw, false); err != nil {
			t.Fatalf("valid declaration rejected: %v", err)
		}
	}
	for _, raw := range []string{guardedQuotaServices, "{", `{ "private-secret": "do-not-print" }`, "crewsvc:invalid-secret"} {
		err := requireSupportedServiceConfig(raw, false)
		if err == nil || strings.Contains(err.Error(), "do-not-print") || strings.Contains(err.Error(), "invalid-secret") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	for _, value := range []any{guardedQuotaServices, []byte(guardedQuotaServices), 123} {
		if err := requireSupportedDumpServiceBackups(&DBDump{Tables: map[string][]map[string]any{"crews": {{"services_json": value}}}}, nil); err == nil {
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

func TestServiceBackupGuardPreservesCancellation(t *testing.T) {
	db := openMigratedDBCov(t)
	_, crew := seedCovWorkspace(t, db, "cancelguard")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := requireSupportedServiceBackups(ctx, db, []CrewTarget{{ID: crew}}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost query cancellation: %v", err)
	}
}

func TestServiceBackupDumpRequiresExactCapturedIdentityAndIntent(t *testing.T) {
	proof := serviceSnapshot{Namespace: "synthetic-instance", CrewID: "crew", CrewSlug: "crew-slug", Service: "db", Volume: "data", Generation: 7, Bytes: 33554432, SHA256: strings.Repeat("a", 64), DesiredState: "running", IntentVersion: 9}
	row := map[string]any{"id": "crew", "slug": "crew-slug", "services_json": guardedQuotaServices}
	intent := map[string]any{"crew_id": "crew", "service_name": "db", "desired_state": "running", "version": int64(9)}
	dump := &DBDump{Tables: map[string][]map[string]any{"crews": {row}, "service_runtime_intents": {intent}}}
	if err := requireSupportedDumpServiceBackups(dump, []serviceSnapshot{proof}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(){
		func() {
			row["services_json"] = strings.ReplaceAll(guardedQuotaServices, `"generation":7`, `"generation":8`)
		},
		func() { row["services_json"] = strings.ReplaceAll(guardedQuotaServices, `33554432`, `67108864`) },
		func() { row["id"] = "foreign-crew" },
		func() { intent["version"] = int64(10) },
		func() { intent["desired_state"] = "stopped" },
	} {
		row["id"] = "crew"
		row["services_json"] = guardedQuotaServices
		intent["version"] = int64(9)
		intent["desired_state"] = "running"
		mutate()
		if err := requireSupportedDumpServiceBackups(dump, []serviceSnapshot{proof}); err == nil {
			t.Fatal("same-count mutable dump bypassed snapshot consistency")
		}
	}
}

func TestServiceBackupAdmissionAcceptsOnlyConfiguredHostTransport(t *testing.T) {
	db := openMigratedDBCov(t)
	_, crew := seedCovWorkspace(t, db, "configured_snapshot_guard")
	if _, err := db.Exec(`UPDATE crews SET services_json=? WHERE id=?`, guardedQuotaServices, crew); err != nil {
		t.Fatal(err)
	}
	target := []CrewTarget{{ID: crew}}
	if err := requireSupportedServiceBackups(t.Context(), db, target, nil); err == nil {
		t.Fatal("missing transport admitted quota data")
	}
	if err := requireSupportedServiceBackups(t.Context(), db, target, &snapshotProbe{}); err != nil {
		t.Fatal(err)
	}
}
