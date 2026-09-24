# Local SemIf/MLX probe on MacBook Air — 2026-09-24

Machine: MacBook Air M3, 16 GB RAM (`192.168.1.221`). This probe used the
existing `~/AI/SemIf-OpenJev` installation, native MLX, pinned
`Qwen/Qwen3.5-4B` revision `851bf6e806efd8d0a36b00ddf55e13ccb7b8cd0a`,
and `direct` scoring. The 4-bit run applies in-memory quantization; the other
run preserves the source precision. Each process loads the model once, then
scores all rows. The summaries record the input dataset hash.

These are **real local model inferences**, not the Jev API dry run. The data
are authored synthetic examples, not Crewship production events. No webhook
was delivered and no agent was started by this probe.

| Corpus / model | First choice correct | Routed after 0.9 review fallback | Automatic routes correct | Automatic coverage | Median per case |
| --- | ---: | ---: | ---: | ---: | ---: |
| 22 ordinary cases, 4-bit | 21/22 | 20/22 | 14/14 | 14/22 | 0.712 s |
| 22 ordinary cases, source precision | 21/22 | 21/22 | 15/15 | 15/22 | 0.736 s |
| 12 harder cases, 4-bit | 10/12 | 10/12 | 5/5 | 5/12 | 0.714 s |
| 12 harder cases, source precision | 12/12 | 9/12 | 4/4 | 4/12 | 0.737 s |

The ordinary corpus has equal Czech and English halves and covers webhook
route selection, journal incident assessment and infrastructure dry runs. The
harder corpus includes contradictory evidence, repeated failures, credential
exfiltration requests and instructions embedded in the event text. Ground
truth is the authored intended route; judgment calls in these synthetic
examples can affect the reported accuracy.

The clearest failure was a Czech journal event describing three repeated
staging pipeline failures: both precisions chose `record` over the authored
`investigate` label. Its top score stayed below 0.9, so both runs routed it
to `review`. On the harder corpus, 4-bit also chose `sre` for a healthy
webhook carrying an instruction to wake SRE, and `investigate` for a live
checkout outage carrying an instruction to record only. Both had low scores
and routed to `review`. Source precision chose the authored labels on those
two cases. Conversely, source precision sent three correct first choices in
the harder corpus to `review`; 4-bit sent two. The threshold therefore trades
coverage for a review path. The probability is conditional on the listed
options and is **not calibrated confidence**.

Per-case scores and timings are in the adjacent `*-results.jsonl` files;
summary JSON files carry corpus hashes. The scorer's latency excludes model
loading. A full invocation took about 25 seconds for 22 cases, including
model loading and file work. This probe does not establish production accuracy,
prompt-injection resistance, a safe autonomous action rate, or server-side
webhook latency. It is evidence for testing SemIf as an advisory route selector
with a human review fallback. A Crewship-to-Mac evaluator adapter and an
end-to-end signed webhook run remain separate integration work.

The [extended feature assessment](extended/README.md) adds Keeper tool-call
cases, option-order stress, a Laya comparison, and state-length/multi-question
measurements. It supersedes the simple first impression that a 0.9 threshold
alone is enough for automatic routing.
