package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPageDraftKeyUsesAuthenticatedScopedEndpoint(t *testing.T) {
	stub := pageStub(t)
	command := newPageDraftKeyCommand()
	if command == nil {
		t.Fatal("page draft-key command missing")
	}
	route := "/api/v1/pages/@c1234567890123456789012345/draft-key"
	stub.OnPost(route, func(r *http.Request, raw []byte) (int, []byte, string) {
		var body map[string]string
		if r.Header.Get("Authorization") != "Bearer page-acceptance-token" || json.Unmarshal(raw, &body) != nil || body["operation"] != "wrap" || body["human"] != "alice" || body["key"] != "a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s=" {
			t.Errorf("wrong authority or payload: %s", raw)
		}
		return 200, []byte(`{"envelope":"v1:synthetic-envelope"}`), "application/json"
	})
	command.PersistentPreRun = rootCmd.PersistentPreRun
	command.SetIn(strings.NewReader(`{"human":"alice","operation":"wrap","key":"a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s="}`))
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"@c1234567890123456789012345", "--file", "-"})
	stdout, err := captureStdout(t, command.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.CallsFor("POST", route)) != 1 || !strings.Contains(stdout, "synthetic-envelope") {
		t.Fatal("scoped recovery key request or response missing", out.String())
	}
}

func TestPageDraftKeyRejectsAgentsAndInvalidInputBeforeNetwork(t *testing.T) {
	for _, tc := range []struct{ name, agent, ref, body string }{
		{"agent", "agtv2.synthetic", "@page", `{"human":"alice","operation":"wrap"}`},
		{"slug", "", "page", `{"human":"alice","operation":"unwrap","envelope":"v1:synthetic"}`},
		{"missing identity", "", "@page", `{"operation":"unwrap","envelope":"v1:synthetic"}`},
		{"duplicate field", "", "@page", `{"human":"alice","human":"bob","operation":"unwrap","envelope":"v1:x"}`},
		{"unknown field", "", "@page", `{"human":"alice","operation":"unwrap","envelope":"v1:x","workspace":"other"}`},
		{"invalid key", "", "@page", `{"human":"alice","operation":"wrap","key":"secret"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_AGENT_TOKEN", tc.agent)
			cmd := newPageDraftKeyCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetIn(strings.NewReader(tc.body))
			cmd.SetArgs([]string{tc.ref, "--file", "-"})
			if err := cmd.Execute(); err == nil {
				t.Fatal("invalid request reached transport")
			}
		})
	}
}
