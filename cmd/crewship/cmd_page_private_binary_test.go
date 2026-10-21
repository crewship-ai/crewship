package main

import (
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

func TestPrivatePageBinaryUsesUnsupportedSafeRoute(t *testing.T) {
	binary := buildCrewshipBinary(t)
	for _, response := range []string{
		`{"id":"page-id","slug":"human-document","panels":[]}`,
		`{"id":"page-id","slug":"human-document","panels":[],"private_audience":false}`,
	} {
		t.Run(response, func(t *testing.T) {
			stub := pageStub(t)
			stub.OnPost("/api/v1/pages", func(_ *http.Request, _ []byte) (int, []byte, string) {
				return 201, []byte(response), "application/json"
			})
			stub.OnPost("/api/v1/private-pages", func(_ *http.Request, _ []byte) (int, []byte, string) {
				return 404, []byte(`{"error":"not found"}`), "application/json"
			})
			command := exec.CommandContext(t.Context(), binary, "page", "create", "--private", "--allow-empty", "--file", "-")
			command.Stdin = strings.NewReader(emptyDocPage)
			command.Env = os.Environ()
			out, err := command.CombinedOutput()
			if err == nil || len(stub.CallsFor("POST", "/api/v1/pages")) != 0 {
				t.Fatalf("private payload reached legacy creation: %v %s", err, out)
			}
			if len(stub.CallsFor("POST", "/api/v1/private-pages")) != 1 {
				t.Fatal("private creation must make exactly one request without retry or fallback")
			}
		})
	}
}

func TestPrivatePageBinaryNeverRedirectsPayloadToLegacyCreation(t *testing.T) {
	binary := buildCrewshipBinary(t)
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var privateRequests, legacyRequests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/private-pages":
					privateRequests.Add(1)
					http.Redirect(w, r, "/api/v1/pages", status)
				case "/api/v1/pages":
					legacyRequests.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"created-public","slug":"human-document","name":"Legacy result","panels":[]}`))
				default:
					t.Errorf("unexpected CLI route %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			config := filepath.Join(t.TempDir(), "cli.yaml")
			if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\ntoken: redirect-fixture-token\nworkspace: %s\nformat: json\n", server.URL, covWSCli3)), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), binary, "page", "create", "--private", "--allow-empty", "--file", "-")
			command.Stdin = strings.NewReader(strings.ReplaceAll(emptyDocPage, "Human document", "Secret redirect title sentinel"))
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "CREWSHIP_") {
					command.Env = append(command.Env, entry)
				}
			}
			command.Env = append(command.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
			out, err := command.CombinedOutput()
			if err == nil || privateRequests.Load() != 1 || legacyRequests.Load() != 0 {
				t.Fatalf("private redirect reached legacy creation: private=%d legacy=%d err=%v output=%s", privateRequests.Load(), legacyRequests.Load(), err, out)
			}
		})
	}
}
