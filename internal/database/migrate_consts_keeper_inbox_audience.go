package database

// Re-target credential escalations that are still addressed to MANAGER.
//
// crewship#1671 aligned the audience with the authority: a keeper credential
// escalation is resolved with roleManage — OWNER or ADMIN — so addressing the
// inbox item at MANAGER showed every manager a production-credential decision
// they could not take. Inbox visibility is hierarchical, so "MANAGER" means
// MANAGER and everyone above.
//
// That fix only governs rows written after it. On an upgraded instance every
// escalation raised before it keeps its old audience, and those are exactly the
// interesting ones: an open request naming a production credential, its
// justification, its risk score and the agent that asked. The credential's VALUE
// was never in there — the judge does not receive it and the card does not
// render it — but the shape of the estate is, and that is what somebody choosing
// a target is looking for. Leaving the old rows behind would make the fix apply
// to the next leak and not the one already sitting in people's inboxes.
//
// Three things this deliberately does NOT do:
//
//   - Resolved rows are left alone. They are history; re-addressing a decision
//     somebody already made would rewrite who it was for.
//   - Only rows whose source_id is a keeper_requests id are touched. An
//     escalations-backed row is a different flow with its own audience, and a
//     skill review legitimately targets MANAGER — it names no credential.
//   - target_user_id is untouched. Where a security contact was named, they stay
//     named; this widens nothing and narrows only the role fanout.

// keeperInboxAudienceVersion is this migration's slice version, named so the
// test can land the schema one step before it and seed the legacy rows this
// fills — the only way to test a backfill is to have something to fill.
const keeperInboxAudienceVersion = 20260802160000

const migrationKeeperInboxAudience = `
UPDATE inbox_items
   SET target_role = 'ADMIN',
       updated_at  = updated_at
 WHERE kind = 'escalation'
   AND state != 'resolved'
   AND target_role = 'MANAGER'
   AND source_id IN (SELECT id FROM keeper_requests);
`
