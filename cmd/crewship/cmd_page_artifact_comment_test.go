package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPageArtifactCommentSendsExactAnchorOnce(t *testing.T) {
	stub := pageStub(t)
	const route = "/api/v1/pages/demo/artifact/comments"
	stub.OnPost(route, func(_ *http.Request, raw []byte) (int, []byte, string) {
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil || len(body) != 6 || body["version"] != float64(3) || body["path"] != "index.html" || body["selector"] != "#artifact-hero" || body["quote"] != "Žlutý titul 😀" || body["text"] != "Change the title" || body["send_to_agent"] != true {
			t.Errorf("lost exact comment anchor: %s %v", raw, err)
		}
		return 409, []byte(`{"error":"anchor changed; select again"}`), "application/json"
	})
	cmd := newPageArtifactCommand()
	cmd.PersistentPreRun = rootCmd.PersistentPreRun
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"comment", "demo", "--version", "3", "--path", "index.html", "--selector", "#artifact-hero", "--quote", "Žlutý titul 😀", "--text", "Change the title", "--send-to-agent"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "anchor changed") {
		t.Fatalf("conflict was not preserved: %v", err)
	}
	if len(stub.CallsFor("POST", route)) != 1 {
		t.Fatal("comment was not sent exactly once")
	}
}

func TestPageArtifactCommentInvalidInputAndAgentCannotFallBack(t *testing.T) {
	for _, tc := range []struct{ name, version, selector, quote, text, agent string }{
		{"missing-version", "0", "#hero", "Title", "Change", ""},
		{"missing-selector", "1", "", "Title", "Change", ""},
		{"empty-quote", "1", "#hero", " ", "Change", ""},
		{"empty-comment", "1", "#hero", "Title", " ", ""},
		{"invalid-quote", "1", "#hero", string([]byte{0xff}), "Change", ""},
		{"invalid-comment", "1", "#hero", "Title", string([]byte{0xff}), ""},
		{"protected-agent", "1", "#hero", "Title", "Change", "agtv2.synthetic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := pageStub(t)
			t.Setenv("CREWSHIP_AGENT_TOKEN", tc.agent)
			cmd := newPageArtifactCommand()
			cmd.PersistentPreRun = rootCmd.PersistentPreRun
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetArgs([]string{"comment", "demo", "--version", tc.version, "--path", "index.html", "--selector", tc.selector, "--quote", tc.quote, "--text", tc.text})
			if err := cmd.Execute(); err == nil {
				t.Fatal("invalid or agent-only comment was accepted")
			}
			if len(stub.Calls()) != 0 {
				t.Fatal("rejected comment reached human transport")
			}
		})
	}
}
