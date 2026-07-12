package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// captureStderrSkew redirects os.Stderr while fn runs. Not parallel-safe.
func captureStderrSkew(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}

func skewServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if version != "" {
			w.Header().Set("X-Crewship-Server-Version", version)
		}
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func resetSkewState(t *testing.T, clientVersion string) {
	t.Helper()
	origVer := clientVersion
	_ = origVer
	prevVer, prevWarned := skewClientVersion, skewWarned
	SetClientVersion(clientVersion)
	skewWarned = false
	t.Cleanup(func() { skewClientVersion, skewWarned = prevVer, prevWarned })
	t.Setenv("CREWSHIP_SKIP_UPDATE_CHECK", "")
}

// TestVersionSkewWarnsOncePerProcess: a release CLI talking to a different
// release server prints ONE stderr hint per process — at the moment the skew
// can actually cause a confusing API error, with the fix (self-update) named.
func TestVersionSkewWarnsOncePerProcess(t *testing.T) {
	resetSkewState(t, "0.4.0")
	srv := skewServer(t, "0.5.0")

	c := NewClient(srv.URL, "", "")
	out := captureStderrSkew(t, func() {
		for i := 0; i < 3; i++ {
			resp, err := c.Get("/api/v1/agents")
			if err != nil {
				t.Errorf("get: %v", err)
				return
			}
			resp.Body.Close()
		}
	})
	if !strings.Contains(out, "0.5.0") || !strings.Contains(out, "0.4.0") || !strings.Contains(out, "self-update") {
		t.Errorf("skew warning missing versions or remedy; got: %q", out)
	}
	if n := strings.Count(out, "self-update"); n != 1 {
		t.Errorf("warning printed %d times, want exactly 1 per process", n)
	}
}

func TestVersionSkewSilentCases(t *testing.T) {
	cases := []struct {
		name          string
		clientVersion string
		serverVersion string
		env           string
	}{
		{"versions match", "0.5.0", "0.5.0", ""},
		{"dev client", "dev", "0.5.0", ""},
		{"empty client version", "", "0.5.0", ""},
		{"dev server", "0.5.0", "dev", ""},
		{"no header from server", "0.5.0", "", ""},
		{"suppressed by env", "0.4.0", "0.5.0", "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetSkewState(t, tc.clientVersion)
			t.Setenv("CREWSHIP_SKIP_UPDATE_CHECK", tc.env)
			srv := skewServer(t, tc.serverVersion)

			c := NewClient(srv.URL, "", "")
			out := captureStderrSkew(t, func() {
				resp, err := c.Get("/api/v1/agents")
				if err != nil {
					t.Errorf("get: %v", err)
					return
				}
				resp.Body.Close()
			})
			if strings.Contains(out, "self-update") {
				t.Errorf("unexpected skew warning: %q", out)
			}
		})
	}
}
