package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
)

type testPageBuilder struct {
	calls   atomic.Int32
	release chan struct{}
	fail    bool
}

func (b *testPageBuilder) Build(ctx context.Context, p *pages.SourceProject) (*pagebuild.Artifact, error) {
	b.calls.Add(1)
	if b.release != nil {
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if b.fail {
		return nil, errors.New("typecheck failed")
	}
	return &pagebuild.Artifact{Format: pagebuild.ArtifactFormat, JavaScript: "document.body.dataset.ready='yes'", CSS: "body{color:blue}", Toolchain: "test"}, nil
}
func buildRequest(t *testing.T, h *PageHandler, method, path, ws, user, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesRequest(t, method, path, ws, user, role, body)
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	if method == "POST" {
		h.BuildProject(w, r)
	} else {
		h.GetProjectPreview(w, r)
	}
	return w
}
func awaitPageBuild(t *testing.T, h *PageHandler, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var got string
		_ = h.db.QueryRow(`SELECT state FROM page_project_builds LIMIT 1`).Scan(&got)
		if got == state {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("build never reached %s", state)
}
func TestPageBuildLifecycleRBACAndRevision(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	worker := &testPageBuilder{release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-worker.release:
		default:
			close(worker.release)
		}
	})
	h.SetBuildWorker(worker, &pagebuild.Store{Directory: t.TempDir()})
	pagesCreate(t, h, ws, user, "health")
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, method := range []string{"POST", "GET"} {
		if w := buildRequest(t, h, method, "/", ws, "other", "MEMBER", `{"expected_revision":1}`); w.Code != 403 {
			t.Fatalf("unauthorized %s: %d", method, w.Code)
		}
		if w := buildRequest(t, h, method, "/", "other-workspace", user, "OWNER", `{"expected_revision":1}`); w.Code != 404 {
			t.Fatalf("cross workspace %s: %d", method, w.Code)
		}
	}
	if w := buildRequest(t, h, "POST", "/", ws, user, "OWNER", `{"expected_revision":2}`); w.Code != 409 {
		t.Fatalf("stale build %d", w.Code)
	}
	w := buildRequest(t, h, "POST", "/", ws, user, "OWNER", `{"expected_revision":1}`)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	if w := buildRequest(t, h, "POST", "/", ws, user, "OWNER", `{"expected_revision":1}`); w.Code != 429 {
		t.Fatalf("concurrent build %d", w.Code)
	}
	// Source edits during a build cannot silently replace that build's input.
	if w := projectPut(t, h, ws, user, "OWNER", "health", 1, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	close(worker.release)
	awaitPageBuild(t, h, "ready")
	w = buildRequest(t, h, "GET", "/", ws, user, "OWNER", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var got struct {
		Revision int64               `json:"revision"`
		Build    pageBuildRecord     `json:"build"`
		Artifact *pagebuild.Artifact `json:"artifact"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || got.Build.SourceRevision != 1 || got.Artifact == nil || got.Artifact.JavaScript == "" {
		t.Fatalf("preview lost revision/artifact: %s", w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("preview may be cached")
	}
	if worker.calls.Load() != 1 {
		t.Fatal("duplicate build executed")
	}
}
func TestPageBuildFailureAndRestartRecovery(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	h.SetBuildWorker(&testPageBuilder{fail: true}, &pagebuild.Store{Directory: t.TempDir()})
	pagesCreate(t, h, ws, user, "health")
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := buildRequest(t, h, "POST", "/", ws, user, "OWNER", `{"expected_revision":1}`); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	awaitPageBuild(t, h, "failed")
	w := buildRequest(t, h, "GET", "/", ws, user, "OWNER", "")
	if !strings.Contains(w.Body.String(), "typecheck failed") || strings.Contains(w.Body.String(), `"artifact":`) {
		t.Fatal(w.Body.String())
	}
	if _, err := h.db.Exec(`UPDATE page_project_builds SET state='running'`); err != nil {
		t.Fatal(err)
	}
	if err := h.recoverPageBuilds(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitPageBuild(t, h, "interrupted")
}

func TestPageRuntimeOnlyConfiguredHost(t *testing.T) {
	h := &PageHandler{pageRuntimeOrigin: "https://pages.example.net", pageStudioOrigin: "https://studio.example.com"}
	for _, host := range []string{"studio.example.com", "pages.example.net"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "https://"+host+pagebuild.RuntimePath, nil)
		h.PageRuntime(w, r)
		if host == "studio.example.com" {
			if w.Code != 404 {
				t.Fatalf("Studio host served runtime: %d", w.Code)
			}
		} else if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors https://studio.example.com") {
			t.Fatalf("runtime response %d: %v", w.Code, w.Header())
		}
	}
}

func TestPageBuildCompilerErrorSurvivesMaintenanceAndCompletedBuildWaits(t *testing.T) {
	for _, failed := range []bool{true, false} {
		t.Run(map[bool]string{true: "compiler error", false: "completed artifact"}[failed], func(t *testing.T) {
			h, _, _, ws, user := newPagesFixture(t)
			h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
			worker := &testPageBuilder{fail: failed, release: make(chan struct{})}
			h.SetBuildWorker(worker, &pagebuild.Store{Directory: t.TempDir()})
			pagesCreate(t, h, ws, user, "health")
			if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			if w := buildRequest(t, h, "POST", "/", ws, user, "OWNER", `{"expected_revision":1}`); w.Code != 202 {
				t.Fatal(w.Body.String())
			}
			release, err := h.projectStore.Lease(context.Background(), ws, true)
			if err != nil {
				close(worker.release)
				t.Fatal(err)
			}
			defer release()
			close(worker.release)
			if failed {
				awaitPageBuild(t, h, "failed")
				var message string
				if err := h.db.QueryRow(`SELECT error FROM page_project_builds`).Scan(&message); err != nil || !strings.Contains(message, "typecheck failed") {
					t.Fatalf("compiler diagnostic lost: %s %v", message, err)
				}
			} else {
				time.Sleep(100 * time.Millisecond)
				var state string
				if err := h.db.QueryRow(`SELECT state FROM page_project_builds`).Scan(&state); err != nil || state != "running" {
					t.Fatalf("completed build discarded during maintenance: %s %v", state, err)
				}
				release()
				awaitPageBuild(t, h, "ready")
			}
		})
	}
}
