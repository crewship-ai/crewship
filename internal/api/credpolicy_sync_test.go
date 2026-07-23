package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/credpolicy"
)

// The credpolicy delivery table must stay in lockstep with the credential type
// enum: every valid type needs an explicit policy row so its Keeper-gating and
// delivery posture is a deliberate decision, not the fail-safe fallback. If a
// new type is added to validCredentialTypes without a credpolicy row, this
// fails — forcing the author to classify it. (credpolicy can't import api, so
// the sync check lives here.)
func TestCredPolicy_CoversEveryCredentialType(t *testing.T) {
	for ty := range validCredentialTypes {
		if !credpolicy.Known(ty) {
			t.Errorf("credential type %q has no credpolicy row — add one in internal/credpolicy", ty)
		}
	}
}
