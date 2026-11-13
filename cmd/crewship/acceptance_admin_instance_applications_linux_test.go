//go:build !clionly && linux

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
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/craftassets"
	"github.com/crewship-ai/crewship/internal/craftassets/craftassetstest"
	"github.com/crewship-ai/crewship/internal/craftengine"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// testInstaller installs real packages into a store the way the server does
// and returns a profile whose WASM engine really runs.
func testInstaller(catalog craftassets.Catalog, store craftassets.Store) api.CraftComponentInstall {
	return func(ctx context.Context, app string, archive io.Reader) (api.CraftNativeDocumentProfile, craftapps.Component, error) {
		var inst *craftassets.Installed
		var err error
		if archive == nil {
			inst, err = store.Download(ctx, catalog, app, nil)
		} else {
			inst, err = store.Install(catalog.Pins[app], archive)
		}
		if err != nil {
			return api.CraftNativeDocumentProfile{}, craftapps.Component{}, err
		}
		c := inst.Component
		engine, err := craftengine.NewLazyWASM(app, c.Engine.SHA256, inst.EngineLoader(), craftengine.Limits{})
		if err != nil {
			return api.CraftNativeDocumentProfile{}, craftapps.Component{}, err
		}
		return api.CraftNativeDocumentProfile{Application: app, EngineRevision: c.EngineRevision, Engine: engine, CLISHA256: c.Engine.SHA256,
				EditorBundle: c.Editor.Digest, EditorURL: "/_editor/b/" + c.Editor.Digest + "/index.html", Bundled: true},
			craftapps.Component{Available: true, Runtime: craftengine.RuntimeWASM, Version: c.Version, EngineRevision: c.EngineRevision,
				EngineSHA256: c.Engine.SHA256, EditorBundle: c.Editor.Digest}, nil
	}
}

// A real CLI process against a real router: Word, Sheet and agents start off
// and neither application is installed. An instance administrator turns Word
// on, which downloads, verifies and starts it without a restart; installs
// Sheet offline from a file (a wrong file is refused and says why); runs each
// real WASM engine's self-test; turns Word off (drain, immediate close); a
// non-admin is refused; an operator-fixed setting cannot be changed; and the
// "New Word" document status follows the setting.
func TestAcceptance_AdminInstanceApplications(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := testutil.MigratedDB(t).DB
	admin, member := "crewship_cli_craftappsadmin00000000", "crewship_cli_craftappsmember000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('ca-a','Alpha','alpha','2026-01-01 00:00:00'),('ca-b','Beta','beta','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('ca-boss','boss@alpha.invalid','Boss'),('ca-eve','eve@beta.invalid','Eve')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ca-m1','ca-a','ca-boss','OWNER'),('ca-m2','ca-b','ca-eve','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	for id, tok := range map[string]string{"ca-boss": admin, "ca-eve": member} {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'test',?,datetime('now'))`, "tok-"+id, id, sha256HexToken(tok)); err != nil {
			t.Fatal(err)
		}
	}
	word := craftassetstest.Build("wordcraft", "0.11.0+b0fb5cb571b6", "Word")
	sheet := craftassetstest.Build("gridcraft", "0.4.0+e68d19220a1b", "Sheet")
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.0.0/"+craftassets.AssetName("wordcraft") {
			_, _ = w.Write(word.Archive)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(mirror.Close)
	catalog := craftassets.Catalog{Pins: map[string]craftassets.Pin{"wordcraft": word.Pin, "gridcraft": sheet.Pin}, BaseURL: mirror.URL + "/v1.0.0/"}
	packages := t.TempDir()
	sheetFile, wordFile := filepath.Join(packages, "sheet.craftpkg"), filepath.Join(packages, "word.craftpkg")
	if os.WriteFile(sheetFile, sheet.Archive, 0o600) != nil || os.WriteFile(wordFile, word.Archive, 0o600) != nil {
		t.Fatal("write packages")
	}
	newServer := func(policy *craftapps.Policy) *httptest.Server {
		t.Helper()
		router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCraftNativeHTTP(api.CraftNativeHTTPConfig{
			Enabled: true, Policy: policy, Install: testInstaller(catalog, craftassets.Store{Root: t.TempDir()}),
			Components: map[string]craftapps.Component{
				"wordcraft": {Reason: craftapps.ReasonNotInstalled, Version: word.Pin.Version},
				"gridcraft": {Reason: craftapps.ReasonNotInstalled, Version: sheet.Pin.Version},
			}}))
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(router)
		t.Cleanup(srv.Close)
		return srv
	}
	srv := newServer(&craftapps.Policy{})
	binary := buildCrewshipBinary(t)
	runAt := func(url, token string, args ...string) (string, error) {
		cfg := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(cfg, []byte("server: "+url+"\ntoken: "+token+"\nformat: table\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	run := func(token string, args ...string) (string, error) { return runAt(srv.URL, token, args...) }
	show := func() adminCraftApplications {
		t.Helper()
		raw, err := run(admin, "admin", "instance", "applications", "list", "--format", "json")
		if err != nil {
			t.Fatalf("list: %v\n%s", err, raw)
		}
		var got adminCraftApplications
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("json: %v\n%s", err, raw)
		}
		return got
	}
	app := func(v adminCraftApplications, id string) adminCraftApplication {
		for _, a := range v.Applications {
			if a.ID == id {
				return a
			}
		}
		t.Fatalf("%s missing from %+v", id, v)
		return adminCraftApplication{}
	}
	newWord := func() bool {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/documents/status", nil)
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("X-Workspace-ID", "ca-a")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var status struct {
			WordCraftNative bool `json:"wordcraft_native"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			t.Fatalf("documents status %d: %v", resp.StatusCode, err)
		}
		return status.WordCraftNative
	}

	ready := func(id string) adminCraftApplication {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			a := app(show(), id)
			if a.Reason != "component_installing" || time.Now().After(deadline) {
				return a
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Everything starts off; nothing is installed.
	v := show()
	if w := app(v, "wordcraft"); w.Desired != "disabled" || w.Source != "default" || w.State != "unavailable" || w.Reason != "component_not_installed" || w.Documents == nil || *w.Documents != 0 {
		t.Fatalf("Word default = %+v", w)
	}
	if s := app(v, "gridcraft"); s.State != "unavailable" || s.Reason != "component_not_installed" {
		t.Fatalf("Sheet default = %+v", s)
	}
	if v.Agents.State != "disabled" {
		t.Fatalf("agents default = %+v", v.Agents)
	}
	if newWord() {
		t.Fatal("New Word offered while Word is off")
	}
	if out, err := run(admin, "admin", "instance", "applications"); err != nil || !strings.Contains(out, "Word") || !strings.Contains(out, "Not installed") {
		t.Fatalf("show: %v\n%s", err, out)
	}

	// Not an instance administrator: refused, nothing written or installed.
	for _, args := range [][]string{{"applications"}, {"applications", "enable", "word"}, {"applications", "doctor", "word"}, {"applications", "install", "sheet", "--file", sheetFile}} {
		if out, err := run(member, append([]string{"admin", "instance"}, args...)...); err == nil {
			t.Fatalf("non-admin %v succeeded:\n%s", args, out)
		}
	}
	// Invalid input.
	for _, args := range [][]string{{"enable", "powerpoint"}, {"disable", "word", "--drain", "25h"}, {"doctor", "agents"}, {"enable"}, {"install", "agents"}, {"install", "sheet", "--file", filepath.Join(packages, "missing")}} {
		if out, err := run(admin, append([]string{"admin", "instance", "applications"}, args...)...); err == nil {
			t.Fatalf("%v accepted:\n%s", args, out)
		}
	}
	if w := app(show(), "wordcraft"); w.Source != "default" || w.Reason != "component_not_installed" {
		t.Fatalf("a refused change wrote: %+v", w)
	}

	// Agents alone do nothing until an application is on.
	if out, err := run(admin, "admin", "instance", "applications", "enable", "agents"); err != nil {
		t.Fatalf("enable agents: %v\n%s", err, out)
	}
	if a := show().Agents; a.State != "unavailable" || a.Reason != "agents_need_application" {
		t.Fatalf("agents without an application = %+v", a)
	}
	// Turning Word on downloads it; it runs without a restart.
	if out, err := run(admin, "admin", "instance", "applications", "enable", "word"); err != nil {
		t.Fatalf("enable word: %v\n%s", err, out)
	}
	if w := ready("wordcraft"); w.State != "ready" || w.Source != "admin" || w.UpdatedBy != "boss@alpha.invalid" || w.Component == nil || w.Component.EditorBundle != word.Editor || w.Component.EngineSHA256 != word.EngineSHA256 {
		t.Fatalf("after enable = %+v", w)
	}
	if a := show().Agents; a.State != "ready" {
		t.Fatalf("agents with Word on = %+v", a)
	}
	if !newWord() {
		t.Fatal("New Word not offered while Word is on")
	}
	// Repeating is a no-op and writes no second audit row; a running
	// application is not installed again.
	if _, err := run(admin, "admin", "instance", "applications", "enable", "word"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(admin, "admin", "instance", "applications", "install", "word"); err == nil || !strings.Contains(out, "already runs") {
		t.Fatalf("install a running application: %v\n%s", err, out)
	}
	out, err := run(admin, "admin", "instance", "applications", "doctor", "word")
	if err != nil || !strings.Contains(out, "passed") || !strings.Contains(out, "wordcraft-cli 0.11.0+b0fb5cb571b6") {
		t.Fatalf("doctor word: %v\n%s", err, out)
	}
	if out, err := run(admin, "admin", "instance", "applications", "doctor", "sheet"); err == nil || !strings.Contains(out, "Not installed") {
		t.Fatalf("doctor sheet before install: %v\n%s", err, out)
	}

	// Offline: the Word package is not Sheet's, and Admin says why; the
	// right file installs Sheet, still off for everyone.
	if out, err := run(admin, "admin", "instance", "applications", "install", "sheet", "--file", wordFile); err == nil || !strings.Contains(out, "refused") {
		t.Fatalf("wrong package: %v\n%s", err, out)
	}
	if s := app(show(), "gridcraft"); s.Reason != "component_install_failed" || s.Component == nil || !strings.Contains(s.Component.Detail, "pinned") {
		t.Fatalf("after a refused package = %+v", s)
	}
	if out, err := run(admin, "admin", "instance", "applications", "install", "sheet", "--file", sheetFile); err != nil {
		t.Fatalf("install sheet from a file: %v\n%s", err, out)
	}
	if s := app(show(), "gridcraft"); s.State != "disabled" || s.Component == nil || !s.Component.Available || s.Component.Version != sheet.Pin.Version {
		t.Fatalf("Sheet installed = %+v", s)
	}
	if out, err := run(admin, "admin", "instance", "applications", "doctor", "sheet"); err != nil || !strings.Contains(out, "gridcraft-cli 0.4.0") {
		t.Fatalf("doctor sheet: %v\n%s", err, out)
	}

	// Off with a drain, then at once.
	if out, err := run(admin, "admin", "instance", "applications", "disable", "word", "--drain", "10m"); err != nil || !strings.Contains(out, "draining") {
		t.Fatalf("disable with drain: %v\n%s", err, out)
	}
	if w := app(show(), "wordcraft"); w.State != "draining" || w.DrainUntil == nil {
		t.Fatalf("draining = %+v", w)
	}
	if newWord() {
		t.Fatal("New Word offered while Word drains")
	}
	if out, err := run(admin, "admin", "instance", "applications", "disable", "word", "--now"); err != nil {
		t.Fatalf("disable now: %v\n%s", err, out)
	}
	if w := app(show(), "wordcraft"); w.State != "disabled" || w.DrainUntil != nil {
		t.Fatalf("closed = %+v", w)
	}
	var audited int
	if err := db.QueryRow(`SELECT count(*) FROM instance_audit_logs WHERE action='instance.craft_application_set'`).Scan(&audited); err != nil || audited != 4 {
		t.Fatalf("audit rows %d %v (agents on, word on, word draining, word closed)", audited, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM instance_audit_logs WHERE action='instance.craft_application_install'`).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("install audit rows %d %v (two Sheet files)", audited, err)
	}

	// A setting the operator fixed in the server configuration cannot be
	// changed here, and Admin says so.
	locked := newServer(&craftapps.Policy{Locked: map[string]bool{"wordcraft": true}})
	out, err = runAt(locked.URL, admin, "admin", "instance", "applications", "disable", "word")
	if err == nil || !strings.Contains(out, "server configuration") {
		t.Fatalf("locked disable: %v\n%s", err, out)
	}
	raw, err := runAt(locked.URL, admin, "admin", "instance", "applications", "--format", "json")
	var lv adminCraftApplications
	if err != nil || json.Unmarshal([]byte(raw), &lv) != nil {
		t.Fatalf("locked list: %v\n%s", err, raw)
	}
	if w := app(lv, "wordcraft"); !w.Locked || w.Source != "server_config" || w.Reason != "component_not_installed" {
		t.Fatalf("locked Word = %+v", w)
	}
}
