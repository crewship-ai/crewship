package pipeline

import (
	"errors"
	"strings"
	"testing"
)

// ODCH-RT-25: concurrency_policy says what a start does when the routine is
// still running its concurrency_key slot: "queue" (the default: wait or be
// refused as before) or "skip" (drop the start, quietly).

func policyDSL(policy, key string) *DSL {
	d := concurrencyProbeDSL()
	d.ConcurrencyKey = key
	d.ConcurrencyPolicy = policy
	return d
}

func TestValidate_ConcurrencyPolicy(t *testing.T) {
	cases := []struct {
		name    string
		policy  string
		key     string
		wantErr string
	}{
		{"empty is the default", "", "{{ inputs.account_id }}", ""},
		{"queue", "queue", "{{ inputs.account_id }}", ""},
		{"skip with a key", "skip", "{{ inputs.account_id }}", ""},
		{"skip with a literal key", "skip", "serial", ""},
		{"unknown value", "drop", "serial", "concurrency_policy"},
		{"skip needs a gate to skip on", "skip", "", "concurrency_key"},
		{"case matters", "Skip", "serial", "concurrency_policy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(policyDSL(tc.policy, tc.key), nil, nil)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want one mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestSkipsStartWhenBusy(t *testing.T) {
	skipDef := `{"name":"x","concurrency_key":"serial","concurrency_policy":"skip","steps":[]}`
	queueDef := `{"name":"x","concurrency_key":"serial","concurrency_policy":"queue","steps":[]}`
	plainDef := `{"name":"x","concurrency_key":"serial","steps":[]}`
	busy := ErrConcurrencyLimitReached
	cases := []struct {
		name string
		def  string
		err  error
		want bool
	}{
		{"skip + busy", skipDef, busy, true},
		{"skip + busy, wrapped", skipDef, errors.Join(errors.New("x"), busy), true},
		{"skip + another error", skipDef, errors.New("boom"), false},
		{"skip + no error", skipDef, nil, false},
		{"queue + busy", queueDef, busy, false},
		{"default + busy", plainDef, busy, false},
		{"unparseable definition + busy", `{`, busy, false},
		{"empty definition + busy", ``, busy, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SkipsStartWhenBusy(tc.def, tc.err); got != tc.want {
				t.Fatalf("SkipsStartWhenBusy = %v, want %v", got, tc.want)
			}
		})
	}
}
