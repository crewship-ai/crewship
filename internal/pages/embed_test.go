package pages

import (
	"errors"
	"strings"
	"testing"
)

// A policy with two vetted sources, used by most of the table tests.
func testEmbedPolicy(t *testing.T) EmbedPolicy {
	t.Helper()
	p, err := ParseEmbedPolicy(
		"grafana-fleet=https://grafana.example.com/d/abc/fleet,status=https://status.example.com/",
		"https://crewship.example.com")
	if err != nil {
		t.Fatalf("ParseEmbedPolicy: %v", err)
	}
	return p
}

func TestParseEmbedPolicy(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		self    string
		wantErr string
	}{
		{"empty is a disabled policy, not an error", "", "https://crewship.example.com", ""},
		{"one source", "a=https://a.example/x", "https://crewship.example.com", ""},
		{"whitespace and trailing comma tolerated", " a = https://a.example/x , ", "", ""},

		// The name is the producer-facing vocabulary, so it is a slug.
		{"name must be a slug", "A_b=https://a.example/", "", "not a valid embed source name"},
		{"name may not be empty", "=https://a.example/", "", "not a valid embed source name"},
		{"duplicate name", "a=https://a.example/,a=https://b.example/", "", "declared twice"},
		{"missing '='", "https://a.example/", "", "expected name=url"},

		// The URL rules. Each one is a hole a browser would otherwise open.
		{"http is refused", "a=http://a.example/", "", "scheme"},
		{"userinfo is refused", "a=https://u:p@a.example/", "", "userinfo"},
		{"loopback literal is refused", "a=https://127.0.0.1/", "", "not allowed"},
		{"rfc1918 literal is refused", "a=https://192.168.1.1/", "", "not allowed"},
		{"fragment is refused", "a=https://a.example/x#top", "", "fragment"},
		{"relative url is refused", "a=/dashboards/1", "", "scheme"},
		{"javascript: is refused", "a=javascript:alert(1)", "", "scheme"},

		// The one that makes "cross-origin" true rather than aspirational.
		{
			"a source on our own origin is refused",
			"a=https://crewship.example.com/exposed/tok",
			"https://crewship.example.com",
			"own origin",
		},
		{
			"our own origin is compared without the default port",
			"a=https://crewship.example.com:443/x",
			"https://crewship.example.com",
			"own origin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseEmbedPolicy(tc.spec, tc.self)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("expected an error containing %q, got none", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestEmbedPolicy_FrameSrc(t *testing.T) {
	// §3.1: frame-src 'none' by DEFAULT. An unconfigured instance must emit a
	// policy that forbids every frame, not one that forgets to mention them.
	var zero EmbedPolicy
	if got := zero.FrameSrc(); got != "frame-src 'none'" {
		t.Fatalf("empty policy frame-src = %q, want frame-src 'none'", got)
	}

	p := testEmbedPolicy(t)
	// Origins only, deduplicated and sorted: a CSP source list is matched on
	// origin, so shipping the paths would be noise a reviewer has to discount.
	want := "frame-src https://grafana.example.com https://status.example.com"
	if got := p.FrameSrc(); got != want {
		t.Fatalf("frame-src = %q, want %q", got, want)
	}
}

func TestEmbedPolicy_FrameSrcDedupesOrigins(t *testing.T) {
	p, err := ParseEmbedPolicy("a=https://g.example/1,b=https://g.example/2", "")
	if err != nil {
		t.Fatalf("ParseEmbedPolicy: %v", err)
	}
	if got := p.FrameSrc(); got != "frame-src https://g.example" {
		t.Fatalf("frame-src = %q", got)
	}
}

func TestEmbedDisabledByDefault(t *testing.T) {
	// The zero value of the process policy is "no vetted sources", and that
	// has to fail CLOSED in all three places it is asked.
	restore := SetEmbedPolicy(EmbedPolicy{})
	defer restore()

	if EmbedEnabled() {
		t.Fatal("embed is enabled with no configured sources")
	}
	if SchemaEmbed.Producible() {
		t.Fatal("embed.v1 is producible with no sandbox configured; a page could declare a panel that can never render")
	}
	_, err := ValidatePayload(SchemaEmbed, []byte(`{"source":"grafana-fleet"}`))
	if err == nil {
		t.Fatal("an embed payload was accepted with no configured sources")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Code != CodeUnknownSchema {
		t.Fatalf("err = %#v, want a %s ValidationError", err, CodeUnknownSchema)
	}
	if !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("refusal %q does not say the instance has embeds turned off", err)
	}
}

func TestEmbedProducibleFollowsThePolicy(t *testing.T) {
	restore := SetEmbedPolicy(testEmbedPolicy(t))
	defer restore()

	if !SchemaEmbed.Producible() {
		t.Fatal("embed.v1 is not producible with a configured allow-list")
	}
	// The other five never depend on it.
	for _, s := range []PanelSchema{SchemaMetric, SchemaSeries, SchemaStatus, SchemaTable, SchemaNarrative} {
		if !s.Producible() {
			t.Fatalf("%s stopped being producible", s)
		}
	}
}

func TestValidateEmbed(t *testing.T) {
	restore := SetEmbedPolicy(testEmbedPolicy(t))
	defer restore()

	cases := []struct {
		name    string
		raw     string
		wantErr ErrorCode
		detail  string
	}{
		{name: "a declared source", raw: `{"source":"grafana-fleet"}`},
		{name: "with a caption", raw: `{"source":"status","caption":"eu-west"}`},

		{"an undeclared source is refused", `{"source":"evil"}`, CodeInconsistentPayload, "not a declared embed source"},
		{"source is required", `{"caption":"x"}`, CodeSchemaViolation, "source"},
		{"a source that is not a slug", `{"source":"Grafana Fleet"}`, CodeSchemaViolation, "pattern"},

		// The fields that must not exist. Each is refused by
		// additionalProperties, not by a sanitiser (§8 rules 1-3).
		{"no url field", `{"source":"status","url":"https://evil.example"}`, CodeSchemaViolation, "additional"},
		{"no src field", `{"source":"status","src":"https://evil.example"}`, CodeSchemaViolation, "additional"},
		{"no html field", `{"source":"status","html":"<script>x</script>"}`, CodeSchemaViolation, "additional"},
		{"no srcdoc field", `{"source":"status","srcdoc":"<b>x</b>"}`, CodeSchemaViolation, "additional"},
		{"no sandbox field", `{"source":"status","sandbox":"allow-scripts allow-same-origin"}`, CodeSchemaViolation, "additional"},
		{"no allow field", `{"source":"status","allow":"camera; microphone"}`, CodeSchemaViolation, "additional"},
		{"no height field", `{"source":"status","height":9000}`, CodeSchemaViolation, "additional"},
		{"no image field", `{"source":"status","image":"https://camo.example/x.png"}`, CodeSchemaViolation, "additional"},
		{"no producer timestamp", `{"source":"status","produced_at":"2020-01-01T00:00:00Z"}`, CodeSchemaViolation, "additional"},

		{"caption is bounded", `{"source":"status","caption":"` + strings.Repeat("x", 201) + `"}`, CodeSchemaViolation, "must be <= 200"},
		{"not JSON", `{`, CodeInvalidJSON, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ValidateEmbed([]byte(tc.raw))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if p.Schema() != SchemaEmbed {
					t.Fatalf("Schema() = %s", p.Schema())
				}
				return
			}
			if err == nil {
				t.Fatalf("expected %s, got a valid payload %+v", tc.wantErr, p)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %#v, want *ValidationError", err)
			}
			if ve.Code != tc.wantErr {
				t.Fatalf("code = %s, want %s (%v)", ve.Code, tc.wantErr, err)
			}
			if tc.detail != "" && !strings.Contains(strings.ToLower(err.Error()), tc.detail) {
				t.Fatalf("detail %q does not mention %q", err, tc.detail)
			}
		})
	}
}

func TestValidateEmbedNamesTheVocabularyItRefused(t *testing.T) {
	// A producer script prints this string and a human reads it. "unknown
	// source" with no list is a bug report we get instead of a fixed script.
	restore := SetEmbedPolicy(testEmbedPolicy(t))
	defer restore()

	_, err := ValidateEmbed([]byte(`{"source":"nope"}`))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"grafana-fleet", "status"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not list the declared source %q", err, want)
		}
	}
}

func TestValidateEmbedResolvesTheURLServerSide(t *testing.T) {
	// The resolved URL is what the client is eventually handed. It comes from
	// the operator's allow-list, never from the bytes the producer pushed.
	restore := SetEmbedPolicy(testEmbedPolicy(t))
	defer restore()

	p, err := ValidateEmbed([]byte(`{"source":"grafana-fleet","caption":"eu-west"}`))
	if err != nil {
		t.Fatalf("ValidateEmbed: %v", err)
	}
	src, ok := ResolveEmbedSource(p.Source)
	if !ok {
		t.Fatalf("ResolveEmbedSource(%q) not found", p.Source)
	}
	if src.URL != "https://grafana.example.com/d/abc/fleet" {
		t.Fatalf("URL = %q", src.URL)
	}
	if p.Caption != "eu-west" {
		t.Fatalf("Caption = %q", p.Caption)
	}
}

func TestValidateEmbedRefusesAnOversizedPayload(t *testing.T) {
	restore := SetEmbedPolicy(testEmbedPolicy(t))
	defer restore()

	raw := `{"source":"status","caption":"` + strings.Repeat("x", MaxPayloadBytes) + `"}`
	_, err := ValidateEmbed([]byte(raw))
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Code != CodeTooLarge {
		t.Fatalf("err = %#v, want %s", err, CodeTooLarge)
	}
}

func TestEmbedSandboxTokens(t *testing.T) {
	// The whole security argument for the panel rests on these three facts,
	// so they are asserted rather than described. `allow-same-origin` next to
	// `allow-scripts` would let the framed document reach up and REMOVE its
	// own sandbox attribute, which is the one mistake that turns this feature
	// into a cross-site scripting vector on our own origin.
	got := EmbedSandbox
	if got != "allow-scripts" {
		t.Fatalf("EmbedSandbox = %q; the minimum that renders anything is the maximum we grant", got)
	}
	for _, forbidden := range []string{
		"allow-same-origin",
		"allow-top-navigation",
		"allow-forms",
		"allow-popups",
		"allow-modals",
		"allow-downloads",
		"allow-pointer-lock",
		"allow-presentation",
		"allow-storage-access-by-user-activation",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("EmbedSandbox grants %s", forbidden)
		}
	}
}
