package providertest

import "testing"

// A contract suite nobody has watched fail is indistinguishable from a suite of
// checks that always pass — and "always passes" is precisely how a provider
// shipped an Exec that discarded stdin while its package's tests were green.
//
// So each Break here reintroduces one real defect (several are verbatim the
// bugs this package was written for) and asserts the matching contract goes
// red. Adding a contract without adding its mutation case leaves the new check
// unproven.
func TestContracts_HaveTeeth(t *testing.T) {
	cases := []struct {
		name     string
		breaks   Breaks
		contract string
	}{
		{
			// The #1779 bug itself: bytes handed to ExecConfig.Stdin never
			// reach the process, and the exec still reports success.
			name:     "stdin discarded",
			breaks:   Breaks{DiscardStdin: true},
			contract: "exec/stdin_reaches_the_process",
		},
		{
			name:     "stdin write side never half-closed",
			breaks:   Breaks{StdinNoEOF: true},
			contract: "exec/stdin_write_side_is_half_closed",
		},
		{
			name:     "stdin attached when the caller passed none",
			breaks:   Breaks{AlwaysAttachStdin: true},
			contract: "exec/nil_stdin_attaches_nothing",
		},
		{
			// The #1158 bug: an explicit root user runs anyway.
			name:     "privileged guard ignored",
			breaks:   Breaks{IgnorePrivilegedGuard: true},
			contract: "exec/refuses_privileged_user",
		},
		{
			name:     "privileged guard ignored (interactive path)",
			breaks:   Breaks{IgnorePrivilegedGuard: true},
			contract: "execinteractive/refuses_privileged_user",
		},
		{
			name:     "empty user passed through to the runtime",
			breaks:   Breaks{EmptyUserPassthrough: true},
			contract: "exec/empty_user_resolves_to_a_non_root_user",
		},
		{
			name:     "audited AllowPrivileged opt-in ignored",
			breaks:   Breaks{IgnoreAllowPrivileged: true},
			contract: "exec/allow_privileged_is_honoured",
		},
		{
			name:     "exec id not returned",
			breaks:   Breaks{NoExecID: true},
			contract: "exec/result_carries_an_exec_id_and_a_readable_stream",
		},
		{
			name:     "stderr dropped from the output stream",
			breaks:   Breaks{DropStderr: true},
			contract: "exec/stderr_reaches_the_output_stream",
		},
		{
			name:     "running process reported as finished",
			breaks:   Breaks{NotRunning: true},
			contract: "execinspect/reports_running_while_the_process_runs",
		},
		{
			name:     "running process reported with exit code 0",
			breaks:   Breaks{RunningExitZero: true},
			contract: "execinspect/running_is_not_mistakable_for_success",
		},
		{
			name:     "exit code replaced with 0",
			breaks:   Breaks{LoseExitCode: true},
			contract: "execinspect/reports_the_real_exit_code_after_completion",
		},
		{
			name:     "unknown exec id answered as success",
			breaks:   Breaks{UnknownExecOK: true},
			contract: "execinspect/unknown_exec_id_is_an_error_not_a_success",
		},
		{
			// Audit C1: two tenants with the same crew slug collide.
			name:     "container name keyed on slug alone",
			breaks:   Breaks{NameIgnoresID: true},
			contract: "naming/crew_container_name_is_keyed_by_crew_id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := contractByName(tc.contract)
			if !ok {
				t.Fatalf("no contract named %q", tc.contract)
			}
			p := NewFakeProvider()
			p.Breaks = tc.breaks
			rt := NewFakeRuntime(t, p)

			res := c.Check(t.Context(), rt)
			if res.Skipped != "" {
				t.Fatalf("contract %s skipped instead of running: %s", c.Name, res.Skipped)
			}
			if len(res.Violations) == 0 {
				t.Errorf("contract %s passed against an implementation that deliberately breaks it (%+v) — the check has no teeth", c.Name, tc.breaks)
			}
		})
	}
}

// Every contract must appear in the mutation table above; a check with no
// mutation case is a check nobody has proven can fail.
func TestContracts_EveryContractHasAMutationCase(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range mutationContractNames() {
		covered[tc] = true
	}
	for _, c := range Contracts() {
		if !covered[c.Name] {
			t.Errorf("contract %s has no mutation case in TestContracts_HaveTeeth", c.Name)
		}
	}
}

// mutationContractNames mirrors the table above. Kept as a plain list rather
// than reflection so adding a contract fails loudly at the assertion, not
// silently at the reflection.
func mutationContractNames() []string {
	return []string{
		"exec/stdin_reaches_the_process",
		"exec/stdin_write_side_is_half_closed",
		"exec/nil_stdin_attaches_nothing",
		"exec/refuses_privileged_user",
		"exec/empty_user_resolves_to_a_non_root_user",
		"exec/allow_privileged_is_honoured",
		"exec/result_carries_an_exec_id_and_a_readable_stream",
		"exec/stderr_reaches_the_output_stream",
		"execinspect/reports_running_while_the_process_runs",
		"execinspect/running_is_not_mistakable_for_success",
		"execinspect/reports_the_real_exit_code_after_completion",
		"execinspect/unknown_exec_id_is_an_error_not_a_success",
		"naming/crew_container_name_is_keyed_by_crew_id",
		"execinteractive/refuses_privileged_user",
	}
}

func contractByName(name string) (Contract, bool) {
	for _, c := range Contracts() {
		if c.Name == name {
			return c, true
		}
	}
	return Contract{}, false
}
