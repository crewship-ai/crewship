package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

// fakeDockerOnPath shadows docker with a script that records its arguments
// and fails every `run`, so a wired DockerBuilder never reaches a daemon.
// Not parallel-safe (t.Setenv).
func fakeDockerOnPath(t *testing.T) func() string {
	t.Helper()
	directory := t.TempDir()
	log := filepath.Join(directory, "calls")
	script := "#!/bin/sh\necho \"$@\" >>'" + log + "'\nif [ \"$1\" = run ]; then cat >/dev/null; echo 'compiler unavailable in test' >&2; exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(directory, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() string {
		b, _ := os.ReadFile(log)
		return string(b)
	}
}

// labelLookup is a resolver Lookup whose answer the test changes, standing in
// for `crewship page enable` / `disable` between requests.
type labelLookup struct {
	mu  sync.Mutex
	ids []string
	n   int
}

func (l *labelLookup) set(ids ...string) { l.mu.Lock(); l.ids = ids; l.mu.Unlock() }
func (l *labelLookup) lookup(context.Context) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.n++
	return l.ids, nil
}

func pageBuildRows(t *testing.T, h *PageHandler) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT count(*) FROM page_project_builds`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func decodeErrorCode(t *testing.T, w *httptest.ResponseRecorder) (msg, code string) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %s", w.Body.String())
	}
	return body.Error, body.Code
}

// TestPageBuildEnablement — the wired DockerBuilder refuses before any build
// row exists until a labelled image (or an explicit one) is present (#3108).
func TestPageBuildEnablement(t *testing.T) {
	labelled := "sha256:" + strings.Repeat("d", 64)
	explicit := "sha256:" + strings.Repeat("e", 64)
	for _, tc := range []struct {
		name     string
		explicit string
		labels   []string
		enabled  bool
		image    string
	}{
		{name: "no image is not enabled"},
		{name: "labelled image enables", labels: []string{labelled}, enabled: true, image: labelled},
		{name: "explicit image wins", explicit: explicit, labels: []string{labelled}, enabled: true, image: explicit},
		{name: "explicit image without label", explicit: explicit, enabled: true, image: explicit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := fakeDockerOnPath(t)
			h, _, _, ws, user := newPagesFixture(t)
			h.SetProjectStore(&pages.ProjectStore{Directory: storageDir(t)})
			labels := &labelLookup{ids: tc.labels}
			h.SetBuildWorker(&pagebuild.DockerBuilder{Resolver: &pagebuild.ImageResolver{Explicit: tc.explicit, Lookup: labels.lookup}}, &pagebuild.Store{Directory: storageDir(t)})
			pagesCreate(t, h, ws, user, "health")
			if w := projectPut(t, h, ws, user, "OWNER", "health", 0, pageprofile.Source()); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			w := buildRequest(t, h, "POST", "/", ws, user, "OWNER", `{"expected_revision":1}`)
			if !tc.enabled {
				if w.Code != 503 {
					t.Fatalf("not enabled build = %d %s", w.Code, w.Body.String())
				}
				msg, code := decodeErrorCode(t, w)
				if code != pagebuild.CodeNotEnabled || !strings.Contains(msg, pagebuild.EnableCommand) {
					t.Fatalf("refusal lacks code or remedy: %s", w.Body.String())
				}
				if n := pageBuildRows(t, h); n != 0 {
					t.Fatalf("refused build recorded %d rows", n)
				}
				if strings.Contains(calls(), "run") {
					t.Fatalf("a Pages container was started while not enabled: %s", calls())
				}
				return
			}
			if w.Code != 202 {
				t.Fatalf("enabled build = %d %s", w.Code, w.Body.String())
			}
			if !waitForBackgroundWork(5 * time.Second) {
				t.Fatal("build did not finish")
			}
			if !strings.Contains(calls(), " "+tc.image+" ") {
				t.Fatalf("build did not run %s: %s", tc.image, calls())
			}
			if tc.explicit != "" && labels.n != 0 {
				t.Fatalf("explicit image still looked up labels %d times", labels.n)
			}
		})
	}
}

// TestPageBuildWorkerWiring — project storage alone wires the worker (its
// enablement is per request); an invalid explicit image leaves it unwired.
func TestPageBuildWorkerWiring(t *testing.T) {
	noLabels := func(context.Context) ([]string, error) { return nil, nil }
	for _, tc := range []struct {
		name     string
		projects bool
		image    string
		wired    bool
	}{
		{name: "no project storage", image: "sha256:" + strings.Repeat("a", 64)},
		{name: "storage alone wires a not-enabled worker", projects: true, wired: true},
		{name: "explicit pinned image", projects: true, image: "sha256:" + strings.Repeat("a", 64), wired: true},
		{name: "explicit mutable tag is refused", projects: true, image: "crewship-pages-build:local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := []RouterOption{WithPageBuildImage(tc.image), WithPageBuildImageLookup(noLabels)}
			if tc.projects {
				opts = append(opts, WithPageProjectsPath(storageDir(t)))
			}
			router, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger(), opts...)
			if err != nil {
				t.Fatal(err)
			}
			if (router.pages.builds != nil) != tc.wired {
				t.Fatalf("wired = %v, want %v", router.pages.builds != nil, tc.wired)
			}
			if !tc.wired {
				return
			}
			err = router.pages.builds.worker.(pagebuild.Availability).Available(t.Context())
			if want := tc.image == ""; errors.Is(err, pagebuild.ErrNotEnabled) != want {
				t.Fatalf("Available = %v, not enabled want %v", err, want)
			}
		})
	}
}

// TestPageBuildEnableWithoutRestart — a refusal, then `crewship page enable`,
// then the very next request builds: the refusal is not cached.
func TestPageBuildEnableWithoutRestart(t *testing.T) {
	fakeDockerOnPath(t)
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: storageDir(t)})
	labels := &labelLookup{}
	h.SetBuildWorker(&pagebuild.DockerBuilder{Resolver: &pagebuild.ImageResolver{Lookup: labels.lookup}}, &pagebuild.Store{Directory: storageDir(t)})
	pagesCreate(t, h, ws, user, "health")
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, pageprofile.Source()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := buildRequest(t, h, "POST", "/", ws, user, "OWNER", `{"expected_revision":1}`); w.Code != 503 {
		t.Fatalf("before enable: %d", w.Code)
	}
	labels.set("sha256:" + strings.Repeat("d", 64))
	if w := buildRequest(t, h, "POST", "/", ws, user, "OWNER", `{"expected_revision":1}`); w.Code != 202 {
		t.Fatalf("after enable: %d %s", w.Code, w.Body.String())
	}
	if !waitForBackgroundWork(5 * time.Second) {
		t.Fatal("build did not finish")
	}
}

// TestPageBuildWorkerMissing — no coordinator at all keeps the sentence older
// clients matched and adds the code.
func TestPageBuildWorkerMissing(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: storageDir(t)})
	pagesCreate(t, h, ws, user, "health")
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, pageprofile.Source()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, method := range []string{"POST", "GET"} {
		w := buildRequest(t, h, method, "/", ws, user, "OWNER", `{"expected_revision":1}`)
		msg, code := decodeErrorCode(t, w)
		if w.Code != 503 || code != pagebuild.CodeNotEnabled || !strings.HasPrefix(msg, "Page build worker is not configured") {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
		}
	}
}

// TestPagePreviewWhenNotEnabled — the Studio offer probe (no draft) learns
// that builds are not enabled; an existing build stays readable.
func TestPagePreviewWhenNotEnabled(t *testing.T) {
	fakeDockerOnPath(t)
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: storageDir(t)})
	labels := &labelLookup{}
	h.SetBuildWorker(&pagebuild.DockerBuilder{Resolver: &pagebuild.ImageResolver{Lookup: labels.lookup}}, &pagebuild.Store{Directory: storageDir(t)})
	pagesCreate(t, h, ws, user, "health")

	w := buildRequest(t, h, "GET", "/", ws, user, "OWNER", "")
	if _, code := decodeErrorCode(t, w); w.Code != 503 || code != pagebuild.CodeNotEnabled {
		t.Fatalf("offer probe while not enabled: %d %s", w.Code, w.Body.String())
	}
	labels.set("sha256:" + strings.Repeat("d", 64))
	if w := buildRequest(t, h, "GET", "/", ws, user, "OWNER", ""); w.Code != 404 {
		t.Fatalf("offer probe once enabled: %d %s", w.Code, w.Body.String())
	}
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, pageprofile.Source()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := buildRequest(t, h, "POST", "/", ws, user, "OWNER", `{"expected_revision":1}`); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	if !waitForBackgroundWork(5 * time.Second) {
		t.Fatal("build did not finish")
	}
	labels.set() // `crewship page disable`
	w = buildRequest(t, h, "GET", "/", ws, user, "OWNER", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "compiler unavailable in test") {
		t.Fatalf("existing build unreadable once disabled: %d %s", w.Code, w.Body.String())
	}
}

// TestPagePublicationRefusalCodes — check, publish and the review name the
// one missing setting: storage (not enabled) or the runtime origin.
func TestPagePublicationRefusalCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		strip  func(h *PageHandler)
		code   string
		remedy string
	}{
		{name: "no artifact store", strip: func(h *PageHandler) { h.pageArtifacts = nil }, code: pagebuild.CodeNotEnabled, remedy: pagebuild.EnableCommand},
		{name: "no runtime origin", strip: func(h *PageHandler) { h.pageRuntimeOrigin = "" }, code: pagebuild.CodeRuntimeOriginMissing, remedy: "CREWSHIP_PAGE_RUNTIME_ORIGIN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, ws, user := reviewFixture(t)
			if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			tc.strip(h)
			body := `{"build_id":"missing","expected_revision":1}`
			for name, handler := range map[string]func(*httptest.ResponseRecorder, *PageHandler, string){
				"check": func(w *httptest.ResponseRecorder, h *PageHandler, body string) {
					r := pagesRequest(t, "POST", "/", ws, user, "OWNER", body)
					r.SetPathValue("slug", "health")
					h.CheckProject(w, r)
				},
				"publish": func(w *httptest.ResponseRecorder, h *PageHandler, body string) {
					r := pagesRequest(t, "POST", "/", ws, user, "OWNER", `{"build_id":"missing","expected_revision":1,"expected_publication":0,"reviewed_code":true,"expected_definition_digest":"`+strings.Repeat("a", 64)+`","expected_routine_digests":{}}`)
					r.SetPathValue("slug", "health")
					h.PublishProject(w, r)
				},
			} {
				w := httptest.NewRecorder()
				handler(w, h, body)
				msg, code := decodeErrorCode(t, w)
				if w.Code != 503 || code != tc.code || !strings.Contains(msg, tc.remedy) {
					t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
				}
			}
			w, snapshot := reviewCall(t, h, ws, user, "OWNER", "health")
			if w.Code != 200 {
				t.Fatalf("review: %d %s", w.Code, w.Body.String())
			}
			if msg := reviewBlockers(snapshot)[reviewBlockerStorageUnavailable]; !strings.Contains(msg, tc.remedy) {
				t.Fatalf("review blocker does not name %s: %+v", tc.remedy, snapshot.Blockers)
			}
		})
	}
}
