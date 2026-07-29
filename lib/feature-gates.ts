/**
 * Surfaces that exist in the codebase but are not offered to users yet.
 *
 * A gate here means "the code works, the product story does not". Deleting the
 * implementation would cost us the work; leaving the UI up promises a workflow
 * that has no supporting product around it. So the code stays compiled and
 * tested, and the entry point is one boolean.
 *
 * Flip a gate back on in the same commit that ships the thing it needs.
 */

/**
 * Waking an agent from an outside system (webhook signing secret, hooks).
 *
 * Off because only two ways of giving an agent work are actually finished —
 * issues and routines — and both are inside the product. A third-party trigger
 * asks someone to wire up an external system against a surface with no docs
 * page, no delivery log, no retry story and no way to see a fire land. The
 * server side keeps working for anyone who already configured it; it simply
 * stops being advertised.
 *
 * Turn back on with: a webhook deliveries view, docs, and a way to test a fire
 * from the UI.
 */
export const AGENT_EXTERNAL_TRIGGERS = false

/**
 * Self-improving mode — the per-agent switch deciding whether a keeper lesson
 * or a persona change applies immediately or waits for approval.
 *
 * Off because only half of it is demonstrable. The switch itself is proven
 * end to end (PATCH persists with an audit trail, the reload reads it back),
 * and the gate is proven at the handler: with it on a lesson lands in
 * lessons.md, with it off a blocking inbox item is queued instead — Go tests
 * assert both against the real filesystem.
 *
 * What nobody has shown is that a LIVE agent run ever reaches those handlers.
 * That chain runs sidecar → POST /api/v1/internal/keeper/behavior → evaluator
 * → lesson proposal, it is internal-auth only, there is no CLI for it, and no
 * harness suite covers it. Offering a switch for a behaviour we cannot
 * demonstrate is the same promise the external-trigger card was making.
 *
 * Turn back on with: a test-harness suite that drives a real run through to a
 * lesson (and to an inbox item with the flag off), plus the missing CLI for
 * GET/PATCH /api/v1/agents/{id}/learning — the endpoints have no command
 * today, which breaks the repo's API↔CLI parity rule and is why this could
 * not be verified outside a browser in the first place.
 */
export const AGENT_SELF_LEARNING = false
