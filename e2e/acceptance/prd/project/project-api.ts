// Helpers of the "project" PRD scenario (milestone B): an artifact Project with
// three linked screens, element comments, versions, version sharing and
// restore. Everything goes through the public HTTP API or the browser; the
// origin, workspace and accounts come from the harness.
import { expect, type APIRequestContext, type BrowserContext, type FrameLocator, type Page } from "@playwright/test"
import { randomUUID } from "node:crypto"

export interface ProjectFile {
  path: string
  media_type: string
  content: string
}

export interface ProjectVersion {
  version: number
  restored_from?: number | null
  message?: string | null
}

export interface ProjectComment {
  id: string
  version: number
  path: string
  selector?: string
  quote?: string
  text: string
  task_id?: string | null
  replies?: Array<{ version: number; text: string }>
}

export interface ProjectSnapshot {
  page_id: string
  slug: string
  latest_version: number
  shared_version: number
  can_write?: boolean
  can_share?: boolean
  can_restore?: boolean
  can_send_to_agent?: boolean
  versions: ProjectVersion[]
  comments: ProjectComment[]
  manifest: { version: number; entrypoint: string; files: Array<{ path: string; content?: string }> }
}

export const SCREENS = ["index.html", "menu.html", "kontakt.html"] as const
export const HERO = "#artifact-hero-heading"

/** Unique slug per run, so reruns against the same origin never collide. */
export function projectSlug(prefix = "prd-project"): string {
  return `${prefix}-${randomUUID().replaceAll("-", "").slice(0, 10)}`
}

const nav = '<nav><a href="index.html">Home</a> · <a href="menu.html">Menu</a> · <a href="kontakt.html">Contact</a></nav>'

/** Three linked HTML screens; `heading` is the hero text of the entry screen. */
export function threeScreens(heading: string): ProjectFile[] {
  const html = (body: string) => `<!doctype html><html><body>${nav}<main>${body}</main></body></html>`
  return [
    { path: "index.html", media_type: "text/html", content: html(`<h1 id="artifact-hero-heading">${heading}</h1><p id="artifact-hero-text">Fresh coffee every morning.</p>`) },
    { path: "menu.html", media_type: "text/html", content: html('<h1 id="artifact-menu-heading">Menu</h1><ul id="artifact-menu-list"><li>Espresso 45</li><li>Cappuccino 65</li><li>Flat white 75</li></ul>') },
    { path: "kontakt.html", media_type: "text/html", content: html('<h1 id="artifact-contact-heading">Contact</h1><p id="artifact-contact-address">1 Example Street</p><p>hello@example.test</p>') },
  ]
}

export function wsQuery(workspaceId: string): string {
  return `workspace_id=${encodeURIComponent(workspaceId)}`
}

function endpoint(slug: string, workspaceId: string, sub = ""): string {
  return `/api/v1/pages/${encodeURIComponent(slug)}/artifact${sub}?${wsQuery(workspaceId)}`
}

async function body(res: { text(): Promise<string> }): Promise<string> {
  return (await res.text()).slice(0, 400)
}

/**
 * Projects sit behind the per-workspace "artifact_projects" flag. A fresh QA
 * workspace inherits the instance default (off on a new server), so the
 * scenario turns it on for its own workspace, as a workspace manager would.
 */
export async function enableProjects(api: APIRequestContext, workspaceId: string): Promise<"already" | "enabled"> {
  const available = async () => {
    const res = await api.get(`/api/v1/pages/artifacts/availability?${wsQuery(workspaceId)}`)
    expect(res.status(), `project availability: ${await body(res)}`).toBe(200)
    return ((await res.json()) as { enabled?: boolean }).enabled === true
  }
  if (await available()) return "already"
  const res = await api.put(`/api/v1/feature-flags/artifact_projects/override?${wsQuery(workspaceId)}`, { data: { enabled: true } })
  expect(res.status(), `enable artifact_projects for the QA workspace: ${await body(res)}`).toBeLessThan(300)
  expect(await available(), "projects are available after enabling the workspace flag").toBe(true)
  return "enabled"
}

export async function createProject(api: APIRequestContext, workspaceId: string, slug: string, files: ProjectFile[]): Promise<ProjectSnapshot> {
  const res = await api.post(`/api/v1/pages/artifacts?${wsQuery(workspaceId)}`, {
    data: { slug, name: `PRD project ${slug.slice(-10)}`, entrypoint: "index.html", files, message: "Three linked screens" },
  })
  expect(res.status(), `create project: ${await body(res)}`).toBe(201)
  return snapshot(api, workspaceId, slug)
}

export async function snapshot(api: APIRequestContext, workspaceId: string, slug: string, version?: number): Promise<ProjectSnapshot> {
  const url = version ? `${endpoint(slug, workspaceId)}&version=${version}` : endpoint(slug, workspaceId)
  const res = await api.get(url)
  expect(res.status(), `read project: ${await body(res)}`).toBe(200)
  return (await res.json()) as ProjectSnapshot
}

/** Snapshot or null while the project does not exist yet (agent still working). */
export async function trySnapshot(api: APIRequestContext, workspaceId: string, slug: string): Promise<ProjectSnapshot | null> {
  const res = await api.get(endpoint(slug, workspaceId))
  if (res.status() === 404) return null
  expect(res.status(), `read project: ${await body(res)}`).toBe(200)
  return (await res.json()) as ProjectSnapshot
}

export async function publish(api: APIRequestContext, workspaceId: string, slug: string, baseVersion: number, files: ProjectFile[], message: string) {
  return api.post(endpoint(slug, workspaceId, "/versions"), { data: { base_version: baseVersion, entrypoint: "index.html", files, message } })
}

export async function reply(api: APIRequestContext, workspaceId: string, slug: string, commentId: string, version: number, text: string) {
  const res = await api.post(endpoint(slug, workspaceId, `/comments/${encodeURIComponent(commentId)}/reply`), { data: { version, text } })
  expect(res.status(), `reply: ${await body(res)}`).toBe(200)
}

export async function grantRead(api: APIRequestContext, workspaceId: string, slug: string, userId: string) {
  const res = await api.put(`/api/v1/pages/${encodeURIComponent(slug)}/grants?${wsQuery(workspaceId)}`, {
    data: { subject_type: "user", subject: userId, level: "read" },
  })
  expect(res.status(), `grant read: ${await body(res)}`).toBe(200)
}

export async function deletePage(api: APIRequestContext, workspaceId: string, slug: string): Promise<number> {
  const res = await api.delete(`/api/v1/pages/${encodeURIComponent(slug)}?${wsQuery(workspaceId)}`)
  return res.status()
}

/** The hero heading text in a stored manifest (no browser needed). */
export function heroOf(snap: ProjectSnapshot): string | null {
  const index = snap.manifest.files.find((f) => f.path === "index.html")?.content ?? ""
  return index.match(/<h1[^>]*id="artifact-hero-heading"[^>]*>([^<]*)/)?.[1] ?? index.match(/<h1[^>]*>([^<]*)/)?.[1] ?? null
}

/** Every screen links to the two others with relative links. */
export function linkedScreens(snap: ProjectSnapshot): Record<string, string[]> {
  const out: Record<string, string[]> = {}
  for (const f of snap.manifest.files) {
    if (!f.path.endsWith(".html")) continue
    out[f.path] = [...(f.content ?? "").matchAll(/href="([^"#?]+\.html)"/g)].map((m) => m[1].replace(/^\.\//, ""))
  }
  return out
}

/** Points the web app of `context` at `workspaceId` (the app keeps the choice per person). */
export async function pinWorkspace(context: BrowserContext, workspaceId: string): Promise<string> {
  const res = await context.request.get("/api/auth/session")
  const session = (await res.json()) as { user?: { id?: string } }
  const userId = session.user?.id
  if (!userId) throw new Error("browser context has no session")
  await context.addInitScript(({ ws, id }) => {
    try {
      localStorage.setItem(`crewship.workspaceId:${id}`, ws)
    } catch {
      // storage unavailable: the workspace_id query parameter still applies
    }
  }, { ws: workspaceId, id: userId })
  return userId
}

export function preview(page: Page): FrameLocator {
  return page.frameLocator('iframe[title^="Preview of"]')
}

/** Opens the project in the Artifacts library and waits for its preview. */
export async function openProject(page: Page, workspaceId: string, slug: string): Promise<void> {
  await page.goto(`/artifacts?item=${encodeURIComponent(slug)}&${wsQuery(workspaceId)}`)
  await expect(preview(page).locator("h1").first()).toBeVisible({ timeout: 60_000 })
}

export async function openHistory(page: Page) {
  await page.getByRole("tab", { name: /History/ }).click()
  const history = page.getByRole("region", { name: "Version history" })
  await expect(history).toBeVisible()
  return history
}

/** The history row of one version. */
export function historyRow(page: Page, version: number) {
  return page
    .getByRole("region", { name: "Version history" })
    .locator("li")
    .filter({ has: page.locator('[data-slot="version-number"]', { hasText: new RegExp(`^v${version}$`) }) })
}

/** Selects the hero element in the preview and fills the comment composer. */
export async function commentOnHero(page: Page, text: string, version: number): Promise<string> {
  await page.getByRole("button", { name: "Select", exact: true }).click()
  await preview(page).locator(HERO).click()
  const form = page.getByRole("form", { name: "New comment" })
  await expect(form).toBeVisible()
  const anchor = await form.locator("b").first().innerText()
  expect(anchor, "the composer names the selected element and version").toContain(HERO)
  expect(anchor).toContain(`v${version}`)
  await form.getByRole("textbox", { name: "Comment" }).fill(text)
  return anchor
}

/** Thrown from a `waitFor` check to stop waiting at once. */
export class FatalWait extends Error {}

/** Polls until `check` returns a value, or fails with the last state after `timeoutMs`. */
export async function waitFor<T>(what: string, timeoutMs: number, intervalMs: number, check: () => Promise<T | null | undefined>): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let last: unknown = null
  for (;;) {
    try {
      const v = await check()
      if (v !== null && v !== undefined) return v
    } catch (err) {
      if (err instanceof FatalWait) throw err
      last = err
    }
    if (Date.now() > deadline) throw new Error(`timed out after ${Math.round(timeoutMs / 1000)} s waiting for ${what}${last ? `: ${String(last)}` : ""}`)
    await new Promise((r) => setTimeout(r, intervalMs))
  }
}
