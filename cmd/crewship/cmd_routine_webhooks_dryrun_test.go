package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestRoutineWebhooksTestRunE(t *testing.T) {
	path := covWebhooksPath + "/wh-1/test"
	tests := []struct {
		name    string
		sample  string
		headers []string
		reply   func(*clitest.StubServer)
		want    []string
		errHas  string
	}{
		{
			name: "a valid mapping prints the inputs", sample: `{"action":"opened"}`,
			reply: func(s *clitest.StubServer) {
				s.OnPost(path, clitest.JSONResponse(200, map[string]any{
					"inputs": map[string]any{"qty": 9, "event": map[string]any{"action": "opened"}}, "ignored_template_keys": []string{}, "valid": true}))
			},
			want: []string{"would accept", "qty: 9", `event: {"action":"opened"}`},
		},
		{
			name: "a refused mapping says why and names ignored keys", sample: `{}`,
			reply: func(s *clitest.StubServer) {
				s.OnPost(path, clitest.JSONResponse(200, map[string]any{
					"inputs": map[string]any{}, "ignored_template_keys": []string{"event"}, "valid": false, "problem": `input "qty" is required`}))
			},
			want: []string{"would refuse", `input "qty" is required`, "Ignored template keys", "event"},
		},
		{name: "invalid JSON never reaches the server", sample: `nope`, reply: func(*clitest.StubServer) {}, errHas: "valid JSON"},
		{name: "a malformed header is refused", sample: `{}`, headers: []string{"oops"}, reply: func(*clitest.StubServer) {}, errHas: "NAME=value"},
		{
			name: "the server's refusal surfaces", sample: `{}`,
			reply:  func(s *clitest.StubServer) { s.OnPost(path, clitest.ErrorResponse(404, "webhook not found")) },
			errHas: "webhook not found",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := covSetupCli4(t)
			tc.reply(stub)
			covSetFlag(t, routineWebhooksTestCmd, "sample", tc.sample)
			for _, h := range tc.headers {
				covSetFlag(t, routineWebhooksTestCmd, "header", h)
			}
			out, err := covCaptureStdoutCli4(t, func() error { return routineWebhooksTestCmd.RunE(routineWebhooksTestCmd, []string{"wh-1"}) })
			if tc.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("RunE: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q:\n%s", w, out)
				}
			}
		})
	}
}
