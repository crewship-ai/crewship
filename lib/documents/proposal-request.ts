/**
 * Asking an agent for a proposal on a schema-4 Doc (#5137). The Docs view
 * hands the chat one target: the Page and the revision and generation the
 * person was looking at. The chat sends it with the person's message as
 * `metadata.document_iteration_intent` (schema 4 = propose only). The server
 * re-checks everything: a private chat, Edit for the person, and on a shared
 * Doc a write grant naming the agent. Nothing here grants anything.
 */
export interface ProposalRequestTarget { pageId: string; rev: number; generation: number }

export interface ProposalIterationIntent {
  submission_id: string
  page_id: string
  schema: 4
  rev: number
  generation: number
}

const PARAM = /^([A-Za-z0-9_-]{1,128})\.(\d{1,15})\.(\d{1,15})$/

export function proposalRequestParam(target: ProposalRequestTarget): string {
  return `${target.pageId}.${target.rev}.${target.generation}`
}

/** The target from a `propose` query value, or null when it is not exactly one. */
export function parseProposalRequest(value: string | null | undefined): ProposalRequestTarget | null {
  const match = value ? PARAM.exec(value) : null
  if (!match) return null
  const rev = Number(match[2]), generation = Number(match[3])
  return Number.isSafeInteger(rev) && Number.isSafeInteger(generation) ? { pageId: match[1], rev, generation } : null
}

/** The chat address that asks for a proposal, the instruction as an unsent draft. */
export function proposalChatHref(workspaceId: string, target: ProposalRequestTarget, prompt: string): string {
  const query = new URLSearchParams({ workspace_id: workspaceId, propose: proposalRequestParam(target) })
  if (prompt) { query.set("prompt", prompt); query.set("draft", "1") }
  return `/chat?${query}`
}

export function proposalIntent(target: ProposalRequestTarget, submissionId: string): ProposalIterationIntent {
  return { submission_id: submissionId, page_id: target.pageId, schema: 4, rev: target.rev, generation: target.generation }
}
