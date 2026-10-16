package pagebuild

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactPreparationStrictHostAndOrigin(t *testing.T) {
	const runtime = "https://runtime.example.net"
	const studio = "https://studio.example.com"
	for _, tc := range []struct {
		name, origin, parent, host, path, method string
		want                                     int
	}{
		{"allowed", runtime, studio, "runtime.example.net", ArtifactRuntimePreparationPath, "GET", 200},
		{"wrong host", runtime, studio, "studio.example.com", ArtifactRuntimePreparationPath, "GET", 404},
		{"sibling site", "https://pages.example.com", studio, "pages.example.com", ArtifactRuntimePreparationPath, "GET", 503},
		{"same origin", studio, studio, "studio.example.com", ArtifactRuntimePreparationPath, "GET", 503},
		{"loopbackHTTP", "http://127.0.0.2", "http://127.0.0.1", "127.0.0.2", ArtifactRuntimePreparationPath, "GET", 503},
		{"query", runtime, studio, "runtime.example.net", ArtifactRuntimePreparationPath + "?identity=claimed", "GET", 404},
		{"POST", runtime, studio, "runtime.example.net", ArtifactRuntimePreparationPath, "POST", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "https://"+tc.host+tc.path, nil)
			w := httptest.NewRecorder()
			ServeArtifactRuntimePreparation(w, r, tc.origin, tc.parent)
			if w.Code != tc.want {
				t.Fatalf("status%d body%s", w.Code, w.Body.String())
			}
			if tc.want != 200 && strings.Contains(w.Body.String(), "artifact.connect") {
				t.Fatal("bootstrap leaked on denied route")
			}
		})
	}
}
func TestArtifactPreparationCanonicalHeadersAndSource(t *testing.T) {
	r := httptest.NewRequest("GET", "https://runtime.example.net"+ArtifactRuntimePreparationPath, nil)
	w := httptest.NewRecorder()
	w.Header().Set("X-Frame-Options", "DENY")
	ServeArtifactRuntimePreparation(w, r, "https://runtime.example.net", "https://STUDIO.example.com:443/")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, part := range []string{"sandbox allow-scripts", "connect-src 'none'", "frame-src 'none'", "worker-src 'none'", "object-src 'none'", "media-src 'none'", "base-uri 'none'", "form-action 'none'", "frame-ancestors https://studio.example.com"} {
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), part) {
			t.Fatal(part)
		}
	}
	for key, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff", "Origin-Agent-Cluster": "?1", "Cross-Origin-Resource-Policy": "cross-origin", "X-Frame-Options": ""} {
		if w.Header().Get(key) != want {
			t.Errorf("%s=%q", key, w.Header().Get(key))
		}
	}
	body := w.Body.String()
	if !strings.Contains(body, artifactBootstrap) || !strings.Contains(body, `window.__CREWSHIP_ARTIFACT_PARENT_ORIGIN__="https://studio.example.com"`) {
		t.Fatal("not canonical embedded program")
	}
	for _, part := range []string{"event.source !== parent", "event.origin !== parentOrigin", "event.ports.length !== 1", "loaded ||", "document.open(); document.write(data.html); document.close()"} {
		if !strings.Contains(body, part) {
			t.Fatal(part)
		}
	}
}

func TestArtifactPreparationDoesNotReflectCredentialsOrIdentity(t *testing.T) {
	r := httptest.NewRequest("GET", "https://runtime.example.net"+ArtifactRuntimePreparationPath, nil)
	first := httptest.NewRecorder()
	ServeArtifactRuntimePreparation(first, r, "https://runtime.example.net", "https://studio.example.com")
	r.Header.Set("Authorization", "Bearer PRIVATE_RUN_TOKEN")
	r.Header.Set("Cookie", "session=PRIVATE_SESSION")
	r.Header.Set("X-Artifact-ID", "PRIVATE_ARTIFACT")
	second := httptest.NewRecorder()
	ServeArtifactRuntimePreparation(second, r, "https://runtime.example.net", "https://studio.example.com")
	if second.Body.String() != first.Body.String() || second.Header().Get("Set-Cookie") != "" {
		t.Fatal("constant bootstrap incorporated request identity")
	}
}
