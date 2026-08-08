package main

// #1845 — acceptance tests for `crewship crew image-status` and
// `crewship crew refresh-image`.
//
// These drive the BUILT BINARY against a stub server rather than calling RunE,
// because the contract project rule #3 is about is the one an agent gets:
// argument parsing, crew slug resolution, the exact path called, and what
// lands on stdout in each --format. A RunE test proves none of those.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	imgCLICrewID   = "c0000000000000000crew1"
	imgCLIRunning  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	imgCLIResolved = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// imgCLIServer stubs the two crew endpoints the commands touch plus the crew
// list they resolve a slug through, and records every path it was asked for.
func imgCLIServer(t *testing.T, statusBody, refreshBody map[string]any) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q, want \"Bearer test-token\"", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/crews":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": imgCLICrewID, "slug": "alpha"}})
		case strings.HasSuffix(r.URL.Path, "/image-status"):
			_ = json.NewEncoder(w).Encode(statusBody)
		case strings.HasSuffix(r.URL.Path, "/refresh-image"):
			_ = json.NewEncoder(w).Encode(refreshBody)
		default:
			http.Error(w, `{"error":"unexpected path"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func imgCLIConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(path, []byte("token: test-token\nworkspace: c000000000000000000acc\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestAcceptance_CrewImageStatus_Behind: an agent (or an operator) asks
// whether a crew is behind and gets a machine-readable answer with both
// digests. The `behind` flag alone is not enough — an agent deciding whether
// to act needs to see what moved.
func TestAcceptance_CrewImageStatus_Behind(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv, seen := imgCLIServer(t, map[string]any{
		"crew_id":         imgCLICrewID,
		"image":           "ghcr.io/acme/runtime:latest",
		"container_id":    "ctr_abcdef012345",
		"running":         true,
		"running_digest":  imgCLIRunning,
		"resolved_digest": imgCLIResolved,
		"behind":          true,
		"reason":          "",
	}, nil)

	cmd := exec.Command(bin, "crew", "image-status", "alpha", "--server", srv.URL, "--format", "json")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+imgCLIConfig(t))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}

	var got struct {
		Behind         bool   `json:"behind"`
		RunningDigest  string `json:"running_digest"`
		ResolvedDigest string `json:"resolved_digest"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("stdout is not the JSON body: %v\n%s", err, out)
	}
	if !got.Behind {
		t.Error("behind = false, want true")
	}
	if got.RunningDigest != imgCLIRunning || got.ResolvedDigest != imgCLIResolved {
		t.Errorf("digests = (%q, %q), want (%q, %q)", got.RunningDigest, got.ResolvedDigest, imgCLIRunning, imgCLIResolved)
	}
	if !containsPath(*seen, "GET /api/v1/crews/"+imgCLICrewID+"/image-status") {
		t.Errorf("server never saw the image-status call; saw %v", *seen)
	}
}

// TestAcceptance_CrewImageStatus_HumanSaysWhyNotJustNo. When a crew is not
// behind because nothing could be checked, the human output must say so —
// rendering "up to date" for an unreachable registry is the failure the
// provider's Reason field exists to prevent, and it has to survive to the
// surface a person reads.
func TestAcceptance_CrewImageStatus_HumanSaysWhyNotJustNo(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv, _ := imgCLIServer(t, map[string]any{
		"crew_id": imgCLICrewID,
		"image":   "ghcr.io/acme/runtime:latest",
		"behind":  false,
		"reason":  "registry unreachable",
	}, nil)

	cmd := exec.Command(bin, "crew", "image-status", "alpha", "--server", srv.URL, "--no-color")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+imgCLIConfig(t))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), "registry unreachable") {
		t.Errorf("human output hides the reason a check could not be made:\n%s", out)
	}
}

// TestAcceptance_CrewRefreshImage reports what actually changed. "Refreshed."
// with no numbers leaves the operator exactly where the notification found
// them.
func TestAcceptance_CrewRefreshImage(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv, seen := imgCLIServer(t, nil, map[string]any{
		"crew_id":           imgCLICrewID,
		"image":             "ghcr.io/acme/runtime:latest",
		"previous_digest":   imgCLIRunning,
		"new_digest":        imgCLIResolved,
		"container_removed": true,
	})

	cmd := exec.Command(bin, "crew", "refresh-image", "alpha", "--server", srv.URL, "--no-color")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+imgCLIConfig(t))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	if !containsPath(*seen, "POST /api/v1/crews/"+imgCLICrewID+"/refresh-image") {
		t.Errorf("server never saw the refresh call; saw %v", *seen)
	}
	got := string(out)
	for _, want := range []string{imgCLIResolved[:19], "alpha"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

// TestAcceptance_CrewRefreshImage_UnknownCrewFailsLoudly — a typo'd slug must
// exit non-zero rather than quietly refreshing nothing.
func TestAcceptance_CrewRefreshImage_UnknownCrewFailsLoudly(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv, _ := imgCLIServer(t, nil, nil)

	cmd := exec.Command(bin, "crew", "refresh-image", "nope", "--server", srv.URL, "--no-color")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+imgCLIConfig(t))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("exit code 0 for an unknown crew; output: %s", out)
	}
}

func containsPath(seen []string, want string) bool {
	for _, s := range seen {
		if s == want {
			return true
		}
	}
	return false
}
