package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestP3PrivateUsageRecorderDiagnosticIsOpaque(t *testing.T) {
	const marker = "p3-private-usage-recorder-original-diagnostic"
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "private"}[private], func(t *testing.T) {
			var log bytes.Buffer
			o := New(nil, newMemState(), slog.New(slog.NewJSONHandler(&log, nil)))
			req := AgentRunRequest{CLIAdapter: "CLAUDE_CODE", Credentials: []Credential{{ID: "login", Provider: "ANTHROPIC", Type: "AI_CLI_TOKEN", PlainValue: "sk-ant-oat01-fixture"}}}
			if private {
				req = req.WithStrictContentPrivacy()
			}
			calls := 0
			o.SetSubscriptionUsageRecorder(func(context.Context, SubscriptionUsage) error { calls++; return errors.New(marker) })
			getAdapter("CLAUDE_CODE").ParseStreamLine([]byte(`{"type":"result","subtype":"success","usage":{"input_tokens":17,"output_tokens":8}}`), func(event AgentEvent) { o.recordSubscriptionUsage(t.Context(), req, "run", "model", event) })
			if calls != 1 {
				t.Fatal("usage path not exercised", calls)
			}
			if strings.Contains(log.String(), marker) == private {
				t.Fatal("request-local privacy did not select usage error sink")
			}
		})
	}
}
