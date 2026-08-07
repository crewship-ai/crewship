package providertest

import "testing"

// The suite must pass against an implementation that honours every contract.
// If this goes red, a contract is over-specified (asserting an accident of one
// provider rather than a documented semantic) — fix the contract, not the
// provider that tripped it.
func TestContractSuite_ConformingFakeProviderPasses(t *testing.T) {
	RunContractSuite(t, func(t *testing.T) Runtime {
		return NewFakeRuntime(t, NewFakeProvider())
	})
}

// The live tier drops the fake-only contracts. Running it against the fake is
// not a live test; it proves the tier split itself works, i.e. that a Runtime
// with no observation hooks still gets the interface-level contracts asserted
// rather than silently skipping everything.
func TestLiveContractSuite_RunsWithoutObservationHooks(t *testing.T) {
	RunLiveContractSuite(t, func(t *testing.T) Runtime {
		p := NewFakeProvider()
		rt := NewFakeRuntime(t, p)
		// Strip the fake-runtime observations, as a live harness would.
		rt.AttachedStdin = nil
		rt.ExecUser = nil
		rt.RuntimeCalls = nil
		return rt
	})
}

// A contract that neither passes nor fails is worthless, so every one of them
// has to actually run somewhere. This pins that the conforming harness leaves
// none of them skipped — the state that made the old per-provider tests
// misleading was exactly "not asserted, read as fine".
func TestContractSuite_ConformingHarnessSkipsNothing(t *testing.T) {
	for _, c := range Contracts() {
		// A fresh provider per contract: the block/unblock contracts mutate
		// runtime state, and a shared one would make the table order-dependent.
		rt := NewFakeRuntime(t, NewFakeProvider())
		if res := c.Check(t.Context(), rt); res.Skipped != "" {
			t.Errorf("contract %s was skipped against the fully-wired fake harness: %s", c.Name, res.Skipped)
		}
	}
}
