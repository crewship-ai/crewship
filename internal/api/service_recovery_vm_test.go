//go:build linux && service_recovery_vm

package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/provider"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/quota"
	"github.com/crewship-ai/crewship/internal/servicelifecycle"
)

// This test binary runs in a disposable VM across separate process, Docker and
// guest-OS restarts. The caller owns the VM; the test never restarts a daemon.
// Its fixed synthetic database persists between phases and never opens dev data.
func TestServiceRecoveryVM(t *testing.T) {
	phase := os.Getenv("CREWSHIP_SERVICE_VM_PHASE")
	if phase == "" {
		t.Skip("disposable VM acceptance only")
	}
	switch phase {
	case "seed", "initialize", "start", "verify-running", "stop", "verify-stopped":
	default:
		t.Fatal("unknown acceptance phase")
	}
	if _, err := os.Stat("/var/lib/crewship-acceptance-ready"); err != nil {
		t.Fatal("dedicated guest marker missing")
	}
	root, crew := "/var/lib/crewship-service-acceptance", "recovery-fixture"
	config := dockerprovider.Config{ContainerPrefix: "crewship-recovery-acceptance"}
	declaration := `[{"name":"storage","image":"alpine:3","command":["sh","-c","sleep 86400"],"volumes":[{"name":"data","mount":"/data"}]}]`
	if os.Getenv("CREWSHIP_SERVICE_VM_QUOTA") == "1" {
		if os.Geteuid() != 1000 {
			t.Fatal("quota acceptance requires actual server UID1000")
		}
		root, crew = "/var/lib/crewship-service-quota-acceptance", "quota-recovery-fixture"
		config.ContainerPrefix = "crewship-quota-controller-acceptance"
		config.QuotaCatalog = quota.Client{Socket: "/run/crewship-quota/quota-acceptance/helper.sock", Namespace: "quota-acceptance"}
		declaration = `[{"name":"storage","image":"alpine:3","quota_enforced":true,"command":["sh","-c","sleep 86400"],"volumes":[{"name":"data","mount":"/data","quota_bytes":67108864,"generation":1}]}]`
	}
	slug := crew
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Join(root, "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if err := database.Migrate(ctx, db.DB, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if phase == "seed" {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM crews`).Scan(&n); err != nil || n != 0 {
			t.Fatal("seed requires an empty synthetic fixture")
		}
		user := seedTestUser(t, db.DB)
		workspace := seedTestWorkspace(t, db.DB, user)
		mustExec(t, db.DB, `INSERT INTO crews(id,workspace_id,name,slug,services_json) VALUES(?,?,?,?,?)`, crew, workspace, slug, slug,
			declaration)
		mustExec(t, db.DB, `INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,updated_at) VALUES('intent',?,'storage','running','')`, crew)
	}
	p, err := dockerprovider.New(ctx, config, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	controller := &servicelifecycle.Controller{DB: db.DB, Runtime: p, Resolve: func(ctx context.Context, c, w, name string) (provider.CrewConfig, error) {
		return ResolveManagedService(ctx, db.DB, c, w, name)
	}}
	if phase == "stop" {
		mustExec(t, db.DB, `UPDATE service_runtime_intents SET desired_state='stopped',version=version+1 WHERE id='intent'`)
	}
	if phase == "start" {
		mustExec(t, db.DB, `UPDATE service_runtime_intents SET desired_state='running',version=version+1 WHERE id='intent'`)
	}
	if phase == "verify-stopped" {
		// Inspect BEFORE reconciliation: otherwise a controller stop would hide
		// Docker resurrecting a manually stopped service during boot.
		out, err := exec.CommandContext(ctx, "docker", "ps", "-q", "--filter", "label=crewship.crew-id="+crew, "--filter", "label=crewship.kind=sidecar").CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "" {
			t.Fatalf("stopped service resurrected before controller recovery: %v %s", err, out)
		}
	}
	// Fixture scheduling only: production uses the normal retry interval.
	mustExec(t, db.DB, `UPDATE service_runtime_intents SET next_attempt_at='' WHERE id='intent'`)
	controller.Reconcile(ctx)
	var state, code string
	if err := db.QueryRowContext(ctx, `SELECT observed_state,last_error FROM service_runtime_intents WHERE id='intent'`).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	items, err := p.ListCrewServices(ctx, crew, slug)
	if err != nil || len(items) != 1 {
		t.Fatalf("expected exactly one service: %+v %v", items, err)
	}
	command := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	id := command("ps", "-aq", "--filter", "label=crewship.crew-id="+crew, "--filter", "label=crewship.kind=sidecar")
	if len(id) != 12 {
		t.Fatalf("duplicate or missing service ID: %q", id)
	}
	if phase == "stop" || phase == "verify-stopped" {
		if state != "stopped" || code != "" || command("inspect", "--format", "{{.State.Running}}", id) != "false" {
			t.Fatalf("stop not durable: state=%s code=%s", state, code)
		}
	} else {
		if state != "running" || code != "" {
			t.Fatalf("service failed: state=%s code=%s", state, code)
		}
		if phase == "seed" || phase == "initialize" {
			command("exec", id, "sh", "-c", "printf synthetic-recovery-canary > /data/canary")
			if os.Getenv("CREWSHIP_SERVICE_VM_QUOTA") == "1" {
				out, err := exec.CommandContext(ctx, "docker", "exec", id, "sh", "-c", "dd if=/dev/zero of=/data/overflow bs=1M count=80 2>&1; code=$?; rm /data/overflow; exit $code").CombinedOutput()
				if err == nil || !strings.Contains(string(out), "No space left on device") {
					t.Fatalf("physical quota did not reject overflow: %v %s", err, out)
				}
			}
		} else if command("exec", id, "cat", "/data/canary") != "synthetic-recovery-canary" {
			t.Fatal("persistent data lost after recovery")
		}
		// Repeated reconciliation must attach to the same observation, never
		// duplicate a service or start an agent process.
		mustExec(t, db.DB, `UPDATE service_runtime_intents SET next_attempt_at='' WHERE id='intent'`)
		controller.Reconcile(ctx)
		if next := command("ps", "-q", "--filter", "label=crewship.crew-id="+crew, "--filter", "label=crewship.kind=sidecar"); next != id {
			t.Fatalf("non-idempotent restore: %s -> %s", id, next)
		}
	}
	if agents := command("ps", "-aq", "--filter", "label=crewship.crew-id="+crew, "--filter", "label=crewship.kind=crew"); agents != "" {
		t.Fatal("service recovery woke an agent")
	}
	t.Log(fmt.Sprintf("phase=%s observed=%s container=%s canary retained; no agent started", phase, state, id))
}
