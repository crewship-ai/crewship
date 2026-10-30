package main

// Binary acceptance for the per-run inspection surface that had no
// acceptance test driving the built CLI:
//
//   - `routine tree`      GET   /pipeline-runs/{runId}/tree
//   - `routine metadata`  PATCH /pipeline-runs/{runId}/metadata
//   - `routine signal`    POST  /pipeline-runs/{runId}/signal
//   - `routine logs --full` GET /pipeline-runs/{runId}/logs
//   - `routine result`    GET   /pipeline-runs/{runId} + /files
//   - `routine calendar --ics` GET /pipelines/calendar.ics
//   - `routine records`   GET   /pipelines/{slug}/run-records
//
// Every case goes through the BUILT BINARY against the REAL api router over
// a REAL migrated database. The runs inspected are ones a real executor
// wrote (a transform run and a run parked on wait:event), never seeded rows.
// Each command is checked for its success output, its -f json shape, a
// readable 404 for an unknown run id, and denial for a token whose user is
// not a member of the workspace. The two mutations are also checked against
// a VIEWER member, which may read but not write (MANAGER+ tier).

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const runInspectWorkspaceID = "cinspectws000000000001"

const runInspectTransformDef = `{"dsl_version":"1.0","name":"inspect-transform","inputs":[{"name":"who","type":"string","widget":"text","required":true}],"steps":[{"id":"greet","type":"transform","transform":{"input":"{{ inputs.who }}","expression":"."}}]}`

// runInspectEventDef parks on a top-level wait:event so `routine signal`
// has a real waiter to deliver to.
const runInspectEventDef = `{"dsl_version":"1.0","name":"inspect-event","steps":[{"id":"gate","type":"wait","wait":{"kind":"event","event_type":"approve"},"timeout_seconds":3600}]}`

type runInspectConfigs struct {
	owner, viewer, outsider string
}

func startRunInspectAcceptanceServer(t *testing.T) runInspectConfigs {
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
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Inspect', 'inspect-ws')`, runInspectWorkspaceID)
	// The outsider owns a workspace of their own, so their token is valid
	// and authenticates — the refusal under test is membership, not auth.
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES ('cinspectotherws0000001', 'Other', 'inspect-other')`)
	for _, u := range []struct{ id, email string }{
		{"rri-owner", "owner@rri-ex.com"},
		{"rri-viewer", "viewer@rri-ex.com"},
		{"rri-outsider", "outsider@rri-ex.com"},
	} {
		mustExec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, u.id, u.email, u.id)
	}
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('rrim-owner', ?, 'rri-owner', 'OWNER')`, runInspectWorkspaceID)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('rrim-viewer', ?, 'rri-viewer', 'VIEWER')`, runInspectWorkspaceID)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('rrim-outsider', 'cinspectotherws0000001', 'rri-outsider', 'OWNER')`)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
		VALUES ('rri-crew', ?, 'Crew', 'rri-crew', 'free', 4096, 2.0)`, runInspectWorkspaceID)

	tokens := map[string]string{
		"rri-owner":    "crewship_cli_rriowner00000000000000000000",
		"rri-viewer":   "crewship_cli_rriviewer000000000000000000",
		"rri-outsider": "crewship_cli_rrioutsider0000000000000000",
	}
	for user, tok := range tokens {
		mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, 't', ?, datetime('now'))`,
			"clt-"+user, user, sha256HexToken(tok))
	}

	// The run-logs endpoint reads journal_entries; server.go attaches the
	// journal writer to the pipelines handler the same way.
	writer := journal.NewWriter(db, logger, journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger, api.WithJournal(writer))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	router.PipelinesHandler.SetJournal(writer)
	router.PipelinesHandler.SetSaveTokenSecret([]byte("acceptance-test-save-token-secret-32b"))
	router.PipelinesHandler.SetScheduleStore(pipeline.NewScheduleStore(db))
	router.PipelinesHandler.SetRunner(unusedAgentRunner{})
	router.PipelinesHandler.SetRunStore(pipeline.NewRunStore(db))
	router.PipelinesHandler.SetRunRegistry(pipeline.NewRunRegistry())
	// cmd_start.go wires the signal registry; without it the signal
	// endpoint answers 503 and the success path cannot be reached.
	router.PipelinesHandler.SetSignalRegistry(pipeline.NewSignalRegistry())
	waitpoints := pipeline.NewSQLWaitpointStore(db)
	t.Cleanup(waitpoints.Close)
	router.PipelinesHandler.SetWaitpointStore(waitpoints)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	write := func(user string) string {
		p := filepath.Join(dir, user+".yaml")
		cfg := "server: " + srv.URL + "\nworkspace: " + runInspectWorkspaceID +
			"\ntoken: " + tokens[user] + "\nformat: table\n"
		if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		return p
	}
	return runInspectConfigs{owner: write("rri-owner"), viewer: write("rri-viewer"), outsider: write("rri-outsider")}
}

// saveAndRunInspectTransform saves the transform recipe and runs it once,
// returning the run id the CLI reported.
func saveAndRunInspectTransform(t *testing.T, cfg string) string {
	t.Helper()
	if out, err := runRoutineDetailCLI(t, cfg, "routine", "save",
		"--name", "inspect-transform",
		"--definition", writeRoutineDetailDefinition(t, "inspect-transform", runInspectTransformDef),
		"--author-crew", "rri-crew",
		"--sample-inputs", `{"who":"sample"}`,
	); err != nil {
		t.Fatalf("routine save inspect-transform: %v\n%s", err, out)
	}
	out, err := runRoutineDetailCLI(t, cfg, "routine", "run", "inspect-transform", "--inputs", `{"who":"erin"}`)
	if err != nil {
		t.Fatalf("routine run inspect-transform: %v\n%s", err, out)
	}
	m := routineDetailRunIDRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no run id in:\n%s", out)
	}
	return m[1]
}

// mustFailWith runs the CLI expecting a non-zero exit whose combined output
// carries every one of wants.
func mustFailWith(t *testing.T, cfg string, wants []string, args ...string) {
	t.Helper()
	out, err := runRoutineDetailCLI(t, cfg, args...)
	if err == nil {
		t.Fatalf("%v: expected a non-zero exit, got success:\n%s", args, out)
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Fatalf("%v: expected %q in the error, got:\n%s", args, w, out)
		}
	}
}

func TestAcceptanceRoutineRunInspect_ReadSurface(t *testing.T) {
	cfgs := startRunInspectAcceptanceServer(t)
	runID := saveAndRunInspectTransform(t, cfgs.owner)
	const unknown = "run_does_not_exist"

	// --- tree -------------------------------------------------------------
	treeOut, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "tree", runID)
	if err != nil {
		t.Fatalf("routine tree: %v\n%s", err, treeOut)
	}
	for _, want := range []string{"RUN ID", "PARENT", runID, "(root)", "inspect-transform", "completed"} {
		if !strings.Contains(treeOut, want) {
			t.Fatalf("tree table is missing %q:\n%s", want, treeOut)
		}
	}
	treeJSON, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "tree", runID, "-f", "json")
	if err != nil {
		t.Fatalf("routine tree -f json: %v\n%s", err, treeJSON)
	}
	var nodes []map[string]any
	if err := json.Unmarshal([]byte(treeJSON), &nodes); err != nil || len(nodes) != 1 {
		t.Fatalf("tree JSON is not a one-node list (err=%v):\n%s", err, treeJSON)
	}
	// A root keeps parent_id "" in machine output; "(root)" is display only.
	if nodes[0]["id"] != runID || nodes[0]["parent_id"] != "" || nodes[0]["pipeline_slug"] != "inspect-transform" {
		t.Fatalf("unexpected tree node: %v", nodes[0])
	}
	if _, ok := nodes[0]["cost_usd"]; !ok {
		t.Fatalf("tree node lost its cost_usd key: %v", nodes[0])
	}
	mustFailWith(t, cfgs.owner, []string{"404", "run not found"}, "routine", "tree", unknown)

	// --- result (+ the /files section) ------------------------------------
	resOut, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "result", runID)
	if err != nil {
		t.Fatalf("routine result: %v\n%s", err, resOut)
	}
	for _, want := range []string{"Run " + runID + ": COMPLETED", "Final output:", "erin", "Files changed during this run: (none)"} {
		if !strings.Contains(resOut, want) {
			t.Fatalf("result is missing %q:\n%s", want, resOut)
		}
	}
	resJSON, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "result", runID, "-f", "json")
	if err != nil {
		t.Fatalf("routine result -f json: %v\n%s", err, resJSON)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(resJSON), &detail); err != nil {
		t.Fatalf("result JSON does not parse: %v\n%s", err, resJSON)
	}
	if detail["id"] != runID || !strings.EqualFold(runInspectString(detail["status"]), "completed") || !strings.Contains(runInspectString(detail["output"]), "erin") {
		t.Fatalf("unexpected result JSON: %s", resJSON)
	}
	mustFailWith(t, cfgs.owner, []string{"404"}, "routine", "result", unknown)

	// --- logs --full ------------------------------------------------------
	logsOut, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "logs", runID, "--full")
	if err != nil {
		t.Fatalf("routine logs --full: %v\n%s", err, logsOut)
	}
	if !strings.Contains(logsOut, "TIME") || !strings.Contains(logsOut, "LEVEL") || !strings.Contains(logsOut, "EVENT") {
		t.Fatalf("logs --full is missing its table header:\n%s", logsOut)
	}
	type logEntry struct {
		TS    string `json:"ts"`
		Level string `json:"level"`
		Type  string `json:"type"`
	}
	var entries []logEntry
	var logsJSON string
	// The journal writer flushes asynchronously; a real run journals at
	// least its start and finish, so wait for them to land.
	for deadline := time.Now().Add(10 * time.Second); ; {
		logsJSON, err = runRoutineDetailCLI(t, cfgs.owner, "routine", "logs", runID, "--full", "-f", "json")
		if err != nil {
			t.Fatalf("routine logs --full -f json: %v\n%s", err, logsJSON)
		}
		entries = nil
		if err := json.Unmarshal([]byte(logsJSON), &entries); err != nil {
			t.Fatalf("logs --full JSON is not the endpoint's entry list: %v\n%s", err, logsJSON)
		}
		if len(entries) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("logs --full returned fewer than two journal entries for a finished run:\n%s", logsJSON)
		}
		time.Sleep(100 * time.Millisecond)
	}
	types := map[string]bool{}
	for _, e := range entries {
		types[e.Type] = true
	}
	if !types["pipeline.run.started"] || !types["pipeline.run.completed"] {
		t.Fatalf("logs --full should carry the run's start and completion events, got %v", types)
	}
	if pretty, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "logs", runID, "--full"); err != nil || !strings.Contains(pretty, "run.completed") {
		t.Fatalf("logs --full table should list run.completed, err=%v:\n%s", err, pretty)
	}
	for _, e := range entries {
		if e.TS == "" || e.Level == "" || e.Type == "" {
			t.Fatalf("log entry missing ts/level/type: %+v\n%s", e, logsJSON)
		}
	}
	mustFailWith(t, cfgs.owner, []string{"404", "run not found"}, "routine", "logs", unknown, "--full")

	// --- records ----------------------------------------------------------
	recOut, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "records", "inspect-transform")
	if err != nil {
		t.Fatalf("routine records: %v\n%s", err, recOut)
	}
	// The table truncates ids to 16 chars; the prefix must still be there.
	prefix := runID
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	for _, want := range []string{"RUN ID", "STATUS", prefix, "completed", "manual"} {
		if !strings.Contains(recOut, want) {
			t.Fatalf("records table is missing %q:\n%s", want, recOut)
		}
	}
	recJSON, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "records", "inspect-transform", "-f", "json")
	if err != nil {
		t.Fatalf("routine records -f json: %v\n%s", err, recJSON)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(recJSON), &rows); err != nil || len(rows) != 1 || rows[0]["id"] != runID {
		t.Fatalf("records JSON is not the one full-id row (err=%v):\n%s", err, recJSON)
	}
	// A status filter with no match is an empty list, never "null".
	emptyJSON, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "records", "inspect-transform", "--status", "failed", "-f", "json")
	if err != nil || strings.TrimSpace(emptyJSON) != "[]" {
		t.Fatalf("expected [] for a filter with no match, got err=%v:\n%s", err, emptyJSON)
	}

	// --- calendar --ics ---------------------------------------------------
	from := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	to := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	icsOut, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "calendar", "--ics", "--from", from, "--to", to)
	if err != nil {
		t.Fatalf("routine calendar --ics: %v\n%s", err, icsOut)
	}
	for _, want := range []string{"BEGIN:VCALENDAR", "BEGIN:VEVENT", "inspect-transform", "CATEGORIES:run", "END:VCALENDAR"} {
		if !strings.Contains(icsOut, want) {
			t.Fatalf("ics output is missing %q:\n%s", want, icsOut)
		}
	}
	icsJSON, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "calendar", "--ics", "--from", from, "--to", to, "-f", "json")
	if err != nil {
		t.Fatalf("routine calendar --ics -f json: %v\n%s", err, icsJSON)
	}
	var wrapped map[string]string
	if err := json.Unmarshal([]byte(icsJSON), &wrapped); err != nil || !strings.HasPrefix(wrapped["ics"], "BEGIN:VCALENDAR\r\n") {
		t.Fatalf("calendar --ics -f json should wrap the CRLF file as {\"ics\":…} (err=%v):\n%s", err, icsJSON)
	}
	wide := time.Now().UTC().Add(40 * 24 * time.Hour).Format(time.RFC3339)
	mustFailWith(t, cfgs.owner, []string{"400", "32 days"}, "routine", "calendar", "--ics", "--from", from, "--to", wide)

	// --- a VIEWER may read every one of these -----------------------------
	if out, err := runRoutineDetailCLI(t, cfgs.viewer, "routine", "tree", runID); err != nil || !strings.Contains(out, runID) {
		t.Fatalf("a VIEWER should read the tree, got err=%v:\n%s", err, out)
	}
	if out, err := runRoutineDetailCLI(t, cfgs.viewer, "routine", "logs", runID, "--full", "-f", "json"); err != nil {
		t.Fatalf("a VIEWER should read logs --full, got err=%v:\n%s", err, out)
	}

	// --- a non-member is refused on every command -------------------------
	for _, args := range [][]string{
		{"routine", "tree", runID},
		{"routine", "result", runID},
		{"routine", "logs", runID, "--full"},
		{"routine", "records", "inspect-transform"},
		{"routine", "calendar", "--ics"},
	} {
		out, err := runRoutineDetailCLI(t, cfgs.outsider, args...)
		if err == nil {
			t.Fatalf("%v: a non-member read the run surface:\n%s", args, out)
		}
		if strings.Contains(out, "erin") || strings.Contains(out, "BEGIN:VCALENDAR") {
			t.Fatalf("%v: refusal leaked run data:\n%s", args, out)
		}
		if !strings.Contains(out, "403") && !strings.Contains(out, "404") {
			t.Fatalf("%v: expected a 403/404 refusal, got:\n%s", args, out)
		}
	}
}

// The --help pages are what an agent reads first; each one must show a
// runnable example of its own command.
func TestAcceptanceRoutineRunInspect_Help(t *testing.T) {
	cfgs := startRunInspectAcceptanceServer(t)
	for cmd, want := range map[string]string{
		"tree":     "crewship routine tree run_abc123",
		"metadata": "crewship routine metadata run_abc123 --set",
		"signal":   "crewship routine signal run_abc123 --event approve",
		"result":   "crewship routine result run_abc123",
		"records":  "crewship routine records summarize-text",
		"logs":     "--full",
		"calendar": "--ics",
	} {
		out, err := runRoutineDetailCLI(t, cfgs.owner, "routine", cmd, "--help")
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("routine %s --help should show %q, err=%v:\n%s", cmd, want, err, out)
		}
	}
}

func TestAcceptanceRoutineRunInspect_MetadataAndSignal(t *testing.T) {
	cfgs := startRunInspectAcceptanceServer(t)
	runID := saveAndRunInspectTransform(t, cfgs.owner)
	const unknown = "run_does_not_exist"

	// --- metadata ---------------------------------------------------------
	mdOut, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "metadata", runID,
		"--set", `{"stage":"review"}`, "--increment", `{"count":2}`, "--append", `{"notes":"first"}`)
	if err != nil {
		t.Fatalf("routine metadata: %v\n%s", err, mdOut)
	}
	if !strings.Contains(mdOut, "Updated metadata for "+runID) || !strings.Contains(mdOut, `"stage": "review"`) {
		t.Fatalf("unexpected metadata output:\n%s", mdOut)
	}
	mdJSON, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "metadata", runID,
		"--increment", `{"count":1}`, "--append", `{"notes":"second"}`, "-f", "json")
	if err != nil {
		t.Fatalf("routine metadata -f json: %v\n%s", err, mdJSON)
	}
	var md struct {
		RunID    string         `json:"run_id"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(mdJSON), &md); err != nil {
		t.Fatalf("metadata JSON does not parse: %v\n%s", err, mdJSON)
	}
	// The second call reads back the merged document: increments add,
	// appends accumulate, untouched keys survive.
	notes, _ := md.Metadata["notes"].([]any)
	if md.RunID != runID || md.Metadata["stage"] != "review" || md.Metadata["count"] != float64(3) ||
		len(notes) != 2 || notes[0] != "first" || notes[1] != "second" {
		t.Fatalf("metadata was not merged across calls: %s", mdJSON)
	}
	mustFailWith(t, cfgs.owner, []string{"pass --set, --increment, and/or --append"}, "routine", "metadata", runID)
	mustFailWith(t, cfgs.owner, []string{"parse --set JSON"}, "routine", "metadata", runID, "--set", "not-json")
	mustFailWith(t, cfgs.owner, []string{"404", "run not found"}, "routine", "metadata", unknown, "--set", `{"a":1}`)
	// VIEWER reads but may not write (MANAGER+ tier); the non-member is
	// refused outright. Neither may change the document.
	mustFailWith(t, cfgs.viewer, []string{"403"}, "routine", "metadata", runID, "--set", `{"stage":"hijacked"}`)
	if out, err := runRoutineDetailCLI(t, cfgs.outsider, "routine", "metadata", runID, "--set", `{"stage":"hijacked"}`); err == nil ||
		(!strings.Contains(out, "403") && !strings.Contains(out, "404")) {
		t.Fatalf("a non-member was not refused a metadata write, err=%v:\n%s", err, out)
	}
	after, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "metadata", runID, "--increment", `{"count":0}`, "-f", "json")
	if err != nil || strings.Contains(after, "hijacked") {
		t.Fatalf("a refused write changed the metadata, err=%v:\n%s", err, after)
	}

	// --- signal -----------------------------------------------------------
	if out, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "save",
		"--name", "inspect-event",
		"--definition", writeRoutineDetailDefinition(t, "inspect-event", runInspectEventDef),
		"--author-crew", "rri-crew",
	); err != nil {
		t.Fatalf("routine save inspect-event: %v\n%s", err, out)
	}
	parkOut, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "run", "inspect-event")
	if err != nil {
		t.Fatalf("routine run inspect-event: %v\n%s", err, parkOut)
	}
	m := routineDetailRunIDRE.FindStringSubmatch(parkOut)
	if m == nil || !strings.Contains(parkOut, "WAITING") {
		t.Fatalf("expected a WAITING run with an id, got:\n%s", parkOut)
	}
	parked := m[1]

	mustFailWith(t, cfgs.owner, []string{"--event <type> required"}, "routine", "signal", parked)
	mustFailWith(t, cfgs.owner, []string{"404", "no run waiting on that event"}, "routine", "signal", parked, "--event", "wrong-event")
	mustFailWith(t, cfgs.owner, []string{"404", "run not found"}, "routine", "signal", unknown, "--event", "approve")
	mustFailWith(t, cfgs.viewer, []string{"403"}, "routine", "signal", parked, "--event", "approve", "--payload", "viewer")
	if out, err := runRoutineDetailCLI(t, cfgs.outsider, "routine", "signal", parked, "--event", "approve", "--payload", "outsider"); err == nil ||
		(!strings.Contains(out, "403") && !strings.Contains(out, "404")) {
		t.Fatalf("a non-member was not refused a signal, err=%v:\n%s", err, out)
	}

	sigOut, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "signal", parked, "--event", "approve", "--payload", "approved-by-cli")
	if err != nil {
		t.Fatalf("routine signal: %v\n%s", err, sigOut)
	}
	if !strings.Contains(sigOut, `Signal "approve" delivered to run `+parked) {
		t.Fatalf("unexpected signal output:\n%s", sigOut)
	}

	// The resume runs in the background; follow it to the end through the
	// CLI and check the delivered payload became the run's output (not the
	// refused viewer/outsider payloads).
	deadline := time.Now().Add(15 * time.Second)
	var final map[string]any
	for {
		out, err := runRoutineDetailCLI(t, cfgs.owner, "routine", "result", parked, "-f", "json")
		if err == nil && json.Unmarshal([]byte(out), &final) == nil && strings.EqualFold(runInspectString(final["status"]), "completed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("signalled run did not complete: err=%v\n%s", err, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if out := runInspectString(final["output"]); !strings.Contains(out, "approved-by-cli") || strings.Contains(out, "viewer") || strings.Contains(out, "outsider") {
		t.Fatalf("expected the owner's payload as the run output, got %q", out)
	}
	// Once the wait is consumed there is nothing left to wake.
	mustFailWith(t, cfgs.owner, []string{"404", "no run waiting on that event"}, "routine", "signal", parked, "--event", "approve")
}

func runInspectString(v any) string {
	s, _ := v.(string)
	return s
}
