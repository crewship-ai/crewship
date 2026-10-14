package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pagebuild"
)

func TestArtifactRuntimeProductGateCannotBeEnabledByConfiguration(t *testing.T) {
	if artifactInteractiveRuntimeEnabled {
		t.Fatal("interactive activation requires a separate reviewed decision")
	}
	for _, tc := range []struct {
		runtime, studio, host string
		development           bool
	}{
		{"https://runtime.example.net", "https://studio.example.com", "runtime.example.net", false},
		{"https://studio.example.com", "https://studio.example.com", "studio.example.com", true},
		{"http://127.0.0.2", "http://127.0.0.1", "127.0.0.2", true},
		{"", "", "unknown.example.net", false},
	} {
		h := &PageHandler{pageRuntimeOrigin: tc.runtime, pageStudioOrigin: tc.studio, pageRuntimeDevelopmentSameOrigin: tc.development}
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "https://"+tc.host+pagebuild.ArtifactRuntimePreparationPath, nil)
		h.ArtifactRuntimePreparation(w, r)
		if w.Code != 404 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "artifact.connect") || strings.Contains(w.Header().Get("Content-Type"), "html") {
			t.Fatalf("gate exposed program: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestArtifactExecutionDescriptorDoesNotGrantExecutionOrChangeLease(t *testing.T) {
	h, _, clock, ws, user, page := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<script>untrusted()</script>", "static", 0), 201)
	h.pageRuntimeOrigin = "https://runtime.example.net"
	h.pageStudioOrigin = "https://studio.example.com"
	lease := artifactLeaseCall(t, h, ws, user, "?revision=1")
	artifactExpect(t, lease, 200)
	var metadata artifactLeaseMetadata
	if err := json.Unmarshal(lease.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Execution.Enabled || metadata.Execution.Reason != "feature_disabled" || metadata.PageID != page || metadata.Revision != 1 || metadata.RuntimeProfile != artifactStaticProfile || metadata.ContentSHA256 != attachmentDigest([]byte("<script>untrusted()</script>")) {
		t.Fatalf("wrong metadata %+v", metadata)
	}
	expiry, err := time.Parse(time.RFC3339Nano, metadata.ExpiresAt)
	if err != nil || !expiry.Equal(clock.Now().Add(15*time.Second)) {
		t.Fatal("metadata expiry changed")
	}
	r := pagesGrantRequest(t, "GET", "/api/v1/pages/report/artifact", ws, user, "OWNER", "")
	r.SetPathValue("slug", "report")
	w := httptest.NewRecorder()
	h.GetArtifact(w, r)
	artifactExpect(t, w, 200)
	var result struct {
		Execution artifactExecutionDescriptor `json:"execution"`
		HTML      string                      `json:"html"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Execution != metadata.Execution || result.HTML != "<script>untrusted()</script>" {
		t.Fatal("static source compatibility changed")
	}
	for _, body := range []string{`{"html":"x","base_rev":1,"mutation_id":"interactive","runtime_profile":"interactive-html-v1"}`, `{"html":"x","base_rev":1,"mutation_id":"interactive-null","runtime_profile":null}`} {
		r := pagesGrantRequest(t, "POST", "/api/v1/pages/report/artifact/revisions", ws, user, "OWNER", body)
		r.SetPathValue("slug", "report")
		w := httptest.NewRecorder()
		h.PublishArtifact(w, r)
		artifactExpect(t, w, 422)
	}
}
