package paymaster

import "testing"

func TestOpenCodeRatesKeepBillingProductsSeparate(t *testing.T) {
	zen, source := ExplainRate("opencode", "claude-sonnet-5")
	if source != SourceCatalog || zen.InputPerM <= 0 || zen.OutputPerM <= 0 {
		t.Fatalf("Zen has no independent price: %+v %s", zen, source)
	}
	goRate, source := ExplainRate("opencode-go", "kimi-k3")
	if source != SourceNone || goRate != (modelPrice{}) {
		t.Fatal("Go subscription marginal/overage billing cannot be inferred from model tokens")
	}
}
