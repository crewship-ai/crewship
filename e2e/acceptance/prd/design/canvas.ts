// Helpers of the "design" PRD scenario: a live Design canvas opened by several
// people, driven through the same REST routes the CLI (`crewship page design …`)
// and the agent's design_* tools use. Scenario-local; not a shared harness API.
import { expect, type APIRequestContext, type BrowserContext, type Page } from "@playwright/test"
import { trackPage } from "../created"

export interface DesignNode {
  id: string
  type: "frame" | "rect" | "text" | "image"
  name?: string
  text?: string
  parentId?: string
  x: number
  y: number
  width: number
  height: number
  fill?: string
}

export interface DesignState {
  page_id: string
  version: number
  latest_version?: number
  can_edit?: boolean
  origin?: { chat_id: string; agent_id: string } | null
  nodes: DesignNode[]
}

export interface DesignVersion {
  version: number
  message?: string
  actor?: string
  created_at?: string
}

/** Appends workspace_id, which every workspace-scoped route requires. */
export function scoped(path: string, workspaceId: string): string {
  return `${path}${path.includes("?") ? "&" : "?"}workspace_id=${encodeURIComponent(workspaceId)}`
}

async function json<T>(res: Awaited<ReturnType<APIRequestContext["get"]>>, what: string, status = 200): Promise<T> {
  const body = await res.text()
  expect(res.status(), `${what}: ${body.slice(0, 300)}`).toBe(status)
  return JSON.parse(body) as T
}

/** Signed-in user id of an API context (used to scope the UI's workspace choice). */
export async function sessionUserId(api: APIRequestContext): Promise<string> {
  const session = await json<{ user?: { id?: string } }>(await api.get("/api/auth/session"), "GET /api/auth/session")
  expect(session.user?.id, "session must carry a user id").toBeTruthy()
  return session.user!.id!
}

/**
 * The UI keeps the selected workspace per user in localStorage; set it before
 * the first navigation so the context opens the scenario's workspace.
 */
export async function selectWorkspace(ctx: BrowserContext, userId: string, workspaceId: string): Promise<void> {
  await ctx.addInitScript(({ key, workspace }) => {
    try {
      window.localStorage.setItem(key, workspace)
    } catch {
      /* storage unavailable: the app falls back to its default workspace */
    }
  }, { key: `crewship.workspaceId:${userId}`, workspace: workspaceId })
}

/** Turns the per-workspace `artifacts_design` server flag on (owner of that workspace). */
export async function enableDesign(api: APIRequestContext, workspaceId: string): Promise<void> {
  const res = await api.put(scoped("/api/v1/feature-flags/artifacts_design/override", workspaceId), { data: { enabled: true } })
  expect(res.status(), `enable artifacts_design: ${(await res.text()).slice(0, 300)}`).toBeLessThan(300)
}

export async function createDesign(api: APIRequestContext, workspaceId: string, slug: string, name: string): Promise<DesignState & { slug: string }> {
  const res = await api.post(scoped("/api/v1/pages/design", workspaceId), { data: { slug, name } })
  expect([200, 201], `create Design: ${(await res.text()).slice(0, 300)}`).toContain(res.status())
  trackPage(workspaceId, slug)
  const state = await getDesign(api, workspaceId, slug)
  return { ...state, slug }
}

export async function getDesign(api: APIRequestContext, workspaceId: string, ref: string, version?: number): Promise<DesignState> {
  const q = version ? `?version=${version}` : ""
  return json<DesignState>(await api.get(scoped(`/api/v1/pages/${encodeURIComponent(ref)}/design${q}`, workspaceId)), `GET design ${ref}`)
}

export async function listVersions(api: APIRequestContext, workspaceId: string, ref: string): Promise<{ versions: DesignVersion[]; latest_version?: number }> {
  return json(await api.get(scoped(`/api/v1/pages/${encodeURIComponent(ref)}/design/versions`, workspaceId)), `GET design versions ${ref}`)
}

/** POST …/design/operations — the route behind `crewship page design apply` and the agent's design_apply. */
export async function applyOperations(api: APIRequestContext, workspaceId: string, ref: string, operations: unknown[], message: string): Promise<{ version?: number; nodes: DesignNode[] }> {
  return json(await api.post(scoped(`/api/v1/pages/${encodeURIComponent(ref)}/design/operations`, workspaceId), { data: { operations, message } }), "apply Design operations")
}

/** POST …/design/snapshots — the route behind `crewship page design snapshot` and design_snapshot. */
export async function snapshot(api: APIRequestContext, workspaceId: string, ref: string, message: string): Promise<{ version: number }> {
  const res = await api.post(scoped(`/api/v1/pages/${encodeURIComponent(ref)}/design/snapshots`, workspaceId), { data: { message } })
  return json(res, "save Design version")
}

export async function grantWrite(api: APIRequestContext, workspaceId: string, pageId: string, userId: string): Promise<void> {
  const res = await api.put(scoped(`/api/v1/pages/@${pageId}/grants`, workspaceId), { data: { subject_type: "user", subject: userId, level: "write" } })
  expect(res.status(), `grant Edit: ${(await res.text()).slice(0, 300)}`).toBeLessThan(300)
}

export async function revokeWrite(api: APIRequestContext, workspaceId: string, pageId: string, userId: string): Promise<number> {
  const res = await api.delete(scoped(`/api/v1/pages/@${pageId}/grants?subject_type=user&subject=${encodeURIComponent(userId)}&level=write`, workspaceId))
  return res.status()
}

export async function deletePage(api: APIRequestContext, workspaceId: string, pageId: string): Promise<number> {
  const res = await api.delete(scoped(`/api/v1/pages/@${pageId}`, workspaceId))
  return res.status()
}

export function designUrl(slug: string, workspaceId: string): string {
  return `/artifacts?design=1&item=${encodeURIComponent(slug)}&workspace_id=${encodeURIComponent(workspaceId)}`
}

export const editor = (page: Page) => page.getByRole("region", { name: "Design editor" })

/** Texts rendered on the SVG canvas (what a person actually sees). */
export function canvasTexts(page: Page): Promise<string[]> {
  return page.locator('svg[aria-label="Design canvas"] text').allTextContents()
}

export const DESIGN_UI_DISABLED = "Design is not enabled."

/**
 * Opens the canvas and waits for the live connection. Returns false when the
 * build ships without the Design UI (the client-side build flag is off), so the
 * caller can decide whether that is a skip (quick) or a failure (full).
 */
export async function openCanvas(page: Page, slug: string, workspaceId: string): Promise<boolean> {
  await page.goto(designUrl(slug, workspaceId))
  const disabled = page.getByText(DESIGN_UI_DISABLED, { exact: true })
  await expect(editor(page).or(disabled)).toBeVisible({ timeout: 60_000 })
  if (await disabled.isVisible()) return false
  await expect(page.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 45_000 })
  return true
}

export async function expectOnCanvas(page: Page, text: string, timeout = 30_000): Promise<number> {
  const started = Date.now()
  await expect.poll(() => canvasTexts(page), { timeout, intervals: [250, 500, 1000] }).toContain(text)
  return Date.now() - started
}

/** Assistant tool calls of a chat, in order (names without the MCP server prefix). */
export async function chatToolCalls(api: APIRequestContext, workspaceId: string, chatId: string): Promise<string[]> {
  const body = await json<{ messages?: Array<{ role?: string; parts?: Array<{ type?: string; tool_name?: string }> }> }>(
    await api.get(scoped(`/api/v1/chats/${encodeURIComponent(chatId)}/messages`, workspaceId)),
    "GET chat messages",
  )
  const calls: string[] = []
  for (const m of body.messages ?? []) {
    if (m.role !== "assistant") continue
    for (const part of m.parts ?? []) {
      if (part.type === "tool_call" && part.tool_name) calls.push(part.tool_name.split("__").pop()!)
    }
  }
  return calls
}

export interface AgentRow {
  id: string
  slug: string
  status?: string
}

export async function findAgent(api: APIRequestContext, workspaceId: string, slug: string): Promise<AgentRow> {
  const body = await json<unknown>(await api.get(scoped("/api/v1/agents", workspaceId)), "GET agents")
  const list = (Array.isArray(body) ? body : ((body as { agents?: unknown[] }).agents ?? [])) as AgentRow[]
  const agent = list.find((a) => a.slug === slug)
  expect(agent, `agent ${slug} must exist in PRD_AGENT_WORKSPACE_ID`).toBeTruthy()
  return agent!
}

/** Waits until the agent is idle, so the dispatch is not refused as busy. */
export async function waitAgentIdle(api: APIRequestContext, workspaceId: string, slug: string, timeoutMs: number): Promise<string> {
  let status = ""
  await expect
    .poll(async () => {
      status = (await findAgent(api, workspaceId, slug)).status ?? ""
      return status.toUpperCase()
    }, { timeout: timeoutMs, intervals: [5_000, 15_000] })
    .toBe("IDLE")
  return status
}
