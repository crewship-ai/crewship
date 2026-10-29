/**
 * Creating a workbook Page: `POST /api/v1/workbooks` stages an empty personal
 * private Page the person then fills. The slug is the server's idempotency
 * key, so a retry with the same name and slug returns the same empty Page.
 */

import { apiFetch } from "@/lib/api-fetch"
import { flagEnabled, WORKBOOK_EDITOR_FLAG } from "./editor-client"

export interface CreatedWorkbook { pageId: string; slug: string; name: string }

export class WorkbookCreateError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = "WorkbookCreateError"
    this.status = status
  }
}

export async function createWorkbook(workspaceId: string, input: { name: string; slug: string }, send: typeof apiFetch = apiFetch): Promise<CreatedWorkbook> {
  const response = await send(`/api/v1/workbooks?${new URLSearchParams({ workspace_id: workspaceId })}`, {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: input.name, slug: input.slug }),
  })
  const body = await response.json().catch(() => null)
  if (!response.ok) throw new WorkbookCreateError(response.status, typeof body?.error === "string" && body.error ? body.error : "The workbook could not be created.")
  if (typeof body?.page_id !== "string" || !body.page_id || typeof body?.slug !== "string") throw new WorkbookCreateError(0, "The workbook creation was not confirmed.")
  return { pageId: body.page_id, slug: body.slug, name: typeof body.name === "string" ? body.name : input.name }
}

/**
 * Whether a person may start a Sheet: the workspace's `gridcraft_editor` flag.
 * Without the live editor a new workbook would be a Page nobody can type in.
 * Note: the server opens editor sessions only from revision 1, i.e. after an
 * import; a blank workbook stays empty until one lands.
 */
export async function workbookCreationEnabled(workspaceId: string, signal?: AbortSignal, send: typeof apiFetch = apiFetch): Promise<boolean> {
  const response = await send(`/api/v1/feature-flags?${new URLSearchParams({ workspace_id: workspaceId })}`, { signal, cache: "no-store" })
  if (!response.ok) return false
  return flagEnabled(await response.json().catch(() => null), WORKBOOK_EDITOR_FLAG)
}
