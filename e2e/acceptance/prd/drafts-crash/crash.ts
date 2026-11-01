// Helpers of the drafts-crash scenario: a browser profile that can be killed
// like a real crash (SIGKILL of every process of that profile, no unload, no
// storage flush beyond what the browser already did) and the Project API calls
// the scenario needs. Nothing here names a host or an account.
import { expect, type APIRequestContext, type BrowserContext, type BrowserType, type Page } from "@playwright/test"
import { execFileSync } from "node:child_process"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import type { StorageState } from "../auth"
import { trackPage } from "../created"

export const pause = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

export interface Traffic {
  /** Mutating API requests seen by the crashable browser, in order. */
  writes: Array<{ method: string; path: string; at: string }>
}

/**
 * A persistent Chromium profile in a private temp directory. `crash()`
 * SIGKILLs the browser (all processes started with this profile), `reopen()`
 * starts a new browser on the same profile, as a user restarting it would.
 */
export class CrashableProfile {
  readonly dir: string
  context: BrowserContext | null = null
  readonly traffic: Traffic = { writes: [] }
  /** Date.now() when the last `crash()` sent SIGKILL. */
  killedAt: number | null = null

  constructor(
    private readonly chromium: BrowserType,
    private readonly baseURL: string,
    private state: StorageState,
    private readonly workspace: { userId: string; workspaceId: string },
  ) {
    this.dir = fs.mkdtempSync(path.join(os.tmpdir(), "prd-drafts-crash-"))
  }

  async open(): Promise<BrowserContext> {
    const ctx = await this.chromium.launchPersistentContext(this.dir, {
      baseURL: this.baseURL,
      viewport: { width: 1400, height: 950 },
    })
    // Cookies are re-added on every launch: a crash may lose session cookies
    // that were not yet flushed, and the scenario is about drafts, not login.
    await ctx.addCookies(this.state.cookies)
    await ctx.addInitScript(({ uid, ws }) => {
      try {
        localStorage.setItem(`crewship.workspaceId:${uid}`, ws)
      } catch {
        /* storage may be unavailable; URLs carry workspace_id anyway */
      }
    }, { uid: this.workspace.userId, ws: this.workspace.workspaceId })
    ctx.on("request", (req) => {
      const u = new URL(req.url())
      if (req.method() !== "GET" && u.pathname.startsWith("/api/v1/")) {
        this.traffic.writes.push({ method: req.method(), path: u.pathname, at: new Date().toISOString() })
      }
    })
    this.context = ctx
    return ctx
  }

  async page(): Promise<Page> {
    const ctx = this.context ?? (await this.open())
    const p = ctx.pages()[0] ?? (await ctx.newPage())
    p.setDefaultTimeout(60_000)
    return p
  }

  /**
   * Waits until every marker is in the profile's on-disk browser storage
   * (Local Storage / IndexedDB LevelDB files). Chromium commits localStorage
   * lazily and rate-limits commits per origin, so how long this takes is
   * itself evidence. Returns the wait in ms, or null if it timed out.
   */
  async waitPersisted(markers: string[], timeoutMs: number, pollMs = 1000): Promise<number | null> {
    const t0 = Date.now()
    const needles = markers.flatMap((m) => [Buffer.from(m, "latin1"), Buffer.from(m, "utf16le")])
    const root = path.join(this.dir, "Default")
    const files = (dir: string): string[] => {
      let out: string[] = []
      for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
        const p = path.join(dir, e.name)
        if (e.isDirectory()) out = out.concat(files(p))
        else if (/\.(log|ldb)$/.test(e.name)) out.push(p)
      }
      return out
    }
    while (Date.now() - t0 < timeoutMs) {
      const found = new Set<number>()
      for (const dir of ["Local Storage", "IndexedDB"].map((d) => path.join(root, d)).filter((d) => fs.existsSync(d))) {
        for (const f of files(dir)) {
          let buf: Buffer
          try {
            buf = fs.readFileSync(f)
          } catch {
            continue
          }
          needles.forEach((n, i) => {
            if (buf.includes(n)) found.add(Math.floor(i / 2))
          })
        }
      }
      if (found.size === markers.length) return Date.now() - t0
      await pause(pollMs)
    }
    return null
  }

  /** Kills every process of this profile with SIGKILL. Returns how many. */
  async crash(): Promise<number> {
    // The session may have been refreshed (rotated) while the browser ran;
    // keep the latest cookies so the restarted browser is still signed in.
    if (this.context) {
      const cookies = await this.context.cookies().catch(() => null)
      if (cookies?.length) this.state = { cookies, origins: [] }
    }
    const marker = `--user-data-dir=${this.dir}`
    const out = execFileSync("ps", ["-eo", "pid=,args="], { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 })
    const pids = out
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line.includes(marker))
      .map((line) => Number(line.split(/\s+/, 1)[0]))
      .filter((pid) => Number.isInteger(pid) && pid > 0 && pid !== process.pid)
    this.killedAt = Date.now()
    for (const pid of pids) {
      try {
        process.kill(pid, "SIGKILL")
      } catch {
        /* already gone */
      }
    }
    await pause(1500)
    await this.context?.close().catch(() => {})
    this.context = null
    return pids.length
  }

  /** Restarts the browser on the same profile; `fresh` replaces the session cookies (a new sign-in). */
  async reopen(fresh?: StorageState): Promise<Page> {
    if (fresh) this.state = fresh
    await pause(500)
    await this.open()
    return this.page()
  }

  /**
   * Signs the same browser profile in as another person, as on a shared
   * computer: the crashed person's local storage stays on disk.
   */
  async switchUser(state: StorageState): Promise<Page> {
    const ctx = this.context ?? (await this.open())
    await ctx.clearCookies()
    await ctx.addCookies(state.cookies)
    const p = await ctx.newPage()
    p.setDefaultTimeout(60_000)
    return p
  }

  async dispose(): Promise<void> {
    await this.context?.close().catch(() => {})
    this.context = null
    fs.rmSync(this.dir, { recursive: true, force: true })
  }
}

/** Mutating requests to the given page that a crash must not have produced. */
export function writesTo(traffic: Traffic, pageId: string, since = 0) {
  return traffic.writes.slice(since).filter((w) => w.path.includes(pageId) || w.path.includes("/comments"))
}

export const projectFiles = (marker: string) => [
  {
    path: "index.html",
    media_type: "text/html",
    content: `<!doctype html><html><head><link rel="stylesheet" href="style.css"></head><body><h1 id="qa-head">${marker}</h1><p id="qa-para">Drafts crash paragraph.</p></body></html>`,
  },
  { path: "style.css", media_type: "text/css", content: "body{font-family:sans-serif}h1{color:#123456}" },
]

export interface ProjectRef {
  slug: string
  pageId: string
}

/**
 * Makes sure artifact Projects are available in the QA workspace. A server
 * without CREWSHIP_ARTIFACT_PROJECTS_ENABLED (CI's test server) honours the
 * workspace feature flag, so the scenario turns it on for its own disposable
 * workspace only. Returns how availability was reached.
 */
export async function ensureArtifactProjects(api: APIRequestContext, workspaceId: string, mayFlip: boolean): Promise<"server" | "workspace-flag"> {
  const q = `workspace_id=${encodeURIComponent(workspaceId)}`
  const available = async () => {
    const res = await api.get(`/api/v1/pages/artifacts/availability?${q}`)
    expect(res.status(), "artifact Project availability").toBe(200)
    return ((await res.json()) as { enabled?: boolean }).enabled === true
  }
  if (await available()) return "server"
  // Never change flags of a workspace the operator supplied (PRD_WORKSPACE_ID).
  expect(mayFlip, "artifact Projects are disabled in PRD_WORKSPACE_ID; enable artifact_projects there or let the scenario create its workspace").toBe(true)
  const res = await api.put(`/api/v1/feature-flags/artifact_projects/override?${q}`, { data: { enabled: true } })
  expect(res.status(), `enable artifact_projects in the QA workspace: ${(await res.text()).slice(0, 200)}`).toBeLessThan(300)
  expect(await available(), "artifact Projects available after the workspace flag").toBe(true)
  return "workspace-flag"
}

export async function createProject(api: APIRequestContext, workspaceId: string, tag: string): Promise<ProjectRef> {
  const slug = `prd-drafts-${tag}-${Date.now().toString(36)}`.toLowerCase()
  const res = await api.post(`/api/v1/pages/artifacts?workspace_id=${encodeURIComponent(workspaceId)}`, {
    data: { slug, name: `PRD drafts crash ${tag}`, created_by_test: true, base_version: 0, entrypoint: "index.html", files: projectFiles(slug) },
  })
  expect(res.status(), `create Project: ${(await res.text()).slice(0, 300)}`).toBe(201)
  const body = (await res.json()) as { page_id: string }
  trackPage(workspaceId, body.page_id)
  return { slug, pageId: body.page_id }
}

export interface ArtifactState {
  latest_version: number
  manifest: unknown
  comments: unknown[]
  can_write?: boolean
}

export async function readArtifact(api: APIRequestContext, workspaceId: string, pageId: string): Promise<ArtifactState> {
  const res = await api.get(`/api/v1/pages/@${encodeURIComponent(pageId)}/artifact?workspace_id=${encodeURIComponent(workspaceId)}`)
  expect(res.status(), "read Project").toBe(200)
  return (await res.json()) as ArtifactState
}

/**
 * Shares a Project version with the workspace (a new Project has a private
 * audience, which ordinary grants cannot widen). Returns the HTTP status.
 */
export async function shareProject(api: APIRequestContext, workspaceId: string, pageId: string, version: number): Promise<number> {
  const res = await api.post(`/api/v1/pages/@${encodeURIComponent(pageId)}/artifact/share?workspace_id=${encodeURIComponent(workspaceId)}`, {
    data: { base_version: version, version },
  })
  return res.status()
}

/** Grants a user a level on a page (read, comment, write). Returns the HTTP status. */
export async function grantPage(api: APIRequestContext, workspaceId: string, pageId: string, userId: string, level: string): Promise<number> {
  const res = await api.put(`/api/v1/pages/@${encodeURIComponent(pageId)}/grants?workspace_id=${encodeURIComponent(workspaceId)}`, {
    data: { subject_type: "user", subject: userId, level },
  })
  return res.status()
}

export const projectURL = (slug: string, ws: string) => `/pages/${encodeURIComponent(slug)}?artifact=1&workspace_id=${encodeURIComponent(ws)}`
export const viewerURL = (slug: string, ws: string) => `/artifacts?item=${encodeURIComponent(slug)}&workspace_id=${encodeURIComponent(ws)}`

export async function openProject(page: Page, slug: string, ws: string): Promise<void> {
  await page.goto(projectURL(slug, ws), { waitUntil: "domcontentloaded" })
  await expect(page.getByTitle("Artifact preview")).toBeVisible({ timeout: 60_000 })
}

export const previewFrame = (page: Page) => page.frameLocator('iframe[title^="Preview of"]')

export async function openViewer(page: Page, slug: string, ws: string): Promise<void> {
  await page.goto(viewerURL(slug, ws), { waitUntil: "domcontentloaded" })
  await expect(previewFrame(page).locator("#qa-head")).toBeVisible({ timeout: 60_000 })
  await pause(1000)
}

/** Opens the source editor and replaces the paragraph with `marker`; returns the typed file text. */
export async function typeSourceDraft(page: Page, marker: string): Promise<string> {
  const fill = await prepareSourceDraft(page, marker)
  return fill()
}

/**
 * Opens the source editor and returns a function that types the draft (the
 * paragraph replaced with `marker`) in one step and returns the typed text.
 */
export async function prepareSourceDraft(page: Page, marker: string): Promise<() => Promise<string>> {
  await page.getByRole("button", { name: "Edit source", exact: true }).click()
  const src = page.getByRole("textbox", { name: "Source file", exact: true })
  const text = (await src.inputValue()).replace("Drafts crash paragraph.", `${marker} typed but never saved`)
  expect(text).toContain(marker)
  return async () => {
    await src.fill(text)
    return text
  }
}

/** Selects the heading in the viewer preview and types an unsent comment. */
export async function typeViewerComment(page: Page, text: string): Promise<void> {
  const fill = await prepareViewerComment(page)
  await fill(text)
}

/** Selects the heading in the viewer preview; returns a function that types the unsent comment. */
export async function prepareViewerComment(page: Page): Promise<(text: string) => Promise<void>> {
  await page.getByRole("button", { name: "Select", exact: true }).click()
  await previewFrame(page).locator("#qa-head").click()
  const form = page.locator('form[aria-label="New comment"]')
  await expect(form).toBeVisible({ timeout: 15_000 })
  const box = form.getByRole("textbox", { name: "Comment" })
  return (text) => box.fill(text)
}

/**
 * Where the open page keeps artifact drafts: the strict IndexedDB store, or
 * localStorage only. Chromium commits localStorage to disk on a timer, so
 * a localStorage-only draft typed just before a crash is lost by design.
 */
export async function artifactDraftStore(page: Page, marker: string) {
  return page.evaluate(async (m) => {
    const dbs = typeof indexedDB.databases === "function" ? await indexedDB.databases() : []
    let inLocalStorage = false
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i)
      if (k && (localStorage.getItem(k) ?? "").includes(m)) inLocalStorage = true
    }
    return { indexedDb: dbs.some((d) => d.name === "crewship-artifact-drafts"), inLocalStorage }
  }, marker)
}

/**
 * Lets Chromium commit pending localStorage changes of the page's origin and
 * returns when that commit is on disk. Chromium waits at least 5 s after the
 * first change before the next commit, so changes made right after this
 * returns stay in memory for at least that long.
 */
export async function settleLocalStorageCommit(profile: CrashableProfile, page: Page, timeoutMs = 90_000): Promise<number> {
  const sentinel = `PRD-COMMIT-SENTINEL-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
  await page.evaluate((v) => localStorage.setItem("prd-acceptance-commit-sentinel", v), sentinel)
  const waited = await profile.waitPersisted([sentinel], timeoutMs, 50)
  expect(waited, "the browser committed localStorage to disk").not.toBeNull()
  return waited!
}

export async function viewerCommentValue(page: Page): Promise<string | null> {
  const form = page.locator('form[aria-label="New comment"]')
  if (!(await form.count())) return null
  return form.getByRole("textbox", { name: "Comment" }).inputValue()
}

export const projectCommentBox = (page: Page) => page.getByRole("textbox", { name: /^Comment/ })

/** What a reopened Project page offers, read before touching anything. */
export async function projectOffer(page: Page) {
  const restore = page.getByRole("button", { name: "Restore source draft", exact: true })
  const sourceOpen = page.getByRole("textbox", { name: "Source file", exact: true })
  const statuses = (await page.getByRole("status").allInnerTexts()).map((s) => s.trim()).filter((s) => s && !s.startsWith("Storage:"))
  return {
    restoreOffered: await restore.count(),
    sourceOpenBeforeRestore: await sourceOpen.count(),
    statuses,
    commentBox: (await projectCommentBox(page).count()) ? await projectCommentBox(page).inputValue() : null,
  }
}
