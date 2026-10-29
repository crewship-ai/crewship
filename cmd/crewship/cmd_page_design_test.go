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

func TestPageDesignApplyUsesAuthenticatedAPI(t *testing.T) {
	stub := pageStub(t)
	route := "/api/v1/pages/demo/design/operations"
	stub.OnPost(route, func(r *http.Request, raw []byte) (int, []byte, string) {
		var body struct {
			Operations []map[string]any `json:"operations"`
		}
		if r.Header.Get("Authorization") != "Bearer page-acceptance-token" || json.Unmarshal(raw, &body) != nil || len(body.Operations) != 1 || body.Operations[0]["op"] != "upsert" {
			t.Errorf("lost design operation or authority: %s", raw)
		}
		return 200, []byte(`{"version":2}`), "application/json"
	})
	cmd := newPageDesignCommand()
	cmd.PersistentPreRun = rootCmd.PersistentPreRun
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader(`{"operations":[{"op":"upsert","node":{"id":"cart","type":"frame","name":"Cart","x":20,"y":20,"width":390,"height":844,"fill":"#ffffff"}}]}`))
	cmd.SetArgs([]string{"apply", "demo", "--file", "-"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(stub.CallsFor("POST", route)) != 1 {
		t.Fatal("operation not sent once")
	}
}

func TestPageDesignRejectsAmbiguousJSONBeforeNetwork(t *testing.T) {
	for _, raw := range []string{`{"operations":[],"operations":[]}`, `{"operations":[{"op":"delete","id":"a","id":"b"}]}`, `{"operations":[],"human_user_id":"victim"}`} {
		t.Run(raw, func(t *testing.T) {
			stub := pageStub(t)
			cmd := newPageDesignCommand()
			cmd.PersistentPreRun = rootCmd.PersistentPreRun
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetIn(strings.NewReader(raw))
			cmd.SetArgs([]string{"apply", "demo", "--file", "-"})
			if err := cmd.Execute(); err == nil {
				t.Fatal("invalid JSON accepted")
			}
			if len(stub.CallsFor("POST", "/api/v1/pages/demo/design/operations")) != 0 {
				t.Fatal("invalid operation reached server")
			}
		})
	}
}

func TestPageDesignProtectedRunUsesOriginalMCP(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.test")
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetOut(&bytes.Buffer{})
	transport := artifactAgentRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != selectedAgentOrigin+"/mcp/routines" || r.Header.Get("Authorization") != "Bearer agtv2.test" {
			t.Fatal("changed original run")
		}
		var packet struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&packet) != nil || packet.Params.Name != "design_apply" || packet.Params.Arguments["slug"] != "demo" {
			t.Error("changed design tool")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"version\":2}"}]}}`))}, nil
	})
	if err := artifactCLIRequestWithTransport(cmd, "POST", "/api/v1/pages/demo/design/operations", map[string]any{"operations": []any{}}, transport); err != nil {
		t.Fatal(err)
	}
}

func TestPageDesignAgentCannotCreateOrWatchUsingHumanProfile(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.test")
	for _, args := range [][]string{{"create", "demo", "--name", "Demo"}, {"watch", "demo", "--count", "1"}} {
		cmd := newPageDesignCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "human authentication") {
			t.Fatalf("agent fell back to human command %v: %v", args, err)
		}
	}
}
