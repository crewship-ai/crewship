package manifest

import (
	"strings"
	"testing"
)

func TestKnownKindsMatchesParserCatalogue(t *testing.T) {
	got := strings.Join(KnownKinds(), ", ")
	if got != knownKindList {
		t.Fatalf("KnownKinds drifted from parser catalogue:\n got: %s\nwant: %s", got, knownKindList)
	}
}
