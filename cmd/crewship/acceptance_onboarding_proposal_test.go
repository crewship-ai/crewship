package main

// Acceptance for `crewship onboarding proposal create --crew-icon
// --crew-color`, driven through the BUILT BINARY against a REAL api.Router
// over a REAL migrated database.
//
// POST /api/v1/onboarding/proposals takes `crew_icon` / `crew_color` (#2305)
// — the Guide's choice for the card and the crew row — checked against the
// icon vocabulary (internal/api/crew_icons.go) and the colour palette, and
// dropped when unknown. The CLI had neither flag and its output struct
// carried `crew_icon` but not `crew_color`, so a proposal's colour vanished
// from `-f json` even when the server had stored it. Only the real handler
// can prove the round trip: what the flag sends, what validation keeps, and
// what `proposal get` reads back.

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// onboardingProposalWorkspaceID is CUID-shaped so the CLI treats it as an
// already resolved workspace id and fires no slug→id round-trip.
const onboardingProposalWorkspaceID = "conbproposalws00001a"

func startOnboardingProposalServer(t *testing.T) string {
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
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Onboarding', 'onboarding-ws')`, onboardingProposalWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('op-owner', 'owner@op-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('opm-owner', ?, 'op-owner', 'OWNER')`,
		onboardingProposalWorkspaceID)
	const ownerToken = "crewship_cli_opowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-op-owner', 'op-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + onboardingProposalWorkspaceID +
		"\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runOnboardingProposalCLI(t *testing.T, cfgPath string, args ...string) string {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

// proposalLook is the slice of the create/get JSON this test cares about.
type proposalLook struct {
	ID      string `json:"id"`
	Payload struct {
		CrewSlug  string  `json:"crew_slug"`
		CrewIcon  *string `json:"crew_icon"`
		CrewColor *string `json:"crew_color"`
	} `json:"payload"`
}

func decodeProposal(t *testing.T, out string) proposalLook {
	t.Helper()
	// `create` prints a success line before the document; the JSON starts
	// at the first brace.
	i := strings.Index(out, "{")
	if i < 0 {
		t.Fatalf("no JSON in output:\n%s", out)
	}
	var p proposalLook
	if err := json.Unmarshal([]byte(out[i:]), &p); err != nil {
		t.Fatalf("decode proposal: %v\n%s", err, out)
	}
	return p
}

func TestAcceptance_OnboardingProposalCreate_IconAndColourRoundTrip(t *testing.T) {
	cfg := startOnboardingProposalServer(t)

	out := runOnboardingProposalCLI(t, cfg, "onboarding", "proposal", "create",
		"--crew-name", "Uptime Watch",
		"--agent", "Watcher:Polls the status page and reports outages",
		"--crew-icon", "shield", "--crew-color", "emerald",
		"-f", "json")
	created := decodeProposal(t, out)
	if created.Payload.CrewIcon == nil || *created.Payload.CrewIcon != "shield" {
		t.Errorf("crew_icon = %v, want shield", created.Payload.CrewIcon)
	}
	if created.Payload.CrewColor == nil || *created.Payload.CrewColor != "emerald" {
		t.Errorf("crew_color = %v, want emerald — the CLI's own output struct must carry it", created.Payload.CrewColor)
	}

	// The stored payload is what apply reads, so `get` must show the same
	// look — this is the value the approval card will render.
	got := decodeProposal(t, runOnboardingProposalCLI(t, cfg, "onboarding", "proposal", "get", created.ID, "-f", "json"))
	if got.Payload.CrewIcon == nil || *got.Payload.CrewIcon != "shield" || got.Payload.CrewColor == nil || *got.Payload.CrewColor != "emerald" {
		t.Errorf("stored proposal look = icon %v colour %v, want shield / emerald", got.Payload.CrewIcon, got.Payload.CrewColor)
	}

	// The human view names the look too, so a person approving from the
	// terminal sees what the card will.
	human := runOnboardingProposalCLI(t, cfg, "onboarding", "proposal", "get", created.ID)
	if !strings.Contains(human, "shield") || !strings.Contains(human, "emerald") {
		t.Errorf("human view should show the icon and colour:\n%s", human)
	}
}

func TestAcceptance_OnboardingProposalCreate_HexColourIsNormalised(t *testing.T) {
	cfg := startOnboardingProposalServer(t)

	// A bare hex is accepted and stored with its leading '#'.
	out := runOnboardingProposalCLI(t, cfg, "onboarding", "proposal", "create",
		"--crew-name", "Night Shift",
		"--agent", "Owl:Watches the queue overnight",
		"--crew-color", "1a2b3c",
		"-f", "json")
	created := decodeProposal(t, out)
	if created.Payload.CrewColor == nil || *created.Payload.CrewColor != "#1a2b3c" {
		t.Errorf("crew_color = %v, want #1a2b3c", created.Payload.CrewColor)
	}
}

func TestAcceptance_OnboardingProposalCreate_UnknownLookIsDroppedNotRefused(t *testing.T) {
	cfg := startOnboardingProposalServer(t)

	// The server drops an icon or colour outside its vocabulary rather than
	// failing the proposal: the person is midway through onboarding, and a
	// crew without a custom look beats a red error. The CLI must not
	// second-guess that with its own validation.
	out := runOnboardingProposalCLI(t, cfg, "onboarding", "proposal", "create",
		"--crew-name", "Plain Crew",
		"--agent", "Bob:Does the thing",
		"--crew-icon", "not-an-icon", "--crew-color", "not-a-colour",
		"-f", "json")
	created := decodeProposal(t, out)
	if created.ID == "" {
		t.Fatalf("proposal should still be created:\n%s", out)
	}
	if created.Payload.CrewIcon != nil {
		t.Errorf("an unknown icon must be dropped, got %q", *created.Payload.CrewIcon)
	}
	if created.Payload.CrewColor != nil {
		t.Errorf("an unknown colour must be dropped, got %q", *created.Payload.CrewColor)
	}
}
