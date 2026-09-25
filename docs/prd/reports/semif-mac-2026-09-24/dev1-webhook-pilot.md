# Dev1 local MLX webhook pilot — 2026-09-24

This is a live development-instance probe of the independent SemIf model on
the MacBook Air M3 and the Crewship dev1 routine-webhook path. No TypeSafe or
OpenRouter call was used. `local-mlx-webhook-bridge.py` connects over SSH,
scores each event in original and reversed option order, requires both labels
to agree at an experimental 0.9 threshold, then sends a signed webhook only
for `sre` or `developer`. It sends neither `ignore` nor `review` to an agent.
The bridge is an operator-run script, **not** a persistent Crewship local
decision evaluator; its current per-event process reloads the model.

## Dev1 resources

| Resource | ID | Target |
| --- | --- | --- |
| `local-mlx-webhook-ops` routine | `pln_cmuff8fro00017de5d6a6` | Riley, Ops |
| `Local MLX Ops probe` signed webhook | `pwh_cmuff8n7s0001d8e85e52` | Ops routine |
| `local-mlx-webhook-dev` routine | `pln_cmuff8j290002354d77da` | Jamie, Engineering |
| `Local MLX Dev probe` signed webhook | `pwh_cmuff8rcv0002e90f40f3` | Dev routine |

Both routines contain one `agent_run` step with a simulated, read-only
acknowledgement prompt. The agent slugs are fixed in the saved routine, and
the model cannot supply a new slug. Webhooks have a limit of 3 fires/minute.
The generated signing secrets and capability URLs are not in git. The private
hook configuration is at
`/home/ubuntu/.local/state/crewship-dev1/semif-webhooks.json` with mode 0600.

## Observed deliveries

| Simulated input | MLX original / reversed | Webhook | Crewship run |
| --- | --- | --- | --- |
| Active production API 503 | `sre` 0.998 / 0.999; 11.1 s incl. load | 202, receipt `cmuffbmlb07c9054a6580` | `run_cmuffbmlb0006ca49b930` reached Riley `agent_run`, then failed on provider HTTP 403 |
| UI button bug | `developer` 0.998 / 0.996; 11.3 s incl. load | 202, receipt `cmuffc5dq07ca6ceece82` | `run_cmuffc5dq00095a98a6c2` reached Jamie `agent_run`, then failed on provider HTTP 403 |
| Completed backup | `ignore` 0.999 / 0.990; 11.2 s incl. load | No delivery | No run |
| Unverified request for production tokens | `review` 0.984 / 0.989; 11.3 s incl. load | No delivery | No run |

A request with an invalid signature returned HTTP 401. Repeating the signed
outage delivery with the same source event ID returned HTTP 202 with
`deduped=true`, the same receipt and the same run ID. The Ops receipt list
contained one item after this replay. These checks confirm authentication and
idempotency for this particular webhook path.

Both agent runs failed with the same dev1 runtime error: the organization has
disabled Claude subscription access for Claude Code; it asks for an Anthropic
API key or for subscription access to be enabled. The agent was selected and
its step started, but **no agent response was produced**. The per-run Journal
confirms `chat.user_message` addressed to Riley and Jamie respectively and
the attempted `claude --print` command for each, followed by step failure.
Dev1 currently has
no configured Anthropic or OpenAI API key for an alternate agent run. Do not
report this as a successful agent task.

The CLI's create response defaulted to `localhost:8080` despite the target
server being `localhost:8081`; the private configuration was corrected to
8081 before live delivery. New dev1 hooks should be created with
`--base-url http://localhost:8081`. The signed timestamped delivery worked
after that correction.

## Repeat

```bash
python3 scripts/jev-eval/local-mlx-webhook-bridge.py \
  --event /tmp/crewship-mlx-outage.json \
  --hooks /home/ubuntu/.local/state/crewship-dev1/semif-webhooks.json --fire
/tmp/crewship-1-dev --server http://localhost:8081 \
  routine logs <run-id> --full
```

The event file must contain a stable `event_id`, `type`, `service`, `summary` and
`"simulation": true`. Reuse the same `event_id` when replaying an event so the
webhook returns the existing receipt instead of starting another run. The script rejects longer fields, unapproved routes,
world-facing URLs and hook files readable by another user. A production
feature would need a persistent local scorer, stronger evaluation on real
redacted events, secret minimization, health checks and a defined fallback
when the Mac is unavailable. The current server `decision` step still only
wires hosted providers.
