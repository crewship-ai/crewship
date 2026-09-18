package paymaster

import "testing"

func TestZAICodingPlanRateStaysUnpriced(t *testing.T) {
	rate, source := ExplainRate("zai-coding-plan", "glm-5.3")
	if source != SourceNone || rate != (modelPrice{}) {
		t.Fatal("Coding Plan subscription limits cannot be priced from model tokens; inventing a rate would misreport billed usage")
	}
}
