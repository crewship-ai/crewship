// Registry of the pages (including Designs and Projects) a test creates, so
// the fixture deletes them after the test even when the test failed or its
// own session was revoked. Creators call trackPage(); PrdScenario.dispose()
// drains the list with a fresh login. Titles carry the "PRD " prefix and slugs
// "prd-", which is what `run.mjs --cleanup` matches for older leftovers.
export interface CreatedPage {
  workspaceId: string
  /** Page id (without the "@"), or a slug when the id is not known yet. */
  ref: string
}

const created: CreatedPage[] = []

export function trackPage(workspaceId: string, ref: string): void {
  if (workspaceId && ref) created.push({ workspaceId, ref })
}

/** Takes (and forgets) everything tracked so far, newest first. */
export function takeTrackedPages(): CreatedPage[] {
  return created.splice(0).reverse()
}

/**
 * Names a full agent workspace. The seeded agent works only in the workspace
 * that holds its model credential, which in practice is the shared demo
 * workspace; other people's pages count against the same caps there (100 pages
 * per workspace -> HTTP 422, Project/Design history budget -> HTTP 413). Those
 * are environment capacity, not product failures: say so and say what to do.
 */
export function capacityHint(status: number, body: string): string {
  if (status !== 422 && status !== 413) return ""
  if (!/limit|quota|capacity|full|\bcap\b|pages_/i.test(body)) return ""
  return (
    ` [the agent's workspace (PRD_AGENT_WORKSPACE_ID) is full, HTTP ${status}: this is capacity, not the scenario. ` +
    "Free pages there that you own, or point PRD_AGENT_SLUG/PRD_AGENT_WORKSPACE_ID at a dedicated agent workspace - see \"Agent workspace\" in docs/development/prd-acceptance.md]"
  )
}
