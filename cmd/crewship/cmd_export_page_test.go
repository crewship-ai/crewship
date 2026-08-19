package main

// Acceptance test for `crewship export page` — the door PRD §13 obstacle 6
// predicted would be missing (docs/prd/pages.md): kinds.ExportPages shipped
// tested and unreachable because `crewship export` knew only Crew and
// Workspace.
//
// What this file pins is the half only the CLI can own:
//
//   - the command EXISTS under `export`, because a manifest exporter nobody
//     can call is the bug this closes;
//   - what it prints is a document `crewship apply` reads back — proved by
//     parsing the output with manifest.Load rather than by eyeballing a
//     substring;
//   - the sealed-panel refusal (kinds.TestExportPages_SealedPanelRefuses)
//     reaches the operator as a sentence naming the panel, and leaves NO
//     file behind — an export that half-wrote over a backup is worse than
//     one that refused;
//   - the authored half the read API does not echo (`public:`, `actions:`,
//     `wake:`, `on_failure:`) is disclosed on stderr every time, because the
//     exporter cannot detect it and a silent gap here deletes buttons and
//     gates on the next apply.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/crewship-ai/crewship/internal/manifest"
)

const (
	// exportPageFleetJSON is GET /api/v1/pages/fleet-201 as the server
	// serialises it (kinds.PageRemote): panels complete, snapshot state
	// absent, `public`/`actions`/`wake`/`on_failure` never echoed.
	exportPageFleetJSON = `{
		"id":"pg_fleet","slug":"fleet-201","name":"Flotila .201","description":"prehled",
		"panels":[
			{"id":"sluzby","schema":"status.v1","title":"Jede to?","owner":"crew/lookout",
			 "producer":"routine/watch","sla_seconds":30,"span":8},
			{"id":"naklady","schema":"metric.v1","owner":"crew/lookout",
			 "producer":"agent/herald","sla_seconds":3600,"span":4}
		]}`

	// exportPageAuthoredJSON is what an EDITOR receives: the panel wire plus
	// the authored half attachAuthoredHalf copies onto it.
	exportPageAuthoredJSON = `{
		"id":"pg_fleet","slug":"fleet-201","name":"Flotila .201",
		"panels":[
			{"id":"sluzby","schema":"status.v1","owner":"crew/lookout",
			 "producer":"routine/watch","sla_seconds":30,"span":8,"public":true,
			 "actions":[{"id":"restart","kind":"call","label":"Restart","routine":"line-restart"}],
			 "wake":[{"when":"any(state == \"critical\")","agent":"crew/devops","writes":"sluzby"}],
			 "on_failure":{"issue":"crew/lookout"}}
		]}`

	exportPageAlphaJSON = `{
		"id":"pg_alpha","slug":"alpha","name":"Alpha",
		"panels":[{"id":"p","schema":"metric.v1","owner":"crew/x","producer":"script/s.sh",
		           "sla_seconds":90,"span":12}]}`

	// exportPageSealedJSON is a page holding a panel this account may not
	// see (§7.1 rule 2 — the sealed placeholder).
	exportPageSealedJSON = `{
		"id":"pg_sealed","slug":"fleet-201","name":"Flotila .201",
		"panels":[{"panel_id":"tajne","span":6,"sealed":true,"owner_crew_name":"Devops"}]}`
)

// exportPageJSON serves a canned JSON body verbatim. The fixtures above are
// written as wire JSON rather than as Go maps because they are the SERVER's
// shape — round-tripping them through a map would let a typo in a field name
// pass as "the exporter dropped it".
func exportPageJSON(body string) clitest.Handler {
	return func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, []byte(body), "application/json"
	}
}

// exportPageStub wires a stub server holding one page index plus the
// documents the tests fetch by slug.
func exportPageStub(t *testing.T, index string, docs map[string]string) *clitest.StubServer {
	t.Helper()
	s := clitest.NewStubServer()
	t.Cleanup(s.Close)
	if index != "" {
		s.OnGet("/api/v1/pages", exportPageJSON(index))
	}
	for slug, body := range docs {
		s.OnGet("/api/v1/pages/"+slug, exportPageJSON(body))
	}
	covSetupCli10(t, s.URL())
	return s
}

// ─── 1. Registration ─────────────────────────────────────────────────────────

// TestExportPageCmd_IsRegistered — the whole point of the change. A kind
// whose exporter is not on the command tree does not exist to an agent
// reading `--help`.
func TestExportPageCmd_IsRegistered(t *testing.T) {
	t.Parallel()

	root := findSubcommand(rootCmd.Commands(), "export")
	if root == nil {
		t.Fatal("no `export` command on rootCmd")
	}
	page := findSubcommand(root.Commands(), "page")
	if page == nil {
		t.Fatal("`crewship export page` is not registered; kinds.ExportPages is unreachable again")
	}
	if page.Flags().Lookup("output") == nil {
		t.Error("`export page` has no --output flag; the sibling exports all do")
	}
}

// ─── 2. One page ─────────────────────────────────────────────────────────────

func TestRunExportPage_SinglePageIsAnApplyableDocument(t *testing.T) {
	exportPageStub(t, "", map[string]string{"fleet-201": exportPageFleetJSON})

	out, err := captureStdoutCovCli10(t, func() error {
		return runExportPage(exportPageCmd, []string{"fleet-201"})
	})
	if err != nil {
		t.Fatalf("runExportPage: %v", err)
	}
	for _, want := range []string{"kind: Page", "slug: fleet-201", "sla: 30s", "sla: 1h", "producer: routine/watch"} {
		if !strings.Contains(out, want) {
			t.Errorf("export missing %q:\n%s", want, out)
		}
	}

	// The real contract: apply can read it back.
	b, err := manifest.Load([]byte(out))
	if err != nil {
		t.Fatalf("exported YAML does not parse as a manifest: %v\n%s", err, out)
	}
	if len(b.Pages) != 1 {
		t.Fatalf("bundle carries %d Page documents, want 1:\n%s", len(b.Pages), out)
	}
	if got := b.Pages[0].Metadata.Slug; got != "fleet-201" {
		t.Errorf("slug = %q, want fleet-201", got)
	}
	if got := len(b.Pages[0].Spec.Panels); got != 2 {
		t.Errorf("panels = %d, want 2", got)
	}
}

func TestRunExportPage_UnknownSlugIsAnError(t *testing.T) {
	exportPageStub(t, "", nil) // every fetch 404s

	err := runExportPage(exportPageCmd, []string{"ghost"})
	if err == nil {
		t.Fatal("exporting a page that does not exist must fail, not print an empty document")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error does not name the slug: %v", err)
	}
}

// ─── 3. Every page ───────────────────────────────────────────────────────────

func TestRunExportPage_AllPagesIsAMultiDocumentStream(t *testing.T) {
	exportPageStub(t,
		`[{"slug":"fleet-201","name":"Flotila .201"},{"slug":"alpha","name":"Alpha"}]`,
		map[string]string{"fleet-201": exportPageFleetJSON, "alpha": exportPageAlphaJSON})

	out, err := captureStdoutCovCli10(t, func() error {
		return runExportPage(exportPageCmd, nil)
	})
	if err != nil {
		t.Fatalf("runExportPage: %v", err)
	}
	if n := strings.Count(out, "kind: Page"); n != 2 {
		t.Fatalf("kind: Page appears %d times, want 2:\n%s", n, out)
	}
	if !strings.Contains(out, "\n---\n") {
		t.Errorf("documents are not separated by ---, so apply reads one:\n%s", out)
	}
	b, err := manifest.Load([]byte(out))
	if err != nil {
		t.Fatalf("multi-document export does not parse: %v\n%s", err, out)
	}
	if len(b.Pages) != 2 {
		t.Fatalf("bundle carries %d Page documents, want 2", len(b.Pages))
	}
	// Sorted by slug (kinds.ExportPages), so the file is diff-stable.
	if b.Pages[0].Metadata.Slug != "alpha" || b.Pages[1].Metadata.Slug != "fleet-201" {
		t.Errorf("not sorted by slug: %q, %q", b.Pages[0].Metadata.Slug, b.Pages[1].Metadata.Slug)
	}
}

func TestRunExportPage_EmptyWorkspaceWritesNothing(t *testing.T) {
	exportPageStub(t, `[]`, nil)
	out := filepath.Join(t.TempDir(), "pages.yaml")
	setFlagCovCli10(t, exportPageCmd, "output", out)

	stderr, err := captureStderrCov(t, func() error {
		return runExportPage(exportPageCmd, nil)
	})
	if err != nil {
		t.Fatalf("runExportPage: %v", err)
	}
	if !strings.Contains(stderr, "no pages") {
		t.Errorf("silence about an empty workspace: %q", stderr)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("an empty workspace wrote a file; an empty manifest deletes nothing but says nothing either")
	}
}

// ─── 4. The refusals ─────────────────────────────────────────────────────────

// TestRunExportPage_SealedPanelRefusalIsUsable is the CLI end of
// kinds.TestExportPages_SealedPanelRefuses: the refusal has to arrive as a
// sentence that says what to do, and it must not leave a truncated file
// where the operator's backup was.
func TestRunExportPage_SealedPanelRefusalIsUsable(t *testing.T) {
	exportPageStub(t, "", map[string]string{"fleet-201": exportPageSealedJSON})
	out := filepath.Join(t.TempDir(), "fleet.page.yaml")
	setFlagCovCli10(t, exportPageCmd, "output", out)

	err := runExportPage(exportPageCmd, []string{"fleet-201"})
	if err == nil {
		t.Fatal("a sealed panel must refuse the export, not emit a page missing a panel")
	}
	for _, want := range []string{"sealed", "tajne", "fleet-201"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("the refused export still created --output; a partial file is what the refusal exists to prevent")
	}
}

func TestRunExportPage_ListFailureIsNotAnEmptyExport(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/pages", clitest.ErrorResponse(503, "pages unavailable"))
	covSetupCli10(t, s.URL())

	if err := runExportPage(exportPageCmd, nil); err == nil {
		t.Fatal("a failed list must not export as a workspace with no pages")
	}
}

func TestRunExportPage_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := runExportPage(exportPageCmd, []string{"fleet-201"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

// ─── 5. The gap, disclosed ───────────────────────────────────────────────────

// TestRunExportPage_DisclosesTheAuthoredHalf — the authored half reaches only
// an account that may EDIT the page (attachAuthoredHalf, pages_handler.go).
// Exported by a reader or a producer the same command emits the grid alone,
// and the exporter cannot tell that page from one that simply declares no
// actions: an absent field is not evidence. So the caveat names the condition
// on every run, because the command cannot know which case it is in.
//
// The companion below pins the other half — that an editor's export is NOT
// lossy — because the first version of this command shipped a caveat claiming
// it always was, which would have sent every operator merging by hand against
// a loss that was not there.
func TestRunExportPage_DisclosesTheAuthoredHalf(t *testing.T) {
	exportPageStub(t, "", map[string]string{"fleet-201": exportPageFleetJSON})

	stderr, err := captureStderrCov(t, func() error {
		_, runErr := captureStdoutCovCli10(t, func() error {
			return runExportPage(exportPageCmd, []string{"fleet-201"})
		})
		return runErr
	})
	if err != nil {
		t.Fatalf("runExportPage: %v", err)
	}
	for _, want := range []string{"actions", "wake", "on_failure", "public"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the caveat does not name %q:\n%s", want, stderr)
		}
	}
}

func TestRunExportPage_WritesOutputFile(t *testing.T) {
	exportPageStub(t, "", map[string]string{"fleet-201": exportPageFleetJSON})
	out := filepath.Join(t.TempDir(), "fleet.page.yaml")
	setFlagCovCli10(t, exportPageCmd, "output", out)

	stderr, err := captureStderrCov(t, func() error {
		return runExportPage(exportPageCmd, []string{"fleet-201"})
	})
	if err != nil {
		t.Fatalf("runExportPage: %v", err)
	}
	if !strings.Contains(stderr, "wrote "+out) {
		t.Errorf("wrote-banner missing: %q", stderr)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(b), "kind: Page") {
		t.Errorf("file content wrong:\n%s", b)
	}
}

// TestRunExportPage_AnEditorsExportCarriesTheAuthoredHalf is the claim the
// caveat used to deny. `public`, `actions`, `wake` and `on_failure` are on the
// wire for a caller who may edit the spec, and kinds/page.go writes every one
// of them into the document — so an editor's export round-trips, and telling
// them otherwise would be a warning that costs real work and buys nothing.
func TestRunExportPage_AnEditorsExportCarriesTheAuthoredHalf(t *testing.T) {
	exportPageStub(t, "", map[string]string{"fleet-201": exportPageAuthoredJSON})

	out, err := captureStdoutCovCli10(t, func() error {
		return runExportPage(exportPageCmd, []string{"fleet-201"})
	})
	if err != nil {
		t.Fatalf("runExportPage: %v", err)
	}
	for _, want := range []string{"actions:", "wake:", "on_failure:", "public:"} {
		if !strings.Contains(out, want) {
			t.Errorf("the exported manifest dropped %q:\n%s", want, out)
		}
	}
}
