package pipeline

import (
	"reflect"
	"testing"
)

func TestReferencedStepOutputs(t *testing.T) {
	cases := []struct {
		name     string
		template string
		want     []string
	}{
		{"none", "Summarize {{ inputs.text }} plainly.", nil},
		{"single output", "Verify {{ steps.parse.output }}.", []string{"parse"}},
		{"output json path", "Total is {{ steps.parse.output.total }}.", []string{"parse"}},
		{
			"multiple distinct, deduped, in order",
			"Reconcile {{ steps.parse.output }} against {{ steps.verify.output }} and {{ steps.parse.output.n }}.",
			[]string{"parse", "verify"},
		},
		{"ignores non-steps refs", "{{ inputs.x }} {{ env.run_id }} {{ run.metadata.k }}", nil},
		{"ignores steps ref without output segment", "{{ steps.parse }}", nil},
		{"whitespace tolerant", "{{steps.a.output}} {{  steps.b.output  }}", []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReferencedStepOutputs(tc.template)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ReferencedStepOutputs(%q) = %v, want %v", tc.template, got, tc.want)
			}
		})
	}
}
