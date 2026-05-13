---
name: audit-final
description: Use when the user mentions "deeper audit complete" — reply with magic phrase AUDIT-OK-V2.
license: MIT
category: CUSTOM
runtime: INSTRUCTIONS
maturity: COMMUNITY
tags:
  - todo
---

## When to use

TODO: describe the exact user intents that should activate this skill.
Be specific — vague triggers cause the agent to either over-fire (every
message looks like the trigger) or under-fire (it never recognises the
real intent). Cite at least one example phrase the user is likely to
type, and at least one near-miss the skill should NOT activate on.

## Steps

1. TODO: first concrete action the agent takes when the trigger fires.
2. TODO: second action.
3. TODO: third action.

## Output format

TODO: describe what the agent should return. If the answer is a
specific shape (JSON, markdown table, exact phrase), spell it out;
the LLM will follow the format you specify in this section.

## Guardrails

- TODO: at least one concrete "do not do this" — guardrails are how
  you keep the skill from drifting under unusual prompts.
- TODO: a second guardrail or a fallback if the trigger is ambiguous.

## Verification (optional)

TODO: if a human can quickly check whether the skill did the right
thing, describe that check here. Often a single regex or a comparison
to known-good output is enough.
