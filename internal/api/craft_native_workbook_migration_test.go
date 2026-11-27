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
	sha                    string
}

func (*migrationTestGridCraft) Application() string { return "gridcraft" }
func (*migrationTestGridCraft) Runtime() string     { return "wasm" }
func (e *migrationTestGridCraft) SHA256() string {
	if e.sha != "" {
		return e.sha
	}
	return strings.Repeat("b", 64)
}
func (*migrationTestGridCraft) Root(string) string { return "/work" }
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

func migrationTestWorkbook(t *testing.T, sheet string) []byte {
	t.Helper()
	var result bytes.Buffer
	archive := zip.NewWriter(&result)
	for name, text := range map[string]string{
		"[Content_Types].xml":      `<Types><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`,
		"xl/workbook.xml":          `<workbook/>`,
		"xl/worksheets/sheet1.xml": sheet,
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
	const page = "native-page"
	original := []byte("{\n  \"sheets\":[{\"name\":\"Sheet1\",\"value\":7,\"formula\":\"=3+4\",\"style\":{\"bold\":true},\"pivots\":[{\"name\":\"Summary\"}]}], \"names\":[\"Total\"]\n}\n")
	workbook := migrationTestWorkbook(t, "<worksheet/>")
	for _, scenario := range []struct {
		name, altered              string
		moveHead                   bool
		liveLease                  bool
		parallel, upgrade, history bool
	}{
		{name: "faithful"},
		{name: "pin upgrade", upgrade: true},
		{name: "history and shared preview", history: true},
		{name: "two simultaneous requests", parallel: true},
		{name: "concurrent edit", moveHead: true},
		{name: "live lease", liveLease: true},
		{name: "value", altered: `"value":7|"value":9`},
		{name: "formula", altered: `"formula":"=3+4"|"formula":"=7"`},
		{name: "style", altered: `"bold":true|"bold":false`},
		{name: "defined name", altered: `"names":["Total"]|"names":[]`},
		{name: "pivot", altered: `"pivots":[{"name":"Summary"}]|"pivots":[]`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, _, _ := craftNativeHTTPFixture(t)
			mustExec(t, f.db, `DELETE FROM craft_native_documents WHERE page_id=?`, page)
			mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
			mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES(?,?,'gridcraft','e68d192282499bed81a9800d742cb9b239d367e5',?,?)`, page, f.wsID, strings.Repeat("a", 64), strings.Repeat("d", 64))
			key := artifacts.VersionKey{WorkspaceID: f.wsID, PageID: page}
			store := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
			author := func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }
			if _, err := store.Apply(t.Context(), key, artifacts.VersionRequest{EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: original, MediaType: "application/json"}}}, author); err != nil {
				t.Fatal(err)
			}
			source := original
			sourceVersion := 1
			if scenario.history {
				craftNativeEnableVersionHistory(t, f.db, true)
				if err := store.Share(t.Context(), key, 1, 1, author); err != nil {
					t.Fatal(err)
				}
				source = bytes.Replace(original, []byte(`"value":7`), []byte(`"value":42`), 1)
				if _, err := store.Apply(t.Context(), key, artifacts.VersionRequest{BaseVersion: 1, EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: source, MediaType: "application/json"}}}, author); err != nil {
					t.Fatal(err)
				}
				sourceVersion = 2
			}
			expectedVersion := fmt.Sprint(sourceVersion + 1)
			reopened := source
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
			arrived, proceed := make(chan struct{}, 2), make(chan struct{})
			if scenario.parallel {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				engine.beforeExport = func() error {
					arrived <- struct{}{}
					select {
					case <-proceed:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
			bundle := strings.Repeat("c", 64)
			cfg := CraftNativeHTTPConfig{Enabled: true, newOwnerClock: func() (mooring.OwnerClock, error) {
				return &collaborationAdmissionTestClock{boot: "legacy-sheet-boot", now: time.Second}, nil
			}, Profiles: map[string]CraftNativeDocumentProfile{
				"gridcraft": {Application: "gridcraft", EngineRevision: strings.Repeat("c", 40), Engine: engine, EditorBundle: bundle, EditorURL: "/_editor/b/" + bundle + "/index.html", Workbook: true},
			}}
			bearer := mintIngressScopedToken(t, f.db, f.userID, "legacy-sheet-owner", "pages:*")
			start := func() (*Router, func(string, string, string, string, string, any) (int, json.RawMessage), string, string) {
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
					ID    string `json:"session_id"`
				}
				if status != 201 || json.Unmarshal(raw, &session) != nil || session.Token == "" {
					t.Fatalf("native admission: %d %s", status, raw)
				}
				return router, call, session.Token, session.ID
			}
			checkSource := func(status int, raw json.RawMessage) {
				t.Helper()
				if status != 200 || !bytes.Equal(raw, source) {
					t.Fatalf("original download: HTTP %d, SHA-256 %x", status, sha256.Sum256(raw))
				}
			}
			router, call, token, id := start()
			status, raw := call(http.MethodGet, page, "document", bearer, token, nil)
			if status != 409 || !bytes.Contains(raw, []byte(`"legacy_workbook"`)) {
				t.Fatalf("legacy read: %d %s", status, raw)
			}
			if scenario.liveLease {
				status, raw = call(http.MethodPost, page, "lease/acquire", bearer, token, struct{}{})
				if status != 200 {
					t.Fatalf("establish live lease: %d %s", status, raw)
				}
			}
			if scenario.parallel {
				type result struct {
					status int
					body   json.RawMessage
				}
				responses := make(chan result, 2)
				for range 2 {
					go func() {
						code, body := call(http.MethodPost, page, "document/migrate-workbook", bearer, token, nil)
						responses <- result{code, body}
					}()
				}
				deadline := time.After(15 * time.Second)
				for range 2 {
					select {
					case <-arrived:
					case <-deadline:
						t.Fatal("both requests must reach conversion before either can publish")
					}
				}
				close(proceed)
				for range 2 {
					select {
					case response := <-responses:
						if response.status != 200 || !bytes.Contains(response.body, []byte(`"version":"2"`)) {
							t.Fatalf("racing request: %d %s", response.status, response.body)
						}
					case <-deadline:
						t.Fatal("migration requests did not finish")
					}
				}
				var migrations, versions int
				if err := f.db.QueryRow(`SELECT (SELECT count(*) FROM craft_workbook_migrations WHERE page_id=?), (SELECT count(*) FROM artifact_versions WHERE page_id=?)`, page, page).Scan(&migrations, &versions); err != nil ||
					migrations != 1 || versions != 2 || engine.conversions.Load() != 2 {
					t.Fatalf("racing requests: %d migrations, %d versions, %d exports, error %v", migrations, versions, engine.conversions.Load(), err)
				}
				return
			}
			status, raw = call(http.MethodPost, page, "document/migrate-workbook", bearer, token, nil)
			if scenario.liveLease {
				state, err := store.Read(t.Context(), key, author)
				if status != 409 || !bytes.Contains(raw, []byte("legacy_workbook_busy")) || err != nil || state.LatestVersion != 1 {
					t.Fatalf("live lease: migration HTTP %d, head %d, state error %v", status, state.LatestVersion, err)
				}
				checkSource(call(http.MethodGet, page, "document/original-workbook", bearer, token, nil))
				return
			}

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

			var receipt craftNativeWorkbookMigrationResponse
			if status != 200 || json.Unmarshal(raw, &receipt) != nil || receipt.Version != expectedVersion {
				t.Fatalf("conversion: %d %s", status, raw)
			}
			wantHead, wantWorkbook := expectedVersion, workbook
			var initial craftNativeDocumentResponse
			status, raw = call(http.MethodGet, page, "document", bearer, token, nil)
			if status != 200 || json.Unmarshal(raw, &initial) != nil || initial.Version != expectedVersion {
				t.Fatalf("converted document: %d %s", status, raw)
			}
			if scenario.upgrade {
				wantHead = "3"
				wantWorkbook = migrationTestWorkbook(t, `<worksheet><sheetData><row r="2"><c r="B2"><v>17</v></c></row></sheetData></worksheet>`)
				status, raw = call(http.MethodPost, page, "lease/acquire", bearer, token, struct{}{})
				var lease craftNativeLeaseResponse
				if status != 200 || json.Unmarshal(raw, &lease) != nil || lease.Generation == nil {
					t.Fatalf("acquire converted workbook: %d %s", status, raw)
				}
				edit := craftNativeSnapshotBody{
					WorkspaceID: f.wsID, PageID: page, ApplicationID: "gridcraft", EnginePin: initial.EnginePin,
					SessionID: id, Generation: *lease.Generation, BaseVersion: expectedVersion,
					EditorRevision: "edit-before-upgrade", Bytes: wantWorkbook, ContentHash: fmt.Sprintf("sha256:%x", sha256.Sum256(wantWorkbook)),
				}
				status, raw = call(http.MethodPost, page, "document/save", bearer, token, edit)
				if status != 200 || !bytes.Contains(raw, []byte(`"version":"3"`)) {
					t.Fatalf("save before upgrade: %d %s", status, raw)
				}
				status, raw = call(http.MethodPost, page, "lease/release", bearer, token, craftNativeLeaseBody{Generation: *lease.Generation})
				if status != 200 {
					t.Fatalf("release before upgrade: %d %s", status, raw)
				}
				nextBundle := strings.Repeat("e", 64)
				cfg.Profiles["gridcraft"] = CraftNativeDocumentProfile{
					Application: "gridcraft", EngineRevision: strings.Repeat("e", 40),
					Engine:       &migrationTestGridCraft{sha: strings.Repeat("f", 64)},
					EditorBundle: nextBundle, EditorURL: "/_editor/b/" + nextBundle + "/index.html", Workbook: true,
				}
			}
			router.Shutdown()
			router, call, token, _ = start()
			status, raw = call(http.MethodPost, page, "document/migrate-workbook", bearer, token, nil)
			var retry craftNativeWorkbookMigrationResponse
			if status != 200 || json.Unmarshal(raw, &retry) != nil || retry != receipt || engine.conversions.Load() != 1 {
				t.Fatalf("migration retry: %d %s, conversions %d", status, raw, engine.conversions.Load())
			}
			status, raw = call(http.MethodGet, page, "document", bearer, token, nil)
			var snapshot craftNativeDocumentResponse
			if status != 200 || json.Unmarshal(raw, &snapshot) != nil || snapshot.Version != wantHead || snapshot.Format != "xlsx" ||
				!snapshot.LegacySource || !bytes.Equal(snapshot.Bytes, wantWorkbook) {
				t.Fatalf("reopen after restart: %d %s", status, raw)
			}
			if scenario.upgrade && (snapshot.EnginePin == initial.EnginePin || snapshot.EditorBundle != cfg.Profiles["gridcraft"].EditorBundle) {
				t.Fatal("reopened workbook did not advertise the upgraded package")
			}
			if scenario.history {
				status, raw = call(http.MethodGet, page, "document/versions", bearer, token, nil)
				var history craftNativeVersionsResponse
				if status != 200 || json.Unmarshal(raw, &history) != nil || history.CurrentVersion != "3" ||
					len(history.Versions) != 3 || history.Versions[0].Format != "" || history.Versions[1].Format != "native" || history.Versions[2].Format != "native" {
					t.Fatalf("history listing: %d %s", status, raw)
				}
				for i, saved := range [][]byte{original, source} {
					version := fmt.Sprint(i + 1)
					status, raw := call(http.MethodGet, page, "document/versions/"+version, bearer, token, nil)
					var historical craftNativeVersionDocumentResponse
					if status != 200 || json.Unmarshal(raw, &historical) != nil || historical.Version != version ||
						historical.CurrentVersion != "3" || historical.Format != "native" || !bytes.Equal(historical.Bytes, saved) {
						t.Fatalf("private version %s: %d %s", version, status, raw)
					}
				}
				peer := craftNativePreviewPeer(t, f, "read")
				peerToken, _ := craftNativeIssued(t, router.craftNativeHTTP, peer)
				preview := craftNativePreviewCall(router.craftNativeDocuments, peer)
				var shared craftNativePreviewResponse
				if preview.Code != 200 || json.Unmarshal(preview.Body.Bytes(), &shared) != nil || shared.Version != "1" ||
					shared.Format != "native" || !bytes.Equal(shared.Bytes, original) {
					t.Fatalf("shared pin: %d %s", preview.Code, preview.Body)
				}
				if w := craftNativeVersionReadCall(router.craftNativeDocuments, peer, peerToken, "2"); w.Code != 403 {
					t.Fatalf("shared reader received private history: %d %s", w.Code, w.Body)
				}
			}
			if _, err := f.db.Exec(`DELETE FROM artifact_versions WHERE workspace_id=? AND page_id=? AND version=?`, f.wsID, page, sourceVersion); err == nil {
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
