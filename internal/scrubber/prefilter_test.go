package scrubber

import (
	"encoding/base64"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func checkPrefilterScrubber(t testing.TB, s *Scrubber, text string) {
	t.Helper()
	if a, b := s.Scrub(text), s.legacyPrefilterScrub(text); a != b {
		t.Fatalf("scrub differs input%q got%q want%q", text, a, b)
	}
	if s.ContainsSecret(text) != s.legacyPrefilterContains(text) || !reflect.DeepEqual(s.matchBounds(text), s.legacyPrefilterBounds(text)) {
		t.Fatalf("detection/bounds differ input%q", text)
	}
}
func TestPrefilterScrubberAllProfilesAndCustomRules(t *testing.T) {
	s := New()
	s.AddSecretValues("owned-known-credential", string([]byte{'o', 'w', 'n', 'e', 'd', 255, 's', 'e', 'c', 'r', 'e', 't'}))
	for _, p := range []struct{ name, regex string }{{"openai_key", `zzcustom`}, {"", `zzgeneric`}, {"$x", `(?P<x>zza)|(?P<x>zzb)`}, {"${1}", `zz(capture)`}, {"empty", `x*`}, {"rune", `\x{fffd}`}} {
		if e := s.AddPattern(p.name, p.regex); e != nil {
			t.Fatal(e)
		}
	}
	for _, v := range []string{"", "benign metadata token secret bearer", "API_KEY=abc123xyz", "API_KEY=abc123xyz", "ſecret=abcdefgh", "Bearer abcdefghijklmnopqrstuvwxyz", "sk-ant-" + strings.Repeat("a", 50), `{"apiKey":"abcdefgh"}`, "sk-\u200b" + strings.Repeat("b", 30), "zzcustom zza zzb zzcapture zzgeneric", "owned-known-credential", base64.StdEncoding.EncodeToString([]byte("owned-known-credential")), "\xff\xfe\xe2\x82 owned\xffsecret"} { // gitleaks:allow -- owned synthetic regression fixture, not a credential
		checkPrefilterScrubber(t, s, v)
	}
}
func TestPrefilterSecretRotationAndEveryStreamSplit(t *testing.T) {
	oldValue, newValue := "owned-old-credential-00001", "owned-new-credential-00002"
	for _, secret := range []string{oldValue, newValue, strings.Repeat("long-owned-value-", 18)} {
		for _, encoded := range []string{secret, base64.StdEncoding.EncodeToString([]byte(secret))} {
			text := strings.Repeat("p", 129) + encoded + "\u200b tail"
			for split := 0; split <= len(text); split++ {
				actual, reference := NewStreamScrubber(New()), newLegacyPrefilterStream(New())
				actual.AddSecretValues(secret)
				reference.AddSecretValues(secret)
				got := actual.Write(text[:split]) + actual.Write(text[split:]) + actual.Flush()
				want := reference.Write(text[:split]) + reference.Write(text[split:]) + reference.Flush()
				if got != want {
					t.Fatalf("stream split%d length%d differs", split, len(text))
				}
			}
		}
	}
	// The product API is append-only: rotation/revocation replaces the scoped
	// scrubber instance rather than pretending AddSecretValues removes a value.
	old, next := New(), New()
	old.AddSecretValues(oldValue)
	next.AddSecretValues(newValue)
	for _, v := range []string{oldValue, newValue, oldValue + " " + newValue} {
		checkPrefilterScrubber(t, old, v)
		checkPrefilterScrubber(t, next, v)
	}
	if next.ContainsSecret(oldValue) {
		t.Fatal("new scope retained revoked value")
	}
}
func TestPrefilterConcurrentRegistration(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = s.Scrub("benign metadata")
				_ = s.ContainsSecret("ſecret=abcdefgh")
				_ = s.matchBounds("owned")
			}
		}()
	}
	for range 40 {
		if e := s.AddPattern("same-name", `owned`); e != nil {
			t.Fatal(e)
		}
	}
	wg.Wait()
}
func FuzzPrefilterScrubberMatchesReference(f *testing.F) {
	for _, s := range []string{"", "ſecret=abcdefgh", "API_KEY=abcdefgh", "\xff\xe2\x82", "sk-" + strings.Repeat("a", 30), "owned-known-credential"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 8192 {
			// Bound each fuzz workload while still testing its byte prefix.
			text = text[:8192]
		}
		s := New()
		s.AddSecretValues("owned-known-credential")
		if e := s.AddPattern("openai_key", `zzcustom`); e != nil {
			t.Fatal(e)
		}
		checkPrefilterScrubber(t, s, text)
	})
}
func BenchmarkPrefilterScrubber(b *testing.B) {
	for _, n := range []int{100, 1024, 64 << 10, 1 << 20} {
		for _, p := range []struct{ name, text string }{{"clean", "ordinary completed operation\n"}, {"keywords", "token metadata secret bearer description\n"}, {"matched", "SECRET=ownedcredentialvalue sk-" + strings.Repeat("x", 30) + "\n"}} {
			b.Run(p.name+"/"+sizeName(n), func(b *testing.B) {
				s := New()
				s.AddSecretValues("owned-known-credential")
				text := strings.Repeat(p.text, n/len(p.text)+1)[:n]
				b.SetBytes(int64(n))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					_ = s.Scrub(text)
				}
			})
		}
	}
}
func sizeName(n int) string {
	switch n {
	case 100:
		return "100B"
	case 1024:
		return "1KiB"
	case 64 << 10:
		return "64KiB"
	default:
		return "1MiB"
	}
}

func TestPrefilterEveryBuiltinHasPositiveWitness(t *testing.T) {
	witnesses := []string{
		"-----BEGIN OPENSSH PRIVATE KEY-----\nowned-synthetic-not-a-key\n-----END OPENSSH PRIVATE KEY-----", // gitleaks:allow -- owned synthetic regression fixture, not a credential
		"-----BEGIN RSA PRIVATE KEY-----\nowned-synthetic-not-a-key\n-----END RSA PRIVATE KEY-----",         // gitleaks:allow -- owned synthetic regression fixture, not a credential
		"sk-ant-" + strings.Repeat("x", 30), "sk-or-" + strings.Repeat("x", 30), "sk-proj-" + strings.Repeat("x", 30),
		"AIzaSy" + strings.Repeat("x", 33), "cur_" + strings.Repeat("x", 30), "factory_" + strings.Repeat("x", 30),
		"xai-" + strings.Repeat("x", 30), "gsk_" + strings.Repeat("x", 30), "github_pat_" + strings.Repeat("x", 30),
		"glpat-" + strings.Repeat("x", 30), "xoxb-" + strings.Repeat("x", 30), "AKIA" + strings.Repeat("X", 16),
		"Bearer " + strings.Repeat("x", 30), `"apiKey":"owned-synthetic-value"`, "API_KEY=owned-synthetic-value",
	}
	if len(witnesses) != len(builtinPatterns) {
		t.Fatal("update witnesses for changed builtin profile")
	}
	for i, text := range witnesses {
		t.Run(builtinPatterns[i].name, func(t *testing.T) {
			if !builtinPatterns[i].re.MatchString(text) {
				t.Fatal("positive witness does not match its rule")
			}
			checkPrefilterScrubber(t, New(), text)
		})
	}
}
