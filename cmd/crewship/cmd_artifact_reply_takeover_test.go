package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Exercise the actual HTTP boundary: the save commits and sends 201 headers,
// but the connection closes before its advertised body is complete.
func TestPageArtifactPublishReconcilesTruncatedSuccess(t *testing.T) {
	for _, scenario := range []string{"matching", "different", "absent", "read-fails", "definitive-refusal", "rejected-refusal", "rate-refusal", "clean-partial", "conflict-unreadable"} {
		t.Run(scenario, func(t *testing.T) {
			guardCLIState(t)
			saveCLIState(t)
			var mu sync.Mutex
			posts, gets := 0, 0
			var committed map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch r.Method {
				case http.MethodPost:
					posts++
					if scenario == "rejected-refusal" {
						w.WriteHeader(422)
						_, _ = io.WriteString(w, `{"rejected":true,"message":"invalid manifest"}`)
						return
					}
					if scenario == "rate-refusal" {
						w.WriteHeader(429)
						_, _ = io.WriteString(w, `{"reason":"too many requests","scope":"workspace","retry_after_secs":30}`)
						return
					}
					if scenario == "definitive-refusal" {
						w.WriteHeader(422)
						_, _ = io.WriteString(w, `{"error":"invalid manifest"}`)
						return
					}
					if scenario == "conflict-unreadable" {
						w.WriteHeader(http.StatusConflict)
						_, _ = io.WriteString(w, `{"error":"version conflict"}`)
						return
					}
					if err := json.NewDecoder(r.Body).Decode(&committed); err != nil {
						t.Error(err)
					}
					if scenario != "clean-partial" {
						// clean-partial: no length, so the chunked body ends
						// cleanly on an incomplete document.
						w.Header().Set("Content-Length", "1000")
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"manifest":`)
				case http.MethodGet:
					gets++
					if r.URL.Query().Get("version") != "8" {
						t.Errorf("wrong successor: %s", r.URL)
					}
					if scenario == "absent" {
						w.WriteHeader(404)
						_, _ = io.WriteString(w, `{"error":"absent"}`)
						return
					}
					if scenario == "read-fails" || scenario == "conflict-unreadable" {
						w.WriteHeader(503)
						_, _ = io.WriteString(w, `{"error":"unavailable"}`)
						return
					}
					committed["version"] = 8
					if scenario == "different" {
						committed["entrypoint"] = "other.html"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"page_id": "cpagedemo", "slug": "demo", "manifest": committed})
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
			}))
			defer server.Close()
			cfgPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("server: %s\ntoken: page-acceptance-token\nworkspace: %s\n", server.URL, covWSCli3)), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CREWSHIP_CONFIG", cfgPath)
			out, err := runParityArtifact(t, newPageArtifactCommand, "publish", "demo", "--base-version", "7", "--file", pageBundleFile(t, parityManifest))
			// Each outcome must be distinguishable: exit code plus the
			// detail a caller needs to decide whether to publish again.
			want := map[string]struct {
				code int
				text string
			}{
				"different":  {cli.ExitConflict, "exists with different content"},
				"absent":     {cli.ExitNotFound, "publishing again with --base-version 7 is safe"},
				"read-fails": {cli.ExitGeneric, "may or may not have been applied"},
				// A 409 whose successor cannot be read is unknown, not exit 5.
				"conflict-unreadable": {cli.ExitGeneric, "may or may not have been applied"},
				"definitive-refusal":  {cli.ExitValidation, "invalid manifest"},
				"rejected-refusal":    {cli.ExitValidation, "invalid manifest"},
				"rate-refusal":        {cli.ExitRateLimited, "30"},
			}[scenario]
			switch {
			case scenario == "matching" || scenario == "clean-partial":
				if err != nil || !strings.Contains(out, `"reconciled": true`) {
					t.Errorf("committed save not confirmed: output=%s err=%v", out, err)
				}
			case err == nil:
				t.Error("unproven save reported as successful")
			default:
				if got := cli.ExitCodeFor(err); got != want.code {
					t.Errorf("exit code %d, want %d (err=%v)", got, want.code, err)
				}
				if !strings.Contains(err.Error(), want.text) {
					t.Errorf("error %q lacks %q", err, want.text)
				}
			}
			wantGets := 1
			if strings.HasSuffix(scenario, "refusal") {
				wantGets = 0
			}
			mu.Lock()
			postCount, getCount := posts, gets
			mu.Unlock()
			if postCount != 1 || getCount != wantGets {
				t.Errorf("requests: POST=%d GET=%d, want 1/%d", postCount, getCount, wantGets)
			}
		})
	}
}

func TestArtifactSameFilesRejectsDuplicatePaths(t *testing.T) {
	var local map[string]any
	if err := json.Unmarshal([]byte(parityManifest), &local); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Manifest artifactManifestView `json:"manifest" yaml:"manifest"`
	}
	if err := json.Unmarshal(paritySnapshot(8, parityServerFiles), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !artifactSameFiles(snapshot.Manifest, 7, local) {
		t.Fatal("valid reordered manifest differs")
	}
	files := local["files"].([]any)
	files[1] = files[0]
	if artifactSameFiles(snapshot.Manifest, 7, local) {
		t.Fatal("duplicate local path confirmed foreign content")
	}
	if err := json.Unmarshal([]byte(parityManifest), &local); err != nil {
		t.Fatal(err)
	}
	snapshot.Manifest.Files[1] = snapshot.Manifest.Files[0]
	if artifactSameFiles(snapshot.Manifest, 7, local) {
		t.Fatal("duplicate saved paths confirmed")
	}
}
