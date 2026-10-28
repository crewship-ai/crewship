package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAcceptance_ArtifactSlidesTransportBinary(t *testing.T) {
	binary := buildCrewshipBinary(t)
	pdf := []byte("%PDF-1.7\nCLI transport fixture\n%%EOF\n")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer slides-cli-token" || r.URL.Query().Get("version") != "2" || r.URL.Query().Has("out") || r.URL.Query().Has("path") {
			t.Errorf("unexpected CLI request %s %s", r.Method, r.URL)
		}
		switch r.URL.Path {
		case "/api/v1/pages/@page-id/artifact/slides":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"deck":{"slide_order":["first","second"]},"anchors":{}}`))
		case "/api/v1/pages/@page-id/artifact/export.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(pdf)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	config := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\ntoken: slides-cli-token\nworkspace: %s\nformat: json\n", server.URL, covWSCli3)), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) ([]byte, error) {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, append([]string{"page", "artifact"}, args...)...)
		for _, item := range os.Environ() {
			if !strings.HasPrefix(item, "CREWSHIP_") {
				command.Env = append(command.Env, item)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return command.CombinedOutput()
	}
	out, err := run("slides", "@page-id", "--version", "2")
	if err != nil || !bytes.Contains(out, []byte(`"second"`)) {
		t.Fatalf("actual slides CLI failed: %v %s", err, out)
	}
	path := filepath.Join(dir, "caller.pdf")
	out, err = run("export-pdf", "@page-id", "--version", "2", "--out", path)
	if err != nil {
		t.Fatalf("actual export CLI failed: %v %s", err, out)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, pdf) {
		t.Fatal("PDF not written exactly", err)
	}
	var receipt struct {
		Path        string `json:"path"`
		MediaType   string `json:"media_type"`
		CodeVersion int64  `json:"code_version"`
		ByteLength  int    `json:"byte_length"`
		SHA256      string `json:"sha256"`
	}
	sum := sha256.Sum256(pdf)
	if json.Unmarshal(out, &receipt) != nil || receipt.Path != path || receipt.CodeVersion != 2 || receipt.ByteLength != len(pdf) || receipt.SHA256 != hex.EncodeToString(sum[:]) || receipt.MediaType != "application/pdf" {
		t.Fatalf("invalid receipt (or raw binary leaked to stdout): %s", out)
	}
	before := calls.Load()
	if out, err := run("slides", "@page-id", "--version", "0"); err == nil {
		t.Fatalf("unsafe pin accepted: %s", out)
	}
	if calls.Load() != before {
		t.Fatal("invalid pin reached server")
	}
}
