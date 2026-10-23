package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWorkbookAgentUsesFixedOriginAndRetainsPacket(t *testing.T) {
	guardCLIState(t)
	flagFormat = "json"
	const token = "agtv2.synthetic-workbook-capability"
	t.Setenv("CREWSHIP_AGENT_TOKEN", token)
	t.Setenv("CREWSHIP_SERVER", "https://untrusted.invalid")
	t.Setenv("CREWSHIP_PROFILE", "human")
	raw := `{"action":"apply","artifact_id":"page","mandate_id":"m","request":{"mutation_id":"original","mandate_id":"m","base_rev":1,"generation":1,"read_set":{"cells":[{"sheet":"Sheet1","cell":"A1","rev":1}]},"command":{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"42"}}}`
	var calls []string
	transport := docCreateTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "http://127.0.0.1:9119/api/v1/gridcraft" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("caller changed protected origin or authority")
		}
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, string(body))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"rev":2,"durability":"node"}`)), Header: make(http.Header)}, nil
	})
	for i := 0; i < 2; i++ {
		cmd := newWorkbookCommandWithTransport(transport)
		cmd.SetArgs([]string{"agent", "--file", "-"})
		cmd.SetIn(strings.NewReader(raw))
		cmd.SetOut(&bytes.Buffer{})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 2 || calls[0] != raw || calls[1] != raw {
		t.Fatal("retry packet changed")
	}
	t.Setenv("CREWSHIP_AGENT_TOKEN", "")
	cmd := newWorkbookCommandWithTransport(transport)
	cmd.SetArgs([]string{"agent", "--file", "-"})
	cmd.SetIn(strings.NewReader(raw))
	if err := cmd.Execute(); err == nil {
		t.Fatal("human profile fallback accepted")
	}
}
