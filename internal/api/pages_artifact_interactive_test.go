package api

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func interactiveArtifactBody(base int, marker string) string {
	var body map[string]any
	_ = json.Unmarshal([]byte(dashboardBody(base, "votes")), &body)
	files := body["files"].([]any)
	files[0] = map[string]any{"path": "index.html", "media_type": "text/html", "content": `<h1 id="artifact-poll">Poll</h1><script>crewship.data.get("votes")</script>`}
	if marker != "" {
		files = append(files, map[string]any{"path": "interactive.json", "media_type": "application/json", "content": marker})
	}
	body["files"] = files
	raw, _ := json.Marshal(body)
	return string(raw)
}

// The interactive projection is additive: without every switch the snapshot
// keeps today's exact shape, so a flag-off server cannot advertise a runtime.
func TestArtifactInteractiveSnapshotProjection(t *testing.T) {
	const marker = `{"schema_version":1}`
	cases := []struct {
		name        string
		marker      string
		interactive bool
		data        bool
		runtime     string
		reader      bool
		want        bool
		wantURL     string
	}{
		{name: "flag off keeps snapshot unchanged", marker: marker, data: true, runtime: "https://pages.example.net"},
		{name: "flag on exposes runtime and collections", marker: marker, interactive: true, data: true, runtime: "https://pages.example.net", want: true, wantURL: "https://pages.example.net/api/v1/pages/runtime/bootstrap"},
		{name: "shared reader gets the same runtime", marker: marker, interactive: true, data: true, runtime: "https://pages.example.net", reader: true, want: true, wantURL: "https://pages.example.net/api/v1/pages/runtime/bootstrap"},
		{name: "plain dashboard is not interactive", interactive: true, data: true, runtime: "https://pages.example.net"},
		{name: "data flag is required", marker: marker, interactive: true, runtime: "https://pages.example.net"},
		{name: "invalid marker is ignored", marker: `{"schema_version":1,"cookies":true}`, interactive: true, data: true, runtime: "https://pages.example.net"},
		{name: "missing runtime origin reports no runtime URL", marker: marker, interactive: true, data: true, want: true},
		{name: "runtime on the Studio site is refused", marker: marker, interactive: true, data: true, runtime: "https://pages.example.com", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, ws, owner, call := artifactProjectFixture(t)
			t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", map[bool]string{true: "true"}[tc.data])
			h.pageRuntimeOrigin = tc.runtime
			h.pageStudioOrigin = "https://example.com"
			if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Poll",`+strings.TrimPrefix(interactiveArtifactBody(0, tc.marker), "{")); w.Code != 201 {
				t.Fatalf("create %d %s", w.Code, w.Body)
			}
			var enabled bool
			if err := h.db.QueryRow(`SELECT enabled FROM feature_flags WHERE key='artifact_interactive'`).Scan(&enabled); err != nil || enabled {
				t.Fatalf("flag must be seeded off: %v %v", enabled, err)
			}
			if tc.interactive {
				mustExec(t, h.db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) SELECT 'interactive-on',id,?,1 FROM feature_flags WHERE key='artifact_interactive'`, ws)
			}
			viewer := owner
			path := "three-screens/artifact"
			if tc.reader {
				if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
					t.Fatalf("share %d %s", w.Code, w.Body)
				}
				viewer = "interactive-reader"
				mustExec(t, h.db, `INSERT INTO users(id,email) VALUES(?,'interactive-reader@example.test')`, viewer)
				mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, viewer)
				mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) SELECT id,'user',?,'read',?,datetime('now') FROM pages WHERE slug='three-screens'`, viewer, owner)
			}
			w := call(viewer, "GET", path, "")
			if w.Code != 200 {
				t.Fatalf("get %d %s", w.Code, w.Body)
			}
			var snapshot map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			raw, present := snapshot["interactive"]
			if present != tc.want {
				t.Fatalf("interactive present=%v want %v: %s", present, tc.want, w.Body)
			}
			if !tc.want {
				return
			}
			var runtime artifactInteractiveRuntime
			if err := json.Unmarshal(raw, &runtime); err != nil {
				t.Fatal(err)
			}
			if runtime.RuntimeURL != tc.wantURL || !slices.Equal(runtime.Collections, []string{"votes"}) || runtime.DevelopmentSameOrigin {
				t.Fatalf("runtime %+v", runtime)
			}
			if kind := string(snapshot["artifact_kind"]); kind != "" {
				t.Fatalf("interactive must not change artifact_kind: %s", kind)
			}
		})
	}
}
