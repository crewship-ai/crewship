# OpenCode / Z.AI on main dev3 — 2026-09-22

Main origin: https://crewship-dev3.unifylab.cz (port 8083 backend). No alternate port is required.

## Deployed code and compatibility

The deployed server is clean code commit `df462ff22` from `dev3/zai-main-release`. It combines provider PR #2622 (including `284853a8d`), main at `ff0c8b956`, and the already-deployed unsigned-webhook commits `696fdd1d3` / `9b6e7b153`. The existing database migration `20260916140000` is preserved byte-for-byte. `db migration-status --local` reported all 315 applied migrations recognized and no outstanding work. The live process executable hash was compared with the built binary. The final server retains expected sidecar hash `49e23fad5f11`, matching the sidecar deployed with the first main-origin rollout; the last change is only an API error message.

The shared checkout `/srv/crewship/crewship_3` and its unrelated WIP were preserved. The release source is `/srv/crewship/dev3-zai-main-source`. Main service remains `crewship-ws@3`; its existing launcher and data paths are unchanged. Reloads briefly interrupt service; HTTP 200 and CLI inference were verified after the final reload.

## User credential and agent

Workspace: `cmtpnc4m80002521513b1`. Original agent: **Správce záloh**, slug `coolify-zalohy`, ID `cmu46fedp005b868ca58a`.

The user's stored key was decrypted only in operator memory and passed over stdin to the supported credential-create CLI. New record: `cmucnr0bp0004356e0325`, provider `ZAI_CODING_PLAN`, name **Z.AI Coding Plan**. A scoped agent binding fills `ZAI_CODING_PLAN_API_KEY`. Agent provider/model are now `ZAI_CODING_PLAN` / `zai-coding-plan/glm-5.3`, adapter `OPENCODE`. Readiness is `ready`, source `agent_binding`, delivery `sidecar`.

After successful inference and checking that no other agent or broader binding consumed the old record, `cmub23zkk00b9b618ff0a` (misclassified as metered `ZAI`) was soft-deleted with the official CLI. Other OpenAI/OpenCode Go bindings were preserved. No backup/infrastructure operation was requested or performed by the acceptance prompts.

## Paid acceptance on the main origin

- `msg_1790080605817362012_810840a3a905f785`: `GLM_DEV3_OK`, exit 0.
- `msg_1790080668201265900_b6d40fc0138bffa8`: actual OpenCode `tool_use` event, bash command `printf GLM_DEV3_TOOL_OK`, completed, output `GLM_DEV3_TOOL_OK`, run exit 0. The structured event was verified inside the journal output capture; the printed response alone was not treated as tool proof.
- Final post-reload/post-cleanup CLI run returned `GLM_MAIN_READY`.
- Ledger rows show `zai-coding-plan`, `glm-5.3`, new credential ID, `flat_rate`, `GLM Coding Plan`, `cost_confidence=unknown`. Zero recorded dollar cost is not a claim of a free plan or measured quota.
- Authenticated provider-login API returns five accounts: Anthropic x2, OpenAI, OpenCode Go, Z.AI Coding Plan. The deployed sidebar lists connected facets only. Existing desktop/mobile UI regression evidence comes from the same frontend on acceptance; the saved main browser test session was expired, so main-origin evidence is CLI/API and HTTP, not a successful fresh browser login.

## Validation and review

19 affected frontend tests passed. Webpack static export, Go build, go vet and targeted provider-login/Z.AI/unsigned-webhook/orchestrator/sidecar tests passed. The full release Go suite and required CI were started separately; consult issue #2621 for their final result. Do not infer all-green checks from the successful live run.

PR #2619 review is approved on `7acd72ec7`; its old icon exception was already implemented and the factual provider inventory was retained to satisfy the user's all-provider inventory request. PR #2622's remaining invalid-provider error message was fixed in `284853a8d`, and its inline thread resolved. The user explicitly waived waiting for another CodeRabbit review on September 22. Required CI remains a merge gate. Merge order: #2619 → #2622. Repository auto-merge is disabled; the review waiver does not authorize bypassing incomplete or failing CI.

## Retired acceptance environment and rollback

`crewship-zai-acceptance.service` is stopped and disabled. Its two named `crewship-zai-*` containers and `crewship-zai-agents` network were removed. The exact acceptance-only UFW port 8093 rule and Caddy :8443 block were removed. Caddy has `admin off`, so the normal reload command failed without changing the running proxy; Caddy 2.11.3 successfully reloaded via SIGUSR1 (documented at https://caddyserver.com/docs/command-line#signals). Main origin stayed available during this Caddy reload. No :8443 listener remains.

Private rollback/evidence directory: `/srv/crewship/zai-acceptance` (0700). It holds the pre-deployment SQLite online backup (quick_check=ok), previous server/sidecar binaries in `main-rollback-20260922`, original Caddy configuration, CLI/run/journal/check logs and credential metadata. Secret-containing files must not be committed or posted. The old acceptance database and storage remain offline for rollback/audit; their credential copy is not currently served. A rollback to the pre-integration binary also requires accounting for the new provider record and agent configuration; do not blindly restore an old database over newer user activity.

## Final local verification update

The full release `go test ./... -count=1 -timeout=30m` completed with 145 packages passing and `internal/database` reaching the aggregate 30-minute package timeout. The active test at timeout was `TestMigrateV102_KeeperPhase2`, running for one second; this does not establish a failure in that individual test. Full API package passed in 1497.813s. The isolated retry of that test passed in 10.029s; evidence is in the private logs. This does not convert the timed-out full suite into a pass. Final integration go-vet and targeted package checks passed earlier. Do not describe this full run as green.

CodeRabbit's full-review request at 12:53 UTC was rate-limited, explicitly naming 41 minutes until the next included review (about 13:34 UTC). #2619 still has two running required race jobs and its base moved again during this session. No PR was merged. Main service log scan since rollout found zero exact plaintext-key matches.

## Merge preparation update

Both provider branches were updated to main `6ceddb52d` (workflow action digest updates) on September 22. Required checks are rerunning. The earlier review queue and pending-job counts above are historical observations; consult GitHub for current checks. No merge is claimed by this document.
