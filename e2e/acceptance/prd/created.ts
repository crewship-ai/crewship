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
