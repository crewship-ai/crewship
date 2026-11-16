package orchestrator

import (
	"strings"
	"testing"
)

// v2.3.0 packages/cli/src/run/noninteractive.ts's renderer subtracts printed
// text itself. Identical deltas and a delta extending its predecessor are
// legitimate output, not duplicate accumulated snapshots.
func TestOpenCodeV2RepeatedDeltas(t *testing.T) {
	for _, kind := range []string{"text", "reasoning"} {
		var got strings.Builder
		parse := (opencodeAdapter{}).NewStreamLineParser()
		for _, chunk := range []string{"ha", "ha", "haha"} {
			parse([]byte(`{"type":"`+kind+`","sessionID":"s","part":{"id":"p","text":"`+chunk+`"}}`), func(e AgentEvent) {
				if e.Type == "text" || e.Type == "thinking" {
					got.WriteString(e.Content)
				}
			})
		}
		if got.String() != "hahahaha" {
			t.Fatalf("%s lost delta: %q", kind, got.String())
		}
	}
}

func TestOpenCodeV2ErrorMessage(t *testing.T) {
	got := decodeOpenCodeError([]byte(`{"type":"ProviderError","message":"model access denied"}`))
	if got != "ProviderError: model access denied" {
		t.Fatal(got)
	}
}

func TestOpenCodeBedrockExplicitBillingProvider(t *testing.T) {
	for _, model := range []string{"eu.anthropic.claude-opus-5-5", "eu.anthropic.claude-sonnet-5-5", "amazon-bedrock/eu.anthropic.claude-opus-5-5"} {
		got := qualifyOpenCodeModel("AWS_BEDROCK", model)
		want := model
		if !strings.HasPrefix(model, "amazon-bedrock/") {
			want = "amazon-bedrock/" + model
		}
		if got != want {
			t.Fatalf("%s resolved to %s, want %s", model, got, want)
		}
	}
}
