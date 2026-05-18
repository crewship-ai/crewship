package api

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// port_expose_handler.go — DockerInspectorFunc.ContainerIP +
// safeTokenPrefix.
//
// DockerInspectorFunc is the function-to-interface adapter the router
// uses for the docker container-IP probe. safeTokenPrefix is the
// log-redaction helper that keeps full capability tokens out of log
// aggregators — a regression here would silently leak the entire
// expose token into operator dashboards.
// ---------------------------------------------------------------------------

// ---- DockerInspectorFunc.ContainerIP ----

func TestDockerInspectorFunc_ContainerIP_DelegatesToWrappedFn(t *testing.T) {
	// The adapter must pass ctx + containerID + network through
	// verbatim. Pin both args and the returned (ip, err) tuple.
	var gotContainerID, gotNetwork string
	var gotCtxKey any
	type key string
	ctxKey := key("trace_id")
	wrapped := func(ctx context.Context, containerID, network string) (string, error) {
		gotContainerID = containerID
		gotNetwork = network
		gotCtxKey = ctx.Value(ctxKey)
		return "10.0.0.42", nil
	}

	f := DockerInspectorFunc(wrapped)
	ctx := context.WithValue(context.Background(), ctxKey, "trace-abc")
	ip, err := f.ContainerIP(ctx, "ct-1", "crewship-agents")
	if err != nil {
		t.Fatalf("ContainerIP: %v", err)
	}
	if ip != "10.0.0.42" {
		t.Errorf("ip = %q, want 10.0.0.42", ip)
	}
	if gotContainerID != "ct-1" {
		t.Errorf("containerID = %q", gotContainerID)
	}
	if gotNetwork != "crewship-agents" {
		t.Errorf("network = %q", gotNetwork)
	}
	if gotCtxKey != "trace-abc" {
		t.Errorf("ctx value lost; got %v, want trace-abc", gotCtxKey)
	}
}

func TestDockerInspectorFunc_ContainerIP_PropagatesError(t *testing.T) {
	want := errors.New("container not attached to crew network")
	f := DockerInspectorFunc(func(_ context.Context, _, _ string) (string, error) {
		return "", want
	})
	ip, err := f.ContainerIP(context.Background(), "ct-2", "x")
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want errors.Is(err, %v)", err, want)
	}
	if ip != "" {
		t.Errorf("ip = %q on error, want empty", ip)
	}
}

func TestDockerInspectorFunc_SatisfiesDockerInspector(t *testing.T) {
	// Compile-time interface assertion: DockerInspectorFunc must
	// continue to satisfy DockerInspector. A refactor that adds a
	// method to the interface would surface here AND at every other
	// call site.
	var _ DockerInspector = DockerInspectorFunc(nil)
}

// ---- safeTokenPrefix ----

func TestSafeTokenPrefix_LongTokenTruncatedToEightCharsPlusEllipsis(t *testing.T) {
	// Source: tokens > 8 chars return the first 8 + "…". The whole
	// point is to keep the full capability token out of logs. Pin
	// the exact format AND the ellipsis presence — a regression that
	// dropped the ellipsis would visually look like the full token
	// (operator would assume nothing was redacted).
	full := "abcdefghijklmnopqrstuvwxyz123456789"
	got := safeTokenPrefix(full)
	want := "abcdefgh…"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("redacted output missing ellipsis: %q", got)
	}
	if strings.Contains(got, "9") {
		t.Errorf("redacted output leaked end of token: %q", got)
	}
}

func TestSafeTokenPrefix_ShortTokenPassesThroughUnchanged(t *testing.T) {
	// Tokens <= 8 chars return verbatim — no ellipsis. Pin so a
	// regression that always appended "…" wouldn't silently change
	// log output for short test fixtures.
	for _, in := range []string{"", "a", "12345678"} {
		got := safeTokenPrefix(in)
		if got != in {
			t.Errorf("safeTokenPrefix(%q) = %q, want %q (≤8 chars passes through)", in, got, in)
		}
		if strings.Contains(got, "…") {
			t.Errorf("safeTokenPrefix(%q) added ellipsis on short input: %q", in, got)
		}
	}
}

func TestSafeTokenPrefix_BoundaryAt9Chars_AppliesTruncation(t *testing.T) {
	// 8 chars → pass-through; 9 chars → truncate to first 8 + "…".
	// Pin the exact boundary.
	in9 := "abcdefghi" // 9 chars
	got := safeTokenPrefix(in9)
	want := "abcdefgh…"
	if got != want {
		t.Errorf("9-char input: got %q, want %q (boundary case)", got, want)
	}
}

func TestSafeTokenPrefix_OutputDoesNotContainOriginalFull(t *testing.T) {
	// Property check: for any token > 8 chars, the redacted output
	// must NOT equal the input. A regression that returned the input
	// verbatim past the boundary would silently lift the redaction.
	for _, in := range []string{
		"longtoken123",
		strings.Repeat("a", 100),
		"sk-ant-oat01-very-long-capability-token-do-not-leak-into-logs",
	} {
		got := safeTokenPrefix(in)
		if got == in {
			t.Errorf("safeTokenPrefix(%q) returned input verbatim; should have truncated", in)
		}
		// 8 ASCII chars + "…" (3 bytes in UTF-8) = 11 bytes total; pin
		// that exactly so a regression that started including more of
		// the token would surface here.
		if len(got) != 11 {
			t.Errorf("safeTokenPrefix output = %d bytes for %q, want 11 (8 chars + 3-byte ellipsis)", len(got), in)
		}
	}
}

// ---- generateExposeToken (already 75% covered; add the deterministic
//      property: uniqueness across many calls) ----

func TestGenerateExposeToken_Unique(t *testing.T) {
	// 32 random bytes → 43-char base64 = ~256 bits of entropy. 1000
	// generations must produce zero collisions; a regression that
	// reused a fixed seed or truncated the random source would
	// surface here AND silently weaken the capability-token security.
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		tok, err := generateExposeToken()
		if err != nil {
			t.Fatalf("generateExposeToken: %v", err)
		}
		if len(tok) != 43 {
			t.Errorf("token length = %d, want 43 (32 bytes raw base64url)", len(tok))
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("collision at iteration %d: %q", i, tok)
		}
		seen[tok] = struct{}{}
	}
}
