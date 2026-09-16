package main

// #1693 acceptance — `crewship privacy user-model list --provenance` and
// `show <field>` driven through the BUILT BINARY against the REAL api
// router, over a migrated database.
//
//   - GET /api/v1/users/me/user-model → privacy user-model list [--provenance]
//
// The router is the production one, storage root included: the operator
// model is a real file under it, its index row and its evidence rows are the
// ones the sweep writes, and the read goes through the same handler the
// agent's and the operator's CLI use. The only thing not exercised is the
// model call that produces the evidence — that is internal/usermodel's test,
// and the acceptance test seeds what it would have produced.

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
	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const userModelAcceptanceWorkspaceID = "cumprovws0000000000001a"

// startUserModelAcceptanceServer builds a real router over a migrated DB
// holding one workspace, one MEMBER with a CLI token, one crew, and that
// member's operator model: the file on disk, the user_models row, and the
// evidence rows for two of its three facts (the third is a fact recorded
// before provenance existed).
func startUserModelAcceptanceServer(t *testing.T) (cfgPath string) {
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
	const userID = "ump-member"
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Provenance', 'ump-ws')`, userModelAcceptanceWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES (?, 'member@ump-ex.com', 'Member')`, userID)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('umpm-member', ?, ?, 'MEMBER')`,
		userModelAcceptanceWorkspaceID, userID)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
		VALUES ('ump-crew', ?, 'Crew', 'ump-crew', 'free', 4096, 2.0)`, userModelAcceptanceWorkspaceID)

	const token = "crewship_cli_umpmember0000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-ump-member', ?, 't', ?, datetime('now'))`,
		userID, sha256HexToken(token))

	storageRoot := t.TempDir()
	paths := consolidate.UserModelPathsFor(storageRoot, "ump-crew")
	const content = "- role: runs the platform team\n- timezone: UTC+1\n- language: Czech"
	if err := memory.WriteUserModel(paths, userID, userModelAcceptanceWorkspaceID, content); err != nil {
		t.Fatalf("write model: %v", err)
	}
	slug := memory.UserSlug(userID, userModelAcceptanceWorkspaceID)
	mustExec(`INSERT INTO user_models (id, workspace_id, crew_id, user_id, user_slug, path, bytes)
		VALUES ('um-ump', ?, 'ump-crew', ?, ?, ?, ?)`,
		userModelAcceptanceWorkspaceID, userID, slug, paths.ModelPath(slug), len(content))
	mustExec(`INSERT INTO user_model_provenance
		(id, workspace_id, user_id, user_slug, key, value, quote, message_id, source_type, recorded_at) VALUES
		('ump-1', ?, ?, ?, 'role', 'runs the platform team', 'I run the platform team here', 'msg-role-1', 'stated', '2026-09-14T05:00:00.000Z'),
		('ump-2', ?, ?, ?, 'role', 'runs the platform team', 'I run the platform team, as I said', 'msg-role-2', 'stated', '2026-09-15T05:00:00.000Z'),
		('ump-3', ?, ?, ?, 'timezone', 'UTC+1', 'I am on UTC+1', 'msg-tz', 'stated', '2026-09-15T05:00:00.000Z')`,
		userModelAcceptanceWorkspaceID, userID, slug,
		userModelAcceptanceWorkspaceID, userID, slug,
		userModelAcceptanceWorkspaceID, userID, slug)

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger,
		api.WithOutputBasePath(storageRoot))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	p := filepath.Join(t.TempDir(), "member.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + userModelAcceptanceWorkspaceID +
		"\ntoken: " + token + "\nformat: table\n"
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func runUserModelCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The plain readout is unchanged; --provenance says where each entry came
// from, resolving the NEWEST evidence row per field; an entry with no
// evidence shows a dash; and the JSON body is the server's own, provenance
// object included.
func TestAcceptance_PrivacyUserModel_ListWithProvenance(t *testing.T) {
	cfg := startUserModelAcceptanceServer(t)

	plain, err := runUserModelCLI(t, cfg, "privacy", "user-model", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, plain)
	}
	for _, want := range []string{"FIELD", "VALUE", "role", "runs the platform team", "timezone", "UTC+1", "language", "Czech"} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain list omitted %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "QUOTE") || strings.Contains(plain, "msg-role") {
		t.Errorf("plain list leaked provenance columns:\n%s", plain)
	}

	withProv, err := runUserModelCLI(t, cfg, "privacy", "user-model", "list", "--provenance")
	if err != nil {
		t.Fatalf("list --provenance: %v\n%s", err, withProv)
	}
	for _, want := range []string{"QUOTE", "RECORDED", "MESSAGE",
		"I run the platform team, as I said", "msg-role-2", "2026-09-15T05:00:00.000Z",
		"I am on UTC+1", "msg-tz", "stated"} {
		if !strings.Contains(withProv, want) {
			t.Errorf("list --provenance omitted %q:\n%s", want, withProv)
		}
	}
	if strings.Contains(withProv, "msg-role-1") {
		t.Errorf("list --provenance showed the OLDER evidence row for role — the read must resolve the newest:\n%s", withProv)
	}
	// language has no evidence row: a dash, never an invented origin.
	for _, line := range strings.Split(withProv, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "language") && !strings.Contains(line, "-") {
			t.Errorf("language row carries no dash for its missing provenance: %q", line)
		}
	}

	jsonOut, err := runUserModelCLI(t, cfg, "privacy", "user-model", "list", "--provenance", "-f", "json")
	if err != nil {
		t.Fatalf("list --provenance -f json: %v\n%s", err, jsonOut)
	}
	var res userModelResponse
	if err := json.Unmarshal([]byte(jsonOut), &res); err != nil {
		t.Fatalf("json output is not the server body: %v\n%s", err, jsonOut)
	}
	if !res.Exists || len(res.Facts) != 3 {
		t.Fatalf("json body = %+v, want exists with 3 facts", res)
	}
	byKey := map[string]userModelFact{}
	for _, f := range res.Facts {
		byKey[f.Key] = f
	}
	if p := byKey["role"].Provenance; p == nil || p.MessageID != "msg-role-2" || p.SourceType != "stated" ||
		p.Quote != "I run the platform team, as I said" || p.At != "2026-09-15T05:00:00.000Z" {
		t.Errorf("role provenance in json = %+v", p)
	}
	if p := byKey["language"].Provenance; p != nil {
		t.Errorf("language should carry no provenance object, got %+v", p)
	}
}

// `show <field>` narrows the readout; a field that is not stored is a
// non-zero exit naming it, not an empty table.
func TestAcceptance_PrivacyUserModel_ShowOneField(t *testing.T) {
	cfg := startUserModelAcceptanceServer(t)

	out, err := runUserModelCLI(t, cfg, "privacy", "user-model", "show", "timezone", "--provenance")
	if err != nil {
		t.Fatalf("show timezone: %v\n%s", err, out)
	}
	if !strings.Contains(out, "timezone") || !strings.Contains(out, "I am on UTC+1") {
		t.Errorf("show timezone omitted the field or its quote:\n%s", out)
	}
	if strings.Contains(out, "runs the platform team") {
		t.Errorf("show timezone showed another field:\n%s", out)
	}

	out, err = runUserModelCLI(t, cfg, "privacy", "user-model", "show", "editor")
	if err == nil {
		t.Fatalf("show of an unstored field exited 0:\n%s", out)
	}
	if !strings.Contains(out, `no field "editor"`) {
		t.Errorf("miss does not name the field:\n%s", out)
	}
}
