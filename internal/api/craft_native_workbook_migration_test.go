package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type migrationTestGridCraft struct {
	workbook, reopenedJSON []byte
	conversions            atomic.Int32
	beforeExport           func() error
}

func (*migrationTestGridCraft) Application() string { return "gridcraft" }
func (*migrationTestGridCraft) Runtime() string     { return "wasm" }
func (*migrationTestGridCraft) SHA256() string      { return strings.Repeat("b", 64) }
func (*migrationTestGridCraft) Root(string) string  { return "/work" }
func (e *migrationTestGridCraft) Run(_ context.Context, dir string, args []string, stdout, stderr io.Writer) error {
	if len(args) < 4 || args[0] != "convert" || args[3] != "--saved-values" {
		return fmt.Errorf("unexpected converter call %v", args)
	}
	output := e.reopenedJSON
	if filepath.Ext(args[2]) == ".xlsx" {
		if len(args) != 5 || args[4] != "--fidelity" {
			return fmt.Errorf("conversion omitted fidelity check")
		}
		e.conversions.Add(1)
		if e.beforeExport != nil {
			if err := e.beforeExport(); err != nil {
				return err
			}
		}
		output = e.workbook
		_, _ = io.WriteString(stdout, "{\"parts\":[]}\n")
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(args[2])), output, 0600); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stderr, "wrote %s (%d bytes)\n", args[2], len(output))
	return err
}

func migrationTestWorkbook(t *testing.T) []byte {
	t.Helper()
	var result bytes.Buffer
	archive := zip.NewWriter(&result)
	for name, text := range map[string]string{
		"[Content_Types].xml":      `<Types><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`,
		"xl/workbook.xml":          `<workbook/>`,
		"xl/worksheets/sheet1.xml": `<worksheet/>`,
	} {
		part, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(part, text); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func TestCraftLegacyWorkbookConversion(t *testing.T) {
	const page = "legacy-sheet"
	original := []byte("{\n  \"sheets\":[{\"name\":\"Sheet1\",\"value\":7,\"formula\":\"=3+4\",\"style\":{\"bold\":true},\"pivots\":[{\"name\":\"Summary\"}]}], \"names\":[\"Total\"]\n}\n")
	workbook := migrationTestWorkbook(t)
	for _, scenario := range []struct {
		name, altered string
		moveHead      bool
	}{
		{name: "faithful"},
		{name: "concurrent edit", moveHead: true},
		{name: "value", altered: `"value":7|"value":9`},
		{name: "formula", altered: `"formula":"=3+4"|"formula":"=7"`},
		{name: "style", altered: `"bold":true|"bold":false`},
		{name: "defined name", altered: `"names":["Total"]|"names":[]`},
		{name: "pivot", altered: `"pivots":[{"name":"Summary"}]|"pivots":[]`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, _, _ := craftNativeHTTPFixture(t)
			f.seedPage(t, page, page, "Synthetic legacy Sheet", f.userID, "")
			mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
			mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES(?,?,'gridcraft','e68d192282499bed81a9800d742cb9b239d367e5',?,?)`, page, f.wsID, strings.Repeat("a", 64), strings.Repeat("d", 64))
			key := artifacts.VersionKey{WorkspaceID: f.wsID, PageID: page}
			store := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
			author := func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }
			if _, err := store.Apply(t.Context(), key, artifacts.VersionRequest{EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: original, MediaType: "application/json"}}}, author); err != nil {
				t.Fatal(err)
			}
			reopened := original
			if scenario.altered != "" {
				old, next, _ := strings.Cut(scenario.altered, "|")
				reopened = bytes.Replace(original, []byte(old), []byte(next), 1)
			}
			engine := &migrationTestGridCraft{workbook: workbook, reopenedJSON: reopened}
			concurrentSource := append(append([]byte(nil), original...), ' ')
			if scenario.moveHead {
				engine.beforeExport = func() error {
					_, err := store.Apply(t.Context(), key, artifacts.VersionRequest{
						BaseVersion: 1, EntryPoint: "document.json",
						Files: map[string]artifacts.VersionFile{"document.json": {Content: concurrentSource, MediaType: "application/json"}},
					}, author)
					return err
				}
			}
			bundle := strings.Repeat("c", 64)
			cfg := CraftNativeHTTPConfig{Enabled: true, newOwnerClock: func() (mooring.OwnerClock, error) {
				return &collaborationAdmissionTestClock{boot: "legacy-sheet-boot", now: time.Second}, nil
			}, Profiles: map[string]CraftNativeDocumentProfile{
				"gridcraft": {Application: "gridcraft", EngineRevision: strings.Repeat("c", 40), Engine: engine, EditorBundle: bundle, EditorURL: "/_editor/b/" + bundle + "/index.html", Workbook: true},
			}}
			bearer := mintIngressScopedToken(t, f.db, f.userID, "legacy-sheet-owner", "pages:*")
			start := func() (*Router, func(string, string, string, string, string, any) (int, json.RawMessage), string) {
				t.Helper()
				router, err := NewRouter(f.db, "legacy-sheet-route-secret-32chars", newTestLogger(), WithCraftNativeHTTP(cfg))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(router.Shutdown)
				call := craftNativeInitializeTCP(t, router, f.wsID)
				status, raw := call(http.MethodPost, page, "sessions", bearer, "", struct{}{})
				var session struct {
					Token string `json:"session_token"`
				}
				if status != 201 || json.Unmarshal(raw, &session) != nil || session.Token == "" {
					t.Fatalf("native admission: %d %s", status, raw)
				}
				return router, call, session.Token
			}
			checkSource := func(status int, raw json.RawMessage) {
				t.Helper()
				if status != 200 || sha256.Sum256(raw) != sha256.Sum256(original) {
					t.Fatalf("original download: HTTP %d, SHA-256 %x", status, sha256.Sum256(raw))
				}
			}
			router, call, token := start()
			status, raw := call(http.MethodGet, page, "document", bearer, token, nil)
			if status != 409 || !bytes.Contains(raw, []byte(`"legacy_workbook"`)) {
				t.Fatalf("legacy read: %d %s", status, raw)
			}
			status, raw = call(http.MethodPost, page, "document/migrate-workbook", bearer, token, nil)
			if scenario.moveHead {
				state, err := store.Read(t.Context(), key, author)
				if status != 409 || err != nil || state.LatestVersion != 2 || !bytes.Equal(state.Manifest.Files["document.json"].Content, concurrentSource) {
					t.Fatalf("concurrent edit: migration HTTP %d, head %d, state error %v", status, state.LatestVersion, err)
				}
				return
			}
			if scenario.altered != "" {
				if status != 422 || !bytes.Contains(raw, []byte("legacy_workbook_fidelity")) {
					t.Fatalf("lossy conversion: %d %s", status, raw)
				}
				state, err := store.Read(t.Context(), key, author)
				if err != nil || state.LatestVersion != 1 {
					t.Fatalf("refused conversion changed the head: %d %v", state.LatestVersion, err)
				}
				checkSource(call(http.MethodGet, page, "document/original-workbook", bearer, token, nil))
				return
			}

			if status != 200 || !bytes.Contains(raw, []byte(`"version":"2"`)) {
				t.Fatalf("conversion: %d %s", status, raw)
			}
			router.Shutdown()
			_, call, token = start()
			status, raw = call(http.MethodPost, page, "document/migrate-workbook", bearer, token, nil)
			if status != 200 || !bytes.Contains(raw, []byte(`"version":"2"`)) || engine.conversions.Load() != 1 {
				t.Fatalf("migration retry: %d %s, conversions %d", status, raw, engine.conversions.Load())
			}
			status, raw = call(http.MethodGet, page, "document", bearer, token, nil)
			var snapshot craftNativeDocumentResponse
			if status != 200 || json.Unmarshal(raw, &snapshot) != nil || snapshot.Version != "2" || snapshot.Format != "xlsx" ||
				!snapshot.LegacySource || !bytes.Equal(snapshot.Bytes, workbook) {
				t.Fatalf("reopen after restart: %d %s", status, raw)
			}
			if _, err := f.db.Exec(`DELETE FROM artifact_versions WHERE workspace_id=? AND page_id=? AND version=1`, f.wsID, page); err == nil {
				t.Fatal("retention removed the original version")
			}
			checkSource(call(http.MethodGet, page, "document/original-workbook", bearer, token, nil))
			status, _ = call(http.MethodGet, page, "document/original-workbook", bearer, "", nil)
			if status != 403 {
				t.Errorf("original download without admission: %d", status)
			}
		})
	}
}
