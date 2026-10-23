package lookout

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Frozen pre-optimization algorithm: keep priority, reverse finding order,
// and positions relative to each rule's current intermediate text.
func legacyRedactForParity(text string) (string, []Finding) {
	if text == "" {
		return text, nil
	}
	findings := make([]Finding, 0)
	out := text
	for _, r := range secretRules {
		locs := r.pattern.FindAllStringIndex(out, -1)
		for i := len(locs) - 1; i >= 0; i-- {
			loc := locs[i]
			findings = append(findings, Finding{Kind: r.kind, Severity: r.severity, Detail: r.detail, Matched: string(r.kind), Position: loc[0]})
			out = out[:loc[0]] + fmt.Sprintf("***REDACTED:%s***", r.kind) + out[loc[1]:]
		}
	}
	return out, findings
}

func checkRedactParity(t testing.TB, text string) {
	t.Helper()
	want, wf := legacyRedactForParity(text)
	got, gf := Redact(text)
	if got != want || !reflect.DeepEqual(gf, wf) {
		t.Fatalf("redaction differs for %q: got %q %#v; want %q %#v", text, got, gf, want, wf)
	}
}

func TestRedactPriorityOffsetsAndEmptyParity(t *testing.T) {
	cases := []string{"", "plain text", "\xff\xfe ſ K café", "sk-" + strings.Repeat("a", 24), "sk-ant-" + strings.Repeat("a", 44), "prefix ghp_" + strings.Repeat("b", 36) + " then AKIA1234567890ABCDEF", "password='long password' api_key=abcdefghijklmnop", "BEARER abcdefghijklmnopqrstuvwxyz", "a\x00\xffsk-" + strings.Repeat("b", 24) + "\xffz"}
	cases = append(cases, strings.Repeat("x sk-"+strings.Repeat("a", 24)+" ", 12))
	for _, s := range cases {
		checkRedactParity(t, s)
	}
	// Test-local synthetic rules expose sequential replacement interactions,
	// captureGroup's existing whole-match behavior, and empty match handling.
	original := secretRules
	t.Cleanup(func() { secretRules = original })
	secretRules = []secretRule{
		{pattern: regexp.MustCompile(`(ab)`), kind: KindSecretOpenAI, severity: SeverityCritical, detail: "first", captureGroup: 1},
		{pattern: regexp.MustCompile(`REDACTED`), kind: KindSecretAWS, severity: SeverityHigh, detail: "marker"},
		{pattern: regexp.MustCompile(`x*`), kind: KindSecretBearer, severity: SeverityHigh, detail: "empty"},
	}
	for _, s := range []string{"", "ab ab", "xabxxabz", "é\xff"} {
		checkRedactParity(t, s)
	}
}

func FuzzRedactMatchesLegacy(f *testing.F) {
	for _, s := range []string{"", "plain", "sk-" + strings.Repeat("a", 24), "password='abcdefgh'", "\xffKſ"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 8192 {
			t.Skip()
		}
		checkRedactParity(t, text)
	})
}

func BenchmarkRedactRepeatedSecrets(b *testing.B) {
	text := strings.Repeat("sk-"+strings.Repeat("a", 24)+"\n", 1000)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for range b.N {
		out, findings := Redact(text)
		if len(findings) != 1000 || len(out) == 0 {
			b.Fatal("unexpected fixture result")
		}
	}
}

func BenchmarkRedactCleanText(b *testing.B) {
	text := strings.Repeat("owned harmless application log line\n", 1024)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for range b.N {
		out, findings := Redact(text)
		if out != text || len(findings) != 0 {
			b.Fatal("invalid clean fixture")
		}
	}
}
