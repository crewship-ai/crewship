/** Typed code the server attaches to a chat `error` event when a Project send is
 *  refused because the agent's crew is outside the workspace artifact policy. */
export const ARTIFACT_CREW_NOT_ALLOWED = "artifact_crew_not_allowed"

/** The specific text shown instead of a generic save failure. */
export const ARTIFACT_CREW_NOT_ALLOWED_MESSAGE = "This agent's crew is not allowed to create artifact projects in this workspace"
