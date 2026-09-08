# Live credential acceptance on dev3 — 2026-09-08

Target: explicit CLI profile dev3, http://localhost:8083, workspace
`cmtpnc4m80002521513b1` (`demo-cmtpnc4m`), authenticated OWNER. Running product
HEAD remains `3fd53767`; unmerged provenance UI is not deployed.

## Results

1. Anthropic smoke: Alex returned `DEV3_CREDENTIAL_SMOKE_OK` through streaming
   CLI run, exit 0. Earlier no-stream attempt had returned no text; its root
   cause has not been established and it is not counted as a pass.
2. Isolated manifest applied with `--strict --no-delete --yes`:
   `/tmp/dev3-credential-lab-20260908.yaml`. Crew
   `credential-lab-20260908`, ID `cmtsmunva0392dc542555`, agent
   `credential-lab-tester`, ID `cmtsmunvd0393289ef4ed`. Crew and postgres:16-alpine
   service started. `docker port` confirmed no published host port.
3. Manifest created `POSTGRES_PASSWORD`, credential
   `cmtsmunv60390c8cb8ac6`, ACTIVE, author system, provisioned for
   `credential-lab-20260908/postgres`. Anthropic was explicitly assigned to the
   tester; memory was explicitly enabled.
4. Agent-generated proposal: Alex created a random password through structured
   escalation. Credential `cmtsmxpg6039c487ac035`, name
   `DEV3_AGENT_PROPOSAL_20260908`, was observed PENDING_APPROVAL; inbox item
   `ibx_escalation_cmtsmxpg3039b1d4d2d26` appeared. Operator CLI approval changed
   it to ACTIVE and the escalation to RESOLVED. Read-only DB metadata verified
   author agent, ID `cmtsmc3bs02b30664d40d`, name Alex. The CLI credential-list
   struct omits actor ID; a null extracted with jq was NOT a null in the DB.
5. Human-supply ask: Alex created `DEV3_HUMAN_SUPPLY_20260908` with no value;
   credential `cmtsn1n2x03abed6d73ea` was REQUESTED and handle_only=1. Escalation
   `cmtsn1ijs03a97a3097ed` appeared in inbox. A cryptographically random TEST
   value was piped directly from openssl to `escalation supply` via stdin, not
   argv/chat. It became ACTIVE; inbox resolved; agent response reported only
   grant metadata with handle_only=true. This simulated operator supply, not a
   browser form submission.
6. PostgreSQL: first tester run correctly reported missing database client and
   wrote non-secret connection metadata to memory. apt install was refused by
   the container's read-only filesystem; that protection was not disabled.
   Installed Node pg 8.23.0 with pnpm --ignore-scripts as UID 1001 in temporary
   `/tmp/credential-pg-client.Y2zjXR`. A separate subsequent agent run recovered
   connection metadata from memory, used Keeper execute to inject PGPASSWORD,
   and reported `SELECT 1 AS credential_test` returning 1. Read-only Keeper audit
   independently confirms ALLOW/execute for the tester and matching credential.
   Agent wrote updated non-secret metadata/outcome to persistent memory.

## Evidence

- `/tmp/dev3-credential-smoke2.log`
- `/tmp/dev3-credential-lab-apply.log`
- `/tmp/dev3-agent-proposal-20260908.log`
- `/tmp/dev3-human-supply-agent-20260908.log`
- `/tmp/dev3-postgres-agent-20260908.log` (client missing, not DB success)
- `/tmp/dev3-credential-lab-client-install.log` (apt refused, not a pass)
- `/tmp/dev3-credential-lab-pg-install.log`
- `/tmp/dev3-postgres-agent-retry-20260908.log`

## Limits and retained resources

Resources remain for user inspection: test crew, agent, PostgreSQL container,
three credentials and two resolved inbox requests. Temporary pg installation
is NOT reproducible provisioning and will not survive container replacement.
No password values are included in this report. No existing service roles/data
were modified. Human-supply test value is disposable.

Not a complete security acceptance: automatic service secrets still have the
previously identified literal service-config copy; frontend creator presentation
is not deployed; CLI drops actor ID; browser supply/approval and full RBAC matrix
were not exercised here. No restart/restore persistence test was performed.
The earlier failed backup and reset are separate from these tests.
