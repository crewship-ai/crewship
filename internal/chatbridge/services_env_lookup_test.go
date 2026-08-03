package chatbridge

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// ---------- buildServiceEnvLookup ----------

func TestBuildServiceEnvLookup(t *testing.T) {
	t.Parallel()
	lookup := buildServiceEnvLookup([]orchestrator.Credential{
		{EnvVarName: "API_KEY", PlainValue: "k1"},
		{EnvVarName: "PENDING_CRED", PlainValue: ""}, // status=PENDING → empty value
	})
	if got := lookup("API_KEY"); got != "k1" {
		t.Errorf("lookup(API_KEY) = %q, want k1", got)
	}
	if got := lookup("PENDING_CRED"); got != "" {
		t.Errorf("lookup(PENDING_CRED) = %q, want empty", got)
	}
	if got := lookup("UNKNOWN"); got != "" {
		t.Errorf("lookup(UNKNOWN) = %q, want empty", got)
	}
}

func TestBuildServiceEnvLookupEmptyCreds(t *testing.T) {
	t.Parallel()
	lookup := buildServiceEnvLookup(nil)
	if got := lookup("ANYTHING"); got != "" {
		t.Errorf("lookup on empty creds = %q, want empty", got)
	}
}
