// Roles and revocation on Project and Design (#5004): a person's right changes
// while the artifact is open, and that must neither leak content nor lose work.
//
// Comment role on a Project: reads the shared version and comments, changes
// nothing. Read role on a Design: reads the shared version, cannot write, also
// not on the raw socket. Revocation and member removal with an open tab: the
// tab drops what it showed, new content never reaches it (DOM, HTTP or socket
// frames) and a later read is refused. Design conflict: two people on a stale
// canvas. Unsaved work when the right goes: visible, refused by the server and
// back once the right returns.
//
// Everything runs in the disposable QA workspace with provisioned people. No
// agent and no model key: the Projects have no origin chat and every comment
// is sent with send_to_agent false. Diagnostic rows, see required-ids.json.
import { randomUUID } from "node:crypto"
import type { APIRequestContext, BrowserContext, Page, WebSocketRoute } from "@playwright/test"
import * as Y from "yjs"
import { loginStorageState } from "./auth"
import {
  applyOperations, canvasTexts, createDesign, enableDesign, expectOnCanvas, getDesign, listVersions, openCanvas, scoped, type DesignNode,
} from "./design/canvas"
import { expect, prdRequirement, test, type PrdScenario } from "./fixtures"
import { pause } from "./ops"
import {
  HERO, commentOnHero, createProject, enableProjects, heroOf, openProject, pinWorkspace, preview, projectSlug, publish, snapshot, threeScreens, wsQuery,
} from "./project/project-api"
import { projectURL, sourceBox } from "./quota/project"

type Level = "read" | "comment" | "write"

const REFUSED = [403, 404]
/** Project viewers poll every 5 s, the source editor every 15 s, Design every 10 s. */
const TAB_LOSES_ACCESS_MS = 45_000
/** Long enough for several polls of an open tab: silence must mean refusal, not "not yet". */
const QUIET_MS = 18_000
const NO_ACCESS = /You can't open this artifact|This artifact isn't available|This artifact is not available|Page read access was removed/

const tag = () => randomUUID().replaceAll("-", "").slice(0, 8)

interface Person {
  userId: string
  context: BrowserContext
  page: Page
  api: APIRequestContext
}

/** A provisioned workspace member with an own login and browser context. */
async function person(prd: PrdScenario, workspaceId: string, name: string, role: "MEMBER" | "VIEWER" = "MEMBER"): Promise<Person> {
  const who = await prd.provision(role, name)
  const context = await prd.browserContext(await loginStorageState(prd.origin, who.account))
  await pinWorkspace(context, workspaceId)
  const page = await context.newPage()
  page.setDefaultTimeout(60_000)
  return { userId: who.userId, context, page, api: context.request }
}

async function grant(owner: APIRequestContext, workspaceId: string, ref: string, userId: string, level: Level): Promise<void> {
  const res = await owner.put(scoped(`/api/v1/pages/${encodeURIComponent(ref)}/grants`, workspaceId), { data: { subject_type: "user", subject: userId, level } })
  expect(res.status(), `grant ${level}: ${(await res.text()).slice(0, 300)}`).toBe(200)
}

/** Withdraws one level, or every level the person holds when `level` is left out. */
async function revoke(owner: APIRequestContext, workspaceId: string, ref: string, userId: string, level?: Level): Promise<void> {
  const q = `subject_type=user&subject=${encodeURIComponent(userId)}${level ? `&level=${level}` : ""}`
  const res = await owner.delete(scoped(`/api/v1/pages/${encodeURIComponent(ref)}/grants?${q}`, workspaceId))
  expect(res.status(), `revoke: ${(await res.text()).slice(0, 300)}`).toBeLessThan(300)
}

async function removeMember(owner: APIRequestContext, workspaceId: string, userId: string): Promise<void> {
  const list = await owner.get(scoped(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members`, workspaceId))
  expect(list.status(), "list members").toBe(200)
  const raw = (await list.json()) as unknown
  const members = (Array.isArray(raw) ? raw : ((raw as { members?: unknown[] }).members ?? [])) as Array<{ id: string; user_id: string }>
  const member = members.find((m) => m.user_id === userId)
  expect(member, "the person is a member before the removal").toBeTruthy()
  const res = await owner.delete(scoped(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(member!.id)}`, workspaceId))
  expect(res.status(), `remove member: ${(await res.text()).slice(0, 300)}`).toBeLessThan(300)
}

async function shareProject(owner: APIRequestContext, workspaceId: string, slug: string, base: number, version: number): Promise<void> {
  const res = await owner.post(`/api/v1/pages/${encodeURIComponent(slug)}/artifact/share?${wsQuery(workspaceId)}`, { data: { base_version: base, version } })
  expect(res.status(), `share project: ${(await res.text()).slice(0, 300)}`).toBe(200)
}

async function shareDesign(owner: APIRequestContext, workspaceId: string, slug: string, version: number): Promise<void> {
  const res = await owner.post(scoped(`/api/v1/pages/${encodeURIComponent(slug)}/design/share`, workspaceId), { data: { version } })
  expect(res.status(), `share Design: ${(await res.text()).slice(0, 300)}`).toBeLessThan(300)
}

/**
 * Records every sighting of a forbidden marker in what reaches a browser
 * context: API response bodies and socket frames. Markers are unique per test,
 * so a hit is a leak, whatever the page chose to render.
 */
function watchTraffic(context: BrowserContext) {
  const markers = new Set<string>()
  const hits: Array<{ via: string; marker: string; at: string }> = []
  const seen = (via: string, text: string) => {
    for (const marker of markers) if (text.includes(marker)) hits.push({ via, marker, at: new Date().toISOString() })
  }
  context.on("response", (res) => {
    if (markers.size === 0 || !res.url().includes("/api/")) return
    const via = new URL(res.url()).pathname
    void res.text().then((text) => seen(via, text), () => {})
  })
  const onPage = (page: Page) => {
    page.on("websocket", (socket) => {
      socket.on("framereceived", ({ payload }) => {
        if (markers.size > 0) seen(`socket ${new URL(socket.url()).pathname}`, typeof payload === "string" ? payload : payload.toString("utf8"))
      })
    })
  }
  context.pages().forEach(onPage)
  context.on("page", onPage)
  return { forbid: (marker: string) => markers.add(marker), hits }
}

/** The whole visible text of a page and its frames. */
async function pageText(page: Page): Promise<string> {
  const parts = await Promise.all(page.frames().map((f) => f.locator("body").innerText({ timeout: 2_000 }).catch(() => "")))
  return parts.join("\n")
}

async function visibleMessages(page: Page): Promise<string[]> {
  const texts = await page.locator('[role="alert"],[role="status"]').allInnerTexts()
  return texts.map((s) => s.trim()).filter(Boolean)
}

/** The shell always holds an empty alert region; only one with text says anything. */
/** Project source drafts of one person in one workspace, in IndexedDB and in the localStorage fallback. */
async function storedSourceDrafts(page: Page, userId: string, workspaceId: string): Promise<number> {
  return page.evaluate(async ({ userId, workspaceId }) => {
    const fallback = Object.keys(localStorage).filter((name) => name.startsWith("artifact-source:") && name.includes(userId)).length
    if (!(await indexedDB.databases()).some((item) => item.name === "crewship-artifact-drafts")) return fallback
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      const open = indexedDB.open("crewship-artifact-drafts")
      open.onsuccess = () => resolve(open.result)
      open.onerror = () => reject(open.error)
    })
    try {
      if (!db.objectStoreNames.contains("drafts")) return fallback
      const keys = await new Promise<IDBValidKey[]>((resolve, reject) => {
        const all = db.transaction("drafts").objectStore("drafts").getAllKeys()
        all.onsuccess = () => resolve(all.result)
        all.onerror = () => reject(all.error)
      })
      return fallback + keys.filter((key) => Array.isArray(key) && key[0] === userId && key[1] === "artifact-source" && key[2] === workspaceId).length
    } finally { db.close() }
  }, { userId, workspaceId })
}

const spokenAlert = (page: Page) => page.getByRole("alert").filter({ hasText: /\S/ }).first()

/** Fails unless the open Project tab shows no preview and says that access is gone. */
async function expectProjectGone(page: Page): Promise<number> {
  const started = Date.now()
  await expect(page.locator('iframe[title^="Preview of"]'), "the open tab drops the preview").toHaveCount(0, { timeout: TAB_LOSES_ACCESS_MS })
  await expect(page.getByText(NO_ACCESS).first(), "the tab says the access is gone").toBeVisible({ timeout: 10_000 })
  return Date.now() - started
}

/** Fails unless the open Design tab shows no canvas and says that access is gone. */
async function expectDesignGone(page: Page): Promise<number> {
  const started = Date.now()
  await expect(page.locator('svg[aria-label="Design canvas"]'), "the open tab drops the canvas").toHaveCount(0, { timeout: TAB_LOSES_ACCESS_MS })
  await expect(spokenAlert(page), "the tab says the access is gone").toBeVisible({ timeout: 10_000 })
  return Date.now() - started
}

// ---- Design ----------------------------------------------------------------

interface SeededDesign {
  slug: string
  pageId: string
  frame: DesignNode
  text: DesignNode
  second: DesignNode
  version: number
}

/** A Design with a screen and two text layers, saved and shared as one version. */
async function seedDesign(owner: APIRequestContext, workspaceId: string, sharedText: string): Promise<SeededDesign> {
  const slug = `prd-design-${tag()}`
  const created = await createDesign(owner, workspaceId, slug, `PRD design ${slug}`)
  const frame: DesignNode = { id: `prd-frame-${tag()}`, type: "frame", name: "PRD screen", x: 40, y: 60, width: 640, height: 400, fill: "#ffffff" }
  const text: DesignNode = { id: `prd-text-${tag()}`, type: "text", name: "PRD first", parentId: frame.id, x: 20, y: 20, width: 500, height: 40, fill: "#111111", text: sharedText }
  const second: DesignNode = { id: `prd-text-${tag()}`, type: "text", name: "PRD second", parentId: frame.id, x: 20, y: 120, width: 500, height: 40, fill: "#111111", text: `Second ${tag()}` }
  const applied = await applyOperations(owner, workspaceId, slug, [{ op: "upsert", node: frame }, { op: "upsert", node: text }, { op: "upsert", node: second }], "PRD role matrix: seed")
  expect(applied.version, "a Design tool edit is a retained version").toBeTruthy()
  await shareDesign(owner, workspaceId, slug, applied.version!)
  return { slug, pageId: created.page_id, frame, text, second, version: applied.version! }
}

const ownerApply = (owner: APIRequestContext, workspaceId: string, d: SeededDesign, node: DesignNode, text: string) =>
  applyOperations(owner, workspaceId, d.slug, [{ op: "upsert", node: { ...node, text } }], "PRD role matrix: owner edit")

async function openDesignOrFail(page: Page, slug: string, workspaceId: string): Promise<void> {
  expect(await openCanvas(page, slug, workspaceId), "the deployed build renders the Design editor").toBe(true)
}

async function typeOnLayer(page: Page, layer: string, text: string): Promise<void> {
  await page.getByLabel("Layers").getByRole("button", { name: layer, exact: true }).click()
  const box = page.getByLabel("Text content")
  await expect(box).toBeEditable({ timeout: 15_000 })
  await box.fill(text)
  await expect.poll(() => canvasTexts(page)).toContain(text)
}

/** One y-websocket document update frame carrying `text`, as an editor would send it. */
function designUpdateFrame(text: string): number[] {
  const doc = new Y.Doc()
  const node = new Y.Map<unknown>()
  doc.getMap("nodes").set(`prd-socket-${tag()}`, node)
  for (const [k, v] of Object.entries({ type: "text", name: "PRD socket", x: 10, y: 10, width: 200, height: 30, fill: "#111111", text })) node.set(k, v)
  const update = Y.encodeStateAsUpdate(doc)
  const length: number[] = []
  for (let n = update.length; ; n >>>= 7) {
    if (n < 128) { length.push(n); break }
    length.push((n & 127) | 128)
  }
  doc.destroy()
  return [0, 2, ...length, ...update]
}

/** Opens a Design socket from the page, sends one update frame and reports how the server answered. */
async function socketWrite(page: Page, workspaceId: string, pageId: string, frame: number[]) {
  const path = scoped(`/api/v1/pages/@${encodeURIComponent(pageId)}/design/ws`, workspaceId)
  return page.evaluate(({ path, frame }) => new Promise<{ opened: boolean; closed: boolean; code: number | null }>((resolve) => {
    const url = new URL(path, window.location.href)
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:"
    const socket = new WebSocket(url.href)
    socket.binaryType = "arraybuffer"
    let opened = false
    const timer = setTimeout(() => { socket.close(); resolve({ opened, closed: false, code: null }) }, 15_000)
    socket.onopen = () => { opened = true; socket.send(new Uint8Array(frame).buffer) }
    socket.onclose = (event) => { clearTimeout(timer); resolve({ opened, closed: true, code: event.code }) }
  }), { path, frame })
}

/**
 * Passes the Design socket through and can cut it, so a person keeps working on
 * a stale canvas while HTTP stays up. context.setOffline would also fail the
 * metadata poll, which is a different scenario.
 */
async function gateDesignSocket(context: BrowserContext) {
  let cut = false
  const live = new Set<WebSocketRoute>()
  await context.routeWebSocket(/\/design\/ws/, (socket) => {
    if (cut) {
      void socket.close({ code: 3000, reason: "PRD socket cut" })
      return
    }
    const server = socket.connectToServer()
    socket.onMessage((message) => server.send(message))
    server.onMessage((message) => socket.send(message))
    live.add(socket)
  })
  return {
    cut: async () => {
      cut = true
      await Promise.all([...live].map((s) => s.close({ code: 3000, reason: "PRD socket cut" }).catch(() => {})))
      live.clear()
    },
    restore: () => { cut = false },
  }
}

const connected = (page: Page) =>
  expect(page.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 45_000 })

/** Every retained version's text of a layer, oldest first. */
async function versionTexts(owner: APIRequestContext, workspaceId: string, slug: string, nodeId: string): Promise<string[]> {
  const { versions } = await listVersions(owner, workspaceId, slug)
  const out: string[] = []
  for (const v of [...versions].sort((a, b) => a.version - b.version)) {
    const state = await getDesign(owner, workspaceId, slug, v.version)
    out.push(state.nodes.find((n) => n.id === nodeId)?.text ?? "")
  }
  return out
}

test.describe("role matrix", () => {
  test.beforeEach(() => {
    test.setTimeout(6 * 60_000)
  })

  test(
    "Comment role on a Project through the API: reads and comments, changes nothing",
    { annotation: prdRequirement("role.project.comment-api", "Comment role on a Project (API): reads the shared version and comments; publish, restore, share, grant and delete are refused and the private version stays private") },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      await enableProjects(request, ws.id)
      const slug = projectSlug()
      const V1 = `Shared ${tag()}`
      const PRIVATE = `Private ${tag()}`
      await createProject(request, ws.id, slug, threeScreens(V1))
      await shareProject(request, ws.id, slug, 1, 1)
      expect((await publish(request, ws.id, slug, 1, threeScreens(PRIVATE), "private v2")).status()).toBe(200)

      const stranger = await person(prd, ws.id, "PRD no grant")
      const noGrant = await stranger.api.get(`/api/v1/pages/${encodeURIComponent(slug)}/artifact?${wsQuery(ws.id)}`)
      const noGrantBody = await noGrant.text()

      const who = await person(prd, ws.id, "PRD commenter")
      await grant(request, ws.id, slug, who.userId, "comment")
      const api = who.api
      const raw = await api.get(`/api/v1/pages/${encodeURIComponent(slug)}/artifact?${wsQuery(ws.id)}`)
      const rawBody = await raw.text()
      expect(raw.status(), "the Comment role reads").toBe(200)
      const read = await snapshot(api, ws.id, slug)

      const base = `/api/v1/pages/${encodeURIComponent(slug)}`
      const q = wsQuery(ws.id)
      const text = `Feedback ${tag()}`
      const posted = await api.post(`${base}/artifact/comments?${q}`, { data: { version: 1, path: "index.html", selector: HERO, quote: V1, text, send_to_agent: false } })
      const postedBody = (await posted.json().catch(() => ({}))) as { id?: string; task_id?: string | null }
      const attempts = {
        readPrivate: (await api.get(`${base}/artifact?${q}&version=2`)).status(),
        commentPrivate: (await api.post(`${base}/artifact/comments?${q}`, { data: { version: 2, path: "index.html", selector: HERO, quote: PRIVATE, text: "private target", send_to_agent: false } })).status(),
        publish: (await api.post(`${base}/artifact/versions?${q}`, { data: { base_version: 2, entrypoint: "index.html", files: threeScreens("commenter write"), message: "commenter" } })).status(),
        restore: (await api.post(`${base}/artifact/restore?${q}`, { data: { base_version: 2, version: 1 } })).status(),
        share: (await api.post(`${base}/artifact/share?${q}`, { data: { base_version: 2, version: 2 } })).status(),
        grantSelf: (await api.put(`${base}/grants?${q}`, { data: { subject_type: "user", subject: who.userId, level: "write" } })).status(),
        delete: (await api.delete(`${base}?${q}`)).status(),
      }
      const after = await snapshot(request, ws.id, slug)
      await prd.evidence("result.json", {
        mode: prd.mode, appSha: prd.appSha, workspace: ws.id, slug,
        noGrant: { status: noGrant.status(), holdsContent: noGrantBody.includes(V1) || noGrantBody.includes(PRIVATE) },
        read: { canComment: read.can_comment, canWrite: read.can_write ?? false, versions: read.versions.map((v) => v.version), hero: heroOf(read), origin: read.origin ?? null, holdsPrivate: rawBody.includes(PRIVATE) },
        comment: { status: posted.status(), taskId: postedBody.task_id ?? null },
        attempts,
        after: { latest: after.latest_version, shared: after.shared_version, hero: heroOf(after) },
      })

      expect(REFUSED, "a member without a grant is refused").toContain(noGrant.status())
      expect(noGrantBody, "the refusal holds no content").not.toContain(V1)
      expect(rawBody, "the private version is not in the Comment role's read").not.toContain(PRIVATE)
      expect(read.can_comment, "can_comment").toBe(true)
      expect(read.can_write ?? false, "can_write").toBe(false)
      expect(read.versions.map((v) => v.version), "only the shared version is listed").toEqual([1])
      expect(heroOf(read)).toBe(V1)
      expect(posted.status(), "the comment is saved").toBe(201)
      expect(postedBody.task_id ?? null, "a Comment grant queues no agent task").toBeNull()
      expect(after.comments.find((c) => c.id === postedBody.id)?.text, "the owner sees the comment").toBe(text)
      expect(REFUSED, `read of the private version → ${attempts.readPrivate}`).toContain(attempts.readPrivate)
      expect([403, 404, 409], `comment on the private version → ${attempts.commentPrivate}`).toContain(attempts.commentPrivate)
      for (const what of ["publish", "restore", "share", "grantSelf", "delete"] as const) {
        expect(REFUSED, `${what} → ${attempts[what]}`).toContain(attempts[what])
      }
      expect({ latest: after.latest_version, shared: after.shared_version, hero: heroOf(after) }, "nothing changed").toEqual({ latest: 2, shared: 1, hero: PRIVATE })
    },
  )

  test(
    "Comment role on a Project in the browser: comments, no write controls",
    { annotation: prdRequirement("role.project.comment-browser", "Comment role on a Project (browser): sees the shared version, saves an element comment, has no write controls and the source editor stays closed") },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      await enableProjects(request, ws.id)
      const slug = projectSlug()
      const V1 = `Shared ${tag()}`
      const PRIVATE = `Private ${tag()}`
      await createProject(request, ws.id, slug, threeScreens(V1))
      await shareProject(request, ws.id, slug, 1, 1)
      expect((await publish(request, ws.id, slug, 1, threeScreens(PRIVATE), "private v2")).status()).toBe(200)
      const who = await person(prd, ws.id, "PRD commenter")
      const traffic = watchTraffic(who.context)
      traffic.forbid(PRIVATE)
      await grant(request, ws.id, slug, who.userId, "comment")

      await openProject(who.page, ws.id, slug)
      await expect(preview(who.page).locator(HERO)).toHaveText(V1)

      const text = `Feedback ${tag()}`
      const anchor = await commentOnHero(who.page, text, 1)
      const form = who.page.getByRole("form", { name: "New comment" })
      await expect(form.getByRole("button", { name: "Send to agent" }), "a Comment grant never starts an agent").toHaveCount(0)
      const saved = who.page.waitForResponse((r) => r.url().includes("/artifact/comments") && r.request().method() === "POST")
      await form.getByRole("button", { name: "Save comment", exact: true }).click()
      const res = await saved
      expect(res.status(), await res.text()).toBe(201)
      await expect(who.page.getByText("Comment saved", { exact: true })).toBeVisible({ timeout: 15_000 })

      const controls: Record<string, number> = {}
      for (const name of ["Restore as new version", "Send to agent", "Manage versions", "Review versions", /^Share v\d+$/] as const) {
        controls[String(name)] = await who.page.getByRole("button", { name, exact: typeof name === "string" }).count()
      }
      controls["Edit source link"] = await who.page.getByRole("link", { name: "Edit source" }).count()
      await who.page.screenshot({ path: test.info().outputPath("commenter.png"), fullPage: true })

      // The source editor address is one typed URL away.
      await who.page.goto(projectURL(slug, ws.id), { waitUntil: "domcontentloaded" })
      await expect(who.page.locator('iframe[title="Artifact preview"],iframe[title^="Preview of"]').first()).toBeVisible({ timeout: 60_000 })
      await pause(1_500)
      const sourceControls = {
        editSource: await who.page.getByRole("button", { name: "Edit source", exact: true }).count(),
        saveProject: await who.page.getByRole("button", { name: "Save project", exact: true }).count(),
        sourceBox: await sourceBox(who.page).count(),
      }

      const after = await snapshot(request, ws.id, slug)
      const stored = after.comments.find((c) => c.text === text)
      await prd.evidence("result.json", {
        mode: prd.mode, appSha: prd.appSha, workspace: ws.id, slug, anchor, controls, sourceControls, url: new URL(who.page.url()).pathname,
        stored: stored ? { version: stored.version, selector: stored.selector, task: stored.task_id ?? null } : null,
        after: { latest: after.latest_version, shared: after.shared_version }, leaks: traffic.hits,
      })
      expect(stored, "the owner sees the comment").toBeTruthy()
      expect(stored).toMatchObject({ version: 1, path: "index.html", selector: HERO })
      expect(stored?.task_id ?? null, "no agent task").toBeNull()
      expect(controls, "no write control is offered").toEqual(Object.fromEntries(Object.keys(controls).map((k) => [k, 0])))
      expect(sourceControls, "the source editor stays closed").toEqual({ editSource: 0, saveProject: 0, sourceBox: 0 })
      expect({ latest: after.latest_version, shared: after.shared_version }).toEqual({ latest: 2, shared: 1 })
      expect(traffic.hits, "the private version never reached this browser").toEqual([])
    },
  )

  test(
    "Read role on a Design through the API: reads the shared version, every write is refused",
    { annotation: prdRequirement("role.design.read-api", "Read role on a Design (API and socket): reads the shared version only; operations, snapshot, restore, share, comment, grant and a raw socket update are refused") },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      await enableDesign(request, ws.id)
      const SHARED = `Shared ${tag()}`
      const PRIVATE = `Private ${tag()}`
      const SOCKET = `Socket ${tag()}`
      const d = await seedDesign(request, ws.id, SHARED)
      await ownerApply(request, ws.id, d, d.text, PRIVATE)
      const privateVersion = (await getDesign(request, ws.id, d.slug)).latest_version!

      const stranger = await person(prd, ws.id, "PRD no grant")
      const noGrant = await stranger.api.get(scoped(`/api/v1/pages/${encodeURIComponent(d.slug)}/design`, ws.id))
      const noGrantBody = await noGrant.text()

      const who = await person(prd, ws.id, "PRD reader")
      await grant(request, ws.id, d.slug, who.userId, "read")
      const api = who.api
      const raw = await api.get(scoped(`/api/v1/pages/${encodeURIComponent(d.slug)}/design`, ws.id))
      const rawBody = await raw.text()
      expect(raw.status(), "the Read role reads").toBe(200)
      const read = await getDesign(api, ws.id, d.slug)
      const versions = await listVersions(api, ws.id, d.slug)

      const base = `/api/v1/pages/${encodeURIComponent(d.slug)}`
      const post = async (sub: string, data: unknown) => (await api.post(scoped(`${base}/design/${sub}`, ws.id), { data })).status()
      const attempts = {
        readPrivate: (await api.get(scoped(`${base}/design?version=${privateVersion}`, ws.id))).status(),
        operations: await post("operations", { operations: [{ op: "upsert", node: { ...d.text, text: "reader write" } }], message: "reader" }),
        snapshot: await post("snapshots", { message: "reader" }),
        restore: await post("restore", { version: d.version }),
        share: await post("share", { version: privateVersion }),
        comment: await post("comments", { version: d.version, path: "design.json", selector: d.text.id, quote: "", text: "reader", send_to_agent: false }),
        grantSelf: (await api.put(scoped(`${base}/grants`, ws.id), { data: { subject_type: "user", subject: who.userId, level: "write" } })).status(),
        delete: (await api.delete(scoped(base, ws.id))).status(),
      }

      // The raw socket: a read-only connection that sends a document update.
      await who.page.goto("/artifacts")
      const socket = await socketWrite(who.page, ws.id, d.pageId, designUpdateFrame(SOCKET))
      const after = await getDesign(request, ws.id, d.slug)
      await prd.evidence("result.json", {
        mode: prd.mode, appSha: prd.appSha, workspace: ws.id, design: d.slug,
        noGrant: { status: noGrant.status(), holdsContent: noGrantBody.includes(SHARED) },
        read: { canEdit: read.can_edit, canComment: read.can_comment ?? null, version: read.version, sharedText: read.nodes.find((n) => n.id === d.text.id)?.text, holdsPrivate: rawBody.includes(PRIVATE) },
        versions: versions.versions.map((v) => v.version), attempts, socket,
        after: { latest: after.latest_version, text: after.nodes.find((n) => n.id === d.text.id)?.text, socketText: after.nodes.some((n) => n.text === SOCKET) },
      })

      expect(REFUSED, "a member without a grant is refused").toContain(noGrant.status())
      expect(noGrantBody).not.toContain(SHARED)
      expect(read.can_edit, "can_edit").toBe(false)
      expect(read.nodes.find((n) => n.id === d.text.id)?.text, "the reader gets the shared version").toBe(SHARED)
      expect(rawBody, "the live text is not in the reader's read").not.toContain(PRIVATE)
      expect(versions.versions.map((v) => v.version), "only the shared version is listed").toEqual([d.version])
      for (const [what, status] of Object.entries(attempts)) expect(REFUSED, `${what} → ${status}`).toContain(status)
      expect(socket.closed && socket.code === 1008, `the socket refuses the update (close ${socket.code})`).toBe(true)
      expect(after.latest_version, "no new version").toBe(privateVersion)
      expect(after.nodes.find((n) => n.id === d.text.id)?.text).toBe(PRIVATE)
      expect(after.nodes.some((n) => n.text === SOCKET), "the socket update did not land").toBe(false)
    },
  )

  test(
    "Read role on a Design in the browser: follows the shared version, cannot edit and sees nothing private",
    { annotation: prdRequirement("role.design.read-browser", "Read role on a Design (browser): sees the shared version, read-only controls, follows a newly shared version and never receives unshared live edits") },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      await enableDesign(request, ws.id)
      const SHARED = `Shared ${tag()}`
      const LIVE = `Live ${tag()}`
      const NEXT = `Next ${tag()}`
      const d = await seedDesign(request, ws.id, SHARED)
      const who = await person(prd, ws.id, "PRD reader")
      const traffic = watchTraffic(who.context)
      traffic.forbid(LIVE)
      await grant(request, ws.id, d.slug, who.userId, "read")

      await openDesignOrFail(who.page, d.slug, ws.id)
      await expectOnCanvas(who.page, SHARED)
      await expect(who.page.getByText("Read only", { exact: true })).toBeVisible({ timeout: 15_000 })
      const tools = who.page.getByLabel("Design tools")
      const controls = {
        screen: await tools.getByRole("button", { name: "Screen" }).count(),
        text: await tools.getByRole("button", { name: "Text", exact: true }).count(),
        save: await who.page.getByRole("button", { name: "Save version", exact: true }).count(),
        comment: await who.page.getByRole("button", { name: "Save comment", exact: true }).count(),
      }
      await who.page.getByLabel("Layers").getByRole("button", { name: "PRD first", exact: true }).click()
      await expect(who.page.getByLabel("Text content"), "the text field is locked").toBeDisabled({ timeout: 15_000 })
      await expect(who.page.getByRole("button", { name: /^Delete / }), "no delete control").toHaveCount(0)

      // A drag must not move anything on the shared canvas.
      const node = await who.page.locator(`[data-design-node="${d.text.id}"]`).boundingBox()
      expect(node, "the layer is on the canvas").toBeTruthy()
      await who.page.mouse.move(node!.x + 30, node!.y + 10)
      await who.page.mouse.down()
      await who.page.mouse.move(node!.x + 130, node!.y + 90, { steps: 5 })
      await who.page.mouse.up()

      // A live edit, not shared: silence over several polls, a socket frame included.
      await ownerApply(request, ws.id, d, d.text, LIVE)
      await pause(QUIET_MS)
      const quiet = await pageText(who.page)
      const ownerState = await getDesign(request, ws.id, d.slug)
      const moved = ownerState.nodes.find((n) => n.id === d.text.id)

      // The positive control: the same tab does follow what is shared.
      const nextVersion = (await ownerApply(request, ws.id, d, d.text, NEXT)).version!
      await shareDesign(request, ws.id, d.slug, nextVersion)
      const followedMs = await expectOnCanvas(who.page, NEXT, 45_000)
      await who.page.screenshot({ path: test.info().outputPath("reader.png"), fullPage: true })
      await pause(1_000)

      await prd.evidence("result.json", {
        mode: prd.mode, appSha: prd.appSha, workspace: ws.id, design: d.slug, controls,
        afterDrag: { x: moved?.x, y: moved?.y }, seed: { x: d.text.x, y: d.text.y },
        liveTextInDom: quiet.includes(LIVE), followedMs, leaks: traffic.hits,
      })
      expect(controls, "no edit control is offered").toEqual({ screen: 0, text: 0, save: 0, comment: 0 })
      expect({ x: moved?.x, y: moved?.y }, "the drag moved nothing").toEqual({ x: d.text.x, y: d.text.y })
      expect(quiet.includes(LIVE), "the unshared live edit is not on the reader's page").toBe(false)
      expect(traffic.hits, "the unshared live edit never reached this browser").toEqual([])
    },
  )

  test(
    "revoking a Project while it is open: the tab drops it and new content never arrives",
    { annotation: prdRequirement("role.project.revoke-open", "Revocation with a Project open: the tab drops the preview, says so and gets no newer content; a later read and a reload are refused") },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      await enableProjects(request, ws.id)
      const slug = projectSlug()
      const V1 = `One ${tag()}`
      const V2 = `Two ${tag()}`
      const V3 = `Three ${tag()}`
      await createProject(request, ws.id, slug, threeScreens(V1))
      await shareProject(request, ws.id, slug, 1, 1)
      const who = await person(prd, ws.id, "PRD reader")
      const traffic = watchTraffic(who.context)
      traffic.forbid(V3)
      await grant(request, ws.id, slug, who.userId, "read")
      await openProject(who.page, ws.id, slug)
      await expect(preview(who.page).locator(HERO)).toHaveText(V1)

      // The positive control: while the grant holds, the open tab follows.
      expect((await publish(request, ws.id, slug, 1, threeScreens(V2), "v2")).status()).toBe(200)
      await shareProject(request, ws.id, slug, 2, 2)
      await expect(preview(who.page).locator(HERO)).toHaveText(V2, { timeout: 45_000 })

      await revoke(request, ws.id, slug, who.userId)
      const goneMs = await expectProjectGone(who.page)
      expect((await publish(request, ws.id, slug, 2, threeScreens(V3), "v3")).status()).toBe(200)
      await shareProject(request, ws.id, slug, 3, 3)
      await pause(QUIET_MS)
      const inTab = await pageText(who.page)
      await who.page.screenshot({ path: test.info().outputPath("revoked.png"), fullPage: true })

      const q = wsQuery(ws.id)
      const base = `/api/v1/pages/${encodeURIComponent(slug)}`
      const later = {
        latest: (await who.api.get(`${base}/artifact?${q}`)).status(),
        old: (await who.api.get(`${base}/artifact?${q}&version=2`)).status(),
        page: (await who.api.get(`${base}?${q}`)).status(),
      }
      await who.page.reload({ waitUntil: "domcontentloaded" })
      await expect(who.page.getByText(NO_ACCESS).first(), "a reload stays refused").toBeVisible({ timeout: 45_000 })
      const reloaded = await pageText(who.page)
      await prd.evidence("result.json", {
        mode: prd.mode, appSha: prd.appSha, workspace: ws.id, slug, goneMs, later,
        tab: { oldInDom: inTab.includes(V2), newInDom: inTab.includes(V3) }, reload: { oldInDom: reloaded.includes(V2), newInDom: reloaded.includes(V3) }, leaks: traffic.hits,
      })
      expect(inTab.includes(V2), "the old content left the tab").toBe(false)
      expect(inTab.includes(V3), "the new content never reached the tab").toBe(false)
      for (const [what, status] of Object.entries(later)) expect(REFUSED, `later ${what} read → ${status}`).toContain(status)
      expect(reloaded.includes(V2) || reloaded.includes(V3), "a reload shows no content").toBe(false)
      expect(traffic.hits, "new content never reached this browser").toEqual([])
    },
  )

  test(
    "revoking a Design while it is open: the canvas goes and no new frame arrives",
    { annotation: prdRequirement("role.design.revoke-open", "Revocation with a Design open: reader and live editor lose the canvas, get no newer frame and are refused on a later read, a reload and a new socket") },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      await enableDesign(request, ws.id)
      const SHARED = `Shared ${tag()}`
      const SEEN = `Seen ${tag()}`
      const AFTER = `After ${tag()}`
      const d = await seedDesign(request, ws.id, SHARED)
      const reader = await person(prd, ws.id, "PRD reader")
      const editor = await person(prd, ws.id, "PRD editor")
      const traffic = { reader: watchTraffic(reader.context), editor: watchTraffic(editor.context) }
      traffic.reader.forbid(AFTER)
      traffic.editor.forbid(AFTER)
      await grant(request, ws.id, d.slug, reader.userId, "read")
      await grant(request, ws.id, d.slug, editor.userId, "read")
      await grant(request, ws.id, d.slug, editor.userId, "write")
      await openDesignOrFail(reader.page, d.slug, ws.id)
      await openDesignOrFail(editor.page, d.slug, ws.id)
      await expectOnCanvas(reader.page, SHARED)

      // The positive control: the editor's socket carries live edits.
      await ownerApply(request, ws.id, d, d.text, SEEN)
      await expectOnCanvas(editor.page, SEEN)

      await revoke(request, ws.id, d.slug, reader.userId)
      await revoke(request, ws.id, d.slug, editor.userId)
      const started = Date.now()
      const [readerMs, editorMs] = await Promise.all([expectDesignGone(reader.page), expectDesignGone(editor.page)])
      const secondsToGone = (Date.now() - started) / 1000
      const version = (await ownerApply(request, ws.id, d, d.text, AFTER)).version!
      await shareDesign(request, ws.id, d.slug, version)
      await pause(QUIET_MS)
      const inTab = { reader: await pageText(reader.page), editor: await pageText(editor.page) }
      await editor.page.screenshot({ path: test.info().outputPath("editor-revoked.png"), fullPage: true })

      const later: Record<string, number> = {}
      for (const [name, p] of [["reader", reader], ["editor", editor]] as const) {
        later[`${name} read`] = (await p.api.get(scoped(`/api/v1/pages/${encodeURIComponent(d.slug)}/design`, ws.id))).status()
        later[`${name} versions`] = (await p.api.get(scoped(`/api/v1/pages/${encodeURIComponent(d.slug)}/design/versions`, ws.id))).status()
        later[`${name} write`] = (await p.api.post(scoped(`/api/v1/pages/${encodeURIComponent(d.slug)}/design/operations`, ws.id), { data: { operations: [{ op: "upsert", node: { ...d.text, text: "late write" } }] } })).status()
      }
      const socket = await socketWrite(editor.page, ws.id, d.pageId, designUpdateFrame(`Late ${tag()}`))
      await reader.page.reload({ waitUntil: "domcontentloaded" })
      await expect(reader.page.locator('svg[aria-label="Design canvas"]'), "a reload stays refused").toHaveCount(0, { timeout: 45_000 })
      await expect(spokenAlert(reader.page)).toBeVisible({ timeout: 30_000 })
      const reloaded = await pageText(reader.page)
      const head = await getDesign(request, ws.id, d.slug)
      await prd.evidence("result.json", {
        mode: prd.mode, appSha: prd.appSha, workspace: ws.id, design: d.slug, readerMs, editorMs, secondsToGone, later, socket,
        tab: { readerOld: inTab.reader.includes(SHARED), editorOld: inTab.editor.includes(SEEN), readerNew: inTab.reader.includes(AFTER), editorNew: inTab.editor.includes(AFTER) },
        reloadShowsContent: reloaded.includes(SHARED) || reloaded.includes(AFTER), leaks: { reader: traffic.reader.hits, editor: traffic.editor.hits },
      })
      expect(inTab.reader.includes(SHARED) || inTab.editor.includes(SEEN), "the old content left the tabs").toBe(false)
      expect(inTab.reader.includes(AFTER) || inTab.editor.includes(AFTER), "the new content never reached the tabs").toBe(false)
      for (const [what, status] of Object.entries(later)) expect(REFUSED, `later ${what} → ${status}`).toContain(status)
      expect(socket.opened, "a new socket is refused").toBe(false)
      expect(reloaded.includes(SHARED) || reloaded.includes(AFTER), "a reload shows no content").toBe(false)
      expect(head.nodes.find((n) => n.id === d.text.id)?.text, "no late write landed").toBe(AFTER)
      expect([...traffic.reader.hits, ...traffic.editor.hits], "new content never reached the browsers").toEqual([])
    },
  )

  test(
    "removing a member with a Project open: the tab drops it and later reads are refused",
    { annotation: prdRequirement("role.project.member-removal", "Member removal with a Project open: the tab drops the preview, gets no newer content and a later read is refused") },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      await enableProjects(request, ws.id)
      const slug = projectSlug()
      const V1 = `One ${tag()}`
      const V2 = `Two ${tag()}`
      await createProject(request, ws.id, slug, threeScreens(V1))
      await shareProject(request, ws.id, slug, 1, 1)
      const who = await person(prd, ws.id, "PRD leaver")
      const traffic = watchTraffic(who.context)
      traffic.forbid(V2)
      await grant(request, ws.id, slug, who.userId, "read")
      await openProject(who.page, ws.id, slug)
      await expect(preview(who.page).locator(HERO)).toHaveText(V1)

      await removeMember(request, ws.id, who.userId)
      const goneMs = await expectProjectGone(who.page)
      expect((await publish(request, ws.id, slug, 1, threeScreens(V2), "v2")).status()).toBe(200)
      await shareProject(request, ws.id, slug, 2, 2)
      await pause(QUIET_MS)
      const inTab = await pageText(who.page)
      const later = (await who.api.get(`/api/v1/pages/${encodeURIComponent(slug)}/artifact?${wsQuery(ws.id)}`)).status()
      await prd.evidence("result.json", { mode: prd.mode, appSha: prd.appSha, workspace: ws.id, slug, goneMs, later, oldInDom: inTab.includes(V1), newInDom: inTab.includes(V2), leaks: traffic.hits })
      expect(inTab.includes(V1), "the old content left the tab").toBe(false)
      expect(inTab.includes(V2), "the new content never reached the tab").toBe(false)
      expect(REFUSED, `later read → ${later}`).toContain(later)
      expect(traffic.hits, "new content never reached this browser").toEqual([])
    },
  )

  test(
    "removing a member with a Design open: the canvas goes and no new frame arrives",
    { annotation: prdRequirement("role.design.member-removal", "Member removal with a Design open: the live editor loses the canvas, gets no newer frame and a later read and socket are refused") },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      await enableDesign(request, ws.id)
      const SHARED = `Shared ${tag()}`
      const AFTER = `After ${tag()}`
      const d = await seedDesign(request, ws.id, SHARED)
      const who = await person(prd, ws.id, "PRD leaver")
      const traffic = watchTraffic(who.context)
      traffic.forbid(AFTER)
      await grant(request, ws.id, d.slug, who.userId, "write")
      await openDesignOrFail(who.page, d.slug, ws.id)
      await expectOnCanvas(who.page, SHARED)

      await removeMember(request, ws.id, who.userId)
      const goneMs = await expectDesignGone(who.page)
      await ownerApply(request, ws.id, d, d.text, AFTER)
      await pause(QUIET_MS)
      const inTab = await pageText(who.page)
      const later = (await who.api.get(scoped(`/api/v1/pages/${encodeURIComponent(d.slug)}/design`, ws.id))).status()
      const socket = await socketWrite(who.page, ws.id, d.pageId, designUpdateFrame(`Late ${tag()}`))
      await prd.evidence("result.json", { mode: prd.mode, appSha: prd.appSha, workspace: ws.id, design: d.slug, goneMs, later, socket, oldInDom: inTab.includes(SHARED), newInDom: inTab.includes(AFTER), leaks: traffic.hits })
      expect(inTab.includes(SHARED), "the old content left the tab").toBe(false)
      expect(inTab.includes(AFTER), "the new content never reached the tab").toBe(false)
      expect(REFUSED, `later read → ${later}`).toContain(later)
      expect(socket.opened, "a new socket is refused").toBe(false)
      expect(traffic.hits, "new content never reached this browser").toEqual([])
    },
  )

  test(
    "two people on a stale Design canvas, different layers: both changes survive",
    { annotation: prdRequirement("role.design.stale-merge", "Design: two people work on a stale canvas on different layers; after the reconnect both changes are on both canvases and in the next saved version") },
    async ({ prd, request, page, context }) => {
      const ws = await prd.qaWorkspace()
      await enableDesign(request, ws.id)
      await pinWorkspace(context, ws.id)
      const BASE = `Base ${tag()}`
      const d = await seedDesign(request, ws.id, BASE)
      const who = await person(prd, ws.id, "PRD second")
      await grant(request, ws.id, d.slug, who.userId, "read")
      await grant(request, ws.id, d.slug, who.userId, "write")
      const gate = await gateDesignSocket(who.context)
      await openDesignOrFail(page, d.slug, ws.id)
      await openDesignOrFail(who.page, d.slug, ws.id)
      await expectOnCanvas(who.page, BASE)

      await gate.cut()
      const OWNER = `Owner ${tag()}`
      const SECOND = `Second ${tag()}`
      await typeOnLayer(page, "PRD first", OWNER)
      await typeOnLayer(who.page, "PRD second", SECOND)
      gate.restore()
      await connected(who.page)
      await expectOnCanvas(who.page, OWNER, 45_000)
      await expectOnCanvas(page, SECOND, 45_000)

      const saved = page.waitForResponse((r) => r.url().includes("/design/snapshots") && r.request().method() === "POST" && r.ok(), { timeout: 45_000 })
      await page.getByRole("button", { name: "Save version", exact: true }).click()
      await saved
      const head = await getDesign(request, ws.id, d.slug)
      const texts = head.nodes.map((n) => n.text)
      await prd.evidence("result.json", { mode: prd.mode, appSha: prd.appSha, workspace: ws.id, design: d.slug, latest: head.latest_version, texts })
      expect(texts, "the saved canvas holds the first person's change").toContain(OWNER)
      expect(texts, "the saved canvas holds the second person's change").toContain(SECOND)
    },
  )

  test(
    "two people on a stale Design canvas, same layer: neither loses work and the later one sees the conflict",
    { annotation: prdRequirement("role.design.conflict", "Design: two people change one layer from a stale canvas; both texts stay recoverable and the person who reconnects later sees the conflict") },
    async ({ prd, request, page, context }) => {
      const ws = await prd.qaWorkspace()
      await enableDesign(request, ws.id)
      await pinWorkspace(context, ws.id)
      const BASE = `Base ${tag()}`
      const d = await seedDesign(request, ws.id, BASE)
      const who = await person(prd, ws.id, "PRD second")
      await grant(request, ws.id, d.slug, who.userId, "read")
      await grant(request, ws.id, d.slug, who.userId, "write")
      const gate = await gateDesignSocket(who.context)
      await openDesignOrFail(page, d.slug, ws.id)
      await openDesignOrFail(who.page, d.slug, ws.id)
      await expectOnCanvas(who.page, BASE)

      // The second person is cut off and keeps typing on the stale canvas; the
      // first person changes the same layer and saves a version.
      await gate.cut()
      const OWNER = `Owner ${tag()}`
      const LATER = `Later ${tag()}`
      await typeOnLayer(who.page, "PRD first", LATER)
      await typeOnLayer(page, "PRD first", OWNER)
      const saved = page.waitForResponse((r) => r.url().includes("/design/snapshots") && r.request().method() === "POST" && r.ok(), { timeout: 45_000 })
      await page.getByRole("button", { name: "Save version", exact: true }).click()
      await saved
      await pause(2_000)

      gate.restore()
      await connected(who.page)
      await pause(14_000)
      const owner = await canvasTexts(page)
      const later = await canvasTexts(who.page)
      const messages = await visibleMessages(who.page)
      await who.page.screenshot({ path: test.info().outputPath("later-person.png"), fullPage: true })
      const head = await getDesign(request, ws.id, d.slug)
      const history = await versionTexts(request, ws.id, d.slug, d.text.id)
      const headText = head.nodes.find((n) => n.id === d.text.id)?.text
      // The notice counts only because it survives a reload: it is a sealed draft.
      const notice = who.page.getByRole("alert").filter({ hasText: "Konflikt úprav" })
      const conflictShown = (await notice.count()) > 0
      const loser = headText === LATER ? OWNER : LATER
      await who.page.reload({ waitUntil: "domcontentloaded" })
      await connected(who.page)
      await expect(notice, "the notice survives a reload").toContainText(loser, { timeout: 30_000 })
      const noticeText = await notice.innerText()
      const recoverable = (text: string) => headText === text || history.includes(text) || owner.includes(text) || later.includes(text) || noticeText.includes(text)
      await prd.evidence(`result${test.info().repeatEachIndex || ""}.json`, {
        mode: prd.mode, appSha: prd.appSha, workspace: ws.id, design: d.slug,
        winner: headText === OWNER ? "first person" : headText === LATER ? "later person" : "neither", canvases: { owner, later }, history, messages, conflictShown,
        recoverable: { first: recoverable(OWNER), later: recoverable(LATER) },
      })
      expect.soft(recoverable(OWNER), "the first person's text is on the canvas or in a retained version").toBe(true)
      expect.soft(recoverable(LATER), "the later person's text is on the canvas or in a retained version").toBe(true)
      expect.soft(owner.includes(headText ?? ""), "the first canvas converged").toBe(true)
      expect.soft(later.includes(headText ?? ""), "the later canvas converged").toBe(true)
      expect(conflictShown, `the later person sees the conflict; the page said: ${messages.join(" | ")}`).toBe(true)
      await notice.getByRole("button", { name: "Obnovit tento text" }).click()
      await expect.poll(async () => (await getDesign(request, ws.id, d.slug)).nodes.find((n) => n.id === d.text.id)?.text, { timeout: 30_000 }).toBe(loser)
      await expect(notice, "the restored text ends the notice").toHaveCount(0)
    },
  )

  test(
    "losing the write right on a Project with an unsaved source draft: nothing is lost",
    { annotation: prdRequirement("role.project.revoke-draft", "Write right withdrawn with a Project source draft open: the save is refused, the draft stays visible, nothing reaches the server and it comes back with the right") },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      await enableProjects(request, ws.id)
      const slug = projectSlug()
      await createProject(request, ws.id, slug, threeScreens(`One ${tag()}`))
      await shareProject(request, ws.id, slug, 1, 1)
      const who = await person(prd, ws.id, "PRD editor")
      await grant(request, ws.id, slug, who.userId, "read")
      await grant(request, ws.id, slug, who.userId, "write")

      await who.page.goto(projectURL(slug, ws.id), { waitUntil: "domcontentloaded" })
      await who.page.getByRole("button", { name: "Edit source", exact: true }).click({ timeout: 60_000 })
      const MARK = `Draft ${tag()}`
      const box = sourceBox(who.page)
      const typed = (await box.inputValue()).replace("Fresh coffee every morning.", MARK)
      expect(typed).toContain(MARK)
      await box.fill(typed)
      await expect(who.page.getByText(/Kept in this browser until you save or discard/)).toBeVisible()
      await pause(1_000)

      await revoke(request, ws.id, slug, who.userId, "write")
      const save = who.page.waitForResponse((r) => r.request().method() === "POST" && r.url().includes("/artifact/versions"), { timeout: 10_000 }).catch(() => null)
      await who.page.getByRole("button", { name: "Save project", exact: true }).click({ timeout: 5_000 }).catch(() => {})
      const refused = await save
      const recovery = who.page.getByRole("textbox", { name: "Source recovery draft" })
      await expect(recovery, "the draft stays on screen after the right went").toBeVisible({ timeout: TAB_LOSES_ACCESS_MS })
      const shown = await recovery.inputValue()
      const messages = await visibleMessages(who.page)
      await who.page.screenshot({ path: test.info().outputPath("draft-after-revoke.png"), fullPage: true })
      const mid = await snapshot(request, ws.id, slug)

      await grant(request, ws.id, slug, who.userId, "write")
      await who.page.reload({ waitUntil: "domcontentloaded" })
      const restore = who.page.getByRole("button", { name: "Restore source draft", exact: true })
      await expect(restore, "the draft is offered again with the right").toBeVisible({ timeout: 60_000 })
      await restore.click()
      const back = await sourceBox(who.page).inputValue()
      const resaved = who.page.waitForResponse((r) => r.request().method() === "POST" && r.url().includes("/artifact/versions"), { timeout: 30_000 })
      await who.page.getByRole("button", { name: "Save project", exact: true }).click()
      const resavedStatus = (await resaved).status()
      const after = await snapshot(request, ws.id, slug)
      const stored = after.manifest.files.find((f) => f.path === "index.html")?.content ?? ""
      await prd.evidence("result.json", {
        mode: prd.mode, appSha: prd.appSha, workspace: ws.id, slug, saveAfterRevoke: refused?.status() ?? "not sent, the page had already gone read-only",
        draftVisible: shown.includes(MARK), messages, midLatest: mid.latest_version, restoredIdentical: back === typed, resavedStatus, latest: after.latest_version, storedHasDraft: stored.includes(MARK),
      })
      if (refused) expect(REFUSED, `the late save → ${refused.status()}`).toContain(refused.status())
      expect(shown, "the visible draft holds the typed text").toContain(MARK)
      if (refused) expect(messages.join(" | "), "the refusal is a sentence, not the API's error").toContain("Nemáte oprávnění tuto změnu uložit")
      expect(mid.latest_version, "nothing reached the server").toBe(1)
      expect(back, "the restored draft is what the person typed").toBe(typed)
      expect(resavedStatus, "with the right back the draft saves").toBe(200)
      expect(stored, "the saved version holds the draft").toContain(MARK)
    },
  )

  test(
    "losing every right on a Project with an unsaved source draft: the draft is gone from the screen and the browser, and stays gone",
    { annotation: prdRequirement("role.project.revoke-read-draft", "Read right withdrawn with a Project source draft open: the page says the access went and the draft was deleted, it leaves the screen and browser storage, and it does not come back with the access") },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      await enableProjects(request, ws.id)
      const slug = projectSlug()
      await createProject(request, ws.id, slug, threeScreens(`One ${tag()}`))
      await shareProject(request, ws.id, slug, 1, 1)
      const who = await person(prd, ws.id, "PRD editor")
      await grant(request, ws.id, slug, who.userId, "read")
      await grant(request, ws.id, slug, who.userId, "write")

      await who.page.goto(projectURL(slug, ws.id), { waitUntil: "domcontentloaded" })
      await who.page.getByRole("button", { name: "Edit source", exact: true }).click({ timeout: 60_000 })
      const MARK = `Draft ${tag()}`
      const box = sourceBox(who.page)
      const typed = (await box.inputValue()).replace("Fresh coffee every morning.", MARK)
      await box.fill(typed)
      await expect(who.page.getByText(/Kept in this browser until you save or discard/)).toBeVisible()
      await expect.poll(() => storedSourceDrafts(who.page, who.userId, ws.id), { timeout: 15_000, message: "the probe sees the draft while the person may read it" }).toBeGreaterThan(0)

      await revoke(request, ws.id, slug, who.userId)
      await expect(spokenAlert(who.page), "the page says the access went").toBeVisible({ timeout: TAB_LOSES_ACCESS_MS })
      const purged = await expect.poll(() => storedSourceDrafts(who.page, who.userId, ws.id), { timeout: TAB_LOSES_ACCESS_MS }).toBe(0).then(() => true, () => false)
      const said = (await who.page.getByRole("alert").allInnerTexts()).map((s) => s.trim()).filter(Boolean).join(" | ")
      const inTab = await who.page.locator("body").innerText()
      const fieldValues = await who.page.locator("textarea").evaluateAll((els) => els.map((el) => (el as HTMLTextAreaElement).value))
      await who.page.screenshot({ path: test.info().outputPath("draft-after-read-revoke.png"), fullPage: true })

      await grant(request, ws.id, slug, who.userId, "read")
      await grant(request, ws.id, slug, who.userId, "write")
      await who.page.reload({ waitUntil: "domcontentloaded" })
      await expect(who.page.getByRole("button", { name: /Edit source|Restore source draft/ }).first(), "the editor is back").toBeVisible({ timeout: 60_000 })
      await pause(2_500)
      const offered = await who.page.getByRole("button", { name: "Restore source draft", exact: true }).count()
      const onScreenAfter = (await who.page.locator("body").innerText()).includes(MARK)
      const storedAfter = await storedSourceDrafts(who.page, who.userId, ws.id)
      if (!offered) await who.page.getByRole("button", { name: "Edit source", exact: true }).click({ timeout: 60_000 })
      const editorAfter = offered ? "" : await sourceBox(who.page).inputValue()
      const kept = fieldValues.some((v) => v.includes(MARK)) || inTab.includes(MARK)
      await prd.evidence("result.json", { mode: prd.mode, appSha: prd.appSha, workspace: ws.id, slug, said, draftStillOnScreen: kept, purged, offeredAfterRegrant: offered, onScreenAfter, storedAfter })
      expect(said, "the page says the draft was deleted").toMatch(/draft has been deleted/i)
      expect(kept, "the draft left the screen").toBe(false)
      expect(purged, "the draft left browser storage").toBe(true)
      expect(offered, "no draft is offered after the access returns").toBe(0)
      expect(onScreenAfter || editorAfter.includes(MARK), "the draft is not on screen after the access returns").toBe(false)
      expect(storedAfter, "the draft did not reappear in browser storage").toBe(0)
    },
  )

  test(
    "losing the write right on a Design with stale work: it is not silently dropped",
    { annotation: prdRequirement("role.design.revoke-draft", "Write right withdrawn with unsynced Design work: the server takes nothing, the page says the work is kept and it comes back with the right") },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      await enableDesign(request, ws.id)
      const SHARED = `Shared ${tag()}`
      const d = await seedDesign(request, ws.id, SHARED)
      const who = await person(prd, ws.id, "PRD editor")
      await grant(request, ws.id, d.slug, who.userId, "read")
      await grant(request, ws.id, d.slug, who.userId, "write")
      const gate = await gateDesignSocket(who.context)
      await openDesignOrFail(who.page, d.slug, ws.id)
      await expectOnCanvas(who.page, SHARED)

      await gate.cut()
      const WORK = `Unsynced ${tag()}`
      await typeOnLayer(who.page, "PRD first", WORK)
      await pause(1_000)
      await revoke(request, ws.id, d.slug, who.userId, "write")
      gate.restore()
      await expect(who.page.getByText("Read only", { exact: true }), "the editor turns read-only").toBeVisible({ timeout: TAB_LOSES_ACCESS_MS })
      await pause(4_000)
      const messages = await visibleMessages(who.page)
      const onCanvas = await canvasTexts(who.page)
      await who.page.screenshot({ path: test.info().outputPath("design-draft-after-revoke.png"), fullPage: true })
      const mid = await getDesign(request, ws.id, d.slug)
      const midOnServer = mid.nodes.some((n) => n.text === WORK)

      await grant(request, ws.id, d.slug, who.userId, "write")
      await who.page.reload({ waitUntil: "domcontentloaded" })
      await connected(who.page)
      const returned = await expect.poll(() => canvasTexts(who.page), { timeout: 30_000 }).toContain(WORK).then(() => true, () => false)
      const headAfter = returned
        ? await expect.poll(async () => (await getDesign(request, ws.id, d.slug)).nodes.some((n) => n.text === WORK), { timeout: 30_000 }).toBe(true).then(() => true, () => false)
        : false
      const toldKept = messages.some((m) => /neodeslané změny zůstaly v tomto prohlížeči/.test(m))
      await prd.evidence("result.json", {
        mode: prd.mode, appSha: prd.appSha, workspace: ws.id, design: d.slug, messages, workStillOnCanvas: onCanvas.includes(WORK), serverTookWork: midOnServer, returnedAfterRegrant: returned, serverHasItAfterRegrant: headAfter,
      })
      expect(midOnServer, "the server took nothing from a person without the right").toBe(false)
      expect(messages.join(" | "), "the status never claims unsynced work is saved").not.toContain("changes saved")
      expect.soft(returned, "the work comes back with the right").toBe(true)
      expect.soft(headAfter, "the work reaches the server once the right is back").toBe(true)
      expect(onCanvas.includes(WORK) || toldKept, `the page shows the work or says it is kept; the page said: ${messages.join(" | ")}`).toBe(true)
    },
  )

})
