package access

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestPublicationOriginV2StrictProtocol(t *testing.T) {
	a := publicationIdentityFixture()
	raw, fp, err := CanonicalPublicationAuthority(a)
	if err != nil {
		t.Fatal(err)
	}
	if a.Schema != 2 {
		t.Fatal("origin protocol did not explicitly advance to schema2")
	}
	expected := sha256.Sum256(append([]byte("crewship.publication.authority.v2\x00"), raw...))
	if fp != hex.EncodeToString(expected[:]) {
		t.Fatal("schema2 did not use its independent v2 hash domain")
	}
	oldDomain := sha256.Sum256(append([]byte("crewship.publication.authority.v1\x00"), raw...))
	if _, err := DecodePublicationAuthority(raw, hex.EncodeToString(oldDomain[:])); !errors.Is(err, ErrDenied) {
		t.Fatal("old v1 domain accepted schema2 bytes")
	}
	tail := []byte(`"` + strings.Repeat("b", 64) + `",""]]`)
	oldTail := []byte(`"` + strings.Repeat("b", 64) + `"]]`)
	if !bytes.Contains(raw, tail) {
		t.Fatal("synthetic fixture does not contain expected empty origin item")
	}
	cases := map[string][]byte{
		"missing-root":         bytes.Replace(raw, []byte(`"root_origin":"",`), nil, 1),
		"null-root":            bytes.Replace(raw, []byte(`"root_origin":""`), []byte(`"root_origin":null`), 1),
		"old-seven-tuple":      bytes.Replace(raw, tail, oldTail, 1),
		"long-nine-tuple":      bytes.Replace(raw, tail, []byte(`"`+strings.Repeat("b", 64)+`","","extra"]]`), 1),
		"null-child-origin":    bytes.Replace(raw, tail, []byte(`"`+strings.Repeat("b", 64)+`",null]]`), 1),
		"numeric-child-origin": bytes.Replace(raw, tail, []byte(`"`+strings.Repeat("b", 64)+`",1]]`), 1),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			if bytes.Equal(bad, raw) {
				t.Fatal("malformed fixture did not change source bytes")
			}
			if _, err := DecodePublicationAuthority(bad, rawPublicationFingerprint(bad)); !errors.Is(err, ErrDenied) {
				t.Fatal("missing/null/malformed source representation accepted")
			}
		})
	}
	// Construct the exact old field/tuple encoding, not merely an unknown schema
	// marker with newly added fields. No origin-free compatibility decode exists.
	old := bytes.Replace(cases["missing-root"], tail, oldTail, 1)
	old = bytes.Replace(old, []byte(`"schema":2`), []byte(`"schema":1`), 1)
	oldHash := sha256.Sum256(append([]byte("crewship.publication.authority.v1\x00"), old...))
	if _, err := DecodePublicationAuthority(old, hex.EncodeToString(oldHash[:])); !errors.Is(err, ErrDenied) {
		t.Fatal("origin-free schema1 silently upgraded")
	}
	a.Schema = 1
	if _, _, err := CanonicalPublicationAuthority(a); !errors.Is(err, ErrDenied) {
		t.Fatal("encoder still admits obsolete schema1")
	}
}
