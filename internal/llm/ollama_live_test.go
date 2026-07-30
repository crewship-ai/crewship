package llm

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// Live integration probe against a real Ollama. It SKIPs unless one is actually
// reachable, so CI stays hermetic while a developer (or the dev VM's nightly
// run) gets the real thing exercised.
//
// What it is for: the fake-server tests pin the URL we build, but only a real
// daemon proves the whole shape is right — that a credential stored the way our
// own docs recommend (".../v1") reaches a live model instead of 404ing into a
// fail-closed Keeper DENY.
//
// Override the endpoint with CREWSHIP_TEST_OLLAMA_URL.

func liveOllamaBase(t *testing.T) string {
	t.Helper()
	base := os.Getenv("CREWSHIP_TEST_OLLAMA_URL")
	if base == "" {
		base = "http://localhost:11434"
	}
	host := strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://")
	host = strings.SplitN(host, "/", 2)[0]
	conn, err := net.DialTimeout("tcp", host, 300*time.Millisecond)
	if err != nil {
		t.Skipf("no Ollama reachable at %s (%v) — start one with `ollama serve` to run this", base, err)
	}
	_ = conn.Close()
	return base
}

// liveModel picks a model the daemon actually has pulled, preferring a small
// instruct model — the judge is a classifier, so a 3-8B model is the realistic
// production choice and keeps the test fast.
func liveModel(t *testing.T, base string) string {
	t.Helper()
	models, err := NewOllama(base, "").ListModels(context.Background())
	if err != nil {
		t.Skipf("could not list models at %s: %v", base, err)
	}
	if len(models) == 0 {
		t.Skip("Ollama is running but has no models pulled")
	}
	// Prefer a NON-reasoning model: this test asserts URL shapes, and a
	// reasoning model can legitimately spend its whole budget thinking and return
	// empty content, which would redden a test that is not about model behaviour.
	// The reasoning trap has its own test below.
	for _, want := range []string{"qwen2.5:7b", "mistral:7b", "granite3.3:8b", "qwen2.5-coder:7b"} {
		for _, m := range models {
			if m.ID == want {
				return want
			}
		}
	}
	return models[0].ID
}

// TestLive_OllamaAcceptsEveryStoredShape is the end-to-end proof of the fix: the
// exact value our documentation tells operators to store (".../v1") must reach a
// live model. Before normalization this POSTed to ".../v1/api/chat", got a 404,
// and Keeper — fail-closed — denied every credential request.
func TestLive_OllamaAcceptsEveryStoredShape(t *testing.T) {
	base := liveOllamaBase(t)
	model := liveModel(t, base)
	t.Logf("live Ollama at %s, judging with %s", base, model)

	for _, suffix := range []string{"", "/", "/v1", "/api/chat"} {
		t.Run("stored"+suffix, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			p := NewOllama(base+suffix, model)
			resp, err := p.Complete(ctx, Request{
				Model:     model,
				System:    "You are a security gatekeeper. Reply with ONLY a JSON object.",
				Messages:  []Message{{Role: "user", Content: `Credential: NPM_TOKEN (L1). Intent: "publish the release build". Respond as {"decision":"ALLOW|DENY|ESCALATE","reason":"...","risk":1-10}.`}},
				MaxTokens: 200,
			})
			if err != nil {
				t.Fatalf("stored value %q failed against a live Ollama: %v", base+suffix, err)
			}
			if resp == nil || strings.TrimSpace(resp.Content) == "" {
				t.Fatalf("stored value %q produced an empty completion", base+suffix)
			}
			t.Logf("stored %-10q -> %s", base+suffix, strings.TrimSpace(truncate(resp.Content, 160)))
		})
	}
}

// TestLive_OllamaListModelsEveryStoredShape covers discovery on a live daemon —
// this is what the model picker calls, and a "/v1" value used to make it come
// back empty so the operator concluded the endpoint was down.
func TestLive_OllamaListModelsEveryStoredShape(t *testing.T) {
	base := liveOllamaBase(t)

	var first int
	for i, suffix := range []string{"", "/v1", "/api/tags"} {
		models, err := NewOllama(base+suffix, "").ListModels(context.Background())
		if err != nil {
			t.Fatalf("ListModels with stored value %q: %v", base+suffix, err)
		}
		if len(models) == 0 {
			t.Fatalf("ListModels with stored value %q returned nothing", base+suffix)
		}
		if i == 0 {
			first = len(models)
			t.Logf("live Ollama advertises %d model(s)", first)
			continue
		}
		if len(models) != first {
			t.Fatalf("stored value %q listed %d models, the bare root listed %d — shapes must agree",
				base+suffix, len(models), first)
		}
	}
}

// TestLive_OpenAICompatWireAgainstOllama proves the other wire against the same
// daemon: Ollama also serves an OpenAI-compatible API, and an operator who picks
// the openai_compat provider must get a working judge from the same stored value.
func TestLive_OpenAICompatWireAgainstOllama(t *testing.T) {
	base := liveOllamaBase(t)
	model := liveModel(t, base)

	for _, suffix := range []string{"", "/v1", "/v1/chat/completions"} {
		t.Run("stored"+suffix, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			// Ollama's compat layer ignores the key; pass a placeholder so the
			// Authorization header is well-formed.
			p := NewOpenAIWithBaseURL("ollama", base+suffix)
			resp, err := p.Complete(ctx, Request{
				Model:     model,
				Messages:  []Message{{Role: "user", Content: `Reply with only the JSON {"decision":"ALLOW","risk":1}.`}},
				MaxTokens: 100,
			})
			if err != nil {
				t.Fatalf("openai-compat wire with stored value %q: %v", base+suffix, err)
			}
			if resp == nil || strings.TrimSpace(resp.Content) == "" {
				t.Fatalf("openai-compat wire with stored value %q returned nothing", base+suffix)
			}
			t.Logf("compat stored %-22q -> %s", base+suffix, strings.TrimSpace(truncate(resp.Content, 120)))
		})
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// TestLive_ReasoningModelExhaustsBudget documents, against a real daemon, the
// second way a healthy-looking Keeper denies everything: a reasoning model
// returns its chain of thought in `thinking` and leaves `content` empty when the
// token budget runs out. The judge then parses nothing and — being fail-closed —
// denies, with an HTTP 200 and no error anywhere.
//
// The assertion is deliberately about DIAGNOSABILITY, not about the model
// answering: the provider must surface enough for a caller to say "your judge
// ran out of budget while reasoning" instead of "empty response".
func TestLive_ReasoningModelExhaustsBudget(t *testing.T) {
	base := liveOllamaBase(t)

	models, err := NewOllama(base, "").ListModels(context.Background())
	if err != nil {
		t.Skipf("could not list models: %v", err)
	}
	var reasoning string
	for _, want := range []string{"qwen3:4b", "qwen3:8b", "deepseek-r1:8b"} {
		for _, m := range models {
			if m.ID == want {
				reasoning = want
			}
		}
		if reasoning != "" {
			break
		}
	}
	if reasoning == "" {
		t.Skip("no reasoning model pulled (try `ollama pull qwen3:4b`)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// A budget deliberately too small to think AND answer — the realistic shape
	// of a judge prompt with a tight max-tokens.
	resp, err := NewOllama(base, reasoning).Complete(ctx, Request{
		Model:     reasoning,
		Messages:  []Message{{Role: "user", Content: `Reply with only {"decision":"ALLOW","risk":1}`}},
		MaxTokens: 200,
	})
	if err != nil {
		t.Fatalf("Complete against %s: %v", reasoning, err)
	}
	if resp.Content != "" {
		t.Skipf("%s answered within the budget (content %q) — nothing to diagnose here", reasoning, truncate(resp.Content, 80))
	}
	if resp.Thinking == "" {
		t.Fatalf("%s returned empty content AND empty thinking — the caller has no way to explain the DENY", reasoning)
	}
	if resp.StopReason != StopMaxToks {
		t.Fatalf("%s ran out of budget but reported stop reason %q, want %q", reasoning, resp.StopReason, StopMaxToks)
	}
	t.Logf("%s: empty content, %d chars of thinking, stop=%s — diagnosable", reasoning, len(resp.Thinking), resp.StopReason)
}
