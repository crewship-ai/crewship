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
