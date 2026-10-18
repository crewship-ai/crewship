package access

import (
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
