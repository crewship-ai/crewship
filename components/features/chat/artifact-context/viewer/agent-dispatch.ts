import type { ArtifactComment } from "../projects/types"

/** What the viewer asks its chat to send so the agent works on one saved comment. */
export interface ArtifactAgentRequest { pageId: string; comment: ArtifactComment }

/**
 * The chat message that starts the agent on a comment saved with
 * send_to_agent. The server admits the run only through the
 * artifact_project_intent bound to that comment and base version; the
 * submission id is stable per comment, so a retry is the same submission.
 */
export function artifactCommentMessage(pageId: string, comment: Pick<ArtifactComment, "id" | "version" | "path" | "selector" | "quote" | "text">): { text: string; metadata: Record<string, unknown> } {
  return {
    text: `Update the artifact project according to this exact comment. Page: @${pageId}. Base version: ${comment.version}. File: ${comment.path}. Element: ${comment.selector}. Selected text: ${comment.quote}\nRequested change: ${comment.text}\nPublish a new version with base_version ${comment.version}, then reply to comment ${comment.id} with the new version and what changed. If the anchor no longer matches, ask for clarification.`,
    metadata: { artifact_project_intent: { submission_id: `artifact-comment-${comment.id}`, page_id: pageId, comment_id: comment.id, base_version: comment.version } },
  }
}
