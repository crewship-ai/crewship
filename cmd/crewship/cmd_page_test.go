package main

// cmd_page_test.go — the acceptance test for the Pages CLI surface.
//
// Issue #1937 (Pages slice 2: HTTP + CLI write path), epic #1935.
// Spec: docs/prd/pages.md §11 (API and CLI surface), §7 (permissions),
// §4 (the freshness contract), §10 / §10b.3 (caps).
//
// ─── THIS FILE IS RED ON PURPOSE ─────────────────────────────────────────────
//
// `crewship page` does not exist yet. cmd/crewship/cmd_page.go is issue #1937's
// implementation and belongs to a later slice; this file is only the contract.
//
// The ONE failure this file is allowed to produce today is:
//
//     unknown command "page" for "crewship"
//
// Every test below routes through runPageCLI, which detects that state before
// it runs anything and stops with a message saying exactly that (see the note
// on runPageCLI for why it tests registration rather than pattern-matching the
// error text). If you see any OTHER failure from this file — a nil-pointer, a
// missing stub, a compile error, an "unstubbed request" — the fixture broke and
// that is the bug to fix first, because a test that dies during setup pins
// nothing.
//
// GREEN looks like: `page` is registered on rootCmd with create/get/list/set/
// update/delete, the runPageCLI tripwire never fires, and every assertion below
// passes untouched. Do not weaken an assertion to get there. If a wire shape
// this file guessed at turns out to be wrong (see the ambiguity notes on the
// individual tests), change it here in the SAME commit as the implementation
// and say in the PR which line moved and why.
//
// ─── Why argv and not RunE ───────────────────────────────────────────────────
//
// Most cmd_*_test.go files in this package call the command variable directly —
// `chatCreateCmd.RunE(chatCreateCmd, []string{"atlas"})`
// (cmd_chat_create_test.go:35). That is the established unit pattern and it is
// the right one once a command exists. It cannot be used here: a reference to a
// `pageCreateCmd` symbol that does not exist is a COMPILE error, and a package
// that does not compile tells the next agent nothing and blocks every other
// agent working in cmd/crewship.
//
// So this file drives the command tree the way argv does — rootCmd.SetArgs +
// rootCmd.Execute, the same entry point main() uses (main.go:36-77, exercised
// by main_cov2_test.go:14). The package compiles today, the whole flag/parse/
// dispatch path is real, and the failure is cobra's own unknown-command error.
//
// The end-to-end twin of this file — the one that drives the built binary
// against a live server, where "server-attached" provenance and the 64 KiB cap
// are enforced by the real handler rather than a stub — is
// scripts/test-harness/test-pages.sh.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/spf13/cobra"
)

// ─── Fixture constants ───────────────────────────────────────────────────────

const (
	pageAcceptSlug  = "fleet-201"
	pageAcceptName  = "Flotila .201"
	pageAcceptPanel = "sluzby"

	// The producer and run id the SERVER attaches. Neither string may ever
	// appear in a request body the CLI sends — that is the whole point of
	// PRD §4 rule 5 and §7.1b ("agent identity comes from the token, never
	// from the request body").
	pageAcceptProducer = "script/watch-services.sh"
	pageAcceptRunID    = "crun0000000000000000001"

	// Absolute, not relative: PRD §4 rule 3 — "age shown in absolute terms,
	// not 'a while ago'".
	pageAcceptProducedAt = "2026-08-12T09:14:22Z"

	// PRD §10b.3: payload cap 64 KiB, enforced in Go at the handler.
	pageAcceptPayloadCap = 64 * 1024
)

// pageAcceptSpecYAML is the page definition from PRD §6, layer 1, trimmed to
// the one panel this slice needs. Every field here is load-bearing:
//   - owner is "the permission anchor, not a label" (§6, §7.1 rule 2)
//   - producer is who may write the payload (§7.1 rule 4)
//   - sla is mandatory — "a panel without one does not validate" (§4 rule 1)
const pageAcceptSpecYAML = `apiVersion: crewship/v1
kind: Page
metadata:
  name: ` + pageAcceptName + `
  slug: ` + pageAcceptSlug + `
spec:
  panels:
    - id: ` + pageAcceptPanel + `
      schema: status.v1
      title: Jede to?
      owner: crew/lookout
      producer: ` + pageAcceptProducer + `
      sla: 30s
      span: 8
`

// pageAcceptPayload is a valid status.v1 payload (PRD §3). Note the nested
// "state" key — it is part of the DATA, and must not be confused with the
// server-computed freshness state of §4.
const pageAcceptPayload = `{"items":[{"name":"api","state":"ok","label":"200 OK"}]}`

// ─── Fixture ─────────────────────────────────────────────────────────────────

// pageStub wires a StubServer into a hermetic CLI config and points
// $CREWSHIP_CONFIG at it.
//
// covStub (api_helpers_cov_test.go:29) assigns cliCfg directly, which is right
// for a RunE-level test. It cannot be used here: going through rootCmd.Execute
// means the root PersistentPreRun runs cli.LoadConfig and OVERWRITES cliCfg
// (main.go:53-66), so the target has to be persisted where LoadConfig will find
// it — i.e. the config file $CREWSHIP_CONFIG names (internal/cli/config.go:72).
//
// The workspace id is CUID-shaped so the client resolves it without a
// /workspaces round-trip (internal/cli/client.go:361), and the token host
// matches the stub host so the #571 token-host guard stays quiet.
func pageStub(t *testing.T) *clitest.StubServer {
	t.Helper()
	guardCLIState(t)
	saveCLIState(t)

	stub := clitest.NewStubServer()
	t.Cleanup(stub.Close)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cli-config.yaml")
	cfg := fmt.Sprintf("server: %s\ntoken: page-acceptance-token\nworkspace: %s\n", stub.URL(), covWSCli3)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write CLI config: %v", err)
	}
	t.Setenv("CREWSHIP_CONFIG", cfgPath)
	t.Setenv("HOME", dir) // no user slash commands, no stray ~/.crewship

	// Any route this test did not register is contract drift: PRD §11 lists
	// the complete endpoint set, so a CLI reaching elsewhere is a finding, not
	// a 404 to absorb quietly.
	stub.SetFallback(func(r *http.Request, _ []byte) (int, []byte, string) {
		t.Errorf("CLI called an unstubbed route %s %s — PRD §11 does not list it", r.Method, r.URL.Path)
		return http.StatusNotImplemented, []byte(`{"error":"clitest: unstubbed"}`), "application/json"
	})
	return stub
}

// pageSwapStdin points os.Stdin at a temp FILE holding content.
//
// covSwapStdin (cmd_quickactions_cov_test.go:92) uses a pipe and writes
// synchronously, which deadlocks the moment content exceeds the 64 KiB pipe
// buffer — exactly the case TestPageCLI_OversizePayloadIsRefusedCleanly needs.
// A file also makes term.IsTerminal(os.Stdin.Fd()) false, which is what a
// piped `--data -` looks like in production.
func pageSwapStdin(t *testing.T, content string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "page-stdin-*.json")
	if err != nil {
		t.Fatalf("temp stdin: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("rewind stdin: %v", err)
	}
	orig := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = orig
		_ = f.Close()
	})
}

// runPageCLI executes `crewship <args...>` through the real root command with
// stdin pre-filled, and returns everything written to stdout+stderr.
//
// It is also the RED-BY-DESIGN tripwire, and the tripwire checks REGISTRATION
// rather than pattern-matching the error, for two reasons:
//
//  1. `unknown command "page" for "crewship"` is only what cobra says for a
//     short invocation like `crewship page list`. The root command has a
//     headless fallback (cmd_root_headless.go, looksLikeSubcommandInvocation /
//     maxTypoArgs = 2): three or more bare positionals — `crewship page get
//     fleet-201` — are treated as a one-shot PROMPT for the default agent
//     instead, and a flag cobra does not know about surfaces as
//     `unknown flag: --file`. Three different messages, one cause.
//  2. On an instance that does have a default agent, letting that fallback run
//     would dispatch a paid agent run from a unit test.
//
// So: if `page` is not on rootCmd, stop before Execute and say exactly why.
func runPageCLI(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	guardCLIState(t)

	if findSubcommand(rootCmd.Commands(), "page") == nil {
		t.Fatalf("RED BY DESIGN (issue #1937) — `crewship %s` cannot run: there is no `page`\n"+
			"command on rootCmd. Cobra's own words for the short form are:\n"+
			"    unknown command \"page\" for \"crewship\"\n"+
			"That is the ONLY failure this file may produce before cmd/crewship/cmd_page.go exists.\n"+
			"GREEN = `page` registered on rootCmd (create/get/list/set/update/delete), this Fatalf\n"+
			"never fires, and every assertion in cmd_page_test.go passes unweakened.",
			strings.Join(args, " "))
	}

	pageSwapStdin(t, stdin)

	var err error
	out := covCaptureAll(t, func() {
		rootCmd.SetArgs(args)
		err = rootCmd.Execute()
	})
	rootCmd.SetArgs(nil)
	return out, err
}

// ─── Shape-tolerant JSON helpers ─────────────────────────────────────────────
//
// These pin WHAT the contract carries — slug, schema, owner, producer, sla,
// provenance — without pinning the exact envelope nesting the implementer picks
// (`{...}` vs `{"page":{...}}` vs `{"spec":{...}}`). PRD §11 names the routes
// and §10 names the columns; neither fixes the JSON envelope, so over-pinning
// it here would fail an implementation that is actually correct.

// jsonFind walks decoded JSON and returns the first value stored under key.
// Direct keys win over nested ones, and sibling recursion is in sorted key
// order, so the result never depends on Go's randomised map iteration.
func jsonFind(v any, key string) (any, bool) {
	switch t := v.(type) {
	case map[string]any:
		if got, ok := t[key]; ok {
			return got, true
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if got, ok := jsonFind(t[k], key); ok {
				return got, true
			}
		}
	case []any:
		for _, item := range t {
			if got, ok := jsonFind(item, key); ok {
				return got, true
			}
		}
	}
	return nil, false
}

// jsonFindString is jsonFind for a value the test expects to be a string.
func jsonFindString(v any, key string) (string, bool) {
	got, ok := jsonFind(v, key)
	if !ok {
		return "", false
	}
	s, ok := got.(string)
	return s, ok
}

// decodeJSON decodes b, failing the test with the raw bytes on error — a
// decode failure here usually means the CLI printed a table where the test
// asked for --format json, and the raw text is what identifies that.
func decodeJSON(t *testing.T, what string, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s is not JSON: %v\n---\n%s\n---", what, err, string(b))
	}
	return v
}

// pagePanelObject digs the named panel out of a decoded page document. It
// accepts the panel living anywhere under a "panels" array, which is the one
// structural commitment PRD §10 does make (`page_panels`, UNIQUE(page_id,
// panel_id)).
func pagePanelObject(t *testing.T, doc any, panelID string) map[string]any {
	t.Helper()
	panels, ok := jsonFind(doc, "panels")
	if !ok {
		t.Fatalf("no \"panels\" anywhere in the document: %s", mustMarshal(t, doc))
	}
	list, ok := panels.([]any)
	if !ok {
		t.Fatalf("\"panels\" is %T, want an array (PRD §10: page_panels is a list): %s", panels, mustMarshal(t, doc))
	}
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := obj["id"].(string); id == panelID {
			return obj
		}
		// The panel's own id column is `panel_id` in PRD §10; accept either.
		if id, _ := obj["panel_id"].(string); id == panelID {
			return obj
		}
	}
	t.Fatalf("panel %q not in panels: %s", panelID, mustMarshal(t, doc))
	return nil
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// slaSeconds normalises whatever the wire carries for the SLA into seconds.
//
// AMBIGUITY, flagged deliberately: PRD §6 authors `sla: 30s` in YAML, PRD §10
// stores `sla_seconds` as an integer, and §11 does not say which one crosses
// the wire. Both are accepted here; what is NOT accepted is the SLA going
// missing, because §4 rule 1 makes it mandatory.
func slaSeconds(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case string:
		s := strings.TrimSpace(t)
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "s")); err == nil {
			return n, true
		}
	}
	return 0, false
}

// ─── 1. The command exists at all ────────────────────────────────────────────

// TestPageCLI_IsRegisteredOnRoot is the plainest statement of the red reason.
// It mirrors TestChatCreate_IsRegisteredUnderChat (cmd_chat_create_test.go:105):
// discoverability in the command tree, not just in --help prose, is what the
// harness and any agent actually read.
//
// PRD §11 fixes the one-command-per-endpoint mapping:
//
//	GET    /api/v1/pages                          -> page list
//	GET    /api/v1/pages/{slug}                   -> page get
//	POST   /api/v1/pages                          -> page create
//	PATCH  /api/v1/pages/{slug}                   -> page update
//	DELETE /api/v1/pages/{slug}                   -> page delete
//	PUT    /api/v1/pages/{slug}/panels/{id}/data  -> page set
func TestPageCLI_IsRegisteredOnRoot(t *testing.T) {
	t.Parallel()

	root := findSubcommand(rootCmd.Commands(), "page")
	if root == nil {
		t.Fatalf("RED BY DESIGN (issue #1937): no `page` command on rootCmd.\n" +
			"PRD §11 requires one CLI command per endpoint; none of them exist yet.\n" +
			"GREEN = cmd/crewship/cmd_page.go registers pageCmd and main.go adds it to rootCmd.")
	}
	for _, want := range []string{"list", "get", "create", "update", "delete", "set"} {
		if findSubcommand(root.Commands(), want) == nil {
			t.Errorf("`page %s` is not registered — PRD §11 maps it to an endpoint, so it is not optional", want)
		}
	}
}

func findSubcommand(cmds []*cobra.Command, name string) *cobra.Command {
	for _, c := range cmds {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// ─── 2. create → get round-trips the spec ────────────────────────────────────

// TestPageCLI_CreateThenGetRoundTripsTheSpec drives the two halves of PRD §11's
// first contract: `crewship page create --file <yaml>` then `crewship page get
// <slug>` must return the same document.
//
// The stub deliberately serves back EXACTLY the bytes create sent, so the
// round-trip is proven end to end through the CLI's own encode/decode rather
// than against a server shape this test invented.
//
// AMBIGUITY: §11 does not say whether `page create --file` posts the PARSED
// spec as JSON or the YAML document verbatim as a string. This test pins
// parsed JSON, because §10 stores `spec_json TEXT` ("the validated spec") and
// §10b.1 makes authoring validate the spec against the schema before the save
// — validation the server cannot do on an opaque string it has not parsed. If
// the implementation lands on verbatim YAML instead, change this test in the
// same commit and say so in the PR.
func TestPageCLI_CreateThenGetRoundTripsTheSpec(t *testing.T) {
	stub := pageStub(t)

	var createdBody []byte
	stub.OnPost("/api/v1/pages", func(_ *http.Request, body []byte) (int, []byte, string) {
		createdBody = append([]byte(nil), body...)
		return http.StatusCreated, body, "application/json"
	})
	stub.OnGet("/api/v1/pages/"+pageAcceptSlug, func(*http.Request, []byte) (int, []byte, string) {
		if len(createdBody) == 0 {
			return http.StatusNotFound, []byte(`{"error":"page not found"}`), "application/json"
		}
		return http.StatusOK, createdBody, "application/json"
	})

	specPath := filepath.Join(t.TempDir(), "fleet-201.page.yaml")
	if err := os.WriteFile(specPath, []byte(pageAcceptSpecYAML), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	if _, err := runPageCLI(t, "", "page", "create", "--file", specPath); err != nil {
		t.Fatalf("page create --file: %v", err)
	}

	// PRD §11: POST /api/v1/pages, exactly once.
	posts := stub.CallsFor("POST", "/api/v1/pages")
	if len(posts) != 1 {
		t.Fatalf("POST /api/v1/pages called %d times, want 1", len(posts))
	}

	// The YAML the human authored (§6 layer 1) has to survive into the request.
	sent := decodeJSON(t, "create request body", createdBody)
	if got, _ := jsonFindString(sent, "slug"); got != pageAcceptSlug {
		t.Errorf("create body slug = %q, want %q — §6 metadata.slug is the page's identity", got, pageAcceptSlug)
	}
	if got, _ := jsonFindString(sent, "name"); got != pageAcceptName {
		t.Errorf("create body name = %q, want %q", got, pageAcceptName)
	}

	// Now the read-back. --format json because a table is for humans and this
	// assertion is about the document, not the rendering.
	out, err := runPageCLI(t, "", "page", "get", pageAcceptSlug, "--format", "json")
	if err != nil {
		t.Fatalf("page get: %v", err)
	}
	got := decodeJSON(t, "page get --format json output", []byte(out))

	if slug, _ := jsonFindString(got, "slug"); slug != pageAcceptSlug {
		t.Errorf("get slug = %q, want %q", slug, pageAcceptSlug)
	}
	if name, _ := jsonFindString(got, "name"); name != pageAcceptName {
		t.Errorf("get name = %q, want %q", name, pageAcceptName)
	}

	panel := pagePanelObject(t, got, pageAcceptPanel)
	// PRD §3: the schema set is CLOSED, so the declared schema is not a free
	// string the CLI may drop or normalise away.
	if s, _ := panel["schema"].(string); s != "status.v1" {
		t.Errorf("panel schema = %v, want status.v1 (§3, closed vocabulary)", panel["schema"])
	}
	// PRD §6 / §7.1 rule 2: owner is the ACL, not decoration. Losing it in the
	// round-trip loses the permission anchor.
	if o, _ := panel["owner"].(string); o != "crew/lookout" {
		t.Errorf("panel owner = %v, want crew/lookout (§7.1 rule 2: owner is the ACL)", panel["owner"])
	}
	// PRD §7.1 rule 4: producer authority is separate from viewer authority.
	if p, _ := panel["producer"].(string); p != pageAcceptProducer {
		t.Errorf("panel producer = %v, want %q (§7.1 rule 4)", panel["producer"], pageAcceptProducer)
	}
	// PRD §4 rule 1: "Every panel declares sla. A panel without one does not
	// validate. There is no default that means 'never mind'."
	slaRaw, ok := panel["sla"]
	if !ok {
		slaRaw, ok = panel["sla_seconds"]
	}
	if !ok {
		t.Fatalf("panel carries no sla/sla_seconds — §4 rule 1 makes it mandatory: %s", mustMarshal(t, panel))
	}
	if secs, ok := slaSeconds(slaRaw); !ok || secs != 30 {
		t.Errorf("panel sla = %v, want 30 seconds (spec said `sla: 30s`)", slaRaw)
	}
	if span, _ := panel["span"].(float64); span != 8 {
		t.Errorf("panel span = %v, want 8", panel["span"])
	}
}

// ─── 3. list shows it ────────────────────────────────────────────────────────

// TestPageCLI_ListShowsTheCreatedPage — PRD §11, GET /api/v1/pages.
//
// AMBIGUITY: §11 does not say whether the list body is a bare array (the
// /api/v1/agents convention in this repo) or a wrapped object. The assertion
// below is on the rendered output, so either passes; what must not happen is a
// created page being invisible to `page list`.
func TestPageCLI_ListShowsTheCreatedPage(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/pages", clitest.JSONResponse(http.StatusOK, []map[string]any{
		{
			"id":     "cpage00000000000000001",
			"slug":   pageAcceptSlug,
			"name":   pageAcceptName,
			"panels": 1,
		},
	}))

	out, err := runPageCLI(t, "", "page", "list")
	if err != nil {
		t.Fatalf("page list: %v", err)
	}
	if !strings.Contains(out, pageAcceptSlug) {
		t.Errorf("page list output does not name the page slug %q:\n%s", pageAcceptSlug, out)
	}
	if !strings.Contains(out, pageAcceptName) {
		t.Errorf("page list output does not name the page %q:\n%s", pageAcceptName, out)
	}
	if len(stub.CallsFor("GET", "/api/v1/pages")) != 1 {
		t.Errorf("GET /api/v1/pages called %d times, want 1", len(stub.CallsFor("GET", "/api/v1/pages")))
	}
}

// ─── 4. set reads stdin, and provenance is the SERVER's ──────────────────────

// TestPageCLI_SetReadsStdinAndProvenanceIsServerAttached is the core of #1937.
//
// PRD §11: "`crewship page set <page>/<panel> --data -` reading JSON on stdin is
// the single write path, and it is what appears in every producer script.
// Provenance is attached server-side."
//
// PRD §4 rule 5: "Every panel footer carries provenance: producer, run id,
// timestamp. Server-attached, not producer-claimed."
//
// PRD §7.1b: "Agent identity comes from the token, never from the request body"
// — the sidecar already overwrites caller-supplied identity fields
// (internal/sidecar/identity.go:26-39) and a page write takes the same path.
//
// The proof is byte-level and shape-independent: the run id and timestamp the
// read-back shows must appear NOWHERE in the bytes the CLI sent. A client that
// never transmitted them cannot have claimed them.
func TestPageCLI_SetReadsStdinAndProvenanceIsServerAttached(t *testing.T) {
	stub := pageStub(t)

	dataPath := "/api/v1/pages/" + pageAcceptSlug + "/panels/" + pageAcceptPanel + "/data"
	var pushed []byte
	stub.OnPut(dataPath, func(_ *http.Request, body []byte) (int, []byte, string) {
		pushed = append([]byte(nil), body...)
		return http.StatusOK, []byte(`{"accepted":true}`), "application/json"
	})
	stub.OnGet("/api/v1/pages/"+pageAcceptSlug, clitest.JSONResponse(http.StatusOK,
		pageRecordWithData("fresh", pageAcceptProducedAt)))

	if _, err := runPageCLI(t, pageAcceptPayload, "page", "set",
		pageAcceptSlug+"/"+pageAcceptPanel, "--data", "-"); err != nil {
		t.Fatalf("page set --data -: %v", err)
	}

	// PRD §11: PUT /api/v1/pages/{slug}/panels/{id}/data — one call, no more.
	puts := stub.CallsFor("PUT", dataPath)
	if len(puts) != 1 {
		t.Fatalf("PUT %s called %d times, want 1", dataPath, len(puts))
	}

	// (a) stdin actually reached the wire.
	sent := decodeJSON(t, "page set request body", pushed)
	items, ok := jsonFind(sent, "items")
	if !ok {
		t.Fatalf("the JSON read from stdin did not reach the request body: %s", string(pushed))
	}
	if list, _ := items.([]any); len(list) != 1 {
		t.Errorf("payload items = %v, want the single status.v1 item read from stdin", items)
	}

	// (b) the client claimed no provenance. Top-level identity/freshness keys
	//     are forbidden outright — those are the server's to write (§4 rule 2:
	//     "computed server-side, never by the producer").
	if top, ok := sent.(map[string]any); ok {
		for _, forbidden := range []string{
			"producer", "producer_ref", "producer_run_id", "run_id",
			"produced_at", "timestamp", "provenance", "state",
			"agent_id", "agent_slug", "actor", "author",
		} {
			if _, present := top[forbidden]; present {
				t.Errorf("page set body carries client-supplied %q — provenance and freshness are "+
					"server-attached (§4 rules 2 and 5, §7.1b identity-from-token)", forbidden)
			}
		}
	}
	if _, present := jsonFind(sent, "provenance"); present {
		t.Errorf("page set body carries a nested \"provenance\" object; the client never supplies one (§4 rule 5)")
	}

	// (c) the strongest form: the run id and timestamp the read-back will show
	//     are absent from the sent bytes entirely.
	for _, secret := range []string{pageAcceptRunID, pageAcceptProducedAt} {
		if bytes.Contains(pushed, []byte(secret)) {
			t.Errorf("page set body contains %q — that value is the SERVER's to attach, "+
				"so a client that sent it is claiming provenance (§4 rule 5)", secret)
		}
	}

	// (d) the read-back carries all three provenance facts.
	out, err := runPageCLI(t, "", "page", "get", pageAcceptSlug, "--format", "json")
	if err != nil {
		t.Fatalf("page get after set: %v", err)
	}
	got := decodeJSON(t, "page get --format json output", []byte(out))
	panel := pagePanelObject(t, got, pageAcceptPanel)

	if p, ok := jsonFindString(panel, "producer"); !ok || p != pageAcceptProducer {
		t.Errorf("read-back producer = %v, want %q (§4 rule 5)", panel["producer"], pageAcceptProducer)
	}
	if r, ok := jsonFindString(panel, "run_id"); !ok || r != pageAcceptRunID {
		t.Errorf("read-back run id = %v, want %q (§4 rule 5)", panel["run_id"], pageAcceptRunID)
	}
	if ts, ok := jsonFindString(panel, "produced_at"); !ok || ts != pageAcceptProducedAt {
		t.Errorf("read-back timestamp = %v, want %q (§4 rule 5)", panel["produced_at"], pageAcceptProducedAt)
	}

	// And a human reading the table sees them too — a footer nobody can read is
	// not provenance.
	human, err := runPageCLI(t, "", "page", "get", pageAcceptSlug)
	if err != nil {
		t.Fatalf("page get (table): %v", err)
	}
	for _, want := range []string{pageAcceptProducer, pageAcceptRunID} {
		if !strings.Contains(human, want) {
			t.Errorf("`page get %s` does not show %q; §4 rule 5 puts provenance in every panel footer:\n%s",
				pageAcceptSlug, want, human)
		}
	}
}

// ─── 5. the 64 KiB cap ───────────────────────────────────────────────────────

// TestPageCLI_OversizePayloadIsRefusedCleanly — PRD §10b.3 caps the payload at
// 64 KiB, and PRD §10 fixes HOW: "MaxBytesReader → decode → explicit 400 …
// with the richer 422-plus-rejection-envelope shape from
// internal/sidecar/memory_write.go:47-55 for oversized payloads". Issue #1937:
// "a payload over the cap returns 422 with a rejection envelope, not a 500".
//
// The cap is enforced AT THE HANDLER (§10), so the CLI's contract is to surface
// the rejection, not to duplicate the limit client-side — the same 422 arrives
// from the sidecar path (§11, POST /api/v1/internal/…) where no client-side
// pre-check exists at all. A local pre-check is fine as an ADDITION; it must not
// replace surfacing what the server said.
//
// The trap this catches is real: MemoryWriteRejection has no "error" and no
// "detail" member, so cli.CheckError (internal/cli/client.go:532-575) falls
// through to dumping the raw body as `API error (422): {"rejected":true,…}`.
// That is not a clear message; it is a JSON blob with a number in front.
func TestPageCLI_OversizePayloadIsRefusedCleanly(t *testing.T) {
	stub := pageStub(t)

	oversize := `{"blob":"` + strings.Repeat("x", pageAcceptPayloadCap+2048) + `"}`
	if len(oversize) <= pageAcceptPayloadCap {
		t.Fatalf("fixture bug: payload is %d bytes, needs to exceed the %d-byte cap", len(oversize), pageAcceptPayloadCap)
	}

	const rejectionMessage = "panel payload is 67 606 bytes; the limit is 65 536 (64 KiB)"
	dataPath := "/api/v1/pages/" + pageAcceptSlug + "/panels/" + pageAcceptPanel + "/data"
	stub.OnPut(dataPath, clitest.JSONResponse(http.StatusUnprocessableEntity, map[string]any{
		"rejected": true,
		"kind":     "cap",
		"message":  rejectionMessage,
		"detail": map[string]any{
			"bytes_attempted": len(oversize),
			"bytes_limit":     pageAcceptPayloadCap,
		},
	}))

	out, err := runPageCLI(t, oversize, "page", "set",
		pageAcceptSlug+"/"+pageAcceptPanel, "--data", "-")
	if err == nil {
		t.Fatalf("an oversize payload was accepted; §10b.3 caps it at 64 KiB.\noutput:\n%s", out)
	}

	// 422 is a validation failure, not a server failure. Scripts and agents
	// branch on this without parsing prose (internal/cli/errors.go:9-22).
	if code := cli.ExitCodeFor(err); code != cli.ExitValidation {
		t.Errorf("exit code = %d, want %d (ExitValidation, HTTP 422); got %d = %s",
			code, cli.ExitValidation, code, exitCodeName(code))
	}

	// The user must see WHAT was refused and WHY, which is the rejection
	// envelope's message — not a status code and not the envelope's raw JSON.
	msg := err.Error()
	if !strings.Contains(msg, rejectionMessage) {
		t.Errorf("error does not surface the server's rejection message.\n got: %q\nwant it to contain: %q",
			msg, rejectionMessage)
	}
	if strings.Contains(msg, `"rejected"`) {
		t.Errorf("error dumps the raw rejection envelope instead of reading it: %q", msg)
	}
	if strings.Contains(msg, "500") {
		t.Errorf("an over-cap payload must never read as a server error: %q", msg)
	}
}

// exitCodeName renders a CLI exit code for a failure message.
func exitCodeName(code int) string {
	switch code {
	case cli.ExitOK:
		return "ExitOK"
	case cli.ExitGeneric:
		return "ExitGeneric"
	case cli.ExitValidation:
		return "ExitValidation"
	case cli.ExitNotFound:
		return "ExitNotFound"
	case cli.ExitAuth:
		return "ExitAuth"
	case cli.ExitConflict:
		return "ExitConflict"
	case cli.ExitRateLimited:
		return "ExitRateLimited"
	case cli.ExitServer:
		return "ExitServer"
	case cli.ExitConnection:
		return "ExitConnection"
	}
	return "unknown"
}

// ─── 6. freshness ────────────────────────────────────────────────────────────

// TestPageCLI_StalePanelReportsStale — PRD §4.
//
//	rule 2: three states, "computed server-side, never by the producer":
//	        fresh (within SLA), stale (past SLA), failed.
//	rule 3: "Stale renders degraded — value dimmed, age shown in absolute
//	        terms, not 'a while ago'. It never renders as if it were current."
//
// The CLI is a renderer here: it must repeat the server's verdict, in words,
// and show the absolute timestamp next to it. A stale panel that reads like a
// fresh one is the exact failure §4 exists to prevent.
func TestPageCLI_StalePanelReportsStale(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/pages/"+pageAcceptSlug, clitest.JSONResponse(http.StatusOK,
		pageRecordWithData("stale", pageAcceptProducedAt)))

	// The machine-readable half: the state crosses the wire as the server
	// computed it and the CLI does not launder it.
	jsonOut, err := runPageCLI(t, "", "page", "get", pageAcceptSlug, "--format", "json")
	if err != nil {
		t.Fatalf("page get --format json: %v", err)
	}
	panel := pagePanelObject(t, decodeJSON(t, "page get --format json output", []byte(jsonOut)), pageAcceptPanel)
	if state, _ := jsonFindString(panel, "state"); state != "stale" {
		t.Errorf("panel state = %v, want \"stale\" (§4 rule 2, computed server-side)", panel["state"])
	}

	// The human half: "the CLI says so".
	out, err := runPageCLI(t, "", "page", "get", pageAcceptSlug)
	if err != nil {
		t.Fatalf("page get: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "stale") {
		t.Errorf("`page get %s` never says \"stale\" for a panel past its SLA (§4 rule 3):\n%s",
			pageAcceptSlug, out)
	}
	// §4 rule 3: absolute, not relative. The date the payload was produced has
	// to be on screen.
	if !strings.Contains(out, "2026-08-12") {
		t.Errorf("stale panel output shows no absolute age; §4 rule 3 forbids relative-only rendering:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "a while ago") {
		t.Errorf("§4 rule 3 names \"a while ago\" as the thing not to print:\n%s", out)
	}
}

// ─── 7. delete, and a clean miss afterwards ──────────────────────────────────

// TestPageCLI_DeleteRemovesItAndGetFailsCleanly — PRD §11,
// DELETE /api/v1/pages/{slug} -> `crewship page delete <slug>`.
//
// "Fails cleanly" is the exit-code contract: a missing page is ExitNotFound (3),
// it names the slug, and it renders no half a page. `--yes` is passed because
// every destructive command in this CLI gates on confirmAction (main.go, and
// e.g. cmd_workspace_delete_test.go); a delete that cannot be scripted
// non-interactively is not usable by an agent.
func TestPageCLI_DeleteRemovesItAndGetFailsCleanly(t *testing.T) {
	stub := pageStub(t)

	deleted := false
	stub.OnDelete("/api/v1/pages/"+pageAcceptSlug, func(*http.Request, []byte) (int, []byte, string) {
		deleted = true
		return http.StatusNoContent, nil, ""
	})
	stub.OnGet("/api/v1/pages/"+pageAcceptSlug, func(*http.Request, []byte) (int, []byte, string) {
		if deleted {
			return http.StatusNotFound, []byte(`{"error":"page ` + pageAcceptSlug + ` not found"}`), "application/json"
		}
		return http.StatusOK, mustJSONBytes(t, pageRecordWithData("fresh", pageAcceptProducedAt)), "application/json"
	})

	if _, err := runPageCLI(t, "", "page", "delete", pageAcceptSlug, "--yes"); err != nil {
		t.Fatalf("page delete: %v", err)
	}
	if calls := stub.CallsFor("DELETE", "/api/v1/pages/"+pageAcceptSlug); len(calls) != 1 {
		t.Fatalf("DELETE /api/v1/pages/%s called %d times, want 1", pageAcceptSlug, len(calls))
	}

	out, err := runPageCLI(t, "", "page", "get", pageAcceptSlug)
	if err == nil {
		t.Fatalf("`page get %s` succeeded after the page was deleted:\n%s", pageAcceptSlug, out)
	}
	if code := cli.ExitCodeFor(err); code != cli.ExitNotFound {
		t.Errorf("exit code = %d (%s), want %d (ExitNotFound) — a deleted page is a 404, "+
			"and scripts branch on the code, not the prose", code, exitCodeName(code), cli.ExitNotFound)
	}
	if !strings.Contains(err.Error(), pageAcceptSlug) {
		t.Errorf("the not-found error does not name the page: %q", err.Error())
	}
	if strings.Contains(out, pageAcceptName) {
		t.Errorf("a deleted page still rendered its name — the miss must print nothing at all:\n%s", out)
	}
}

// ─── Shared server fixtures ──────────────────────────────────────────────────

// pageRecordWithData is the server's view of the page once a payload has landed:
// the spec fields plus the last payload, its server-computed freshness state
// (§4 rule 2) and its server-attached provenance (§4 rule 5).
//
// AMBIGUITY: PRD §10 names the COLUMNS (page_panel_data.producer_run_id,
// produced_at, state) but §11 does not fix the JSON envelope. This test reads
// them through jsonFind, so producer/run_id/produced_at may sit flat on the
// panel or nested under "provenance" — both pass. What must not happen is any
// of the three going missing.
func pageRecordWithData(state, producedAt string) map[string]any {
	return map[string]any{
		"id":   "cpage00000000000000001",
		"slug": pageAcceptSlug,
		"name": pageAcceptName,
		"panels": []any{
			map[string]any{
				"id":       pageAcceptPanel,
				"schema":   "status.v1",
				"title":    "Jede to?",
				"owner":    "crew/lookout",
				"producer": pageAcceptProducer,
				"sla":      "30s",
				"span":     8,
				"state":    state,
				"data": map[string]any{
					"items": []any{
						map[string]any{"name": "api", "state": "ok", "label": "200 OK"},
					},
				},
				"provenance": map[string]any{
					"producer":    pageAcceptProducer,
					"run_id":      pageAcceptRunID,
					"produced_at": producedAt,
				},
			},
		},
	}
}

func mustJSONBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return b
}

// --owner is what turns a personal page into a team's board, so its parsing is
// worth pinning: page ownership decides who may hand-write a script-produced
// panel, and isPageOwner counts every member of an owning crew.
func TestPageCLI_CreateOwnerFlag(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		want    string
		wantErr string
	}{
		{
			name: "omitted leaves the server's default — the creator owns it",
			flag: "",
			want: "",
		},
		{
			name: "a crew is handed the page",
			flag: "crew/ops",
			want: "crew/ops",
		},
		{
			name: "whitespace is not a value",
			flag: "   ",
			want: "",
		},
		{
			// Already the default, and admitting it would invite
			// user/<somebody-else>, which is a transfer and not a creation.
			name:    "a user is refused rather than treated as a no-op",
			flag:    "user/me",
			wantErr: "must be crew/<slug>",
		},
		{
			name:    "a bare slug names no kind",
			flag:    "ops",
			wantErr: "must be crew/<slug>",
		},
		{
			name:    "a crew with no slug names no crew",
			flag:    "crew/",
			wantErr: "must be crew/<slug>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().String("owner", "", "")
			if tt.flag != "" {
				if err := cmd.Flags().Set("owner", tt.flag); err != nil {
					t.Fatalf("set flag: %v", err)
				}
			}
			got, err := pageOwnerFromFlag(cmd)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got (%q, %v), want an error containing %q", got, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// The owner rides on the create REQUEST and never on the spec: `page update`
// re-applies a document, and a document that carried ownership would make every
// re-apply a silent transfer.
func TestPageCLI_OwnerIsNotPartOfTheDocument(t *testing.T) {
	doc, err := pages.ParseDocument([]byte(`apiVersion: crewship/v1
kind: Page
metadata:
  name: Provoz
  slug: provoz
spec:
  panels:
    - id: sluzby
      schema: status.v1
      owner: crew/ops
      producer: script/operations.sh
      sla: 5m
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	body := pageWriteFrom(doc)
	if body.Owner != "" {
		t.Errorf("pageWriteFrom carried an owner (%q) — ownership comes from --owner, not the spec", body.Owner)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Asserted on the TOP-LEVEL key, not on the bytes. The panel below carries
	// `"owner":"crew/ops"` of its own, so a substring check passes whatever the
	// page-level field says — including the value this test exists to forbid.
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("create body is not a JSON object: %v", err)
	}
	if _, present := wire["owner"]; present {
		t.Errorf("create body carries a page-level owner without --owner: %s", wire["owner"])
	}
	// The panel's own owner is a different field and must survive.
	if body.Panels[0].Owner != "crew/ops" {
		t.Errorf("panel owner lost: %q", body.Panels[0].Owner)
	}
}

// `-f quiet` is a repo-wide contract: one id per line, so the next command in
// the pipe can consume it. Every page list used to hand-roll its table with a
// tabwriter, which ignores the format entirely — so `quiet` printed the full
// human table, headers and all, into whatever was meant to read ids.
//
// The list commands are covered here as a group rather than one test each,
// because the defect was not in any one of them: it was that a new list
// command starts from a tabwriter unless something says otherwise.
func TestPageCLI_QuietPrintsOnlyTheKeyColumn(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		body    any
		args    []string
		want    []string
		notWant []string
	}{
		{
			name: "page list emits slugs",
			path: "/api/v1/pages",
			body: []map[string]any{
				{"slug": "provoz", "name": "Provoz", "panel_count": 5, "state": "fresh", "owner": "crew/ops"},
				{"slug": "watch", "name": "Watch", "panel_count": 2, "state": "stale", "owner": "crew/ops"},
			},
			args:    []string{"page", "list", "-f", "quiet"},
			want:    []string{"provoz", "watch"},
			notWant: []string{"SLUG", "Provoz", "fresh", "crew/ops"},
		},
		{
			name: "page links emits link ids",
			path: "/api/v1/pages/provoz/public",
			body: map[string]any{"tokens": []map[string]any{
				{"id": "cmsq1f0000", "expires_at": "2026-09-01T00:00:00Z"},
			}},
			args:    []string{"page", "links", "provoz", "-f", "quiet"},
			want:    []string{"cmsq1f0000"},
			notWant: []string{"ID", "STATUS", "EXPIRES"},
		},
		{
			name: "page webhook list emits webhook ids",
			path: "/api/v1/pages/provoz/webhooks",
			body: map[string]any{"webhooks": []map[string]any{
				{"id": "pgwh_abc", "panel": "sluzby", "fire_count": 3},
			}},
			args:    []string{"page", "webhook", "list", "provoz", "-f", "quiet"},
			want:    []string{"pgwh_abc"},
			notWant: []string{"ID", "PANEL", "FIRES", "sluzby"},
		},
		{
			name: "page grants emits the subject key",
			path: "/api/v1/pages/provoz/grants",
			body: map[string]any{"grants": []map[string]any{
				{"subject_type": "crew", "subject": "ops", "level": "produce", "granted_by": "demo@x"},
			}},
			args:    []string{"page", "grants", "provoz", "-f", "quiet"},
			want:    []string{"crew/ops"},
			notWant: []string{"SUBJECT", "LEVEL", "produce", "demo@x"},
		},
		{
			// The starred marker is a human affordance for "you are here"; the
			// bare seq is what `page rollback --to` takes.
			name: "page versions emits a bare seq, never the current-marker",
			path: "/api/v1/pages/provoz/versions",
			body: map[string]any{"retained": 50, "versions": []map[string]any{
				{"seq": 7, "current": true, "panel_count": 5, "created_at": "2026-08-21T00:00:00Z"},
				{"seq": 6, "panel_count": 4, "created_at": "2026-08-20T00:00:00Z"},
			}},
			args:    []string{"page", "versions", "provoz", "-f", "quiet"},
			want:    []string{"7", "6"},
			notWant: []string{"*", "SEQ", "current", "rollback"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := pageStub(t)
			stub.OnGet(tt.path, clitest.JSONResponse(http.StatusOK, tt.body))

			out, err := runPageCLI(t, "", tt.args...)
			if err != nil {
				t.Fatalf("%s: %v\n%s", strings.Join(tt.args, " "), err, out)
			}
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("quiet output is missing %q:\n%s", w, out)
				}
			}
			for _, n := range tt.notWant {
				if strings.Contains(out, n) {
					t.Errorf("quiet output carries %q, which a pipe would read as a key:\n%s", n, out)
				}
			}
		})
	}
}
