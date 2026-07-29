package endpoint

import (
	"strings"
	"testing"
)

// TestNormalize_PasteShapes is the regression table for the bug this package
// exists to kill: one ENDPOINT_URL credential was consumed by three code paths
// that each expected a DIFFERENT URL shape (bare root for llm.Ollama, ".../v1"
// for the OpenCode agent block, ".../v1/chat/completions" for llm.OpenAI), and
// the reachability probe accepted all three. A value stored in the shape our own
// docs recommend made the Keeper judge POST to ".../v1/api/chat" -> 404 ->
// fail-closed DENY on every credential request, with a green Test button.
//
// Every shape an operator can plausibly paste must reduce to the same root, so
// that the per-wire path is appended exactly once.
func TestNormalize_PasteShapes(t *testing.T) {
	const want = "http://192.168.1.40:11434"

	shapes := []string{
		"http://192.168.1.40:11434",
		"http://192.168.1.40:11434/",
		"http://192.168.1.40:11434//",
		"  http://192.168.1.40:11434  ",
		"HTTP://192.168.1.40:11434",
		"http://192.168.1.40:11434/v1",
		"http://192.168.1.40:11434/v1/",
		"http://192.168.1.40:11434/v1/chat/completions",
		"http://192.168.1.40:11434/chat/completions",
		"http://192.168.1.40:11434/v1/responses",
		"http://192.168.1.40:11434/v1/messages",
		"http://192.168.1.40:11434/v1/models",
		"http://192.168.1.40:11434/api",
		"http://192.168.1.40:11434/api/chat",
		"http://192.168.1.40:11434/api/generate",
		"http://192.168.1.40:11434/api/tags",
		"http://192.168.1.40:11434/api/show",
		// Bare host:port — what an operator types when copying from `ollama serve`
		// output. No scheme means http; https would fail to connect to Ollama.
		"192.168.1.40:11434",
	}

	for _, raw := range shapes {
		t.Run(raw, func(t *testing.T) {
			ep, err := Normalize(raw)
			if err != nil {
				t.Fatalf("Normalize(%q) error: %v", raw, err)
			}
			if got := ep.Root.String(); got != want {
				t.Fatalf("Normalize(%q) root = %q, want %q", raw, got, want)
			}
		})
	}
}

// TestNormalize_PreservesMountPrefix guards the reverse-proxy deployment: an
// Ollama behind "https://gw.example.com/ollama" must keep that prefix, because
// only the API suffix is ours to strip. Stripping the whole path would silently
// repoint the judge at the proxy root.
func TestNormalize_PreservesMountPrefix(t *testing.T) {
	cases := map[string]string{
		"https://gw.example.com/ollama":                     "https://gw.example.com/ollama",
		"https://gw.example.com/ollama/":                    "https://gw.example.com/ollama",
		"https://gw.example.com/ollama/v1":                  "https://gw.example.com/ollama",
		"https://gw.example.com/ollama/v1/chat/completions": "https://gw.example.com/ollama",
		"https://gw.example.com/ollama/api/chat":            "https://gw.example.com/ollama",
		"https://gw.example.com/litellm/v1":                 "https://gw.example.com/litellm",
	}
	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			ep, err := Normalize(raw)
			if err != nil {
				t.Fatalf("Normalize(%q) error: %v", raw, err)
			}
			if got := ep.Root.String(); got != want {
				t.Fatalf("Normalize(%q) root = %q, want %q", raw, got, want)
			}
		})
	}
}

// TestNormalize_Rejects covers the values that must not become an endpoint at
// all. Userinfo mirrors the existing ENDPOINT_URL credential policy
// (internal/api/credentials_types.go): a credential smuggled into the URL
// evades the auth-token handling and is a display-confusion footgun.
func TestNormalize_Rejects(t *testing.T) {
	cases := map[string]string{
		"":                               "empty",
		"   ":                            "empty",
		"ftp://host:11434":               "scheme",
		"file:///etc/passwd":             "scheme",
		"http://user:pass@host:11434":    "credentials",
		"http://:11434":                  "host",
		"http://":                        "host",
		"://host":                        "",
		strings.Repeat("h", maxRawLen+1): "too long",
	}
	for raw, wantMsg := range cases {
		name := raw
		if len(name) > 40 {
			name = name[:40] + "…"
		}
		t.Run(name, func(t *testing.T) {
			_, err := Normalize(raw)
			if err == nil {
				t.Fatalf("Normalize(%q) = nil error, want rejection", raw)
			}
			if wantMsg != "" && !strings.Contains(err.Error(), wantMsg) {
				t.Fatalf("Normalize(%q) error = %q, want it to mention %q", raw, err, wantMsg)
			}
		})
	}
}

// TestNormalize_PreservesQuery keeps Azure-style deployments working: the
// api-version query is part of addressing there, and dropping it turns every
// call into a 400. It must survive normalization and re-attach to the built URL.
func TestNormalize_PreservesQuery(t *testing.T) {
	ep, err := Normalize("https://acme.openai.azure.com/openai/v1/chat/completions?api-version=2026-02-01")
	if err != nil {
		t.Fatalf("Normalize error: %v", err)
	}
	if want := "https://acme.openai.azure.com/openai"; ep.Root.String() != want {
		t.Fatalf("root = %q, want %q", ep.Root.String(), want)
	}
	got := ep.WithWire(WireOpenAIChat).ChatURL()
	want := "https://acme.openai.azure.com/openai/v1/chat/completions?api-version=2026-02-01"
	if got != want {
		t.Fatalf("ChatURL = %q, want %q", got, want)
	}
}

// TestChatURL_PerWire is the other half of the §2 fix: from ONE root, each wire
// builds its own path. No caller ever concatenates onto a raw stored string
// again, so the "/v1/api/chat" 404 is unreachable by construction.
func TestChatURL_PerWire(t *testing.T) {
	ep, err := Normalize("http://localhost:11434/v1")
	if err != nil {
		t.Fatalf("Normalize error: %v", err)
	}
	cases := map[Wire]string{
		WireOllama:            "http://localhost:11434/api/chat",
		WireOpenAIChat:        "http://localhost:11434/v1/chat/completions",
		WireOpenAIResponses:   "http://localhost:11434/v1/responses",
		WireAnthropicMessages: "http://localhost:11434/v1/messages",
	}
	for w, want := range cases {
		t.Run(string(w), func(t *testing.T) {
			if got := ep.WithWire(w).ChatURL(); got != want {
				t.Fatalf("ChatURL(%s) = %q, want %q", w, got, want)
			}
		})
	}
}

// TestModelsURL_PerWire covers discovery, which uses a different path per wire
// and is what the model picker lists against.
func TestModelsURL_PerWire(t *testing.T) {
	ep, err := Normalize("http://localhost:11434")
	if err != nil {
		t.Fatalf("Normalize error: %v", err)
	}
	cases := map[Wire]string{
		WireOllama:            "http://localhost:11434/api/tags",
		WireOpenAIChat:        "http://localhost:11434/v1/models",
		WireOpenAIResponses:   "http://localhost:11434/v1/models",
		WireAnthropicMessages: "http://localhost:11434/v1/models",
	}
	for w, want := range cases {
		t.Run(string(w), func(t *testing.T) {
			if got := ep.WithWire(w).ModelsURL(); got != want {
				t.Fatalf("ModelsURL(%s) = %q, want %q", w, got, want)
			}
		})
	}
}

// TestWithWire_DoesNotMutate keeps Endpoint value-semantics honest: the resolver
// reuses one normalized endpoint across wires when probing, and a mutating
// WithWire would make probe order significant.
func TestWithWire_DoesNotMutate(t *testing.T) {
	ep, err := Normalize("http://localhost:11434")
	if err != nil {
		t.Fatalf("Normalize error: %v", err)
	}
	_ = ep.WithWire(WireOpenAIChat)
	if ep.Wire != "" {
		t.Fatalf("WithWire mutated the receiver: wire = %q", ep.Wire)
	}
	a := ep.WithWire(WireOllama).ChatURL()
	b := ep.WithWire(WireOpenAIChat).ChatURL()
	if a == b {
		t.Fatalf("both wires produced %q", a)
	}
}

// TestKnownWire pins the accepted set — the API layer validates operator input
// against the same list the URL builder trusts, so the two cannot drift.
func TestKnownWire(t *testing.T) {
	for _, w := range AllWires() {
		if !KnownWire(string(w)) {
			t.Errorf("KnownWire(%q) = false, want true", w)
		}
	}
	for _, bad := range []string{"", "ollama-native", "openai", "OLLAMA", "grpc"} {
		if KnownWire(bad) {
			t.Errorf("KnownWire(%q) = true, want false", bad)
		}
	}
}

// TestIsHostOnlyResolvableInContainers catches the vantage-point trap: the same
// stored credential serves the agent (dialling from inside a crew container,
// where host.docker.internal is correct) and the Keeper judge (dialling from the
// daemon on the host, where it resolves to nothing). The judge must refuse it
// with an explanation rather than time out.
func TestIsHostOnlyResolvableInContainers(t *testing.T) {
	yes := []string{
		"http://host.docker.internal:11434",
		"http://HOST.DOCKER.INTERNAL:11434",
		"http://host.containers.internal:11434",
		"http://gateway.docker.internal:11434",
	}
	for _, raw := range yes {
		ep, err := Normalize(raw)
		if err != nil {
			t.Fatalf("Normalize(%q) error: %v", raw, err)
		}
		if !ep.IsContainerOnlyHost() {
			t.Errorf("IsContainerOnlyHost(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{"http://localhost:11434", "http://192.168.1.40:11434", "https://ollama.example.com"} {
		ep, err := Normalize(raw)
		if err != nil {
			t.Fatalf("Normalize(%q) error: %v", raw, err)
		}
		if ep.IsContainerOnlyHost() {
			t.Errorf("IsContainerOnlyHost(%q) = true, want false", raw)
		}
	}
}
