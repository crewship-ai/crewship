import { schemaAllows, type ProposalSchema } from "./contracts"

/**
 * Asking an agent for a proposal on a Doc whose schema supports proposals
 * (#5137, #5233). The Docs view hands the chat one target: the Page, its own
 * schema, and the revision and generation the person was looking at. The chat
 * sends it with the person's message as `metadata.document_iteration_intent`
 * (a proposal schema = propose only). The server re-checks everything: the
 * Doc's stored schema, a private chat, Edit for the person, and on a shared
 * Doc a write grant naming the agent. Nothing here grants anything.
 */
export interface ProposalRequestTarget { pageId: string; rev: number; generation: number; schema: ProposalSchema }

export interface ProposalIterationIntent {
  submission_id: string
  page_id: string
  schema: ProposalSchema
  rev: number
  generation: number
}

/** The schema a proposal request names, or undefined when the Doc's schema has no proposals. */
export function proposalSchema(schema: number | undefined): ProposalSchema | undefined {
  return schema !== undefined && schemaAllows(schema, "propose") && (schema === 4 || schema === 5) ? schema : undefined
}

// page.rev.generation.schema; links from before #5233 have no schema and meant schema 4.
const PARAM = /^([A-Za-z0-9_-]{1,128})\.(\d{1,15})\.(\d{1,15})(?:\.(\d{1,2}))?$/

export function proposalRequestParam(target: ProposalRequestTarget): string {
  return `${target.pageId}.${target.rev}.${target.generation}.${target.schema}`
}

/** The target from a `propose` query value, or null when it is not exactly one. */
export function parseProposalRequest(value: string | null | undefined): ProposalRequestTarget | null {
  const match = value ? PARAM.exec(value) : null
  if (!match) return null
  const rev = Number(match[2]), generation = Number(match[3])
  const schema = proposalSchema(match[4] === undefined ? 4 : Number(match[4]))
  return Number.isSafeInteger(rev) && Number.isSafeInteger(generation) && schema ? { pageId: match[1], rev, generation, schema } : null
}

/** The chat address that asks for a proposal, the instruction as an unsent draft. */
export function proposalChatHref(workspaceId: string, target: ProposalRequestTarget, prompt: string): string {
  const query = new URLSearchParams({ workspace_id: workspaceId, propose: proposalRequestParam(target) })
  if (prompt) { query.set("prompt", prompt); query.set("draft", "1") }
  return `/chat?${query}`
}

export function proposalIntent(target: ProposalRequestTarget, submissionId: string): ProposalIterationIntent {
  return { submission_id: submissionId, page_id: target.pageId, schema: target.schema, rev: target.rev, generation: target.generation }
}
