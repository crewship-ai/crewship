package manifest

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stoppedCrewFilesAPI serves one existing, stopped crew "ops" and the
// current bytes of its shared files, on top of the kinds fake.
type stoppedCrewFilesAPI struct {
	*kindsFakeAPI
	files map[string]string
}

func (f *stoppedCrewFilesAPI) Get(ctx context.Context, path string) (*http.Response, error) {
	switch {
	case path == "/api/v1/crews":
		return jsonResp(200, []map[string]any{{"id": "crew_1", "slug": "ops", "name": "Ops", "runtime_image": "debian:bookworm-slim"}}), nil
	case path == "/api/v1/crews/crew_1/container-status":
		return jsonResp(200, map[string]any{"status": "stopped"}), nil
	case strings.HasPrefix(path, "/api/v1/crews/crew_1/files/download?path="):
		p, _ := url.QueryUnescape(strings.TrimPrefix(path, "/api/v1/crews/crew_1/files/download?path="))
		if body, ok := f.files[p]; ok {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
		}
		return jsonResp(404, map[string]any{"error": "File not found"}), nil
	}
	return f.kindsFakeAPI.Get(ctx, path)
}

// A standalone kind: Crew whose files are all already in place writes
// nothing, so the stopped-crew advisory ("those saves will fail with 409")
// must stay silent; a changed file still raises it, counted once.
func TestBuildPlan_KindCrewStoppedAdvisoryCountsOnlyWrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "probe.sh"), []byte("echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: Ops, slug: ops }
spec:
  runtime_image: debian:bookworm-slim
  files:
    - { src: probe.sh, dest: shared/probe.sh }
`)
	for _, tc := range []struct {
		name     string
		remote   string
		wantWarn bool
	}{
		{"file already in place", "echo hi\n", false},
		{"file changed", "echo old\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle, err := Load(body)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(bundle.Crews) != 1 {
				t.Fatalf("Crews = %d, want the standalone kind path", len(bundle.Crews))
			}
			api := &stoppedCrewFilesAPI{kindsFakeAPI: newKindsFakeAPI(), files: map[string]string{"shared/probe.sh": tc.remote}}
			plan, err := BuildPlan(context.Background(), NewClient(api), bundle, Options{BaseDir: dir})
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}
			warned := 0
			for _, w := range plan.Warnings {
				if strings.Contains(w, "is not running") {
					warned++
					if !strings.Contains(w, "writes 1 file(s)") {
						t.Errorf("advisory %q, want it to count the one file to write", w)
					}
				}
			}
			if (warned > 0) != tc.wantWarn {
				t.Fatalf("stopped-crew advisories = %d, want warn=%v; warnings=%v", warned, tc.wantWarn, plan.Warnings)
			}
		})
	}
}
