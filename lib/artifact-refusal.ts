/** Typed code the server attaches to a chat `error` event when a Project send is
 *  refused because the agent's crew is outside the workspace artifact policy. */
export const ARTIFACT_CREW_NOT_ALLOWED = "artifact_crew_not_allowed"

/** The specific text shown instead of a generic save failure. */
export const ARTIFACT_CREW_NOT_ALLOWED_MESSAGE = "This agent's crew is not allowed to create artifact projects in this workspace"

/** The chat's refusal reason for a busy bounce the server did not save (agent_busy without a message_id). */
export const SEND_REFUSED_BUSY = "busy"

/** Shown when the composer gets that message back. */
export const BUSY_NOT_SENT_MESSAGE = "Not sent: the agent is busy with another run. Your message is back here; send it once the agent is free."

/** Any other send the server refused before saving it: the composer gives the message back with the server's words. */
export const SEND_REFUSED = "refused"
