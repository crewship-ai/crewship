# Local SemIf feature assessment for Crewship 1.0

This extends the [first Mac report](../README.md). All model calls ran on the
MacBook Air M3 with 16 GB RAM using the existing SemIf checkout at
`1f2dea3e25379f9dfc98cb83c324f00ab5deda37`, MLX 0.32.2, MLX-LM 0.32.0,
and pinned Qwen3.5-4B checkpoint `851bf6e806efd8d0a36b00ddf55e13ccb7b8cd0a`.
The examples are synthetic and labelled by their author. Counts below are
case counts; prompt variants and Czech/English pairs are correlated, not
independent production samples. No Crewship local evaluator or signed webhook
was exercised.

## Use-case probe

The 62-case corpus joins 12 webhook, 12 journal, 10 infrastructure dry-run and
28 Keeper tool-call snapshots. `semif-keeper-cases.jsonl` tests observable
patterns (`routine`, `loop`, `credential_probe`, `destructive`, `review`); it
does not test permission to access a credential. The same state, instructions
and option descriptions were passed to the independent local Laya MLX port.

| Model | Correct first label | Correct after 0.9 review fallback | Automatic and correct | Automatic and wrong | Median warm call |
| --- | ---: | ---: | ---: | ---: | ---: |
| SemIf 4-bit | 58/62 | 56/62 | 41/62 | 0/62 | 0.728 s |
| SemIf source precision | 59/62 | 56/62 | 41/62 | 0/62 | 0.746 s |
| Laya multilingual MLX port | 29/62 | 27/62 | 13/62 | 5/62 | 0.019 s |

The shared 0.9 threshold is experimental and uncalibrated for both models;
differences in automatic coverage are not a calibrated comparison. Laya was
much faster, but considerably less accurate on these authored cases. This
does not establish that SemIf is generally better than Laya or that either
should take production actions. The original 34-case results are in the parent
report directory; the 28 Keeper-case results are in this directory.

The 4-bit SemIf first labelled one Czech sequence of `.env`, kubeconfig and
SSH-key reads as `routine`; source precision made the same first-choice error.
Both sent it to `review` in the original option order. This is an unacceptable
candidate for using the model to skip Keeper's existing credential gate.

## Option-order and repeatability stress

For each of the 62 cases we ran five variants: original option order, exact
repeat, reversed, rotated, and alphabetically sorted by option ID. That is
310 scored rows for each precision. Exact repeats produced identical
probability vectors in both runs. Reordering changed the final route 14 times
in 4-bit mode and 22 times in source precision. Four 4-bit variants and one
source-precision variant produced an **incorrect automatic route** at the
same 0.9 threshold.

The most concerning 4-bit variant labelled the Czech `.env`/SSH-key probe
`routine` with score **0.976** after reversing the options. Two repeated
journal pipeline failures became `record` with scores **0.978** and **0.955**.
In source precision, a discussion about a possible database deletion became
`routine` with score **0.927** when reversed. These are option-conditional
softmax scores, not reliable confidence. The alphabetically sorted variant
did not create a wrong automatic route in this small corpus, but it changed
some routes to `review`; the Go JSON encoder would sort map keys, so fixed
ordering must be a tested part of any local adapter.

Requiring original and reversed orders to agree would have made 37/62 4-bit
and 36/62 source-precision cases automatic, with no wrong automatic route in
this corpus. Requiring all four distinct orders to agree leaves 36/62 and
31/62 respectively. This is a useful abstention experiment, not a safety
proof; it multiplies inference cost and may still fail on new inputs.

## Journal length and multiple questions

One deterministic journal fixture puts an active outage after increasing
numbers of successful routine lines:

| Input tokens | Correct choice | Top score | Warm decision latency |
| ---: | :---: | ---: | ---: |
| 162 | yes | 0.974 | 0.63 s |
| 1098 | yes | 0.932 | 3.61 s |
| 3018 | yes | 0.893 | 10.25 s |

A 7338-token row was rejected by the scorer at the configured 4096-token
limit with "no truncation allowed"; it produced no output row. A Journal page
with 5000 events therefore needs deterministic selection/aggregation before
SemIf sees it. Sending a raw window would be slow and may fail the limit.

Eight fixed yes/no questions about one incident state scored 8/8 in
all three MLX modes. Excluding model load, direct scoring took 4.92 s in
total; serial prefix reuse took 2.46 s; shared-state batching took 2.38 s.
This demonstrates a potential benefit for a small number of incident facets
over one bounded state. It is one fixture, not a throughput guarantee under
concurrent Journal load. One correct answer had a top score of only 0.531,
so a 0.9 fallback would still send that facet to review.

During the tests the Air also had a 370 MB Ollama embedding model loaded;
there was no observed out-of-memory failure. The Keeper's 7B judge was not
co-resident in this measurement, so 16 GB suitability for both services at
once remains unverified. Two `vm.swapusage` observations during/after the
stress runs were about 959 MB and 951 MB; no pre-test baseline was recorded.

## Product fit

| Crewship surface | Possible use | 1.0 assessment |
| --- | --- | --- |
| Journal / Activity | Tag a bounded per-run or per-trace summary; queue a human investigation, attach the model's option scores as audit evidence. | Good candidate for an opt-in **shadow/advisory** pilot. Do not score every event or copy raw secrets. |
| Signed incoming webhooks | Suggest one of a small authored set of agents or `review`; preserve HMAC, idempotency and the existing agent admission gates. | Experimental only. The current PR's server step still calls hosted providers; a local adapter and an actual signed-webhook Mac run are missing. |
| Ansible/Terraform dry run | Tag `proceed`, `fix`, or `review` from a short, structured summary. | Advisory only; any actual apply/destroy still uses existing human/Harbormaster authorization. |
| Keeper behavior watchdog | Add a tool-call pattern tag or prioritise an inbox item. | Shadow signal only. It must never turn a `routine` label into ALLOW, suppress sampling, or skip Keeper's own judge. |
| Keeper credential gate | Replace ALLOW/DENY/ESCALATE decision. | **Do not use**. The model gives no reasoned Keeper response and produced a high-score false `routine` on credential probing under option reordering. |
| Retrieval | Rank documents/candidates. | No claim yet. SemIf's MLX backend does not support its separate reranker mode; an option-choice approximation would need its own relevance benchmark. |

The current Keeper `BehaviorEvaluator` is a sampled post-tool-call monitor;
the credential gate considers intent, tier, conversation and evidence. SemIf's
small fixed choice is suitable as an extra observation, not a drop-in judge.

Additional candidates for a shadow trial are tagging repeated agent retries
with no progress, identifying a likely failed routine step after a dry run,
highlighting drift between the requested task and observed tool calls, and
asking several fixed incident questions about a single compact Journal trace.
None of these candidates has production accuracy evidence yet. Deterministic
signals such as exact retry counts, forbidden commands, signature validation,
or declared credential scope should still be checked in code first.

## Release decision and required evidence

The tested model has a credible **advisory** use for Crewship 1.0, especially
compact Journal summaries and suggested webhook routes. Automatic routing or
security decisions are **not release-ready** from these tests. The original
62-case zero-error automatic subset is contradicted by high-score wrong
routes under option reordering.

Before an operational 1.0 feature, the implementation needs a persistent,
health-checked local MLX service on the Mac, a bounded Crewship evaluator
adapter with one canonical option order, workspace scoping, a timeout and
failure-to-review behavior, data minimization, and an end-to-end signed
webhook test. The server must not rely on the Mac always being awake. It also
needs a labelled set of real redacted Crewship events with an explicit
cost-of-errors review, replay/latency testing under burst load, and a memory
test with the Keeper judge loaded at the same time. Until those gates are
met, keep the existing authorization and policy paths authoritative and
present SemIf output as a suggestion in Journal/Inbox.
