# Offline crew runtime acceptance preparation

`test-runtime-messages.py` prepares a deterministic Messages script and checks
results using the real Crewship CLI. It does not provision a server, repair seed,
deploy stage, or enable a workflow. This is acceptance infrastructure under
development; no actual Claude crew round trip has yet been demonstrated.

The driver reports only CLI state verification, with
`runtime_acceptance_passed: false`. A future owned runner must additionally
require successful sidecar exit and `RUNTIME_FIXTURE_COMPLETE`, rejecting any
incomplete/rejected provider script. Driver exit zero alone is not a runtime
gate. The manifest check verifies ownership and process existence, but does not
yet bind the listening server to that PID; the runner must establish that
identity before it can be used on a CI host.

Use only a separately owned instance launched with
`scripts/throwaway-server.sh`, an explicit fixture CLI configuration, two real
Claude agents in one crew, and a dedicated issue. A synthetic credential belongs
in the sidecar fixture, never in agent tool arguments. The upstream fixture
checks the injected header; the agent's real Bash diagnostic checks UID, agent
identity, dummy provider key, sidecar endpoint and presence of its assignment
token. Only the synthetic upstream key's hash reaches the diagnostic script.

The test-built `crewship-sidecar` uses the production bootstrap and proxy
admission/routing, with an offline `http.RoundTripper` compiled into its tagged
test executable. The scenario is embedded using `main.fixtureScenarioBase64`
at link time; production configuration and environment cannot enable it. See
[the mock contract](../../internal/testutil/messagesmock/README.md).

Run `python3 scripts/test-harness/test-runtime-messages.py --help` for the
required fixture identities and explicit tool names. `--emit-scenario` writes
the script without executing runtime acceptance. The actual Claude CLI's
advertised MCP tool names and arguments must be verified before the fixture is
started; assumed names are not evidence of protocol compatibility.

The intended scenario exercises mention dispatch, a real Bash tool, a crew
memory write, lead-to-peer delegation, a peer memory read and completion. CLI
assertions correlate the nonce with distinct assignment/run identities, results,
activity, journal transitions and a new memory version's content and hash.
Missing evidence fails instead of being treated as a skip. Deferred queue
draining, independent cross-crew isolation and live-provider compatibility are
outside this scenario's current scope.

Python regressions use a synthetic CLI executable and validate these checks;
they do not execute an agent. A complete owned-fixture runner, verified pinned
Claude protocol and a successful real container run remain prerequisites for
making this a required CI check. The restricted Codex native acceptance is a
separate contract with its own [released-image prerequisite](../ci/native-runtime.md).
