package sidecar

import "testing"

// BenchmarkCredStoreSelectMixed exercises the priority-tier filter in Select
// with a realistic pool: 3 providers × 2 priority tiers × 3 creds = 18 creds,
// of which 3 are in the top tier for the benched provider.
func BenchmarkCredStoreSelectMixed(b *testing.B) {
	cs := NewCredStore()
	var creds []Credential
	for _, p := range []ProviderType{ProviderAnthropic, ProviderOpenAI, ProviderGoogle} {
		for _, prio := range []int{0, 5} {
			for i := 0; i < 3; i++ {
				creds = append(creds, Credential{
					ID:       string(p) + "-" + string(rune('a'+i)) + "-p" + string(rune('0'+prio)),
					Provider: p,
					Token:    "tok",
					Priority: prio,
				})
			}
		}
	}
	cs.Load(creds)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.Select(ProviderAnthropic)
	}
}
