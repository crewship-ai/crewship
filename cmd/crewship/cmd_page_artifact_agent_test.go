package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestArtifactCLIOriginalRunUsesMCPWithoutProfileOrRedirect(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.test")
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetOut(&bytes.Buffer{})
	calls := 0
	transport := artifactAgentRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != selectedAgentOrigin+"/mcp/routines" || r.Header.Get("Authorization") != "Bearer agtv2.test" {
			t.Error("profile or original run changed")
		}
		var packet struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&packet) != nil || packet.Params.Name != "artifact_publish" || packet.Params.Arguments["base_version"] != float64(5) {
			t.Error("lost original CLI CAS")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"{\"error\":\"conflict; preserve draft\"}"}]}}`))}, nil
	})
	err := artifactCLIRequestWithTransport(cmd, "POST", "/api/v1/pages/demo/artifact/versions", map[string]any{"base_version": 5}, transport)
	if err == nil || !strings.Contains(err.Error(), "preserve draft") || calls != 1 {
		t.Fatal("CLI conflict retried or lost", calls, err)
	}
}

type artifactAgentRoundTripper func(*http.Request) (*http.Response, error)

func (f artifactAgentRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
