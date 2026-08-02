-- Re-target credential escalations that are still addressed to MANAGER.
--
-- crewship#1671 aligned the audience with the authority: a keeper credential
-- escalation is resolved with roleManage — OWNER or ADMIN — so addressing the
-- inbox item at MANAGER showed every manager a production-credential decision
-- they could not take. Inbox visibility is hierarchical, so "MANAGER" means
-- MANAGER and everyone above.
--
-- That fix only governs rows written after it. On an upgraded instance every
-- escalation raised BEFORE it keeps its old audience, and those are exactly the
-- interesting ones: an open request naming a production credential, its
-- justification, its risk score and the agent that asked. The credential's VALUE
-- was never in there — the judge does not receive it and the card does not
-- render it — but the shape of the estate is, and that is what somebody choosing
-- a target is looking for. A fix that covers the next leak and not the one
-- already sitting in people's inboxes is half a fix.
--
-- Four deliberate limits, each pinned by a test in
-- ../migrate_keeper_inbox_audience_test.go and each mutation-checked:
--
--   Resolved rows are left alone. They are history; re-addressing a decision
--   somebody already made rewrites who it was for, for no gain — nobody is being
--   asked to act on it.
--
--   Only CREDENTIAL requests move. keeper_requests is not only those:
--   request_type also admits skill_review, behavior, memory_health and
--   negative_learning, and five sites in keeper_phase2.go write inbox
--   escalations for them at MANAGER — legitimately, since none names a
--   credential. `access`/`execute` are the two types that do.
--
--   Escalations-backed rows are a different flow with their own audience, and
--   they carry an escalations id in source_id rather than a keeper request id.
--
--   A narrower audience is never widened. An OWNER-only row stays OWNER-only,
--   and target_user_id is untouched, so a named security contact stays named.
--   This narrows a role fanout; it is not a re-address.

UPDATE inbox_items
   SET target_role = 'ADMIN',
       updated_at  = updated_at
 WHERE kind = 'escalation'
   AND state != 'resolved'
   AND target_role = 'MANAGER'
   AND source_id IN (
         SELECT id FROM keeper_requests
          WHERE request_type IN ('access','execute')
       );
