package mooring

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type profileSegment struct {
	Text   string `json:"text"`
	Repeat int    `json:"repeat"`
}
type profileCase struct {
	ID             string            `json:"id"`
	Segments       []json.RawMessage `json:"segments"`
	Hex            string            `json:"utf8_hex"`
	Value          string            `json:"value"`
	Valid          bool              `json:"valid"`
	RawBytes       *int              `json:"raw_bytes"`
	CanonicalBytes *int              `json:"canonical_bytes"`
}

func expandProfile(t *testing.T, c profileCase) []byte {
	t.Helper()
	if c.Hex != "" {
		b, err := hex.DecodeString(c.Hex)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var b strings.Builder
	for _, s := range c.Segments {
		var text string
		if json.Unmarshal(s, &text) == nil {
			b.WriteString(text)
			continue
		}
		var repeat profileSegment
		if err := json.Unmarshal(s, &repeat); err != nil {
			t.Fatal(err)
		}
		if repeat.Repeat < 0 || repeat.Repeat > 262145 {
			t.Fatal("fixture repeat outside bound")
		}
		b.WriteString(strings.Repeat(repeat.Text, repeat.Repeat))
	}
	return []byte(b.String())
}
func TestSharedBoundedWireProfile(t *testing.T) {
	var fixtures struct {
		Resource    []profileCase `json:"resource_cases"`
		Identifiers []profileCase `json:"identifier_cases"`
		Versions    []profileCase `json:"version_cases"`
	}
	raw, err := os.ReadFile("../../mooring/protocol/wire-profile-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures.Resource) != 14 || len(fixtures.Identifiers) != 9 || len(fixtures.Versions) != 8 {
		t.Fatal("unexpected shared coverage")
	}
	for _, c := range fixtures.Resource {
		t.Run(c.ID, func(t *testing.T) {
			raw := expandProfile(t, c)
			if c.RawBytes != nil && len(raw) != *c.RawBytes {
				t.Fatalf("raw bytes=%d want%d", len(raw), *c.RawBytes)
			}
			canonical, err := CanonicalE3WireJSON(raw)
			if (err == nil) != c.Valid {
				t.Fatalf("valid=%v want%v", err == nil, c.Valid)
			}
			if c.CanonicalBytes != nil {
				full, err := CanonicalJSON(raw)
				if err != nil || len(full) != *c.CanonicalBytes {
					t.Fatalf("canonical size=%d error=%v want%d", len(full), err, *c.CanonicalBytes)
				}
			}
			if err == nil && len(canonical) > MaxE3WireBytes {
				t.Fatal("oversized result")
			}
		})
	}
	for _, c := range fixtures.Identifiers {
		t.Run(c.ID, func(t *testing.T) {
			if got := ValidWireIdentifier(string(expandProfile(t, c))); got != c.Valid {
				t.Fatalf("valid=%v want%v", got, c.Valid)
			}
		})
	}
	for _, c := range fixtures.Versions {
		t.Run(c.Value, func(t *testing.T) {
			if got := ValidWireVersion(c.Value); got != c.Valid {
				t.Fatalf("valid=%v want%v", got, c.Valid)
			}
		})
	}
}
func TestWireIdentifierRejectsInvalidUTF8(t *testing.T) {
	if ValidWireIdentifier(string([]byte{0xc3, 0x28})) {
		t.Fatal("invalid UTF-8 accepted")
	}
	// This is valid JSON application content, not a schema Identifier field.
	if _, err := CanonicalE3WireJSON([]byte(`{"payload":{"op_id":"\u0000"}}`)); err != nil {
		t.Fatal(err)
	}
}

// Exercise persistence admission, not merely the scalar-count helper: the old
// byte-based owner check rejected this schema-valid runtime identity.
func TestOwnerUnicodeScalarIdentityPersists(t *testing.T) {
	store, db, _, key := ownerFixture(t)
	owner := strings.Repeat("😀", 200)
	lease, err := store.Acquire(t.Context(), key, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOwnerTx(t, store, db.DB, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acquire(t.Context(), key, owner+"😀"); err == nil {
		t.Fatal("201 scalars admitted")
	}
}
