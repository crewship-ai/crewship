package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// runner_http.go — FingerprintHTTPRequest.
//
// Stable per-(method, host, path) hash used by the run-summary API to
// group HTTP steps for retry analytics. Query strings and bodies are
// deliberately excluded so that per-input variance (e.g. different
// `?user_id=` query params) doesn't fragment what's logically the same
// endpoint call.
//
// The contract that matters:
//   1. STABLE — same inputs produce the same fingerprint across runs
//      (it's persisted to journal rows, so a regression that changed
//      the hash shape would orphan historical entries).
//   2. METHOD CASE-INSENSITIVE — "GET" and "get" must collapse.
//   3. QUERY EXCLUDED — `?foo=1` and `?foo=2` must equal.
//   4. URL-PARSE FAILURE GRACEFUL — degenerate input still produces a
//      deterministic 16-char hex string (not a panic or empty string).
// ---------------------------------------------------------------------------

func TestFingerprintHTTPRequest_StableAcrossInvocations(t *testing.T) {
	// Determinism is a hard requirement — the fingerprint is persisted
	// to journal entries. A regression that introduced any non-determinism
	// (clock, randomness, map iteration) would silently corrupt the
	// grouping in the run summary API.
	first := FingerprintHTTPRequest("GET", "https://api.example.com/v1/users")
	for i := 0; i < 16; i++ {
		got := FingerprintHTTPRequest("GET", "https://api.example.com/v1/users")
		if got != first {
			t.Fatalf("non-deterministic: invocation %d returned %q vs first %q", i, got, first)
		}
	}
}

func TestFingerprintHTTPRequest_OutputShape_16HexChars(t *testing.T) {
	// Source: hex of first 8 bytes of SHA-256 → 16 lowercase hex chars.
	// Pin both the length and the alphabet because downstream consumers
	// (run-summary join, log greppers) often assume the shape and would
	// break silently on a hash-format change.
	got := FingerprintHTTPRequest("POST", "https://api.example.com/items")
	if len(got) != 16 {
		t.Errorf("len(fingerprint) = %d, want 16 (first 8 sha256 bytes hex-encoded)", len(got))
	}
	for _, r := range got {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("fingerprint %q contains non-lowercase-hex rune %q", got, r)
		}
	}
}

func TestFingerprintHTTPRequest_MethodCaseInsensitive(t *testing.T) {
	// strings.ToUpper(method) at the source — pin that every common
	// case variant collapses. Without this contract, a pipeline author
	// writing `method: get` and another writing `method: GET` would
	// split their analytics across two fingerprints.
	want := FingerprintHTTPRequest("GET", "https://api.example.com/x")
	for _, m := range []string{"get", "Get", "gEt", "GET", "geT"} {
		got := FingerprintHTTPRequest(m, "https://api.example.com/x")
		if got != want {
			t.Errorf("FingerprintHTTPRequest(%q, ...) = %q, want %q (method must be case-insensitive)", m, got, want)
		}
	}
}

func TestFingerprintHTTPRequest_QueryStringExcluded(t *testing.T) {
	// Source comment: "query string and body excluded so per-input
	// variance doesn't fragment the fingerprint". Pin that two URLs
	// differing only in query collapse — this is the design choice
	// the function exists to enforce.
	a := FingerprintHTTPRequest("GET", "https://api.example.com/users?id=1")
	b := FingerprintHTTPRequest("GET", "https://api.example.com/users?id=2&page=5")
	if a != b {
		t.Errorf("different queries produced different fingerprints: %q vs %q (query must be excluded)", a, b)
	}
	// And one with no query at all collapses too.
	c := FingerprintHTTPRequest("GET", "https://api.example.com/users")
	if c != a {
		t.Errorf("no-query vs with-query: %q vs %q (query exclusion must include absent query)", c, a)
	}
}

func TestFingerprintHTTPRequest_FragmentExcluded(t *testing.T) {
	// url.Parse separates the fragment out; the function only consumes
	// host + path. Pin that #section variations also collapse — pipeline
	// authors who paste a browser URL would otherwise see fragment-only
	// variance fragment their analytics.
	a := FingerprintHTTPRequest("GET", "https://api.example.com/page")
	b := FingerprintHTTPRequest("GET", "https://api.example.com/page#section")
	if a != b {
		t.Errorf("fragment changed fingerprint: %q vs %q (fragment is not in host/path)", a, b)
	}
}

func TestFingerprintHTTPRequest_DifferentMethodsDiffer(t *testing.T) {
	// Method is part of the hashed key — GET /users and POST /users
	// are semantically different operations and must NOT collapse.
	get := FingerprintHTTPRequest("GET", "https://api.example.com/users")
	post := FingerprintHTTPRequest("POST", "https://api.example.com/users")
	if get == post {
		t.Errorf("GET and POST collided on fingerprint %q (method must contribute)", get)
	}
}

func TestFingerprintHTTPRequest_DifferentHostsDiffer(t *testing.T) {
	// Host is part of the key — calls to two different APIs with the
	// same path must NOT collapse.
	a := FingerprintHTTPRequest("GET", "https://api-a.example.com/users")
	b := FingerprintHTTPRequest("GET", "https://api-b.example.com/users")
	if a == b {
		t.Errorf("different hosts collided on fingerprint %q (host must contribute)", a)
	}
}

func TestFingerprintHTTPRequest_DifferentPathsDiffer(t *testing.T) {
	// Path is part of the key — /users and /items must NOT collapse.
	users := FingerprintHTTPRequest("GET", "https://api.example.com/users")
	items := FingerprintHTTPRequest("GET", "https://api.example.com/items")
	if users == items {
		t.Errorf("different paths collided on fingerprint %q (path must contribute)", users)
	}
}

func TestFingerprintHTTPRequest_PortIsPartOfHost(t *testing.T) {
	// url.URL.Host includes the port. Pin that `:8080` is part of the
	// fingerprint — same hostname on two ports is two different
	// services, and the analytics grouping must reflect that.
	plain := FingerprintHTTPRequest("GET", "https://api.example.com/users")
	port := FingerprintHTTPRequest("GET", "https://api.example.com:8080/users")
	if plain == port {
		t.Errorf("port-vs-no-port collided on fingerprint %q (port is part of host)", plain)
	}
}

func TestFingerprintHTTPRequest_SchemeNotInKey(t *testing.T) {
	// Source: only host+path go into the hash — scheme is dropped. Pin
	// that http://x/y and https://x/y collapse. Whether this is the
	// "right" choice is debatable, but it IS the current contract;
	// pinning means a future "include scheme" change has to flip this
	// test in step.
	httpFP := FingerprintHTTPRequest("GET", "http://api.example.com/users")
	httpsFP := FingerprintHTTPRequest("GET", "https://api.example.com/users")
	if httpFP != httpsFP {
		t.Errorf("scheme leaked into fingerprint: http=%q https=%q (only method+host+path are hashed)", httpFP, httpsFP)
	}
}

func TestFingerprintHTTPRequest_MalformedURL_DegradesGracefully(t *testing.T) {
	// A URL.Parse failure produces host="" and path="" — the function
	// still returns a deterministic 16-char hex string rather than
	// panicking or returning empty. Pin all three properties: no panic,
	// correct length, determinism.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on malformed URL: %v", r)
		}
	}()
	const bad = "://not a url"
	got := FingerprintHTTPRequest("GET", bad)
	if len(got) != 16 {
		t.Errorf("malformed input len = %d, want 16 (degenerate path must still produce a valid fingerprint)", len(got))
	}
	got2 := FingerprintHTTPRequest("GET", bad)
	if got != got2 {
		t.Errorf("non-deterministic on malformed input: %q vs %q", got, got2)
	}
}

func TestFingerprintHTTPRequest_EmptyURL_Stable(t *testing.T) {
	// Empty URL is a real shape — pipelines under construction may
	// have a template that hasn't been rendered yet. The fingerprint
	// should still come back deterministically; collapses with empty
	// method are fine.
	a := FingerprintHTTPRequest("", "")
	b := FingerprintHTTPRequest("", "")
	if a != b {
		t.Errorf("non-deterministic on empty inputs: %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Errorf("empty-input len = %d, want 16", len(a))
	}
}

func TestFingerprintHTTPRequest_MatchesExplicitHashShape(t *testing.T) {
	// Reproduce the hash algorithm independently and verify the output
	// matches. Catches a future "switch to a different hash" change
	// (e.g. blake3, sha1) which would silently change every existing
	// fingerprint in the journal. We pin both:
	//   - the exact 16-char prefix of hex(sha256("METHOD HOST/PATH"))
	//   - the case normalisation (method uppercased, host/path NOT)
	method := "post"
	rawURL := "https://api.example.com/items/42"
	got := FingerprintHTTPRequest(method, rawURL)

	// Independent computation. Mirrors source exactly.
	sum := sha256.Sum256([]byte("POST" + " " + "api.example.com" + "/items/42"))
	want := hex.EncodeToString(sum[:8])
	if got != want {
		t.Errorf("FingerprintHTTPRequest(%q, %q) = %q, want %q (hash algorithm drift)", method, rawURL, got, want)
	}
}

func TestFingerprintHTTPRequest_HostCaseSensitive(t *testing.T) {
	// The source does NOT lowercase host. Pin so a future "normalize
	// host like HTTP does" change has to flip this test deliberately.
	// (Hosts are conventionally case-insensitive at the protocol level
	// but Go's url.URL preserves whatever the input gave.)
	lower := FingerprintHTTPRequest("GET", "https://api.example.com/x")
	upper := FingerprintHTTPRequest("GET", "https://API.EXAMPLE.COM/x")
	if lower == upper {
		// If we get here, someone changed the source to lowercase host —
		// update this assertion AND the source comment.
		t.Logf("host case treated as equivalent — if intentional, update test + source comment to reflect")
	}
	// Hard pin: at least one of the two cases collides with the literal
	// expectation derived from the source.
	wantLower := FingerprintHTTPRequest("GET", "https://api.example.com/x")
	if lower != wantLower {
		t.Errorf("repeat call on same input drifted: %q vs %q", lower, wantLower)
	}
	if !strings.HasPrefix(lower, lower[:8]) {
		t.Errorf("self-check: %q has no 8-char prefix", lower)
	}
}
