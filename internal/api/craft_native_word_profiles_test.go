package api

import (
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

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type wordProfileTestEngine struct {
	digest      string
	validations atomic.Int32
}

func (*wordProfileTestEngine) Application() string { return "wordcraft" }
func (*wordProfileTestEngine) Runtime() string     { return "wasm" }
func (e *wordProfileTestEngine) SHA256() string    { return e.digest }
func (*wordProfileTestEngine) Root(string) string  { return "/work" }

func (e *wordProfileTestEngine) Run(_ context.Context, dir string, args []string, _, stderr io.Writer) error {
	if len(args) != 3 || args[0] != "convert" {
		return fmt.Errorf("unexpected WordCraft command %v", args)
	}
	e.validations.Add(1)
	raw, err := os.ReadFile(filepath.Join(dir, filepath.Base(args[1])))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(args[2])), raw, 0600); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stderr, "wrote %s\n", args[2])
	return err
}

func TestCraftWordProfilesKeepDocumentPinsAcrossDefaultChange(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	mustExec(t, f.db, "DELETE FROM craft_native_documents WHERE page_id='native-page'")
	const oldPage = "word-old-profile"
	const newPage = "word-new-profile"
	f.seedPage(t, oldPage, oldPage, "Old Word document", f.userID, "")
	f.seedPage(t, newPage, newPage, "New Word document", f.userID, "")
	makeProfile := func(rev, engineHash, bundleHash string) CraftNativeDocumentProfile {
		bundle := strings.Repeat(bundleHash, 64)
		return CraftNativeDocumentProfile{
			Application: "wordcraft", EngineRevision: strings.Repeat(rev, 40),
			Engine:       &wordProfileTestEngine{digest: strings.Repeat(engineHash, 64)},
			EditorBundle: bundle, EditorURL: "/_editor/b/" + bundle + "/index.html", Bundled: true,
		}
	}
	old, next := makeProfile("a", "b", "c"), makeProfile("d", "e", "f")
	openRouter := func(db *sql.DB, defaultProfile CraftNativeDocumentProfile, retained ...CraftNativeDocumentProfile) *Router {
		t.Helper()
		defaults := map[string]CraftNativeDocumentProfile{}
		if defaultProfile.Application != "" {
			defaults["wordcraft"] = defaultProfile
		}
		router, err := NewRouter(db, "word-profile-test-secret-32-chars", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{
			Enabled: true, Profiles: defaults, RetainedWordProfiles: retained, RequireWordCompatibility: true,
			newOwnerClock: func() (mooring.OwnerClock, error) {
				return &collaborationAdmissionTestClock{boot: "word-profile-test", now: time.Second}, nil
			},
		}))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(router.Shutdown)
		return router
	}
	initial := []byte("{\n  \"text\": \"Word profile one\"\n}\n")
	edited := []byte(`{"text":"Word profile one edited"}`)
	bearer := mintIngressScopedToken(t, f.db, f.userID, "dual-word-profile", "pages:*")
	router := openRouter(f.db, CraftNativeDocumentProfile{}, old)
	call := craftNativeInitializeTCP(t, router, f.wsID)
	status, raw := call(http.MethodPost, oldPage, "initialize", bearer, "", map[string]any{"application_id": "wordcraft", "bytes": initial})
	var opened craftNativeInitializeResponse
	if status != 201 || json.Unmarshal(raw, &opened) != nil || opened.Version != "1" {
		t.Fatalf("old Word initialize: %d %s", status, raw)
	}
	read := func(page, token, version string, profile CraftNativeDocumentProfile, want []byte) craftNativeDocumentResponse {
		t.Helper()
		status, raw := call(http.MethodGet, page, "document", bearer, token, nil)
		var snapshot craftNativeDocumentResponse
		if status != 200 || json.Unmarshal(raw, &snapshot) != nil || snapshot.Version != version ||
			snapshot.EditorBundle != profile.EditorBundle || snapshot.EditorURL != profile.EditorURL || !bytes.Equal(snapshot.Bytes, want) {
			t.Fatalf("read %s with expected profile: %d %s", page, status, raw)
		}
		return snapshot
	}
	oldSnapshot := read(oldPage, opened.Token, "1", old, initial)
	status, raw = call(http.MethodPost, oldPage, "document/share", bearer, opened.Token, craftNativeShareBody{
		WorkspaceID: f.wsID, PageID: oldPage, ApplicationID: "wordcraft", EnginePin: oldSnapshot.EnginePin,
		SessionID: opened.SessionID, Generation: opened.Generation, BaseVersion: "1", TargetVersion: "1",
	})
	if status != 200 {
		t.Fatalf("publish old version: %d %s", status, raw)
	}
	if err := router.craftNativeDocuments.activate(next); err != nil {
		t.Fatal(err)
	}

	status, raw = call(http.MethodPost, newPage, "initialize", bearer, "", map[string]any{"application_id": "wordcraft", "bytes": initial})
	var newDoc craftNativeInitializeResponse
	if status != 201 || json.Unmarshal(raw, &newDoc) != nil {
		t.Fatalf("new Word initialize: %d %s", status, raw)
	}
	newSnapshot := read(newPage, newDoc.Token, "1", next, initial)
	edit := craftNativeSnapshotBody{
		WorkspaceID: f.wsID, PageID: oldPage, ApplicationID: "wordcraft", EnginePin: newSnapshot.EnginePin,
		SessionID: opened.SessionID, Generation: opened.Generation, BaseVersion: "1",
		EditorRevision: "word:edit", ContentHash: fmt.Sprintf("sha256:%x", sha256.Sum256(edited)), Bytes: edited,
	}
	if status, raw = call(http.MethodPost, oldPage, "document/save", bearer, opened.Token, edit); status != 400 {
		t.Fatalf("accepted another Word pin: %d %s", status, raw)
	}
	edit.EnginePin = oldSnapshot.EnginePin
	if status, raw = call(http.MethodPost, oldPage, "document/save", bearer, opened.Token, edit); status != 200 {
		t.Fatalf("save old admission across default switch: %d %s", status, raw)
	}
	read(oldPage, opened.Token, "2", old, edited)
	if old.Engine.(*wordProfileTestEngine).validations.Load() != 2 || next.Engine.(*wordProfileTestEngine).validations.Load() != 1 {
		t.Fatal("save reached the wrong WordCraft engine")
	}
	preview := func(wantStatus int) {
		t.Helper()
		status, raw := call(http.MethodGet, oldPage, "preview", bearer, "", nil)
		if status != wantStatus {
			t.Fatalf("old shared preview: %d %s", status, raw)
		}
		if wantStatus == 200 {
			var snapshot craftNativePreviewResponse
			if json.Unmarshal(raw, &snapshot) != nil || snapshot.Version != "1" || snapshot.EditorBundle != old.EditorBundle || !bytes.Equal(snapshot.Bytes, initial) {
				t.Fatalf("old shared preview changed: %s", raw)
			}
		}
	}
	preview(200)

	router.Shutdown()
	router = openRouter(f.db, old, next) // rollback the default while retaining both profiles
	call = craftNativeInitializeTCP(t, router, f.wsID)
	preview(200)
	status, raw = call(http.MethodPost, newPage, "sessions", bearer, "", struct{}{})
	var reopened struct {
		Token string `json:"session_token"`
	}
	if status != 201 || json.Unmarshal(raw, &reopened) != nil || reopened.Token == "" {
		t.Fatalf("new Word admission after rollback: %d %s", status, raw)
	}
	read(newPage, reopened.Token, "1", next, initial)

	router.Shutdown()
	dump, err := backup.DumpWorkspace(t.Context(), f.db, f.wsID)
	if err != nil {
		t.Fatal(err)
	}
	template, err := os.ReadFile(testutil.MigratedTemplatePath(t))
	if err != nil {
		t.Fatal(err)
	}
	targetPath := testutil.ActualEvidenceDBPath(t, filepath.Join(t.TempDir(), "word-restore.db"))
	if err := os.WriteFile(targetPath, template, 0600); err != nil {
		t.Fatal(err)
	}
	target, err := database.Open("file:" + targetPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close() })
	if err := backup.RestoreDump(t.Context(), target.DB, dump); err != nil {
		t.Fatal(err)
	}
	bearer = mintIngressScopedToken(t, target.DB, f.userID, "dual-word-restore", "pages:*")
	router = openRouter(target.DB, next, old)
	call = craftNativeInitializeTCP(t, router, f.wsID)
	preview(200)
	status, raw = call(http.MethodPost, oldPage, "sessions", bearer, "", struct{}{})
	if status != 201 || json.Unmarshal(raw, &reopened) != nil || reopened.Token == "" {
		t.Fatalf("restored old admission: %d %s", status, raw)
	}
	read(oldPage, reopened.Token, "2", old, edited)

	// Present the same engine under an untrusted bundle. A restored tuple does
	// not establish trust in the editor or authorize re-pinning the document.
	router.Shutdown()
	mustExec(t, target.DB, "DELETE FROM craft_native_documents WHERE page_id=? AND workspace_id=?", oldPage, f.wsID)
	mustExec(t, target.DB, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle)
		VALUES(?,?,'wordcraft',?,?,?)`, oldPage, f.wsID, old.EngineRevision, old.Engine.SHA256(), strings.Repeat("3", 64))
	router = openRouter(target.DB, next, old)
	call = craftNativeInitializeTCP(t, router, f.wsID)
	preview(503)

	router.Shutdown()
	router = openRouter(f.db, next) // no old package in the trusted catalog
	bearer = mintIngressScopedToken(t, f.db, f.userID, "dual-word-revoked", "pages:*")
	call = craftNativeInitializeTCP(t, router, f.wsID)
	preview(503)
	status, raw = call(http.MethodPost, oldPage, "sessions", bearer, "", struct{}{})
	if status != 201 || json.Unmarshal(raw, &reopened) != nil || reopened.Token == "" {
		t.Fatalf("old admission before the document profile check: %d %s", status, raw)
	}
	status, raw = call(http.MethodGet, oldPage, "document", bearer, reopened.Token, nil)
	if status != 503 || !bytes.Contains(raw, []byte("word_profile_unavailable")) || bytes.Contains(raw, []byte(`"bytes"`)) {
		t.Fatalf("unavailable profile disclosed data or fell back: %d %s", status, raw)
	}
	const blockedPage = "word-with-incomplete-upgrade"
	f.seedPage(t, blockedPage, blockedPage, "Creation must wait", f.userID, "")
	status, raw = call(http.MethodPost, blockedPage, "initialize", bearer, "", map[string]any{"application_id": "wordcraft", "bytes": initial})
	if status != 503 || !bytes.Contains(raw, []byte("word_profile_unavailable")) {
		t.Fatalf("creation activated despite a missing retained package: %d %s", status, raw)
	}
	var partial bool
	if err := f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM artifact_version_heads WHERE page_id=?
		UNION ALL SELECT 1 FROM craft_native_documents WHERE page_id=?)`, blockedPage, blockedPage).Scan(&partial); err != nil || partial {
		t.Fatalf("failed initialization created native state: %v", err)
	}
	foreign := craftNativeInitializeTCP(t, router, "foreign-word-test-workspace")
	status, raw = foreign(http.MethodGet, oldPage, "preview", bearer, "", nil)
	if status == 200 || status == 503 || bytes.Contains(raw, []byte(`"bytes"`)) {
		t.Fatalf("foreign workspace learned the document profile: %d %s", status, raw)
	}
	for _, db := range []*sql.DB{f.db, target.DB} {
		var head, shared int64
		if err := db.QueryRow("SELECT latest_version,shared_version FROM artifact_version_heads WHERE page_id=?", oldPage).Scan(&head, &shared); err != nil || head != 2 || shared != 1 {
			t.Fatalf("old head or share changed: %d %d %v", head, shared, err)
		}
	}
}
