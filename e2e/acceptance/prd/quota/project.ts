// Project (artifact project) helpers of the quota scenario: availability in a
// QA workspace, a test page, filling its version history to the byte, and
// the message checks. Nothing here names a host or an account.
import { expect, type APIRequestContext, type Page } from "@playwright/test"
import { randomBytes } from "node:crypto"

const MiB = 1 << 20
/** Below the server's 16 MiB request-body cap, including JSON overhead. */
const FILL_CHUNK = Math.round(15.5 * MiB)

const ws = (workspaceId: string) => `workspace_id=${encodeURIComponent(workspaceId)}`

export interface ProjectRef {
  slug: string
  pageId: string
}

export interface ArtifactRead {
  latest_version: number
  versions: Array<{ version: number }>
  manifest: { version: number; files: Array<{ path: string; content: string }> }
  quota: { used_bytes: number; limit_bytes: number; warning: boolean; retention?: string }
  can_write?: boolean
}

export interface Head {
  latest: number
  versions: number[]
  used: number
  limit: number
  warning: boolean
}

/**
 * Projects sit behind CREWSHIP_ARTIFACT_PROJECTS_ENABLED or the per-workspace
 * "artifact_projects" flag. The scenario turns the flag on in its own
 * disposable workspace only.
 */
export async function ensureArtifactProjects(api: APIRequestContext, workspaceId: string): Promise<"server" | "workspace-flag"> {
  const available = async () => {
    const res = await api.get(`/api/v1/pages/artifacts/availability?${ws(workspaceId)}`)
    expect(res.status(), "artifact Project availability").toBe(200)
    return ((await res.json()) as { enabled?: boolean }).enabled === true
  }
  if (await available()) return "server"
  const res = await api.put(`/api/v1/feature-flags/artifact_projects/override?${ws(workspaceId)}`, { data: { enabled: true } })
  expect(res.status(), `enable artifact_projects in the QA workspace: ${(await res.text()).slice(0, 200)}`).toBeLessThan(300)
  expect(await available(), "artifact Projects available after the workspace flag").toBe(true)
  return "workspace-flag"
}

export const FULL_MARK = "QUOTA-FULL"

export const projectFiles = (slug: string, label: string) => [
  {
    path: "index.html",
    media_type: "text/html",
    content: `<!doctype html><html><head><link rel="stylesheet" href="style.css"></head><body><h1 id="qa-head">${slug} ${label}</h1><p id="qa-para">Quota QA paragraph.</p></body></html>`,
  },
  { path: "style.css", media_type: "text/css", content: "body{font-family:sans-serif}h1{color:#123456}" },
]

/**
 * Creates a Project and hands its id to `keep`, the owner of its cleanup. The
 * quota groups share one Project across tests, so the per-test cleanup
 * (trackPage) must not delete it after the first test.
 */
export async function createProject(api: APIRequestContext, workspaceId: string, keep: (pageId: string) => void): Promise<ProjectRef> {
  const slug = `prd-quota-${Date.now().toString(36)}`
  const res = await api.post(`/api/v1/pages/artifacts?${ws(workspaceId)}`, {
    data: { slug, name: "PRD quota project", created_by_test: true, base_version: 0, entrypoint: "index.html", files: projectFiles(slug, "v1") },
  })
  expect(res.status(), `create Project: ${(await res.text()).slice(0, 300)}`).toBe(201)
  const pageId = ((await res.json()) as { page_id: string }).page_id
  keep(pageId)
  return { slug, pageId }
}

export async function readArtifact(api: APIRequestContext, workspaceId: string, pageId: string, version?: number): Promise<{ status: number; body: ArtifactRead }> {
  const v = version === undefined ? "" : `&version=${version}`
  const res = await api.get(`/api/v1/pages/@${encodeURIComponent(pageId)}/artifact?${ws(workspaceId)}${v}`, { timeout: 120_000 })
  return { status: res.status(), body: (await res.json().catch(() => null)) as ArtifactRead }
}

export async function head(api: APIRequestContext, workspaceId: string, pageId: string): Promise<Head> {
  const { status, body } = await readArtifact(api, workspaceId, pageId)
  expect(status, "read Project").toBe(200)
  return {
    latest: body.latest_version,
    versions: body.versions.map((v) => v.version),
    used: body.quota.used_bytes,
    limit: body.quota.limit_bytes,
    warning: body.quota.warning,
  }
}

const pad = (n: number) => randomBytes(Math.ceil((n * 3) / 4) + 4).toString("base64url").slice(0, Math.max(0, n))

export interface SaveResult {
  label: string
  padBytes: number
  status: number
  code: string | null
  error: string | null
  ms: number
  before: Head
  after: Head
}

/** Saves a new version on top of the current head with `padBytes` of filler. */
export async function saveVersion(api: APIRequestContext, workspaceId: string, p: ProjectRef, label: string, padBytes: number): Promise<SaveResult> {
  const before = await head(api, workspaceId, p.pageId)
  const files = [...projectFiles(p.slug, label), ...(padBytes > 0 ? [{ path: "pad.txt", media_type: "text/plain", content: pad(padBytes) }] : [])]
  const t0 = Date.now()
  const res = await api.post(`/api/v1/pages/@${encodeURIComponent(p.pageId)}/artifact/versions?${ws(workspaceId)}`, {
    data: { base_version: before.latest, entrypoint: "index.html", files },
    timeout: 300_000,
    maxRetries: 0,
  })
  const body = (await res.json().catch(() => ({}))) as { code?: string; error?: string }
  const ms = Date.now() - t0
  const after = await head(api, workspaceId, p.pageId)
  return { label, padBytes, status: res.status(), code: body.code ?? null, error: body.error ?? null, ms, before, after }
}

/**
 * Fills the Project's history until less than a few hundred bytes remain:
 * large distinct versions first, then a sized one, then a top-off whose
 * filler is trimmed until the server accepts it. Every accepted save is a
 * real, readable version. Returns the save log (statuses and timings only).
 */
export async function fillHistory(api: APIRequestContext, workspaceId: string, p: ProjectRef) {
  const log: Array<Omit<SaveResult, "before" | "after"> & { used: number; limit: number }> = []
  const record = (r: SaveResult) => {
    log.push({ label: r.label, padBytes: r.padBytes, status: r.status, code: r.code, error: r.error, ms: r.ms, used: r.after.used, limit: r.after.limit })
    return r
  }
  let h = await head(api, workspaceId, p.pageId)
  const t0 = Date.now()
  let n = 0
  while (h.limit - h.used > FILL_CHUNK + 64 * 1024) {
    const r = record(await saveVersion(api, workspaceId, p, `fill${++n}`, FILL_CHUNK))
    expect(r.status, `fill save ${n}: ${r.error}`).toBe(200)
    h = r.after
  }
  {
    const r = record(await saveVersion(api, workspaceId, p, "fill-last", h.limit - h.used - 64 * 1024))
    expect(r.status, `last fill save: ${r.error}`).toBe(200)
    h = r.after
  }
  // Per-version overhead (manifest, rows) measured on a small save.
  const probe = record(await saveVersion(api, workspaceId, p, "probe", 1000))
  expect(probe.status, `probe save: ${probe.error}`).toBe(200)
  h = probe.after
  const overhead = probe.after.used - probe.before.used - 1000 - Buffer.byteLength(projectFiles(p.slug, "probe")[0].content)
  const indexLen = Buffer.byteLength(projectFiles(p.slug, FULL_MARK)[0].content)
  let padBytes = h.limit - h.used - overhead - indexLen - 40
  let top: SaveResult | null = null
  for (let i = 0; i < 40 && padBytes >= 0; i++) {
    top = record(await saveVersion(api, workspaceId, p, FULL_MARK, padBytes))
    if (top.status === 200) break
    expect(top.status, `top-off save: ${top.error}`).toBe(413)
    padBytes -= 64
  }
  expect(top?.status, "the history could be filled to the limit").toBe(200)
  const final = top!.after
  return { log, overhead, fillMs: Date.now() - t0, final, remaining: final.limit - final.used }
}

export const projectURL = (slug: string, workspaceId: string) => `/pages/${encodeURIComponent(slug)}?artifact=1&${ws(workspaceId)}`

export async function openProject(page: Page, slug: string, workspaceId: string): Promise<void> {
  await page.goto(projectURL(slug, workspaceId), { waitUntil: "domcontentloaded" })
  await expect(page.getByTitle("Artifact preview")).toBeVisible({ timeout: 60_000 })
}

export const sourceBox = (page: Page) => page.getByRole("textbox", { name: "Source file", exact: true })

/**
 * Opens the source editor, changes the heading and presses Save. Returns
 * the typed text and the server's answer to that save.
 */
export async function saveFromUi(page: Page, marker: string) {
  await page.getByRole("button", { name: "Edit source", exact: true }).click()
  const src = sourceBox(page)
  const text = (await src.inputValue()).replace(` ${FULL_MARK}</h1>`, ` ${marker}</h1>`)
  expect(text, "the source editor shows the latest version").toContain(marker)
  await src.fill(text)
  const response = page.waitForResponse((r) => r.request().method() === "POST" && r.url().includes("/artifact/versions"), { timeout: 120_000 })
  await page.getByRole("button", { name: "Save project", exact: true }).click()
  const r = await response
  const body = (await r.json().catch(() => null)) as { code?: string } | null
  return { text, status: r.status(), code: body?.code ?? null }
}

export async function alerts(page: Page): Promise<string[]> {
  return (await page.getByRole("alert").allInnerTexts()).map((s) => s.trim()).filter(Boolean)
}

// --- message checks -------------------------------------------------------
//
// PRD: a full history gives a clear choice and keeps the work; the message
// must not promise more space (or a copy) the product cannot provide. The
// product has no way to raise a history limit, so any invitation to get
// more storage is a false promise. A negated mention ("no increase is
// offered") is fine.

const PROMISES_MORE_SPACE: RegExp[] = [
  /\b(ask|request|contact)\b[^.]{0,40}\b(more|additional|extra)\b[^.]{0,20}\b(storage|space|quota)\b/i,
  /\b(increase|raise|extend|expand)\b[^.]{0,20}\b(your|the)\b[^.]{0,20}\b(quota|limit|storage|space)\b/i,
  /\b(upgrade|buy|purchase)\b/i,
  /\bget more (storage|space)\b/i,
  /\b(požádejte|požádat|kontaktujte)\b[^.]{0,40}\b(navýšení|více místa|větší)\b/i,
  /\b(navyšte|zvyšte|můžete navýšit|lze navýšit)\b/i,
]

/** Phrases that announce a product action to keep a copy (needs a control). */
const PRODUCT_COPY: Array<{ phrase: RegExp; control: RegExp }> = [
  { phrase: /\bsave a copy\b/i, control: /save (a )?copy|duplicate/i },
  { phrase: /\bdownload\b/i, control: /download/i },
  { phrase: /\bexport\b/i, control: /export/i },
  { phrase: /\bstáhn(ěte|out)\b/i, control: /stáhnout/i },
]

export function promisesMoreSpace(message: string): string | null {
  for (const re of PROMISES_MORE_SPACE) {
    const m = re.exec(message)
    if (m) return m[0]
  }
  return null
}

/** Copy actions the message announces, each with whether the page offers a matching button. */
export async function announcedCopyActions(page: Page, message: string) {
  const out: Array<{ phrase: string; control: boolean }> = []
  for (const { phrase, control } of PRODUCT_COPY) {
    const m = phrase.exec(message)
    if (!m) continue
    out.push({ phrase: m[0], control: (await page.getByRole("button", { name: control }).count()) + (await page.getByRole("link", { name: control }).count()) > 0 })
  }
  return out
}
