package designsystem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestCanonicalSnapshotAndLiteralValues(t *testing.T) {
	a, err := Parse([]byte(`{"schema_version":1,"colors":{"surface":"#AAbbCC","ink":"#010203"},"spacing":{"large":24,"none":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse([]byte(` {"spacing":{"none":0,"large":24},"colors":{"ink":"#010203","surface":"#aabbcc"},"schema_version":1} `))
	if err != nil {
		t.Fatal(err)
	}
	// Distinct source blobs may deliberately have the same semantic digest.
	rawA := sha256.Sum256([]byte(`{"schema_version":1,"colors":{"ink":"#AABBCC"},"spacing":{}}`))
	rawB := sha256.Sum256([]byte(`{"spacing":{},"colors":{"ink":"#aabbcc"},"schema_version":1}`))
	if rawA == rawB {
		t.Fatal("fixture raw file hashes should differ")
	}
	sourceA, err := Parse([]byte(`{"schema_version":1,"colors":{"ink":"#AABBCC"},"spacing":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	sourceB, err := Parse([]byte(`{"spacing":{},"colors":{"ink":"#aabbcc"},"schema_version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if sourceA.SHA256() != sourceB.SHA256() {
		t.Fatal("equivalent literals have different canonical hashes")
	}
	want := `{"schema_version":1,"colors":{"ink":"#010203","surface":"#aabbcc"},"spacing":{"large":24,"none":0}}`
	if string(a.CanonicalJSON()) != want || a.SHA256() != b.SHA256() {
		t.Fatalf("noncanonical snapshots: %s / %s", a.CanonicalJSON(), b.CanonicalJSON())
	}
	sum := sha256.Sum256([]byte(want))
	if a.SHA256() != hex.EncodeToString(sum[:]) {
		t.Fatal("digest is not over canonical bytes")
	}
	colors := a.Colors()
	colors["ink"] = "url(https://example.com)"
	delete(colors, "surface")
	spacing := a.Spacing()
	spacing["large"] = -1
	raw := a.CanonicalJSON()
	raw[0] = '!'
	if a.Colors()["ink"] != "#010203" || a.Spacing()["large"] != 24 || string(a.CanonicalJSON()) != want {
		t.Fatal("snapshot mutated through returned data")
	}
	c, err := Parse([]byte(strings.Replace(want, `"large":24`, `"large":25`, 1)))
	if err != nil || c.SHA256() == a.SHA256() {
		t.Fatal("changed literal did not change digest")
	}
}

func TestRejectUnsupportedAndAmbiguousTokens(t *testing.T) {
	cases := []string{
		`null`, `[]`, `{}`, `{"schema_version":1,"colors":{},"spacing":{},"fonts":{}}`,
		`{"Schema_version":1,"colors":{},"spacing":{}}`,
		`{"schema_version":1,"schema_version":1,"colors":{},"spacing":{}}`,
		`{"schema_version":1,"colors":{"ink":"#000000","\u0069nk":"#ffffff"},"spacing":{}}`,
		`{"schema_version":1,"colors":{},"spacing":{"gap":1,"gap":2}}`,
		`{"schema_version":1,"colors":null,"spacing":{}}`,
		`{"schema_version":1,"colors":{},"spacing":null}`,
		`{"schema_version":1.0,"colors":{},"spacing":{}}`,
		`{"schema_version":2,"colors":{},"spacing":{}}`,
		`{"schema_version":1,"colors":{"ink":"#fff"},"spacing":{}}`,
		`{"schema_version":1,"colors":{"ink":"url(https://example.com)"},"spacing":{}}`,
		`{"schema_version":1,"colors":{"ink":"{colors.other}"},"spacing":{}}`,
		`{"schema_version":1,"colors":{"ink":{"value":"#000000"}},"spacing":{}}`,
		`{"schema_version":1,"colors":{"--ink":"#000000"},"spacing":{}}`,
		`{"schema_version":1,"colors":{"constructor":"#000000"},"spacing":{}}`,
		`{"schema_version":1,"colors":{"prototype":"#000000"},"spacing":{}}`,
		`{"schema_version":1,"colors":{"bad name":"#000000"},"spacing":{}}`,
		`{"schema_version":1,"colors":{"x; color:red":"#000000"},"spacing":{}}`,
		`{"schema_version":1,"colors":{},"spacing":{"gap":-1}}`,
		`{"schema_version":1,"colors":{},"spacing":{"gap":-0}}`,
		`{"schema_version":1,"colors":{},"spacing":{"gap":1.5}}`,
		`{"schema_version":1,"colors":{},"spacing":{"gap":1e2}}`,
		`{"schema_version":1,"colors":{},"spacing":{"gap":"24px"}}`,
		`{"schema_version":1,"colors":{},"spacing":{"gap":"expression(1)"}}`,
		`{"schema_version":1,"colors":{},"spacing":{"gap":9007199254740993}}`,
		`{"schema_version":1,"colors":{},"spacing":{}} {}`,
		`{"schema_version":1,"colors":{},"spacing":{}} trailing`,
		"{\"schema_version\":1,\"colors\":{\"ink\":\"#00000\xff\"},\"spacing\":{}}",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if value, err := Parse([]byte(input)); err == nil || value != nil {
				t.Fatalf("accepted invalid input: %q", input)
			}
		})
	}
}

func TestProfileLimits(t *testing.T) {
	for _, kind := range []string{"colors", "spacing"} {
		for _, count := range []int{MaxTokensPerGroup, MaxTokensPerGroup + 1} {
			var entries []string
			for i := 0; i < count; i++ {
				value := `"#123456"`
				if kind == "spacing" {
					value = "0"
				}
				entries = append(entries, fmt.Sprintf(`"token-%d":%s`, i, value))
			}
			input := `{"schema_version":1,"colors":{},"spacing":{}}`
			input = strings.Replace(input, `"`+kind+`":{}`, `"`+kind+`":{`+strings.Join(entries, ",")+`}`, 1)
			_, err := Parse([]byte(input))
			if (err == nil) != (count == MaxTokensPerGroup) {
				t.Fatalf("%s count %d: %v", kind, count, err)
			}
		}
	}
	for _, px := range []int{MaxSpacingPX, MaxSpacingPX + 1} {
		_, err := Parse([]byte(fmt.Sprintf(`{"schema_version":1,"colors":{},"spacing":{"gap":%d}}`, px)))
		if (err == nil) != (px == MaxSpacingPX) {
			t.Fatalf("px %d: %v", px, err)
		}
	}
	for _, length := range []int{MaxNameBytes, MaxNameBytes + 1} {
		_, err := Parse([]byte(fmt.Sprintf(`{"schema_version":1,"colors":{"%s":"#000000"},"spacing":{}}`, strings.Repeat("a", length))))
		if (err == nil) != (length == MaxNameBytes) {
			t.Fatalf("name length %d: %v", length, err)
		}
	}
	base := `{"schema_version":1,"colors":{},"spacing":{}}`
	for _, size := range []int{MaxProfileBytes, MaxProfileBytes + 1} {
		_, err := Parse([]byte(base + strings.Repeat(" ", size-len(base))))
		if (err == nil) != (size == MaxProfileBytes) {
			t.Fatalf("bytes %d: %v", size, err)
		}
	}
}

func FuzzParseTokens(f *testing.F) {
	f.Add([]byte(`{"schema_version":1,"colors":{"ink":"#010203"},"spacing":{"gap":12}}`))
	f.Add([]byte(`{"schema_version":1,"colors":{},"spacing":{}}`))
	f.Add([]byte(`{"schema_version":1,"colors":{"x":"#ffffff","x":"#000000"},"spacing":{}}`))
	f.Fuzz(func(t *testing.T, source []byte) {
		snapshot, err := Parse(source)
		if err != nil {
			if snapshot != nil {
				t.Fatal("invalid source returned snapshot")
			}
			return
		}
		again, err := Parse(snapshot.CanonicalJSON())
		if err != nil || again.SHA256() != snapshot.SHA256() {
			t.Fatal("canonical snapshot is not stable")
		}
		for _, value := range snapshot.Colors() {
			if !validColor(value) {
				t.Fatal("nonliteral color escaped validation")
			}
		}
		for _, value := range snapshot.Spacing() {
			if value < 0 || value > MaxSpacingPX {
				t.Fatal("spacing escaped bounds")
			}
		}
	})
}
