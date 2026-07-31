-- How long the credential judge may take, settable at runtime.
--
-- Found by configuring a judge on dev1 by hand. The gatekeeper capped its model
-- call at a hardcoded 5 seconds (audit M4, so an unresponsive Ollama could not pin
-- a goroutine). On the dev box, qwen2.5:7b takes ~12s to return a verdict. The
-- result is the worst shape a security control can have:
--
--   `crewship keeper judge test` says "the judge works" — it measures with its own,
--   longer budget — and then EVERY credential request fails closed with
--   "Keeper LLM unavailable: context deadline exceeded". The operator configured
--   the thing correctly, was told it worked, and nothing works.
--
-- The 5s figure was never wrong for a 3B classifier; it was wrong as a constant.
-- Model choice is the operator's (`keeper config set --model`), the machine is
-- theirs, and only they can know what their hardware returns in. So the budget
-- moves next to the model it applies to, in the same singleton row, with the same
-- inherit semantics: NULL means "use the built-in", which is now 20s rather than 5.
--
-- Why 20s and not 5s: both outcomes block the requesting agent, but one of them
-- blocks it with a WRONG answer. A false DENY on a legitimate request teaches an
-- operator that Keeper is broken and to turn it off, which costs more than the
-- extra 15 seconds of a slow ALLOW. The call is still bounded — an unresponsive
-- endpoint cannot hang a request forever, which is what M4 was actually about.
--
-- The judge test now measures against this budget and says so when the model is
-- too slow for it, so "the judge works" means "works in the credential path".

ALTER TABLE keeper_runtime_settings
    ADD COLUMN judge_timeout_ms INTEGER
    CHECK (judge_timeout_ms IS NULL OR (judge_timeout_ms >= 1000 AND judge_timeout_ms <= 120000));
