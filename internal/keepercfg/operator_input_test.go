package keepercfg

import "testing"

func TestParseTriBoolOperatorAliases(t *testing.T) {
	for _, tc := range []struct {
		inputs []string
		want   TriBool
		valid  bool
	}{
		{[]string{"on", " TRUE ", "1", "yes", "enable", "enabled"}, TriOn, true},
		{[]string{"off", " FALSE ", "0", "no", "disable", "disabled"}, TriOff, true},
		{[]string{"inherit", " DEFAULT ", "unset", "", "  "}, TriInherit, true},
		{[]string{"maybe", "2", "inherited", "enable now"}, "", false},
	} {
		for _, input := range tc.inputs {
			t.Run(input, func(t *testing.T) {
				got, valid := ParseTriBool(input)
				if got != tc.want || valid != tc.valid {
					t.Fatalf("ParseTriBool(%q) = %q, %v; want %q, %v", input, got, valid, tc.want, tc.valid)
				}
			})
		}
	}
}
