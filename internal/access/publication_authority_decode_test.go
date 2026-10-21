package access

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPublicationDecodeRoundTrip(t *testing.T) {
	for _, author := range []string{"", "declared-author-different-from-leaf", "作者"} {
		t.Run(author, func(t *testing.T) {
			a := publicationIdentityFixture()
			a.AuthorAgentID = author
			a.Agents[0].GrantExpiry = "2000-01-01T00:00:00Z" // Identity is not current authorization.
			raw, fp, err := CanonicalPublicationAuthority(a)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodePublicationAuthority(raw, fp)
			if err != nil {
				t.Fatalf("canonical round trip rejected: %v", err)
			}
			again, actualFP, err := CanonicalPublicationAuthority(got)
			if err != nil || !bytes.Equal(raw, again) || actualFP != fp || got.AuthorAgentID != author {
				t.Fatal("identity did not survive decoding")
			}
			for _, v := range []any{got, got.Execution[0], got.Recipes[0], got.Agents[0]} {
				hidden, err := json.Marshal(v)
				if err != nil || string(hidden) != "{}" {
					t.Fatal("decoded host DTO leaked through generic JSON")
				}
			}
		})
	}
}

func rawPublicationFingerprint(raw []byte) string {
	sum := sha256.Sum256(append([]byte("crewship.publication.authority.v2\x00"), raw...))
	return hex.EncodeToString(sum[:])
}

func TestPublicationDecodeRejectsMalformedIdentity(t *testing.T) {
	raw, fp, err := CanonicalPublicationAuthority(publicationIdentityFixture())
	if err != nil {
		t.Fatal(err)
	}
	change := func(fn func(map[string]any)) []byte {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatal(err)
		}
		fn(obj)
		// Preserve root field ordering so malformed cases do not rely on map ordering.
		var ordered map[string]json.RawMessage
		encoded, _ := json.Marshal(obj)
		_ = json.Unmarshal(encoded, &ordered)
		var original map[string]json.RawMessage
		_ = json.Unmarshal(raw, &original)
		out := append([]byte(nil), raw...)
		for key, old := range original {
			replacement := ordered[key]
			if !bytes.Equal(old, replacement) {
				out = bytes.Replace(out, append([]byte(`"`+key+`":`), old...), append([]byte(`"`+key+`":`), replacement...), 1)
			}
		}
		return out
	}
	cases := map[string][]byte{
		"unknown":              bytes.Replace(raw, []byte(`"schema":2`), []byte(`"schema":2,"unknown":true`), 1),
		"duplicate":            bytes.Replace(raw, []byte(`"schema":2`), []byte(`"schema":2,"schema":2`), 1),
		"trailing":             append(append([]byte(nil), raw...), []byte(` {}`)...),
		"whitespace":           append([]byte(" "), raw...),
		"schema":               bytes.Replace(raw, []byte(`"schema":2`), []byte(`"schema":3`), 1),
		"missing header":       bytes.Replace(raw, []byte(`"author_agent":"",`), nil, 1),
		"null array":           bytes.Replace(raw, []byte(`"authoring":[]`), []byte(`"authoring":null`), 1),
		"wrong workspace":      change(func(o map[string]any) { o["workspace"] = "other" }),
		"grant extra":          change(func(o map[string]any) { a := o["execution"].([]any); a[0] = append(a[0].([]any), "extra") }),
		"grant short":          change(func(o map[string]any) { a := o["execution"].([]any); a[0] = a[0].([]any)[:8] }),
		"recipe extra":         change(func(o map[string]any) { a := o["recipes"].([]any); a[0] = append(a[0].([]any), "extra") }),
		"recipe short":         change(func(o map[string]any) { a := o["recipes"].([]any); a[0] = a[0].([]any)[:6] }),
		"agent extra":          change(func(o map[string]any) { a := o["agents"].([]any); a[0] = append(a[0].([]any), "extra") }),
		"agent short":          change(func(o map[string]any) { a := o["agents"].([]any); a[0] = a[0].([]any)[:12] }),
		"integer overflow":     change(func(o map[string]any) { o["recipes"].([]any)[0].([]any)[4] = strings.Repeat("9", 100) }),
		"token overflow":       change(func(o map[string]any) { o["agents"].([]any)[0].([]any)[12] = strings.Repeat("9", 100) }),
		"noncanonical integer": change(func(o map[string]any) { o["recipes"].([]any)[0].([]any)[4] = "01" }),
		"noncanonical expiry":  change(func(o map[string]any) { o["agents"].([]any)[0].([]any)[10] = "2000-01-01T01:00:00+01:00" }),
		"invalid utf8":         bytes.Replace(raw, []byte(`"root"`), []byte{'"', 0xff, '"'}, 1),
		"null tuple field":     bytes.Replace(raw, []byte(`"gpt-test"`), []byte(`null`), 1),
		"reordered keys":       bytes.Replace(raw, []byte(`"schema":2,"workspace":"w"`), []byte(`"workspace":"w","schema":2`), 1),
		"noncanonical escape":  bytes.Replace(raw, []byte(`"root"`), []byte(`"\u0072oot"`), 1),
		"tuple number":         bytes.Replace(raw, []byte(`"4096"`), []byte(`4096`), 1),
		"size":                 bytes.Repeat([]byte(" "), (256<<10)+1),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			// A matching hash of malformed input is insufficient: validate actual bytes/shape.
			got, err := DecodePublicationAuthority(bad, rawPublicationFingerprint(bad))
			if !errors.Is(err, ErrDenied) || got.Schema != 0 || got.WorkspaceID != "" {
				t.Fatalf("malformed identity accepted: %v", err)
			}
		})
	}
	for _, badFP := range []string{"", strings.ToUpper(fp), strings.Repeat("a", 64), LegacyPublicationAuthorityFingerprint} {
		if _, err := DecodePublicationAuthority(raw, badFP); !errors.Is(err, ErrDenied) {
			t.Fatal("invalid expected fingerprint accepted")
		}
	}
	tampered := bytes.Replace(raw, []byte(`"root"`), []byte(`"different-root"`), 1)
	if _, err := DecodePublicationAuthority(tampered, fp); !errors.Is(err, ErrDenied) {
		t.Fatal("tamper accepted against original fingerprint")
	}
}
