package providertest

import (
	"testing"
	"time"
)

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

// Every contract that STARTS a process must also see it exit before it
// returns, and the shape contract did not: it closed the caller's stream and
// returned while the process was still alive.
//
// That is not a tidiness point. Closing says only that the caller stopped
// reading; on a provider that shells out, `container exec` is still running,
// and on this suite's harnesses it is still writing into the fixture directory
// the subtest is about to remove. The apple harness's CLI stub appends its argv
// to a log inside t.TempDir() as it starts, so the sequence was: contract
// returns, subtest ends, RemoveAll lists and empties the directory, the stub
// finally gets scheduled and recreates the log, RemoveAll's unlinkat on the
// directory fails with ENOTEMPTY. `TempDir RemoveAll cleanup: directory not
// empty` on a subtest whose own assertions had all passed (#1951), reproducible
// under CPU contention and invisible on an idle host.
//
// The invariant is teardown-shaped, so no contract in the table can assert it:
// drive the check directly and require that it does not return while the exec
// it started is still running.
func TestExecResultShapeContract_WaitsForItsProcessToExit(t *testing.T) {
	p := NewFakeProvider()
	rt := NewFakeRuntime(t, p)
	// The shape contract runs ExitCmd. Hand it the one command this fake keeps
	// alive until told otherwise, so "still running when the check returned" is
	// a fact the test establishes rather than a race it has to win.
	rt.ExitCmd = func(int) []string { return fakeBlockCmd }

	c, ok := contractByName("exec/result_carries_an_exec_id_and_a_readable_stream")
	if !ok {
		t.Fatal("contract exec/result_carries_an_exec_id_and_a_readable_stream not found in the table")
	}

	done := make(chan Result, 1)
	go func() { done <- c.Check(t.Context(), rt) }()

	select {
	case <-done:
		t.Fatal("the contract returned while the exec it started was still running: the process outlives the subtest body, so t.TempDir's cleanup races whatever that process is still writing (#1951)")
	case <-time.After(250 * time.Millisecond):
	}

	p.Unblock()

	select {
	case res := <-done:
		if res.Skipped != "" {
			t.Fatalf("contract skipped instead of running: %s", res.Skipped)
		}
		if len(res.Violations) > 0 {
			t.Errorf("contract reported violations against the conforming fake: %v", res.Violations)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the contract never returned after its exec was released")
	}
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
