package main

import (
	"net/http"
	"os"
	"os/exec"
	"strings"
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
