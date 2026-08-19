package main

// cmd_page_transfer_test.go — the acceptance test for `crewship page
// export|import|versions|rollback` (docs/prd/pages.md §10b.1, §10b.2, §11b.13).
//
// The endpoint half is proved in internal/api/pages_transfer_test.go and
// internal/api/pages_versions_test.go. This file proves the half only the
// client can:
//
//   - `--bind` is REPEATABLE and is not comma-split (§11b.13). Two flags mean
//     two bindings; one flag containing a comma is one binding, because a slug
//     may contain a comma far more plausibly than a flag may be repeated.
//   - a bundle survives the trip through a FILE: export prints YAML, import
//     reads it back, and the panels arrive at the server unchanged.
//   - a refusal reaches the operator naming the reference and the panels that
//     need it, rather than as a status code.
//   - rollback is destructive of the current arrangement and gates on
//     confirmAction like every other destructive command here (§11b.5).

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

const (
	pageXferSlug   = "weekly-close"
	pageXferBundle = `{
		"format": "crewship-page-bundle/v1",
		"page": {
			"name": "Tydenni uzaverka",
			"slug": "weekly-close",
			"panels": [
				{"id":"sluzby","schema":"status.v1","title":"Jede to?","owner":"crew/ucetni",
				 "producer":"script/watch-services.sh","sla_seconds":30,"span":8},
				{"id":"vysledky","schema":"metric.v1","owner":"crew/ucetni",
				 "producer":"routine/nocni-uzaverka","sla_seconds":3600,"span":4}
			]
		},
		"references": [
			{"ref":"crew/ucetni","kind":"crew","bindable":true,"used_by":["sluzby","vysledky"]},
			{"ref":"routine/nocni-uzaverka","kind":"routine","bindable":true,"used_by":["vysledky"]},
			{"ref":"script/watch-services.sh","kind":"script","bindable":false,"used_by":["sluzby"]}
		],
		"metadata": {"exported_at":"2026-08-12T09:14:22Z","panel_count":2}
	}`
)

var (
	pageExportRoute   = "/api/v1/pages/" + pageXferSlug + "/export"
	pageImportRoute   = "/api/v1/pages/import"
	pageVersionsRoute = "/api/v1/pages/" + pageXferSlug + "/versions"
	pageRollbackRoute = "/api/v1/pages/" + pageXferSlug + "/rollback"
)

// runPageTransferCLI is runPageCLI with this surface's flags reset first.
//
// Same reason as runPageGrantCLI: the command tree is package-level state and
// cobra keeps a flag's value between Execute calls, so `--bind` from one
// invocation would still be set during the next — which for THIS surface reads
// as the operator having bound something they never mentioned.
func runPageTransferCLI(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	page := findSubcommand(rootCmd.Commands(), "page")
	if page == nil {
		return runPageCLI(t, stdin, args...)
	}
	for _, name := range []string{"export", "import", "versions", "rollback"} {
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
	return runPageCLI(t, stdin, args...)
}

// pageBundleFile writes a bundle to a temp file and returns its path.
func pageBundleFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bundle.page.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return path
}

// ─── 1. Registration ─────────────────────────────────────────────────────────

// TestPageTransferCLI_IsRegistered — one command per endpoint is the repo
// rule, and the command tree is what an agent reads.
func TestPageTransferCLI_IsRegistered(t *testing.T) {
	t.Parallel()

	root := findSubcommand(rootCmd.Commands(), "page")
	if root == nil {
		t.Fatalf("no `page` command on rootCmd")
	}
	for _, want := range []string{"export", "import", "versions", "rollback"} {
		if findSubcommand(root.Commands(), want) == nil {
			t.Errorf("`page %s` is not registered — §10b.1/§10b.2 map it to an endpoint", want)
		}
	}
}

// ─── 2. --bind ───────────────────────────────────────────────────────────────

// TestPageCLI_ImportBindIsRepeatableAndNotCommaSplit is §11b.13, which chose
// the repeatable flag over the comma-separated one for a reason this test
// pins in both directions.
func TestPageCLI_ImportBindIsRepeatableAndNotCommaSplit(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageImportRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusCreated,
			[]byte(`{"slug":"uzaverka","name":"Tydenni uzaverka","panels":[{"id":"sluzby"},{"id":"vysledky"}]}`),
			"application/json"
	})
	file := pageBundleFile(t, pageXferBundle)

	// Repeated twice: two bindings.
	out, err := runPageTransferCLI(t, "", "page", "import", file, "--slug", "uzaverka",
		"--bind", "crew/ucetni=crew/finance",
		"--bind", "routine/nocni-uzaverka=routine/mesicni")
	if err != nil {
		t.Fatalf("page import: %v\n%s", err, out)
	}
	calls := stub.CallsFor("POST", pageImportRoute)
	if len(calls) != 1 {
		t.Fatalf("POST %s called %d times, want 1", pageImportRoute, len(calls))
	}
	var body struct {
		Format string            `json:"format"`
		Slug   string            `json:"slug"`
		Bind   map[string]string `json:"bind"`
	}
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, string(calls[0].Body))
	}
	if len(body.Bind) != 2 {
		t.Fatalf("bind = %v, want both bindings — --bind is repeatable", body.Bind)
	}
	if body.Bind["crew/ucetni"] != "crew/finance" || body.Bind["routine/nocni-uzaverka"] != "routine/mesicni" {
		t.Errorf("bind = %v, want each flag's own pair", body.Bind)
	}
	if body.Slug != "uzaverka" {
		t.Errorf("slug = %q, want the --slug the operator chose", body.Slug)
	}
	if body.Format != "crewship-page-bundle/v1" {
		t.Errorf("format = %q, want the bundle's own", body.Format)
	}

	// One flag carrying a comma is ONE binding. §11b.13: "a slug may contain a
	// comma far more plausibly than a repeated flag" — splitting here is how a
	// binding silently becomes two wrong ones.
	stub.ResetCalls()
	if out, err := runPageTransferCLI(t, "", "page", "import", file, "--slug", "uzaverka",
		"--bind", "crew/a,b=crew/c,d"); err != nil {
		t.Fatalf("page import with a comma in a slug: %v\n%s", err, out)
	}
	calls = stub.CallsFor("POST", pageImportRoute)
	if len(calls) != 1 {
		t.Fatalf("POST called %d times, want 1", len(calls))
	}
	body.Bind = nil
	_ = json.Unmarshal(calls[0].Body, &body)
	if len(body.Bind) != 1 || body.Bind["crew/a,b"] != "crew/c,d" {
		t.Errorf("bind = %v, want the one binding the operator wrote, commas intact", body.Bind)
	}
}

// TestPageCLI_ImportRefusesAContradictoryBindLocally — one reference binds to
// one thing. Resolving a repeated left-hand side last-wins would silently pick
// a binding the operator has lost track of.
func TestPageCLI_ImportRefusesAContradictoryBindLocally(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageImportRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		t.Error("a contradictory --bind reached the server; it must be refused locally")
		return http.StatusCreated, []byte(`{}`), "application/json"
	})
	file := pageBundleFile(t, pageXferBundle)

	out, err := runPageTransferCLI(t, "", "page", "import", file,
		"--bind", "crew/ucetni=crew/finance",
		"--bind", "crew/ucetni=crew/provoz")
	if err == nil {
		t.Fatalf("a reference bound twice was accepted:\n%s", out)
	}
	if !strings.Contains(err.Error(), "crew/ucetni") {
		t.Errorf("the refusal does not name the reference bound twice: %v", err)
	}
	if len(stub.CallsFor("POST", pageImportRoute)) != 0 {
		t.Error("the CLI sent the import anyway")
	}

	// A binding with no `=` is refused the same way, before any round trip.
	if _, err := runPageTransferCLI(t, "", "page", "import", file, "--bind", "crew/ucetni"); err == nil {
		t.Error("--bind without an `=` was accepted")
	}
}

// ─── 3. The file round trip ──────────────────────────────────────────────────

// TestPageCLI_ExportWritesABundleImportCanRead — §10b.2's own example is
// `crewship page export <slug> > weekly-close.page.yaml` followed by an
// import of that file, so the two commands have to agree about the document
// on disk.
func TestPageCLI_ExportWritesABundleImportCanRead(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet(pageExportRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, []byte(pageXferBundle), "application/json"
	})
	stub.OnPost(pageImportRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusCreated,
			[]byte(`{"slug":"uzaverka","name":"Tydenni uzaverka","panels":[{"id":"sluzby"},{"id":"vysledky"}]}`),
			"application/json"
	})

	out, err := runPageTransferCLI(t, "", "page", "export", pageXferSlug)
	if err != nil {
		t.Fatalf("page export: %v\n%s", err, out)
	}
	// Default output is YAML: a bundle is a document people keep in a
	// repository, and a table form of it could not be imported.
	var exported map[string]any
	if err := yaml.Unmarshal([]byte(out), &exported); err != nil {
		t.Fatalf("export did not print a YAML document: %v\n%s", err, out)
	}
	if exported["format"] != "crewship-page-bundle/v1" {
		t.Fatalf("exported document has no bundle format: %v", exported["format"])
	}

	path := pageBundleFile(t, out)
	if out, err := runPageTransferCLI(t, "", "page", "import", path, "--slug", "uzaverka"); err != nil {
		t.Fatalf("importing the exported file: %v\n%s", err, out)
	}
	calls := stub.CallsFor("POST", pageImportRoute)
	if len(calls) != 1 {
		t.Fatalf("POST %s called %d times, want 1", pageImportRoute, len(calls))
	}
	var body struct {
		Format string `json:"format"`
		Page   struct {
			Name   string `json:"name"`
			Panels []struct {
				ID         string `json:"id"`
				Schema     string `json:"schema"`
				Title      string `json:"title"`
				Owner      string `json:"owner"`
				Producer   string `json:"producer"`
				SLASeconds int    `json:"sla_seconds"`
				Span       int    `json:"span"`
			} `json:"panels"`
		} `json:"page"`
	}
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("import body is not JSON: %v\n%s", err, string(calls[0].Body))
	}
	if body.Format != "crewship-page-bundle/v1" || body.Page.Name != "Tydenni uzaverka" {
		t.Errorf("the bundle did not survive the file: %+v", body)
	}
	if len(body.Page.Panels) != 2 {
		t.Fatalf("import sent %d panels, want the 2 that were exported", len(body.Page.Panels))
	}
	a := body.Page.Panels[0]
	if a.ID != "sluzby" || a.Schema != "status.v1" || a.Title != "Jede to?" ||
		a.Owner != "crew/ucetni" || a.Producer != "script/watch-services.sh" ||
		a.SLASeconds != 30 || a.Span != 8 {
		t.Errorf("panel lost something on the way through YAML: %+v", a)
	}
	if body.Page.Panels[1].SLASeconds != 3600 {
		t.Errorf("second panel sla_seconds = %d, want 3600", body.Page.Panels[1].SLASeconds)
	}
}

// TestPageCLI_ImportRefusesSomethingThatIsNotABundle — a page DOCUMENT is not
// a bundle, and the error has to say which command the operator wanted.
func TestPageCLI_ImportRefusesSomethingThatIsNotABundle(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageImportRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		t.Error("a non-bundle reached the server")
		return http.StatusCreated, []byte(`{}`), "application/json"
	})
	path := pageBundleFile(t, "apiVersion: crewship/v1\nkind: Page\nmetadata:\n  name: x\n  slug: x\n")

	out, err := runPageTransferCLI(t, "", "page", "import", path)
	if err == nil {
		t.Fatalf("a page document was accepted as a bundle:\n%s", out)
	}
	if !strings.Contains(err.Error(), "page create") {
		t.Errorf("the refusal does not point at the command that authors a page: %v", err)
	}
}

// ─── 4. The refusal an operator has to act on ────────────────────────────────

// TestPageCLI_ImportSurfacesEveryUnresolvedReference — §10b.2: import "either
// binds everything or refuses and names the reference it could not resolve".
// The server sends the list; the CLI is what an operator actually reads.
func TestPageCLI_ImportSurfacesEveryUnresolvedReference(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageImportRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusUnprocessableEntity, []byte(`{
			"error": "import refused, nothing was created: 2 references do not resolve in this workspace",
			"unresolved": [
				{"ref":"crew/ucetni","kind":"crew","used_by":["sluzby","vysledky"],"reason":"no crew \"ucetni\" exists in this workspace"},
				{"ref":"routine/nocni-uzaverka","kind":"routine","used_by":["vysledky"],"reason":"no routine \"nocni-uzaverka\" exists in this workspace"}
			],
			"hint": "bind each one to something that exists here: crewship page import <file> --bind <bundle-ref>=<local-ref>"
		}`), "application/json"
	})
	file := pageBundleFile(t, pageXferBundle)

	out, err := runPageTransferCLI(t, "", "page", "import", file)
	if err == nil {
		t.Fatalf("a refused import returned success:\n%s", out)
	}
	msg := err.Error()
	for _, want := range []string{"crew/ucetni", "routine/nocni-uzaverka", "vysledky", "--bind"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q — an operator has to know what to bind "+
				"and for which panel:\n%s", want, msg)
		}
	}
}

// ─── 5. versions and rollback ────────────────────────────────────────────────

// TestPageCLI_VersionsShowsWhatToRollBackTo — the command exists so a human
// can choose a seq, so the seq and who saved it have to be on screen.
func TestPageCLI_VersionsShowsWhatToRollBackTo(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet(pageVersionsRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, []byte(`{
			"page": "weekly-close", "retained": 50,
			"versions": [
				{"seq":7,"created_at":"2026-08-12T09:14:22Z","author":"agent/uklizec","author_label":"uklizec",
				 "name":"Tydenni uzaverka","panel_count":3,"current":true},
				{"seq":6,"created_at":"2026-08-11T09:14:22Z","author":"user/usr_1","author_label":"ada@example.com",
				 "name":"Tydenni uzaverka","panel_count":4,"current":false}
			]
		}`), "application/json"
	})

	out, err := runPageTransferCLI(t, "", "page", "versions", pageXferSlug)
	if err != nil {
		t.Fatalf("page versions: %v\n%s", err, out)
	}
	for _, want := range []string{"7", "6", "uklizec", "ada@example.com", "2026-08-12T09:14:22Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("the version list does not show %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "rollback") {
		t.Errorf("the listing does not say how to use what it shows:\n%s", out)
	}
}

// TestPageCLI_RollbackSendsTheSeqAndReportsTheDimmedPanels — the flag reaches
// the wire as `to`, and §10b.1's consequence is reported rather than left for
// the operator to discover on the page.
func TestPageCLI_RollbackSendsTheSeqAndReportsTheDimmedPanels(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageRollbackRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, []byte(`{
			"page": {"slug":"weekly-close","name":"Tydenni uzaverka","panels":[]},
			"rolled_back_to": 6, "version": 8,
			"awaiting_data": ["vysledky","gama"]
		}`), "application/json"
	})

	out, err := runPageTransferCLI(t, "", "page", "rollback", pageXferSlug, "--to", "6", "--yes")
	if err != nil {
		t.Fatalf("page rollback: %v\n%s", err, out)
	}
	calls := stub.CallsFor("POST", pageRollbackRoute)
	if len(calls) != 1 {
		t.Fatalf("POST %s called %d times, want 1", pageRollbackRoute, len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("rollback body is not JSON: %v\n%s", err, string(calls[0].Body))
	}
	if to, _ := body["to"].(float64); int(to) != 6 {
		t.Errorf("body to = %v, want 6", body["to"])
	}
	for _, want := range []string{"6", "8", "vysledky", "gama"} {
		if !strings.Contains(out, want) {
			t.Errorf("the confirmation does not report %q — a panel that comes back dimmed is news:\n%s", want, out)
		}
	}
}

// TestPageCLI_RollbackNeedsATargetAndAConfirmation — a rollback replaces the
// current arrangement, so it is destructive and gates like every other
// destructive command here (§11b.5).
func TestPageCLI_RollbackNeedsATargetAndAConfirmation(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageRollbackRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		t.Error("rollback reached the server without a confirmed target")
		return http.StatusOK, []byte(`{}`), "application/json"
	})

	// No --to: refused locally, and the message points at the command that
	// tells you what the seqs are.
	out, err := runPageTransferCLI(t, "", "page", "rollback", pageXferSlug)
	if err == nil {
		t.Fatalf("rollback without --to was accepted:\n%s", out)
	}
	if !strings.Contains(err.Error(), "page versions") {
		t.Errorf("the refusal does not point at `page versions`: %v", err)
	}

	// A target but no --yes, with nothing on stdin: aborted, nothing sent.
	if _, err := runPageTransferCLI(t, "", "page", "rollback", pageXferSlug, "--to", "6"); err == nil {
		t.Error("rollback ran without a confirmation")
	}
	if len(stub.CallsFor("POST", pageRollbackRoute)) != 0 {
		t.Error("an unconfirmed rollback reached the server")
	}
}
