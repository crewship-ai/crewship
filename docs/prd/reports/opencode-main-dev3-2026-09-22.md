# OpenCode / Z.AI on main dev3 — 2026-09-22

Main origin: https://crewship-dev3.unifylab.cz (port 8083 backend). No alternate port is required.

## Deployed code and compatibility

The deployed server is clean code commit `b11ba36f2` from `dev3/zai-main-release`, deployed at approximately 14:21 UTC. It combines the final tested provider head `233c821ba` (squash-merged to main as `2317c32ee`), main dependency updates through `ef36bbfe1`, and the already-deployed unsigned-webhook commits `696fdd1d3` / `9b6e7b153`. All 15 agents were IDLE before the final reload. The previous working server was `df462ff22`; rollback binaries and an integrity-checked SQLite snapshot are under `/srv/crewship/zai-acceptance/final-merge-rollback`. The existing database migration `20260916140000` is preserved byte-for-byte. `db migration-status --local` reported all 315 applied migrations recognized and no outstanding work. The live process executable hash was compared with the built binary. The final server retains expected sidecar hash `49e23fad5f11`, matching the sidecar deployed with the first main-origin rollout; provider, sidecar and frontend source did not change between these two deployments; the final build incorporates the new main Go dependencies. Existing static assets are unchanged and the deployed sidecar is intentionally retained.

The shared checkout `/srv/crewship/crewship_3` and its unrelated WIP were preserved. The release source is `/srv/crewship/dev3-zai-main-source`. Main service remains `crewship-ws@3`; its existing launcher and data paths are unchanged. Reloads briefly interrupt service; HTTP 200 and CLI inference were verified after the final reload.

## User credential and agent

Workspace: `cmtpnc4m80002521513b1`. Original agent: **Správce záloh**, slug `coolify-zalohy`, ID `cmu46fedp005b868ca58a`.

The user's stored key was decrypted only in operator memory and passed over stdin to the supported credential-create CLI. New record: `cmucnr0bp0004356e0325`, provider `ZAI_CODING_PLAN`, name **Z.AI Coding Plan**. A scoped agent binding fills `ZAI_CODING_PLAN_API_KEY`. Agent provider/model are now `ZAI_CODING_PLAN` / `zai-coding-plan/glm-5.3`, adapter `OPENCODE`. Readiness is `ready`, source `agent_binding`, delivery `sidecar`.

After successful inference and checking that no other agent or broader binding consumed the old record, `cmub23zkk00b9b618ff0a` (misclassified as metered `ZAI`) was soft-deleted with the official CLI. Other OpenAI/OpenCode Go bindings were preserved. No backup/infrastructure operation was requested or performed by the acceptance prompts.

## Paid acceptance on the main origin

- `msg_1790080605817362012_810840a3a905f785`: `GLM_DEV3_OK`, exit 0.
- `msg_1790080668201265900_b6d40fc0138bffa8`: actual OpenCode `tool_use` event, bash command `printf GLM_DEV3_TOOL_OK`, completed, output `GLM_DEV3_TOOL_OK`, run exit 0. The structured event was verified inside the journal output capture; the printed response alone was not treated as tool proof.
- Earlier post-cleanup CLI run returned `GLM_MAIN_READY`.
- Final post-merge deployment run `msg_1790086894588435664_a982e5fb5242f08c` completed successfully at 14:21:49 UTC with `GLM_MERGED_OK`, CLI exit 0. Readiness again reported `ready`, `agent_binding`, `sidecar`, `ZAI_CODING_PLAN`.
- Ledger rows show `zai-coding-plan`, `glm-5.3`, new credential ID, `flat_rate`, `GLM Coding Plan`, `cost_confidence=unknown`. Zero recorded dollar cost is not a claim of a free plan or measured quota.
- Authenticated provider-login API returns five accounts: Anthropic x2, OpenAI, OpenCode Go, Z.AI Coding Plan. The deployed sidebar lists connected facets only. Existing desktop/mobile UI regression evidence comes from the same frontend on acceptance; the saved main browser test session was expired, so main-origin evidence is CLI/API and HTTP, not a successful fresh browser login.

## Validation and merge

PR #2622 was squash-merged at 14:19:35 UTC as `2317c32ee`, including the entire #2619 head. PR #2619 is CLOSED as included, not separately merged. Issues #2618 and #2621 are CLOSED. The source tree of the merged main commit was compared with tested head `233c821ba` and matched exactly.

[Final PR CI](https://github.com/crewship-ai/crewship/actions/runs/35734507484) completed successfully. Required CI Result, Security Result and CodeQL Result all passed on `233c821ba`; no failed check or unresolved inline thread remained. API race ran from 13:39:13 to 14:18:38 UTC and passed. The user explicitly waived further CodeRabbit waiting. A normal merge was blocked by review policy, so administrative merge was used for that authorized waiver only after all required checks passed; no tests or security checks were bypassed. Post-merge main CI is a separate run and is not claimed as completed here.

19 affected frontend tests, static export, Go build and vet passed earlier. The final dev3 compatibility build additionally passed targeted unsigned-webhook/provider-login/Z.AI API tests after the new dependencies (2.680s), recognized all 315 migrations and started successfully. The live process executable hash matches `b11ba36f2` and the retained sidecar matches `49e23fad5f11`.

## Retired acceptance environment and rollback

`crewship-zai-acceptance.service` is stopped and disabled. Its two named `crewship-zai-*` containers and `crewship-zai-agents` network were removed. The exact acceptance-only UFW port 8093 rule and Caddy :8443 block were removed. Caddy has `admin off`, so the normal reload command failed without changing the running proxy; Caddy 2.11.3 successfully reloaded via SIGUSR1 (documented at https://caddyserver.com/docs/command-line#signals). Main origin stayed available during this Caddy reload. No :8443 listener remains.

Private rollback/evidence directory: `/srv/crewship/zai-acceptance` (0700). It holds the pre-deployment SQLite online backup (quick_check=ok), previous server/sidecar binaries in `main-rollback-20260922`, original Caddy configuration, CLI/run/journal/check logs and credential metadata. Secret-containing files must not be committed or posted. The old acceptance database and storage remain offline for rollback/audit; their credential copy is not currently served. A rollback to the pre-integration binary also requires accounting for the new provider record and agent configuration; do not blindly restore an old database over newer user activity.

## Final local verification update

The full release `go test ./... -count=1 -timeout=30m` completed with 145 packages passing and `internal/database` reaching the aggregate 30-minute package timeout. The active test at timeout was `TestMigrateV102_KeeperPhase2`, running for one second; this does not establish a failure in that individual test. Full API package passed in 1497.813s. The isolated retry of that test passed in 10.029s; evidence is in the private logs. This does not convert the timed-out full suite into a pass. Final integration go-vet and targeted package checks passed earlier. Do not describe this full run as green.

The earlier CodeRabbit rate-limit and two-PR merge-order notes are superseded by the completed combined merge above. A pre-final-reload main service log scan found zero exact plaintext-key matches; that result is scoped to the scanned interval.

## Remaining scope

Two-real-account and actual quota-exhaustion scenarios were not exercised. P2–P6 (shared registry, broader providers, cloud identity, custom/plugin expansion and OpenCode V2) remain separate backlog. Do not describe this P1 completion as support for every catalog provider. The offline acceptance data remains private for audit/rollback; its service and network exposure are retired.
