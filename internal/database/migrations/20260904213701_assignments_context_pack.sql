-- assignments.context_pack_compaction / context_pack_tokens (PRD-ISSUES-AND-
-- ROUTINES-2026 §11.4, work package B5, #2345).
--
-- F14: "Compaction is real, unpersisted, and silently degrades" — true of
-- buildConversationContextWithStats (the chat-bridge conversation history)
-- and, as of B5, would be equally true of the NEW §11.1 context-pack
-- assembly (issue_context_pack.go) if this column did not exist: the
-- unread-delta rendering (mission_activity rows since last_consumed_seq)
-- has its own overflow decision — render in full ("fit"), compact older
-- rows to one terse line each ("summarized", §11.1 item 4's "never dropped
-- silently"), or, in the genuinely pathological case where even that does
-- not fit the budget, drop the oldest rows outright ("truncated"). §11.4's
-- fourth metrics row — "share of runs whose context was truncated ...
-- reported, alarmed above a threshold" — is unmeasurable without a
-- per-run field to aggregate; a journal entry alone (this migration adds
-- none) has the same blind spot F14 names for the conversation path: it
-- answers "did this run's compaction ever fire" only by re-scanning
-- unstructured entries, not by a query CI or an operator can point a
-- threshold at.
--
-- Nullable, no default: NULL means no context pack was assembled for this
-- run (the issue_agent_sessions flag was off, or this run predates B5) —
-- deliberately distinct from 'fit', which means a pack WAS assembled and
-- fit inside budget without any compaction decision at all.
ALTER TABLE assignments ADD COLUMN context_pack_compaction TEXT
    CHECK (context_pack_compaction IS NULL OR context_pack_compaction IN ('fit', 'summarized', 'truncated'));

-- The §11.4 row-3 metric itself — "assembled pack size in tokens ...
-- capped, not minimised" — needs a number to assert the cap against test-
-- by-test AND to alarm on in production if a future change quietly lets it
-- grow with thread length again. Recorded once, at dispatch time, from the
-- same tokenutil.EstimateTokens heuristic every other budget in this
-- codebase uses (internal/tokenutil/estimate.go) — not re-measured later,
-- since the pack a run received never changes after dispatch.
ALTER TABLE assignments ADD COLUMN context_pack_tokens INTEGER;
