package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts/kindguide"
)

func kindGuideRequest(t *testing.T, path, kind string, user bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if kind != "" {
		req.SetPathValue("kind", kind)
	}
	if user {
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u1"}))
	}
	rr := httptest.NewRecorder()
	if kind == "" {
		ListArtifactKindGuides(rr, req)
	} else {
		GetArtifactKindGuide(rr, req)
	}
	return rr
}

func TestArtifactKindGuidesRequireAUser(t *testing.T) {
	if rr := kindGuideRequest(t, "/api/v1/artifact-kind-guides", "", false); rr.Code != http.StatusUnauthorized {
		t.Fatalf("list without user = %d", rr.Code)
	}
	if rr := kindGuideRequest(t, "/api/v1/artifact-kind-guides/docs", "docs", false); rr.Code != http.StatusUnauthorized {
		t.Fatalf("get without user = %d", rr.Code)
	}
}

func TestArtifactKindGuidesListOmitsContent(t *testing.T) {
	rr := kindGuideRequest(t, "/api/v1/artifact-kind-guides", "", true)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body)
	}
	var body struct {
		Guides []map[string]any `json:"guides"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := kindguide.List()
	if len(body.Guides) != len(want) {
		t.Fatalf("%d guides, want %d", len(body.Guides), len(want))
	}
	for i, g := range body.Guides {
		if g["kind"] != want[i].Kind || g["version"] != want[i].Version || g["sha256"] != want[i].SHA256 {
			t.Fatalf("guide %d = %v", i, g)
		}
		if _, ok := g["content"]; ok {
			t.Fatal("list carries content")
		}
	}
}

func TestArtifactKindGuideServesOneKindAndAliases(t *testing.T) {
	for _, tc := range []struct {
		kind, want string
		status     int
	}{
		{"docs", "docs", 200},
		{"design-system", "library", 200},
		{"App", "app", 200},
		{"word", "", 404},
		{"rules", "", 404},
	} {
		rr := kindGuideRequest(t, "/api/v1/artifact-kind-guides/"+tc.kind, tc.kind, true)
		if rr.Code != tc.status {
			t.Fatalf("%s: status %d %s", tc.kind, rr.Code, rr.Body)
		}
		if tc.status != 200 {
			continue
		}
		var g kindguide.Guide
		if err := json.Unmarshal(rr.Body.Bytes(), &g); err != nil {
			t.Fatal(err)
		}
		want, _ := kindguide.Lookup(tc.want)
		if !reflect.DeepEqual(g, want) {
			t.Fatalf("%s: served guide differs from the registry", tc.kind)
		}
	}
}
