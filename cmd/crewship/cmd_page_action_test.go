package main

// cmd_page_action_test.go — the acceptance test for `crewship page action`
// and `crewship page actions` (docs/prd/pages.md §8b, §11).
//
// Epic #1935. The endpoint half is proved in internal/api/pages_actions_test.go;
// this file proves the client half, which only the CLI can:
//
//   - the request the CLI sends carries the collected inputs AND NOTHING ELSE —
//     no routine, under any spelling, because §8b.2 says the wire format has no
//     field for one and a CLI that quietly added one would undo that;
//   - the path is the index-not-slug shape: the action id is in the URL, the
//     routine is not anywhere;
//   - every dispatch carries an Idempotency-Key, and --idempotency-key pins it,
//     because Pages is the first consumer of that header in this repo (§8b.3);
//   - the command returns on 202 without waiting for the run, and says so;
//   - a 409 and a 429 reach the operator as the server's own words.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/pflag"
)

const (
	pageActionCLIPanel   = "sluzby"
	pageActionCLIID      = "restart-api"
	pageActionCLIRoutine = "restart-api"
)

var (
	pageActionsRoute = "/api/v1/pages/" + pageAcceptSlug + "/panels/" + pageActionCLIPanel + "/actions"
	pageActionRoute  = pageActionsRoute + "/" + pageActionCLIID
)

// runPageActionCLI is runPageCLI with this surface's flags reset first. Cobra
// keeps a flag's value between Execute calls and the command tree is
// package-level state, so a --input from one test would still be set in the
// next. Production is one process per invocation and never sees it.
func runPageActionCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	page := findSubcommand(rootCmd.Commands(), "page")
	if page == nil {
		return runPageCLI(t, "", args...)
	}
	for _, name := range []string{"action", "actions"} {
		sub := findSubcommand(page.Commands(), name)
		if sub == nil {
			continue
		}
		sub.Flags().VisitAll(func(f *pflag.Flag) {
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				_ = sv.Replace(nil)
			} else {
				_ = f.Value.Set(f.DefValue)
			}
			f.Changed = false
		})
	}
	return runPageCLI(t, "", args...)
}

// pageActionReceipt is the 202 the server answers with.
func pageActionReceipt() map[string]any {
	return map[string]any{
		"status":     "SCHEDULED",
		"pending_id": "pnd_0000000000000000001",
		"fire_at":    "2026-08-12T09:14:22Z",
		"deduped":    false,
		"coalesced":  false,
		"page":       pageAcceptSlug,
		"panel":      pageActionCLIPanel,
		"action":     pageActionCLIID,
		"routine":    pageActionCLIRoutine,
	}
}

// ── The request shape IS the property (§8b.2) ──────────────────────────────

// TestPageActionCLI_SendsOnlyTheCollectedInputs is the client-side half of
// "a caller cannot name a routine at click time".
//
// The assertion is deliberately an ALLOW-LIST over the body's top-level keys
// rather than a check that `routine` is absent: a future field called
// `pipeline`, `verb` or `operation` would pass the negative check and fail this
// one, which is the failure worth catching.
func TestPageActionCLI_SendsOnlyTheCollectedInputs(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageActionRoute, clitest.JSONResponse(http.StatusAccepted, pageActionReceipt()))

	out, err := runPageActionCLI(t, "page", "action",
		pageAcceptSlug+"/"+pageActionCLIPanel, pageActionCLIID,
		"--input", "reason=deploy wedged", "--input", "replicas=4")
	if err != nil {
		t.Fatalf("page action: %v\n%s", err, out)
	}

	calls := stub.CallsFor(http.MethodPost, pageActionRoute)
	if len(calls) != 1 {
		t.Fatalf("%d POSTs to %s, want 1", len(calls), pageActionRoute)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v — %s", err, calls[0].Body)
	}
	for k := range body {
		if k != "inputs" {
			t.Errorf("the dispatch body carries a top-level %q; §8b.2 says the body carries only the collected "+
				"inputs, and a field here is a field a compromised client could use", k)
		}
	}
	var inputs map[string]any
	if err := json.Unmarshal(body["inputs"], &inputs); err != nil {
		t.Fatalf("inputs is not an object: %v", err)
	}
	if inputs["reason"] != "deploy wedged" || inputs["replicas"] != "4" {
		t.Errorf("inputs = %v, want the two --input pairs verbatim", inputs)
	}
}

// TestPageActionCLI_HasNoWayToNameARoutine proves the absence is structural,
// not a convention: there is no flag for it and there is no positional for it.
func TestPageActionCLI_HasNoWayToNameARoutine(t *testing.T) {
	guardCLIState(t)
	page := findSubcommand(rootCmd.Commands(), "page")
	if page == nil {
		t.Fatal("`page` is not registered on rootCmd")
	}
	action := findSubcommand(page.Commands(), "action")
	if action == nil {
		t.Fatal("`page action` is not registered")
	}
	for _, forbidden := range []string{"routine", "pipeline", "verb", "operation", "run"} {
		if f := action.Flags().Lookup(forbidden); f != nil {
			t.Errorf("`page action` has a --%s flag; the routine is read from the stored spec at dispatch "+
				"time (§8b.2), and a flag naming one is the field the wire format is specified not to have",
				forbidden)
		}
	}
}

// TestPageActionCLI_ActionIDIsInThePath pins the index-not-slug route shape.
func TestPageActionCLI_ActionIDIsInThePath(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageActionRoute, clitest.JSONResponse(http.StatusAccepted, pageActionReceipt()))

	if out, err := runPageActionCLI(t, "page", "action",
		pageAcceptSlug+"/"+pageActionCLIPanel, pageActionCLIID, "--input", "reason=x"); err != nil {
		t.Fatalf("page action: %v\n%s", err, out)
	}
	calls := stub.CallsFor(http.MethodPost, pageActionRoute)
	if len(calls) != 1 {
		t.Fatalf("the CLI did not POST to %s; §8b.2 fixes that path", pageActionRoute)
	}
}

// ── Idempotency (§8b.3) ────────────────────────────────────────────────────

// TestPageActionCLI_AlwaysSendsAnIdempotencyKey — Pages is the first consumer
// of the header in this codebase, so "the client generates one per logical
// click" has to be tested or it will not happen.
func TestPageActionCLI_AlwaysSendsAnIdempotencyKey(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageActionRoute, clitest.JSONResponse(http.StatusAccepted, pageActionReceipt()))

	if out, err := runPageActionCLI(t, "page", "action",
		pageAcceptSlug+"/"+pageActionCLIPanel, pageActionCLIID, "--input", "reason=x"); err != nil {
		t.Fatalf("page action: %v\n%s", err, out)
	}
	first := stub.CallsFor(http.MethodPost, pageActionRoute)
	if len(first) != 1 {
		t.Fatalf("%d POSTs, want 1", len(first))
	}
	key := first[0].Headers.Get("Idempotency-Key")
	if strings.TrimSpace(key) == "" {
		t.Fatal("no Idempotency-Key on the dispatch; a retried command would start a second run")
	}

	// A second invocation is a second logical click and gets its own key —
	// otherwise a deliberate re-run would be silently deduped for 24h.
	stub.ResetCalls()
	if out, err := runPageActionCLI(t, "page", "action",
		pageAcceptSlug+"/"+pageActionCLIPanel, pageActionCLIID, "--input", "reason=x"); err != nil {
		t.Fatalf("page action: %v\n%s", err, out)
	}
	second := stub.CallsFor(http.MethodPost, pageActionRoute)
	if len(second) != 1 {
		t.Fatalf("%d POSTs, want 1", len(second))
	}
	if second[0].Headers.Get("Idempotency-Key") == key {
		t.Error("two invocations reused one key; a second deliberate click would be deduped away")
	}
}

// TestPageActionCLI_IdempotencyKeyCanBePinned is what makes a retry ACROSS
// processes — a shell loop, a CI step — resolve to the original dispatch.
func TestPageActionCLI_IdempotencyKeyCanBePinned(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageActionRoute, clitest.JSONResponse(http.StatusAccepted, pageActionReceipt()))

	if out, err := runPageActionCLI(t, "page", "action",
		pageAcceptSlug+"/"+pageActionCLIPanel, pageActionCLIID,
		"--input", "reason=x", "--idempotency-key", "deploy-1187"); err != nil {
		t.Fatalf("page action: %v\n%s", err, out)
	}
	calls := stub.CallsFor(http.MethodPost, pageActionRoute)
	if len(calls) != 1 {
		t.Fatalf("%d POSTs, want 1", len(calls))
	}
	if got := calls[0].Headers.Get("Idempotency-Key"); got != "deploy-1187" {
		t.Errorf("Idempotency-Key = %q, want the pinned deploy-1187", got)
	}
}

// ── The receipt, and the refusals ──────────────────────────────────────────

// TestPageActionCLI_ReportsWhatTheServerResolved — the operator learns which
// routine ran from the SERVER, which is the visible half of §8b.2. And the
// output says the command returned before the run did, so a clean exit code is
// not read as a finished run.
func TestPageActionCLI_ReportsWhatTheServerResolved(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageActionRoute, clitest.JSONResponse(http.StatusAccepted, pageActionReceipt()))

	out, err := runPageActionCLI(t, "page", "action",
		pageAcceptSlug+"/"+pageActionCLIPanel, pageActionCLIID, "--input", "reason=x")
	if err != nil {
		t.Fatalf("page action: %v\n%s", err, out)
	}
	for _, want := range []string{pageActionCLIRoutine, "pnd_0000000000000000001"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "not when it finished") {
		t.Errorf("output does not say the run has not finished; a 202 that reads as success is issue #1563's "+
			"shape:\n%s", out)
	}
}

// TestPageActionCLI_DedupedReceiptSaysSo — a replay must not read as a second
// run having started.
func TestPageActionCLI_DedupedReceiptSaysSo(t *testing.T) {
	stub := pageStub(t)
	receipt := pageActionReceipt()
	receipt["status"] = "DEDUPED"
	receipt["deduped"] = true
	stub.OnPost(pageActionRoute, clitest.JSONResponse(http.StatusAccepted, receipt))

	out, err := runPageActionCLI(t, "page", "action",
		pageAcceptSlug+"/"+pageActionCLIPanel, pageActionCLIID,
		"--input", "reason=x", "--idempotency-key", "deploy-1187")
	if err != nil {
		t.Fatalf("page action: %v\n%s", err, out)
	}
	if !strings.Contains(strings.ToLower(out), "already queued") {
		t.Errorf("a deduped dispatch does not say so:\n%s", out)
	}
}

// TestPageActionCLI_ServerRefusalsReachTheOperator — the 409 and the 429 are
// the two answers §8b.3 designs for, and both have to arrive as words.
func TestPageActionCLI_ServerRefusalsReachTheOperator(t *testing.T) {
	guardCLIState(t)

	cases := []struct {
		name   string
		status int
		body   map[string]any
		want   string
	}{
		{
			name:   "a replayed key with different inputs",
			status: http.StatusConflict,
			body:   map[string]any{"error": "this Idempotency-Key was already used for this action with different inputs"},
			want:   "different inputs",
		},
		{
			name:   "already running",
			status: http.StatusTooManyRequests,
			body:   map[string]any{"error": "this action is already running", "retry_after_seconds": 5},
			want:   "already running",
		},
		{
			name:   "an action the panel does not declare",
			status: http.StatusNotFound,
			body:   map[string]any{"error": `panel "sluzby" on page "fleet-201" declares no action "restart-api"`},
			want:   "declares no action",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := pageStub(t)
			stub.OnPost(pageActionRoute, clitest.JSONResponse(tc.status, tc.body))

			out, err := runPageActionCLI(t, "page", "action",
				pageAcceptSlug+"/"+pageActionCLIPanel, pageActionCLIID, "--input", "reason=x")
			if err == nil {
				t.Fatalf("a %d exited 0:\n%s", tc.status, out)
			}
			if !strings.Contains(err.Error()+out, tc.want) {
				t.Errorf("the refusal does not carry the server's words (%q): %v\n%s", tc.want, err, out)
			}
		})
	}
}

// ── Local refusals, before any request ─────────────────────────────────────

func TestPageActionCLI_MalformedInvocationIsRefusedLocally(t *testing.T) {
	guardCLIState(t)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"a ref that is not <page>/<panel>", []string{"page", "action", "fleet-201", "restart-api"}, "<page>/<panel>"},
		{"an --input that is not k=v", []string{"page", "action", "fleet-201/sluzby", "restart-api", "--input", "reason"}, "k=v"},
		{"the same --input twice", []string{"page", "action", "fleet-201/sluzby", "restart-api",
			"--input", "reason=a", "--input", "reason=b"}, "was given twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := pageStub(t)
			// No route registered: the fallback fails the test if anything is
			// sent, which is the assertion — this is refused before the wire.
			_ = stub

			out, err := runPageActionCLI(t, tc.args...)
			if err == nil {
				t.Fatalf("accepted %v:\n%s", tc.args, out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// ── Listing what a panel offers ────────────────────────────────────────────

// TestPageActionsCLI_ListsTheDeclaredActions — the list is how an operator
// finds the id to dispatch, and it prints the routine each call resolves to so
// they can see what a click will run without being able to change it.
func TestPageActionsCLI_ListsTheDeclaredActions(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet(pageActionsRoute, clitest.JSONResponse(http.StatusOK, map[string]any{
		"page":  pageAcceptSlug,
		"panel": pageActionCLIPanel,
		"actions": []map[string]any{
			{
				"id": pageActionCLIID, "kind": "call", "label": "Restart API", "style": "danger",
				"routine": pageActionCLIRoutine,
				"confirm": map[string]any{"title": "Restart the API?", "body": "In-flight requests are dropped."},
				"inputs":  []map[string]any{{"name": "reason", "type": "text", "required": true}},
			},
			{
				"id": "open-incident", "kind": "link", "label": "Open the incident",
				"ref": map[string]any{"kind": "issue", "id": "ENG-15"},
			},
		},
	}))

	out, err := runPageActionCLI(t, "page", "actions", pageAcceptSlug+"/"+pageActionCLIPanel)
	if err != nil {
		t.Fatalf("page actions: %v\n%s", err, out)
	}
	for _, want := range []string{pageActionCLIID, "routine/" + pageActionCLIRoutine, "issue/ENG-15", "reason*"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing does not show %q:\n%s", want, out)
		}
	}
	// A link's target is an entity ref. If a URL ever appears in this column the
	// schema grew a field §8 rule 3 says it must not have.
	if strings.Contains(out, "http://") || strings.Contains(out, "https://") {
		t.Errorf("the listing rendered a URL; a link carries an entity ref and the renderer builds the "+
			"address (§8 rule 3):\n%s", out)
	}
}

func TestPageActionsCLI_EmptyListSaysWhereActionsComeFrom(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet(pageActionsRoute, clitest.JSONResponse(http.StatusOK, map[string]any{
		"page": pageAcceptSlug, "panel": pageActionCLIPanel, "actions": []map[string]any{},
	}))

	out, err := runPageActionCLI(t, "page", "actions", pageAcceptSlug+"/"+pageActionCLIPanel)
	if err != nil {
		t.Fatalf("page actions: %v\n%s", err, out)
	}
	if !strings.Contains(out, "declares no actions") {
		t.Errorf("an empty listing is not an instruction:\n%s", out)
	}
}
