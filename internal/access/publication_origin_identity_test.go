package access

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPublicationOriginFingerprintIdentity(t *testing.T) {
	for _, field := range []string{"root", "child"} {
		t.Run(field, func(t *testing.T) {
			seen := map[string]bool{}
			for _, origin := range []string{"", "origin-a", "origin-b"} {
				a := publicationIdentityFixture()
				if field == "root" {
					a.RootOriginID = origin
				} else {
					a.Recipes[0].VersionOriginID = origin
				}
				fp := identityHash(t, a)
				if seen[fp] {
					t.Fatal("different source origins collapsed despite identical content and authority bindings")
				}
				seen[fp] = true
			}
		})
	}
}

func TestPublicationOriginSameVersionCannotContradict(t *testing.T) {
	a := publicationIdentityFixture()
	a.Recipes[0].VersionOriginID = "origin-a"
	second := a.Recipes[0]
	second.StepID = "other-callsite"
	second.VersionOriginID = "origin-b"
	a.Recipes = append(a.Recipes, second)
	if _, _, err := CanonicalPublicationAuthority(a); !errors.Is(err, ErrDenied) {
		t.Fatal("same immutable version accepted with contradictory origins")
	}
}

func TestPublicationOriginRoundTripAndPrivacy(t *testing.T) {
	for _, root := range []string{"", "root-origin", "kořen"} {
		for _, child := range []string{"", "child-origin", "子"} {
			a := publicationIdentityFixture()
			a.RootOriginID = root
			a.Recipes[0].VersionOriginID = child
			raw, fp, err := CanonicalPublicationAuthority(a)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodePublicationAuthority(raw, fp)
			if err != nil || got.RootOriginID != root || len(got.Recipes) != 1 || got.Recipes[0].VersionOriginID != child {
				t.Fatalf("exact source origins lost in roundtrip: %v", err)
			}
			for _, v := range []any{got, got.Recipes[0]} {
				hidden, err := json.Marshal(v)
				if err != nil || string(hidden) != "{}" {
					t.Fatal("source origin leaked through generic DTO JSON")
				}
			}
		}
	}
}

func TestPublicationOriginRejectsMalformedIdentifiers(t *testing.T) {
	for _, bad := range []string{" bad", "bad\n", string([]byte{0x80}), string([]byte{0x81}), strings.Repeat("x", 257)} {
		for _, field := range []string{"root", "child"} {
			a := publicationIdentityFixture()
			if field == "root" {
				a.RootOriginID = bad
			} else {
				a.Recipes[0].VersionOriginID = bad
			}
			if _, _, err := CanonicalPublicationAuthority(a); !errors.Is(err, ErrDenied) {
				t.Fatal("malformed source origin accepted")
			}
		}
	}
}
