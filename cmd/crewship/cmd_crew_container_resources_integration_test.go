package main

// #1627 acceptance — `crewship crew create/update --cpus 0.005` drives the
// REAL api router, not a stubbed response, because the contract that matters
// is the one an agent actually invokes: the CLI binary against a live server.
//
// Before the fix, both commands returned 2xx here, the value landed in
// crews.container_cpus, and the failure only surfaced later inside the Docker
// daemon ("Range of CPUs is from 0.01") — by which point every agent run for
// that crew wedged on an error naming neither the crew nor the field.
//
// CONTRIBUTING: an acceptance test for an API change drives the CLI, not a
// hand-rolled HTTP request. Same real-router shape as
// cmd_system_keeper_integration_test.go.
//
// #1638 adds the two follow-ups: the floors moved off Docker's minimums (6
// MiB / 0.01 CPU created a container that OOM-killed the agent on first exec),
// and `--memory-mb 0` — documented as "resets to the server default" — stored
// a literal 0 on update, which the runtime's own `<= 0` fallback then read as
// 8 GiB, twice what the same flag produces on create.

import (
	"database/sql"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/spf13/cobra"
)

const (
	crewResWorkspaceID = "ccrewresws000000001a"
	// Must be >= 21 chars and CUID-shaped or resolveCrewID falls through to
	// a slug scan and reports "crew not found".
	crewResCrewID = "ccrewrescrew000000001"
)

// setupCrewResourceServer builds a real router over a migrated SQLite DB with
// one workspace, one OWNER holding a CLI token, and one existing crew to PATCH.
func setupCrewResourceServer(t *testing.T) (*httptest.Server, *sql.DB, string) {
	t.Helper()

	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}

	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Crew Res', 'crew-res')`, crewResWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('cr-owner', 'owner@ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('crm-owner', ?, 'cr-owner', 'OWNER')`, crewResWorkspaceID)

	ownerToken := "crewship_cli_crowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-cr-owner', 'cr-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
		VALUES (?, ?, 'Sized', 'sized', 'free', 4096, 2.0)`, crewResCrewID, crewResWorkspaceID)

	r, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	cliCfg = &cli.CLIConfig{
		Token:     ownerToken,
		Workspace: crewResWorkspaceID,
		Server:    srv.URL,
	}
	return srv, db, ownerToken
}

func crewResDeclareCreateFlags(c *cobra.Command) {
	c.Flags().String("name", "", "")
	c.Flags().String("slug", "", "")
	c.Flags().String("description", "", "")
	c.Flags().String("color", "", "")
	c.Flags().String("icon", "", "")
	c.Flags().Int("memory-mb", 0, "")
	c.Flags().Float64("cpus", 0, "")
	c.Flags().Int("ttl", 0, "")
	c.Flags().String("network-mode", "", "")
	c.Flags().String("allowed-domains", "", "")
	c.Flags().Bool("allow-package-registries", false, "")
	c.Flags().Bool("allow-private-endpoints", false, "")
}

func crewResDeclareUpdateFlags(c *cobra.Command) {
	c.Flags().String("name", "", "")
	c.Flags().String("description", "", "")
	c.Flags().String("color", "", "")
	c.Flags().String("icon", "", "")
	c.Flags().Int("memory-mb", 0, "")
	c.Flags().Float64("cpus", 0, "")
	c.Flags().Int("ttl", -1, "")
	c.Flags().String("network-mode", "", "")
	c.Flags().String("allowed-domains", "", "")
	c.Flags().Bool("allow-package-registries", false, "")
	c.Flags().Bool("allow-private-endpoints", false, "")
}

// TestCrewCreateCLI_SubDockerFloorCPUsRejected — `crew create --cpus 0.005`
// must fail at the API with an actionable range, not persist a crew that can
// never start a container.
func TestCrewCreateCLI_SubDockerFloorCPUsRejected(t *testing.T) {
	saveCLIState(t)
	setupCrewResourceServer(t)

	c := covFreshCmd(crewCreateCmd, crewResDeclareCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"name": "Wedged", "slug": "wedged", "cpus": "0.005"})

	err := c.RunE(c, nil)
	if err == nil {
		t.Fatal("`crew create --cpus 0.005` succeeded; the daemon would then refuse every container create for this crew")
	}
	msg := err.Error()
	if !strings.Contains(msg, "container_cpus") || !strings.Contains(msg, "0.01") {
		t.Fatalf("CLI error must name the field and the valid floor, got: %v", err)
	}
}

// TestCrewUpdateCLI_SubDockerFloorCPUsRejected — the update path had no check
// at all, so an existing healthy crew could be patched into the wedged state.
func TestCrewUpdateCLI_SubDockerFloorCPUsRejected(t *testing.T) {
	saveCLIState(t)
	setupCrewResourceServer(t)

	c := covFreshCmd(crewUpdateCmd, crewResDeclareUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"cpus": "0.005"})

	err := c.RunE(c, []string{crewResCrewID})
	if err == nil {
		t.Fatal("`crew update --cpus 0.005` succeeded; a healthy crew was patched into the wedged state")
	}
	msg := err.Error()
	if !strings.Contains(msg, "container_cpus") || !strings.Contains(msg, "0.01") {
		t.Fatalf("CLI error must name the field and the valid floor, got: %v", err)
	}
}

// TestCrewUpdateCLI_OverCeilingMemoryRejected — the other direction: no
// ceiling existed, so any role that can patch a crew could overcommit the host.
func TestCrewUpdateCLI_OverCeilingMemoryRejected(t *testing.T) {
	saveCLIState(t)
	setupCrewResourceServer(t)

	c := covFreshCmd(crewUpdateCmd, crewResDeclareUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"memory-mb": "999999"})

	err := c.RunE(c, []string{crewResCrewID})
	if err == nil {
		t.Fatal("`crew update --memory-mb 999999` succeeded; nothing bounds the host commitment")
	}
	if !strings.Contains(err.Error(), "container_memory_mb") {
		t.Fatalf("CLI error must name the field, got: %v", err)
	}
}

// TestCrewUpdateCLI_InRangeResourcesAccepted keeps the guard honest — an
// ordinary resize must still go through end to end.
func TestCrewUpdateCLI_InRangeResourcesAccepted(t *testing.T) {
	saveCLIState(t)
	setupCrewResourceServer(t)

	c := covFreshCmd(crewUpdateCmd, crewResDeclareUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"memory-mb": "8192", "cpus": "4"})

	if err := c.RunE(c, []string{crewResCrewID}); err != nil {
		t.Fatalf("`crew update --memory-mb 8192 --cpus 4` should succeed, got: %v", err)
	}
}

// TestCrewCreateCLI_UndersizedCrewIsCreatedWithAWarning — #1638. The floor an
// agent actually needs is an advisory, not a refusal: the operator may have
// meant a small crew, and refusing does not make an undersized one bigger. So
// the crew is created and the operator is TOLD.
//
// This drives the real CLI against the real router because "the warning
// reaches the human" is the whole feature — a warning the server computes and
// the CLI drops is indistinguishable from no warning at all.
func TestCrewCreateCLI_UndersizedCrewIsCreatedWithAWarning(t *testing.T) {
	saveCLIState(t)
	_, db, _ := setupCrewResourceServer(t)

	c := covFreshCmd(crewCreateCmd, crewResDeclareCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"name": "Six", "slug": "six", "memory-mb": "512"})

	stderr := covCaptureStderr(t, func() {
		if err := c.RunE(c, nil); err != nil {
			t.Fatalf("`crew create --memory-mb 512` should succeed with a warning, got: %v", err)
		}
	})

	var mem int
	if err := db.QueryRow(`SELECT container_memory_mb FROM crews WHERE slug = 'six'`).Scan(&mem); err != nil {
		t.Fatalf("the crew was not created: %v", err)
	}
	if mem != 512 {
		t.Errorf("stored %d MiB, want the requested 512", mem)
	}

	if !strings.Contains(stderr, "Warning") {
		t.Fatalf("CLI printed no warning for a 512 MiB crew; stderr was %q", stderr)
	}
	for _, want := range []string{"container_memory_mb", "512", "2048", "OOM"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("CLI warning %q is missing %q", stderr, want)
		}
	}
}

// The update path carries the advisory too — shrinking a healthy crew into the
// band is the case nobody is watching.
func TestCrewUpdateCLI_ShrinkingBelowTheAdvisoryFloorWarns(t *testing.T) {
	saveCLIState(t)
	setupCrewResourceServer(t)

	c := covFreshCmd(crewUpdateCmd, crewResDeclareUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"memory-mb": "512"})

	stderr := covCaptureStderr(t, func() {
		if err := c.RunE(c, []string{crewResCrewID}); err != nil {
			t.Fatalf("`crew update --memory-mb 512` should succeed with a warning, got: %v", err)
		}
	})
	if !strings.Contains(stderr, "Warning") || !strings.Contains(stderr, "container_memory_mb") {
		t.Fatalf("shrinking a crew to 512 MiB printed no warning; stderr was %q", stderr)
	}
}

// An operator who lowers the floor stops being warned. Proves the CLI is
// reporting the SERVER's advisory rather than a second copy of the rule
// compiled into the binary.
func TestCrewCreateCLI_LoweredInstanceFloorSilencesTheWarning(t *testing.T) {
	saveCLIState(t)
	_, db, _ := setupCrewResourceServer(t)

	if _, err := db.Exec(
		`INSERT INTO app_settings (key, value, updated_at) VALUES ('runtime.agent_min_memory_mb', '256', datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`); err != nil {
		t.Fatalf("set floor: %v", err)
	}

	c := covFreshCmd(crewCreateCmd, crewResDeclareCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"name": "Six", "slug": "six", "memory-mb": "512"})

	stderr := covCaptureStderr(t, func() {
		if err := c.RunE(c, nil); err != nil {
			t.Fatalf("create: %v", err)
		}
	})
	if strings.Contains(stderr, "Warning") {
		t.Errorf("floor lowered to 256, but the CLI still warned about 512 MiB: %q", stderr)
	}
}

// TestCrewCreateCLI_SubDaemonMinimumMemoryRejected — below the DAEMON's own
// minimum stays a hard failure. Docker refuses the create outright, so there
// is no configuration to respect here, only an error to deliver early and
// legibly instead of at wake time.
func TestCrewCreateCLI_SubDaemonMinimumMemoryRejected(t *testing.T) {
	saveCLIState(t)
	setupCrewResourceServer(t)

	c := covFreshCmd(crewCreateCmd, crewResDeclareCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"name": "Four", "slug": "four", "memory-mb": "4"})

	err := c.RunE(c, nil)
	if err == nil {
		t.Fatal("`crew create --memory-mb 4` succeeded; the daemon would refuse every container create")
	}
	msg := err.Error()
	if !strings.Contains(msg, "container_memory_mb") || !strings.Contains(msg, "6") {
		t.Fatalf("CLI error must name the field and the daemon's floor, got: %v", err)
	}
}

// TestCrewUpdateCLI_ZeroMemoryResetsToTheDocumentedDefault — #1638. The docs
// say `--memory-mb 0` "resets to the server default", and the server default
// they publish is 4096. The row must carry that number: storing the sentinel
// hands the size to whichever `<= 0` fallback the runtime reaches first, which
// answered 8192.
func TestCrewUpdateCLI_ZeroMemoryResetsToTheDocumentedDefault(t *testing.T) {
	saveCLIState(t)
	_, db, _ := setupCrewResourceServer(t)

	// Move the crew off the default first, so "reset" is observable.
	c := covFreshCmd(crewUpdateCmd, crewResDeclareUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"memory-mb": "16384", "cpus": "8"})
	if err := c.RunE(c, []string{crewResCrewID}); err != nil {
		t.Fatalf("resize to 16384/8: %v", err)
	}

	c = covFreshCmd(crewUpdateCmd, crewResDeclareUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"memory-mb": "0", "cpus": "0"})
	if err := c.RunE(c, []string{crewResCrewID}); err != nil {
		t.Fatalf("`crew update --memory-mb 0 --cpus 0` should succeed, got: %v", err)
	}

	var mem int
	var cpus float64
	if err := db.QueryRow(`SELECT container_memory_mb, container_cpus FROM crews WHERE id = ?`, crewResCrewID).
		Scan(&mem, &cpus); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if mem != 4096 || cpus != 2.0 {
		t.Fatalf("after `--memory-mb 0 --cpus 0` the row is %d MiB / %v CPUs, want 4096 MiB / 2 CPUs "+
			"(the server default docs/cli/crew.mdx publishes)", mem, cpus)
	}
}
