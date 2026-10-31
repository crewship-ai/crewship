// Project (artifact) helpers for the outage-lostack scenario. Everything goes
// through the public Pages API and the Project page; no route mocks of the
// server's answers: a "lost" reply is a reply the server really sent and the
// browser never received.
import type { APIRequestContext, BrowserContext, Page, Request } from "@playwright/test"
import { expect } from "@playwright/test"

export interface ProjectFile {
  path: string
  media_type: string
  content: string
}

export interface ProjectRef {
  pageId: string
  slug: string
  workspaceId: string
}

export interface ProjectSnapshot {
  latest_version: number
  versions: { version: number }[]
  manifest: { version: number; entrypoint: string; files: ProjectFile[] }
}

export const files = (slug: string, label: string): ProjectFile[] => [
  {
    path: "index.html",
    media_type: "text/html",
    content: `<!doctype html><html><head><link rel="stylesheet" href="style.css"></head><body><h1 id="qa-head">${slug} ${label}</h1><p id="qa-para">Outage acceptance paragraph.</p></body></html>`,
  },
  { path: "style.css", media_type: "text/css", content: "body{font-family:sans-serif}h1{color:#123456}" },
]

const q = (workspaceId: string, extra: Record<string, string> = {}) =>
  "?" + new URLSearchParams({ workspace_id: workspaceId, ...extra }).toString()

/**
 * Projects are a pilot: an installation enables them with an environment switch
 * or per workspace with the `artifact_projects` flag. When the API reports them
 * off, the scenario turns the flag on for its own disposable QA workspace only.
 * In quick mode it may first create the (disabled) flag on the throwaway test
 * server; on a deployment it never creates instance-wide flags.
 */
async function enableProjects(api: APIRequestContext, workspaceId: string, mayCreateFlag: boolean): Promise<string> {
  const override = () => api.put(`/api/v1/feature-flags/artifact_projects/override${q(workspaceId)}`, { data: { enabled: true } })
  let res = await override()
  if (res.status() === 404 && mayCreateFlag) {
    const created = await api.post(`/api/v1/feature-flags${q(workspaceId)}`, {
      data: { key: "artifact_projects", enabled: false, percentage: 0, description: "Artifact project pilot" },
    })
    if (created.status() >= 300 && created.status() !== 409) return `create flag ${created.status()}`
    res = await override()
  }
  return `workspace override ${res.status()}`
}

/** Creates a Project at version 1 in the QA workspace. */
export async function createProject(api: APIRequestContext, workspaceId: string, tag: string, mayCreateFlag = false, admin: APIRequestContext = api): Promise<ProjectRef> {
  const slug = `prd-outage-${tag}-${Date.now().toString(36)}`.toLowerCase()
  const create = () =>
    api.post(`/api/v1/pages/artifacts${q(workspaceId)}`, {
      data: { slug, name: `PRD outage ${tag}`, created_by_test: true, base_version: 0, entrypoint: "index.html", files: files(slug, "v1") },
    })
  let res = await create()
  if (res.status() === 404 && /not enabled/.test(await res.text())) {
    const how = await enableProjects(admin, workspaceId, mayCreateFlag)
    res = await create()
    if (res.status() !== 201) throw new Error(`Projects are not enabled on this origin (${how}; create ${res.status()} ${(await res.text()).slice(0, 160)}); enable CREWSHIP_ARTIFACT_PROJECTS_ENABLED or the artifact_projects flag`)
  }
  expect(res.status(), `create project: ${(await res.text()).slice(0, 300)}`).toBe(201)
  const body = (await res.json()) as { page_id: string }
  return { pageId: body.page_id, slug, workspaceId }
}

export async function snapshot(api: APIRequestContext, ref: ProjectRef, version?: number): Promise<ProjectSnapshot> {
  const res = await api.get(`/api/v1/pages/@${ref.pageId}/artifact${q(ref.workspaceId, version ? { version: String(version) } : {})}`)
  expect(res.status(), "read project").toBe(200)
  return (await res.json()) as ProjectSnapshot
}

/** POST a version exactly as the Project page does. Returns status and body. */
export async function postVersion(api: APIRequestContext, ref: ProjectRef, baseVersion: number, body: ProjectFile[]) {
  const res = await api.post(`/api/v1/pages/@${ref.pageId}/artifact/versions${q(ref.workspaceId)}`, {
    data: { base_version: baseVersion, entrypoint: "index.html", files: body },
    maxRetries: 0,
  })
  return { status: res.status(), body: (await res.json().catch(() => null)) as { version?: number; error?: string } | null }
}

export const projectURL = (ref: ProjectRef) => `/pages/${ref.slug}${q(ref.workspaceId, { artifact: "1" })}`

/** Pins the QA workspace for the signed-in person before the app boots. */
export async function pinWorkspace(context: BrowserContext, api: APIRequestContext, workspaceId: string): Promise<void> {
  const session = (await (await api.get("/api/auth/session")).json()) as { user?: { id?: string } }
  const uid = session.user?.id
  expect(uid, "owner session").toBeTruthy()
  await context.addInitScript(
    ({ uid, ws }) => {
      try {
        localStorage.setItem(`crewship.workspaceId:${uid}`, ws)
      } catch {}
    },
    { uid: uid as string, ws: workspaceId },
  )
}

/** Opens the Project page. Returns false when the Project surface is not in this build. */
export async function openProject(page: Page, ref: ProjectRef, timeout = 45_000): Promise<boolean> {
  await page.goto(projectURL(ref), { waitUntil: "domcontentloaded" })
  return page
    .getByTitle("Artifact preview")
    .waitFor({ state: "visible", timeout })
    .then(() => true)
    .catch(() => false)
}

export const previewHeading = (page: Page) => page.frameLocator('iframe[title="Artifact preview"]').locator("#qa-head")
export const sourceBox = (page: Page) => page.getByRole("textbox", { name: "Source file", exact: true })
export const saveButton = (page: Page) => page.getByRole("button", { name: "Save project", exact: true })

export async function alerts(page: Page): Promise<string[]> {
  return (await page.getByRole("alert").allInnerTexts().catch(() => [] as string[])).map((s) => s.trim()).filter(Boolean)
}

export async function localDrafts(page: Page): Promise<string[]> {
  return page
    .evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith("artifact-source:")).map((k) => localStorage.getItem(k) ?? ""))
    .catch(() => [] as string[])
}

/** Counts POST /artifact/versions issued by the page (a save attempt). */
export function countSaves(page: Page): { posts: { at: string }[] } {
  const log = { posts: [] as { at: string }[] }
  page.on("request", (rq: Request) => {
    if (rq.method() === "POST" && new URL(rq.url()).pathname.endsWith("/artifact/versions")) log.posts.push({ at: new Date().toISOString() })
  })
  return log
}

export const pause = (ms: number) => new Promise((r) => setTimeout(r, ms))
