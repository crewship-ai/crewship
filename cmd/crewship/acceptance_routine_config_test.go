package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// routineConfigHarness drives the built CLI binary against the real Router
// over a migrated test database. Every account, token and routine below is
// a synthetic fixture; no live instance, model or runtime is involved.
type routineConfigHarness struct {
	t      *testing.T
	binary string
	server *httptest.Server
	tokens map[string]string

	mu       sync.Mutex
	observed []routineConfigCall
}

type routineConfigCall struct {
	method, path string
	status       int
}

// routineConfigResult is one CLI invocation: combined output, whether the
// process exited non-zero, and the HTTP calls it made.
type routineConfigResult struct {
	out    string
	failed bool
	calls  []routineConfigCall
}

func (r routineConfigResult) lastStatus() int {
	if len(r.calls) == 0 {
		return 0
	}
	return r.calls[len(r.calls)-1].status
}

// routineCalls counts the requests that touched the fixture routine. The
// workspace lookup the CLI performs on start-up is not one of them, so a
// client-side validation error must leave this at zero.
func (r routineConfigResult) routineCalls() int {
	n := 0
	for _, c := range r.calls {
		if strings.Contains(c.path, "/pipelines/") {
			n++
		}
	}
	return n
}

func (h *routineConfigHarness) run(user string, args ...string) routineConfigResult {
	h.t.Helper()
	h.mu.Lock()
	h.observed = nil
	h.mu.Unlock()
	config := filepath.Join(h.t.TempDir(), "cli.yaml")
	if err := os.WriteFile(config, []byte("server: "+h.server.URL+"\nworkspace: cfg-ws\ntoken: "+h.tokens[user]+"\nformat: table\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(h.t.Context(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, h.binary, args...)
	command.Dir = h.t.TempDir()
	command.Env = append(os.Environ(), "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_WORKSPACE=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "NO_COLOR=1")
	out, err := command.CombinedOutput()
	h.mu.Lock()
	defer h.mu.Unlock()
	return routineConfigResult{out: string(out), failed: err != nil, calls: append([]routineConfigCall(nil), h.observed...)}
}

// ok runs a command that must succeed with HTTP 200 on its last call.
func (h *routineConfigHarness) ok(user string, args ...string) string {
	h.t.Helper()
	res := h.run(user, args...)
	if res.failed || res.lastStatus() != http.StatusOK {
		h.t.Fatalf("%s %v: failed=%v status=%d output=%s", user, args, res.failed, res.lastStatus(), res.out)
	}
	return res.out
}

// denied runs a command that must fail with the given HTTP status and print
// a message a person can act on.
func (h *routineConfigHarness) denied(user string, status int, wantText string, args ...string) {
	h.t.Helper()
	res := h.run(user, args...)
	if !res.failed || res.lastStatus() != status {
		h.t.Fatalf("%s %v: failed=%v status=%d want %d output=%s", user, args, res.failed, res.lastStatus(), status, res.out)
	}
	if !strings.Contains(res.out, wantText) {
		h.t.Fatalf("%s %v: output %q does not explain the refusal (want %q)", user, args, res.out, wantText)
	}
}

// rejectedLocally runs a command the CLI must refuse before calling the
// routine endpoints at all.
func (h *routineConfigHarness) rejectedLocally(user, wantText string, args ...string) {
	h.t.Helper()
	res := h.run(user, args...)
	if !res.failed || res.routineCalls() != 0 {
		h.t.Fatalf("%s %v: failed=%v routine calls=%v output=%s", user, args, res.failed, res.calls, res.out)
	}
	if !strings.Contains(res.out, wantText) {
		h.t.Fatalf("%s %v: output %q, want %q", user, args, res.out, wantText)
	}
}

// The -f json shapes of `routine tag` and `routine step-override set|clear`
// are declared here rather than reusing the command's own structs, so the
// test pins the wire contract a script depends on, not the Go type.
type routineConfigTagJSON struct {
	Slug    string   `json:"slug"`
	Added   []string `json:"added"`
	Removed string   `json:"removed"`
	Tags    []string `json:"tags"`
}

type routineConfigOverrideJSON struct {
	OK     bool   `json:"ok"`
	Slug   string `json:"slug"`
	StepID string `json:"step_id"`
}

func decodeRoutineConfigJSON(t *testing.T, out string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("-f json output is not JSON: %v\n%s", err, out)
	}
}

func TestAcceptanceRoutineConfig(t *testing.T) {
	t.Setenv("CREWSHIP_RATELIMIT_DISABLED", "true")
	db := testutil.MigratedDB(t).DB
	sql := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	sql(`INSERT INTO workspaces(id,name,slug) VALUES('cfg-ws','Routine config fixture','cfg-ws')`)
	tokens := map[string]string{}
	for i, member := range []struct{ id, role string }{
		{"cfg-owner", "OWNER"}, {"cfg-manager", "MANAGER"}, {"cfg-member", "MEMBER"}, {"cfg-viewer", "VIEWER"},
	} {
		sql(`INSERT INTO users(id,email) VALUES(?,?)`, member.id, member.id+"@routine-config.invalid")
		sql(`INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES(?,?,'cfg-ws',?)`, member.id, member.id, member.role)
		tokens[member.id] = "crewship_cli_" + strings.Repeat(string(rune('k'+i)), 48)
		sql(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'fixture',?,datetime('now'))`, member.id, member.id, sha256HexToken(tokens[member.id]))
	}
	sql(`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('cfg-routine','cfg-ws','cfg-routine','Config fixture',?,'cfg-hash')`,
		`{"dsl_version":"1.0","name":"cfg-routine","steps":[{"id":"s1","type":"agent_run","agent_slug":"lead","prompt":"go"}]}`)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer := journal.NewWriter(db, logger, journal.WriterOptions{})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := api.NewRouter(db, "routine-config-cli-fixture-secret-long", logger, api.WithJournal(writer), api.WithStoragePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	// Production wires the run store at start-up (cmd_start.go); without it
	// every budget route answers 503.
	router.PipelinesHandler.SetRunStore(pipeline.NewRunStore(db))

	h := &routineConfigHarness{t: t, tokens: tokens}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		h.mu.Lock()
		h.observed = append(h.observed, routineConfigCall{r.Method, r.URL.Path, response.Code})
		h.mu.Unlock()
		for key, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	t.Cleanup(h.server.Close)
	h.binary = buildCrewshipBinary(t)

	// The role middleware answers a bare "Forbidden" without naming the
	// required role. Pinned as-is so a clearer message is a visible,
	// deliberate change; improving it is tracked outside this test.
	const forbidden = "API error (403): Forbidden"

	t.Run("budget", func(t *testing.T) {
		h.t = t
		var set budgetRow
		decodeRoutineConfigJSON(t, h.ok("cfg-owner", "routine", "budget", "set", "cfg-routine", "--amount", "50", "-f", "json"), &set)
		if !set.HasBudget || set.MonthlyBudgetUSD == nil || *set.MonthlyBudgetUSD != 50 {
			t.Fatalf("budget set -f json = %+v", set)
		}
		if out := h.ok("cfg-owner", "routine", "budget", "get", "cfg-routine"); !strings.Contains(out, "$0.00 of $50.00") || !strings.Contains(out, "reporting only") {
			t.Fatalf("budget get human output: %s", out)
		}
		var got budgetRow
		decodeRoutineConfigJSON(t, h.ok("cfg-owner", "routine", "budget", "get", "cfg-routine", "-f", "json"), &got)
		if got.Slug != "cfg-routine" || got.MonthlyBudgetUSD == nil || *got.MonthlyBudgetUSD != 50 || got.SpentUSD == nil {
			t.Fatalf("budget get -f json = %+v", got)
		}
		// Below ADMIN the state stays visible and the amounts are withheld:
		// null, never a misleading $0.
		var hidden budgetRow
		decodeRoutineConfigJSON(t, h.ok("cfg-member", "routine", "budget", "get", "cfg-routine", "-f", "json"), &hidden)
		if !hidden.HasBudget || hidden.MonthlyBudgetUSD != nil || hidden.SpentUSD != nil {
			t.Fatalf("member budget get -f json = %+v, want state without amounts", hidden)
		}
		if out := h.ok("cfg-viewer", "routine", "budget", "get", "cfg-routine"); !strings.Contains(out, "within budget") || !strings.Contains(out, "OWNER and ADMIN") {
			t.Fatalf("viewer budget get human output: %s", out)
		}

		var summary budgetSummaryRowsResponse
		decodeRoutineConfigJSON(t, h.ok("cfg-owner", "routine", "budget", "summary", "-f", "json"), &summary)
		if len(summary.Routines) != 1 || summary.Routines[0].Slug != "cfg-routine" || summary.TotalBudgetUSD == nil || *summary.TotalBudgetUSD != 50 {
			t.Fatalf("budget summary -f json = %+v", summary)
		}
		if out := h.ok("cfg-owner", "routine", "budget", "summary"); !strings.Contains(out, "cfg-routine") || !strings.Contains(out, "$50.00") {
			t.Fatalf("budget summary human output: %s", out)
		}
		if out := h.ok("cfg-viewer", "routine", "budget", "summary"); strings.Contains(out, "$50.00") || !strings.Contains(out, "OWNER and ADMIN") {
			t.Fatalf("viewer budget summary leaked amounts or hid the note: %s", out)
		}

		// Setting a cap is manage tier: MANAGER and below are refused.
		h.denied("cfg-manager", http.StatusForbidden, forbidden, "routine", "budget", "set", "cfg-routine", "--amount", "10")
		h.denied("cfg-member", http.StatusForbidden, forbidden, "routine", "budget", "set", "cfg-routine", "--amount", "10")
		h.denied("cfg-owner", http.StatusNotFound, "not found", "routine", "budget", "get", "no-such-routine")
		h.rejectedLocally("cfg-owner", "--amount cannot be negative", "routine", "budget", "set", "cfg-routine", "--amount=-5")
		h.rejectedLocally("cfg-owner", "--amount <N> is required", "routine", "budget", "set", "cfg-routine")

		var cleared budgetRow
		decodeRoutineConfigJSON(t, h.ok("cfg-owner", "routine", "budget", "set", "cfg-routine", "--amount", "0", "-f", "json"), &cleared)
		if cleared.HasBudget {
			t.Fatalf("--amount 0 did not clear the budget: %+v", cleared)
		}
	})

	t.Run("appearance", func(t *testing.T) {
		h.t = t
		var set appearanceRow
		decodeRoutineConfigJSON(t, h.ok("cfg-manager", "routine", "appearance", "set", "cfg-routine", "--icon", "receipt", "--color", "amber", "-f", "json"), &set)
		if set.Slug != "cfg-routine" || set.Icon != "receipt" || set.Color != "amber" {
			t.Fatalf("appearance set -f json = %+v", set)
		}
		// Changing only the colour must leave the icon alone.
		h.ok("cfg-manager", "routine", "appearance", "set", "cfg-routine", "--color", "cyan")
		if out := h.ok("cfg-viewer", "routine", "appearance", "get", "cfg-routine"); !strings.Contains(out, "icon:  receipt") || !strings.Contains(out, "color: cyan") {
			t.Fatalf("appearance get human output: %s", out)
		}
		var got appearanceRow
		decodeRoutineConfigJSON(t, h.ok("cfg-viewer", "routine", "appearance", "get", "cfg-routine", "-f", "json"), &got)
		if got.Icon != "receipt" || got.Color != "cyan" {
			t.Fatalf("appearance get -f json = %+v", got)
		}

		h.denied("cfg-member", http.StatusForbidden, forbidden, "routine", "appearance", "set", "cfg-routine", "--icon", "rocket")
		h.denied("cfg-viewer", http.StatusForbidden, forbidden, "routine", "appearance", "set", "cfg-routine", "--clear")
		h.denied("cfg-manager", http.StatusBadRequest, "64 characters or fewer", "routine", "appearance", "set", "cfg-routine", "--icon", strings.Repeat("x", 65))
		h.rejectedLocally("cfg-manager", "give --icon, --color, or --clear", "routine", "appearance", "set", "cfg-routine")
		h.rejectedLocally("cfg-manager", "--clear cannot be combined", "routine", "appearance", "set", "cfg-routine", "--clear", "--icon", "rocket")

		if out := h.ok("cfg-manager", "routine", "appearance", "set", "cfg-routine", "--clear"); !strings.Contains(out, "Cleared cfg-routine") {
			t.Fatalf("appearance --clear output: %s", out)
		}
	})

	t.Run("tags", func(t *testing.T) {
		h.t = t
		var added routineConfigTagJSON
		decodeRoutineConfigJSON(t, h.ok("cfg-manager", "routine", "tag", "cfg-routine", "--add", "billing", "--add", "finance", "-f", "json"), &added)
		if added.Slug != "cfg-routine" || strings.Join(added.Tags, ",") != "billing,finance" || len(added.Added) != 2 {
			t.Fatalf("tag --add -f json = %+v", added)
		}
		if out := h.ok("cfg-manager", "routine", "tag", "cfg-routine", "--remove", "finance"); !strings.Contains(out, "Untagged cfg-routine: -finance") {
			t.Fatalf("tag --remove output: %s", out)
		}
		var removed routineConfigTagJSON
		decodeRoutineConfigJSON(t, h.ok("cfg-manager", "routine", "tag", "cfg-routine", "--remove", "billing", "-f", "json"), &removed)
		if removed.Removed != "billing" || removed.Tags != nil {
			t.Fatalf("tag --remove -f json = %+v", removed)
		}
		// Removing a tag unassigns a label; the routine itself stays.
		var tagCount, routineCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_tags WHERE pipeline_id='cfg-routine'`).Scan(&tagCount); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE id='cfg-routine'`).Scan(&routineCount); err != nil {
			t.Fatal(err)
		}
		if tagCount != 0 || routineCount != 1 {
			t.Fatalf("after removals: tags=%d routines=%d, want 0 and 1", tagCount, routineCount)
		}

		h.denied("cfg-member", http.StatusForbidden, forbidden, "routine", "tag", "cfg-routine", "--add", "ops")
		h.denied("cfg-viewer", http.StatusForbidden, forbidden, "routine", "tag", "cfg-routine", "--remove", "ops")
		h.denied("cfg-manager", http.StatusNotFound, "not found", "routine", "tag", "no-such-routine", "--add", "ops")
		h.rejectedLocally("cfg-manager", "pass --add <tag>", "routine", "tag", "cfg-routine")
	})

	t.Run("step overrides", func(t *testing.T) {
		h.t = t
		var set routineConfigOverrideJSON
		decodeRoutineConfigJSON(t, h.ok("cfg-manager", "routine", "step-override", "set", "cfg-routine", "s1", "--model", "smart", "-f", "json"), &set)
		if !set.OK || set.Slug != "cfg-routine" || set.StepID != "s1" {
			t.Fatalf("step-override set -f json = %+v", set)
		}
		var listed []stepOverrideRow
		decodeRoutineConfigJSON(t, h.ok("cfg-viewer", "routine", "step-override", "list", "cfg-routine", "-f", "json"), &listed)
		if len(listed) != 1 || listed[0].StepID != "s1" || listed[0].ModelOverride != "smart" {
			t.Fatalf("step-override list -f json = %+v", listed)
		}
		if out := h.ok("cfg-viewer", "routine", "step-override", "list", "cfg-routine"); !strings.Contains(out, "STEP") || !strings.Contains(out, "smart") {
			t.Fatalf("step-override list human output: %s", out)
		}

		h.denied("cfg-member", http.StatusForbidden, forbidden, "routine", "step-override", "set", "cfg-routine", "s1", "--prompt", "x")
		h.denied("cfg-viewer", http.StatusForbidden, forbidden, "routine", "step-override", "clear", "cfg-routine", "s1")
		h.denied("cfg-manager", http.StatusNotFound, "step not found in routine definition", "routine", "step-override", "set", "cfg-routine", "no-such-step", "--model", "smart")
		h.rejectedLocally("cfg-manager", "pass --prompt and/or --model", "routine", "step-override", "set", "cfg-routine", "s1")

		var clearedResult routineConfigOverrideJSON
		decodeRoutineConfigJSON(t, h.ok("cfg-manager", "routine", "step-override", "clear", "cfg-routine", "s1", "-f", "json"), &clearedResult)
		if !clearedResult.OK || clearedResult.StepID != "s1" {
			t.Fatalf("step-override clear -f json = %+v", clearedResult)
		}
		if out := h.ok("cfg-viewer", "routine", "step-override", "list", "cfg-routine"); !strings.Contains(out, "No step overrides") {
			t.Fatalf("after clear the routine should run as authored: %s", out)
		}
	})
}
