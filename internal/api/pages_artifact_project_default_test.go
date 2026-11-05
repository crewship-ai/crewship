package api

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// The Artifacts address of a project response (#4995): /artifacts/<page_id>
// with the workspace, and for a share the exact shared version.
func artifactResponseURL(t *testing.T, raw []byte) (path string, query url.Values) {
	t.Helper()
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	u, err := url.Parse(out.URL)
	if err != nil {
		t.Fatalf("url %q: %v", out.URL, err)
	}
	return u.Path, u.Query()
}

func TestArtifactProjectResponsesLinkToArtifacts(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Version one"), "{")
	created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Three screens",`+body)
	if created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	published := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Version two"))
	if published.Code != 200 {
		t.Fatalf("v2 %d %s", published.Code, published.Body)
	}
	shared := call(owner, "POST", "three-screens/artifact/share", `{"base_version":2,"version":1}`)
	if shared.Code != 200 {
		t.Fatalf("share %d %s", shared.Code, shared.Body)
	}
	restored := call(owner, "POST", "three-screens/artifact/restore", `{"base_version":2,"version":1}`)
	if restored.Code != 200 {
		t.Fatalf("restore %d %s", restored.Code, restored.Body)
	}
	for _, tc := range []struct {
		name    string
		raw     []byte
		version string
	}{
		{"create", created.Body.Bytes(), ""},
		{"publish", published.Body.Bytes(), ""},
		// A reader of a share link must land on the shared version, not Latest.
		{"share", shared.Body.Bytes(), "1"},
		{"restore", restored.Body.Bytes(), ""},
	} {
		path, query := artifactResponseURL(t, tc.raw)
		if path != "/artifacts/"+page {
			t.Errorf("%s path %q, want /artifacts/%s", tc.name, path, page)
		}
		if query.Get("workspace_id") != ws {
			t.Errorf("%s workspace_id %q, want %q", tc.name, query.Get("workspace_id"), ws)
		}
		if query.Get("version") != tc.version {
			t.Errorf("%s version %q, want %q", tc.name, query.Get("version"), tc.version)
		}
		if query.Has("artifact") {
			t.Errorf("%s still carries the legacy Pages viewer switch: %v", tc.name, query)
		}
	}
}

// Resolution of artifact_projects for one workspace, without the operator
// environment switch: on by default (the 20261105094324 migration), off when
// the workspace or the instance says so explicitly.
func TestArtifactProjectAvailabilityResolution(t *testing.T) {
	const adminOff = `UPDATE feature_flags SET enabled=0, updated_at='2026-11-05T10:00:00Z' WHERE key='artifact_projects'`
	override := func(on int) string {
		return `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('o','ff_artifact_projects',?,` + strconv.Itoa(on) + `)`
	}
	for _, tc := range []struct {
		name               string
		instance, override string
		want               bool
	}{
		{name: "no override inherits the default", want: true},
		{name: "workspace explicitly off", override: override(0), want: false},
		{name: "instance explicitly off by an admin", instance: adminOff, want: false},
		{name: "instance off, workspace explicitly on", instance: adminOff, override: override(1), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ws, owner, call := artifactProjectFixture(t)
			t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "")
			if tc.instance != "" {
				mustExec(t, h.db, tc.instance)
			}
			if tc.override != "" {
				mustExec(t, h.db, tc.override, ws)
			}
			body := strings.TrimPrefix(artifactProjectBody(0, "Default"), "{")
			w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Project",`+body)
			if got := w.Code == 201; got != tc.want {
				t.Fatalf("create %d %s, want available=%v", w.Code, w.Body, tc.want)
			}
		})
	}
}
