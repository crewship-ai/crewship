package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

const artifactPublishCrashEnv = "CREWSHIP_TEST_ARTIFACT_PUBLISH_CRASH_FIXTURE"
const artifactPublishCrashExit = 86
const artifactPublishCrashHTML = "<h1>Committed before the publisher process exited</h1>"
const artifactPublishCrashBody = `{"html":"<h1>Committed before the publisher process exited</h1>","base_rev":0,"mutation_id":"crash-before-response"}`

type artifactPublishCrashFixture struct {
	Database, Storage, Workspace, User, ResponsePath string
}

// This writer terminates the child at the successful response boundary. It
// never emits that status or any response body and skips every defer, including
// SQLite Close. The parent must recover the actual on-disk commit and receipt.
type artifactPublishCrashWriter struct {
	header http.Header
	body   *os.File
}

func (w *artifactPublishCrashWriter) Header() http.Header { return w.header }
func (w *artifactPublishCrashWriter) WriteHeader(status int) {
	if status == http.StatusCreated {
		os.Exit(artifactPublishCrashExit)
	}
}
func (w *artifactPublishCrashWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func artifactPublishCrashChild(t *testing.T, fixturePath string) {
	t.Helper()
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture artifactPublishCrashFixture
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	// The parent already migrated and seeded this private fixture. Opening the
	// same file also avoids creating migrated test copies in a process that is
	// deliberately terminated without TestMain cleanup.
	db, err := database.Open("file:" + fixture.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	response, err := os.OpenFile(fixture.ResponsePath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Close()
	h := NewPageHandler(db.DB, nil, newTestLogger())
	h.artifactStoragePath = fixture.Storage
	r := pagesGrantRequest(t, "POST", "/api/v1/pages/report/artifact/revisions", fixture.Workspace, fixture.User, "OWNER", artifactPublishCrashBody)
	r.SetPathValue("slug", "report")
	h.PublishArtifact(&artifactPublishCrashWriter{header: make(http.Header), body: response}, r)
	t.Fatal("publication returned without reaching the committed success-response crash boundary")
}

// Real process termination and SQLite/blob recovery; authentication is the
// existing synthetic trusted human request fixture, with no server, login,
// Docker or agent process involved.
func TestPageArtifactCrashAfterCommitBeforeResponseRetriesOnce(t *testing.T) {
	if fixture := os.Getenv(artifactPublishCrashEnv); fixture != "" {
		artifactPublishCrashChild(t, fixture)
		return
	}
	h, _, _, workspace, user, page := artifactFixture(t)
	path := reviewDBPath(t, h.db)
	storage := h.artifactStoragePath
	// Close the parent's fixture handle before the child opens it: the later
	// recovery must see the child's persisted commit, not an existing handle.
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	childRoot := t.TempDir()
	responsePath := filepath.Join(childRoot, "response.bytes")
	if err := os.WriteFile(responsePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(childRoot, "fixture.json")
	raw, err := json.Marshal(artifactPublishCrashFixture{Database: path, Storage: storage, Workspace: workspace, User: user, ResponsePath: responsePath})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(fixturePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestPageArtifactCrashAfterCommitBeforeResponseRetriesOnce$", "-test.count=1", "-test.timeout=45s")
	// TestMain's isolated HOME also lives here, so the parent removes all child
	// scratch left by the abrupt exit. The child never invokes MigratedDB and
	// therefore starts neither template migrations nor prefetch workers.
	command.Env = append(os.Environ(), artifactPublishCrashEnv+"="+fixturePath, "TMPDIR="+childRoot, "TMP="+childRoot, "TEMP="+childRoot)
	output, runErr := command.CombinedOutput()
	var exited *exec.ExitError
	if ctx.Err() != nil || !errors.As(runErr, &exited) || exited.ExitCode() != artifactPublishCrashExit {
		t.Fatalf("child did not terminate at committed success: err=%v context=%v output=%s", runErr, ctx.Err(), output)
	}
	response, err := os.Stat(responsePath)
	if err != nil || response.Size() != 0 {
		t.Fatalf("crashed publisher emitted response bytes: file=%v err=%v", response, err)
	}
	recovered, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	h = NewPageHandler(recovered.DB, nil, newTestLogger())
	h.artifactStoragePath = storage
	assertCommittedOnce := func() {
		t.Helper()
		var head, sources, receipts, rate int
		if err := recovered.QueryRow(`SELECT head_rev,(SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?),(SELECT COUNT(*) FROM page_artifact_mutation_receipts WHERE page_id=?),(SELECT COUNT(*) FROM artifact_workspace_writes WHERE workspace_id=?) FROM page_artifacts WHERE page_id=?`, page, page, workspace, page).Scan(&head, &sources, &receipts, &rate); err != nil {
			t.Fatal("crashed child's commit was not recoverable", err)
		}
		if head != 1 || sources != 1 || receipts != 1 || rate != 1 {
			t.Fatalf("crash/retry duplicated or lost durable publication: head=%d sources=%d receipts=%d rate=%d", head, sources, receipts, rate)
		}
	}
	assertCommittedOnce()
	before := artifactCall(t, h, workspace, user, "OWNER", "GET", "?revision=1", "")
	artifactExpect(t, before, 200)
	var source struct {
		HTML string `json:"html"`
	}
	if err = json.Unmarshal(before.Body.Bytes(), &source); err != nil || source.HTML != artifactPublishCrashHTML {
		t.Fatalf("recovered source bytes differ: html=%q err=%v", source.HTML, err)
	}
	retry := artifactCall(t, h, workspace, user, "OWNER", "POST", "/revisions", artifactPublishCrashBody)
	artifactExpect(t, retry, 200)
	var result struct {
		Revision int64 `json:"revision"`
		Head     int64 `json:"head_rev"`
	}
	if err = json.Unmarshal(retry.Body.Bytes(), &result); err != nil || result.Revision != 1 || result.Head != 1 {
		t.Fatalf("recovered exact retry returned another result: %s err=%v", retry.Body.String(), err)
	}
	assertCommittedOnce()
}
