import { randomUUID } from "node:crypto"
import { expect, test, type APIRequestContext, type Frame, type Page, type WebSocketRoute } from "@playwright/test"
import { strToU8, unzipSync, strFromU8, zipSync } from "fflate"
import { loginStorageState } from "../acceptance/prd/auth"
import * as native from "../acceptance/prd/outage-lostack/native"

const origin = process.env.CRAFT_GATE_ORIGIN!
const workspace = process.env.CRAFT_GATE_WORKSPACE!
const account = { email: process.env.CRAFT_GATE_EMAIL!, password: process.env.CRAFT_GATE_PASSWORD! }
const media = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
const query = new URLSearchParams({ workspace_id: workspace })

function workbook(): Buffer {
  const types = "http://schemas.openxmlformats.org/package/2006/content-types"
  const rels = "http://schemas.openxmlformats.org/package/2006/relationships"
  const refs = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
  const sheet = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
  const prefix = "application/vnd.openxmlformats-officedocument.spreadsheetml"
  const files = {
    "[Content_Types].xml": `<Types xmlns="${types}"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="${prefix}.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="${prefix}.worksheet+xml"/><Override PartName="/xl/worksheets/sheet2.xml" ContentType="${prefix}.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="${prefix}.styles+xml"/></Types>`,
    "_rels/.rels": `<Relationships xmlns="${rels}"><Relationship Id="rId1" Type="${refs}/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
    "xl/workbook.xml": `<workbook xmlns="${sheet}" xmlns:r="${refs}"><sheets><sheet name="Data" sheetId="1" r:id="rId1"/><sheet name="Other" sheetId="2" r:id="rId2"/></sheets></workbook>`,
    "xl/_rels/workbook.xml.rels": `<Relationships xmlns="${rels}"><Relationship Id="rId1" Type="${refs}/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="${refs}/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Type="${refs}/styles" Target="styles.xml"/></Relationships>`,
    "xl/styles.xml": `<styleSheet xmlns="${sheet}"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/></cellXfs></styleSheet>`,
    "xl/worksheets/sheet1.xml": `<worksheet xmlns="${sheet}"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>saved base</t></is></c></row></sheetData></worksheet>`,
    "xl/worksheets/sheet2.xml": `<worksheet xmlns="${sheet}"><sheetData/></worksheet>`,
  }
  return Buffer.from(zipSync(Object.fromEntries(Object.entries(files).map(([path, xml]) => [path, strToU8(xml)]))))
}

const hasValue = (bytes: Buffer, value: string) =>
  Object.entries(unzipSync(bytes)).some(([path, file]) => path.endsWith(".xml") && strFromU8(file).includes(value))

async function createWorkbook(api: APIRequestContext, chatId: string) {
  const slug = `sheet-concurrency-${randomUUID()}`
  const name = slug
  const creation = await api.post(`/api/v1/pages?${query}`, { data: { name, slug, panels: [], allow_empty: true, created_by_test: true } })
  expect(creation.status(), await creation.text()).toBe(201)
  const { id: pageId } = await creation.json() as { id: string }
  const craft = (path: string) => `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/${path}?${query}`
  const init = await api.post(craft("initialize"), {
    headers: { "Content-Type": media, "X-Craft-File-Name": "concurrency.xlsx", "X-Craft-Chat": chatId },
    data: workbook(),
  })
  expect(init.status(), await init.text()).toBe(201)
  const { generation, session_token, version } = await init.json() as { generation: string; session_token: string; version: string }
  const release = await api.post(craft("lease/release"), { headers: { "X-Craft-Session": session_token }, data: { generation } })
  expect(release.status(), await release.text()).toBe(200)
  return { name, craft, ref: { pageId, workspaceId: workspace, initVersion: version } }
}

async function fixture(api: APIRequestContext) {
  const agents = await api.get(`/api/v1/agents?${query}`)
  expect(agents.status()).toBe(200)
  const raw = await agents.json()
  const agent = (Array.isArray(raw) ? raw : raw.agents)[0]
  expect(agent, "offline seed includes a chat agent").toBeTruthy()
  const chats = `/api/v1/agents/${encodeURIComponent(agent.id)}/chats`
  const createChat = async (title: string) => {
    const response = await api.post(`${chats}?${query}`, { data: { origin: "UI" } })
    expect(response.status()).toBe(201)
    const { id } = await response.json() as { id: string }
    const rename = await api.patch(`${chats}/${id}?${query}`, { data: { title } })
    expect(rename.ok()).toBe(true)
    return id
  }
  const title = `Sheet concurrency ${randomUUID().slice(0, 8)}`, otherTitle = `Other chat ${randomUUID().slice(0, 8)}`
  const chat = await createChat(title)
  const otherChat = await createChat(otherTitle)
  return { ...await createWorkbook(api, chat), chat, title, otherChat, otherTitle, url: `/chat/${agent.slug}?session=${chat}&${query}` }
}

async function readWorkbook(api: APIRequestContext, craft: (path: string) => string) {
  const session = await api.post(craft("sessions"), { data: {} })
  expect(session.status()).toBe(201)
  const { session_token } = await session.json() as { session_token: string }
  const stored = await api.get(craft("document"), { headers: { "X-Craft-Session": session_token } })
  expect(stored.status()).toBe(200)
  return {
    bytes: await stored.body(),
    version: stored.headers()["x-craft-version"],
    pin: (JSON.parse(stored.headers()["x-craft-document"]) as { enginePin: string }).enginePin,
  }
}

test("Sheet reconciles a committed save whose reply was lost", async ({ browser }) => {
  const context = await browser.newContext({ baseURL: origin, storageState: await loginStorageState(origin, account), viewport: native.VIEWPORT, acceptDownloads: true })
  try {
    const { name, craft, url, ref } = await fixture(context.request)
    const page = await context.newPage()
    let saves = 0
    let lostVersion: string | undefined
    await page.route("**/craft/document/save?**", async route => {
      saves++
      if (saves > 1) { await route.continue(); return }
      const reply = await route.fetch()
      expect(reply.ok(), "the server committed before the browser lost the reply").toBe(true)
      lostVersion = (await reply.json() as { version: string }).version
      await route.abort()
    })
    await page.goto(url)
    await page.getByRole("button", { name: `Open ${name}`, exact: true }).click()
    const frame = await acquire(page)
    await editCell(page, frame, "lost acknowledgement cell", "B2")
    const reconcile = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith("/craft/document/reconcile"))
    await page.keyboard.press("Control+s")
    expect((await reconcile).ok()).toBe(true)
    expect(lostVersion).toBe(String(Number(ref.initVersion) + 1))
    const panel = page.getByTestId("sheet-side-panel")
    await expect(panel.getByTestId("sheet-panel-state")).toHaveText(`Saved · version ${lostVersion}`)
    expect(saves, "the browser asks about the original save without sending it again").toBe(1)

    await panel.getByRole("button", { name: "Close Sheet" }).click()
    await page.getByRole("button", { name: `Open ${name}`, exact: true }).click()
    await expect(panel.getByTestId("sheet-panel-state")).toHaveText(`Viewing · version ${lostVersion}`)
    const head = await readWorkbook(context.request, craft)
    expect(head.version).toBe(lostVersion)
    expect(hasValue(head.bytes, "lost acknowledgement cell")).toBe(true)
    await panel.getByRole("button", { name: "More for this Sheet" }).click()
    const downloading = page.waitForEvent("download")
    await page.getByRole("menuitem", { name: /^Download \.xlsx/ }).click()
    const exported = await (await downloading).createReadStream()
    expect(hasValue(Buffer.concat(await exported.toArray()), "lost acknowledgement cell")).toBe(true)
  } finally {
    await context.close()
  }
})

async function acquire(page: Page) {
  await page.getByRole("button", { name: "Take over editing", exact: true }).click()
  const warning = page.getByRole("button", { name: "Edit anyway", exact: true })
  const handingBack = page.getByRole("button", { name: "Hand back", exact: true })
  await expect(warning.or(handingBack)).toBeVisible()
  if (await warning.isVisible()) await warning.click()
  await expect(handingBack).toBeVisible()
  const frame = await native.frameOf(page, "gridcraft")
  await frame.locator("canvas").first().click({ position: { x: 54, y: 240 } })
  return frame
}

async function editCell(page: Page, frame: Frame, value: string, cell: "A3" | "B2") {
  const position = cell === "A3" ? { x: 54, y: 255 } : { x: 104, y: 239 }
  await frame.locator("canvas").first().click({ position })
  await page.keyboard.type(value)
  await frame.waitForFunction(() => document.activeElement?.tagName === "INPUT", {}, { timeout: 5_000 })
  await page.keyboard.press("Enter")
  await expect(page.getByTestId("sheet-panel-state")).toHaveText("Not saved yet")
}

interface EditorEvent {
  type: string
  revision?: string
  savedRevision?: string
  baseRevision?: string
  code?: string
  keep?: string
  build?: string
  conflicts?: Array<{ sheet: string; cell: string; kept: string; agent: string }>
}

declare global {
  interface Window {
    sheetTestEvents: EditorEvent[]
    sheetTestFiles: number[][]
    sheetTestHoldPaint: boolean
    sheetTestReleasePaint(): void
  }
}

async function watchEditor(page: Page) {
  await page.addInitScript(() => {
    if (window !== window.top) return
    window.sheetTestEvents = []
    window.sheetTestFiles = []
    const channel = window.MessageChannel, outgoing = MessagePort.prototype.postMessage
    const remember = (message: EditorEvent & { bytes?: ArrayBuffer | Uint8Array }) => {
      if (/^gridcraft:(ready|execute|executed|opened|changed)$/.test(message?.type)) window.sheetTestEvents.push(message)
      if (message?.type === "gridcraft:save" && message.bytes) {
        const bytes = message.bytes instanceof Uint8Array ? message.bytes : new Uint8Array(message.bytes)
        window.sheetTestFiles.push(Array.from(bytes))
      }
    }
    window.MessageChannel = class extends channel {
      constructor() {
        super()
        this.port1.addEventListener("message", event => remember(event.data))
        this.port2.addEventListener("message", event => remember(event.data))
      }
    }
    MessagePort.prototype.postMessage = function (message: unknown, transfer?: Transferable[] | StructuredSerializeOptions) {
      remember(message as EditorEvent)
      Reflect.apply(outgoing, this, transfer === undefined ? [message] : [message, transfer])
    }
    const frame = window.requestAnimationFrame.bind(window)
    const timeout = window.setTimeout.bind(window)
    const deferred: Array<() => void> = []
    window.requestAnimationFrame = cb => window.sheetTestHoldPaint
      ? (deferred.push(() => frame(cb)), 0)
      : frame(cb)
    window.setTimeout = ((cb: TimerHandler, ms?: number, ...args: unknown[]) =>
      window.sheetTestHoldPaint && ms === 100 ? (deferred.push(() => timeout(cb, 0, ...args)), 0) : timeout(cb, ms, ...args)) as typeof window.setTimeout
    window.sheetTestReleasePaint = () => {
      window.sheetTestHoldPaint = false
      for (const start of deferred.splice(0)) start()
    }
  })
}

const cellSet = (cell: string, input: string) => ({ command: "cell.set", params: { cell, input } })
const hash = "sha256:" + "1".repeat(64)

for (const race of ["saved revision", "same cell", "other cell", "other sheet"] as const) {
  test(`Sheet live replay preserves the ${race}`, async ({ browser }) => {
    const context = await browser.newContext({ baseURL: origin, storageState: await loginStorageState(origin, account), viewport: native.VIEWPORT })
    try {
      const { name, craft, ref, chat, url } = await fixture(context.request)
      const { pin } = await readWorkbook(context.request, craft)
      expect(pin, "workbook has a real engine pin").toBeTruthy()
      const page = await context.newPage()
      await watchEditor(page)
      let socket: WebSocketRoute | undefined
      await page.routeWebSocket(/\/ws(?:\?|$)/, ws => {
        const server = ws.connectToServer()
        ws.onMessage(message => {
          if (typeof message === "string") {
            const body = JSON.parse(message) as { type?: string; channel?: string }
            if (body.type === "subscribe" && body.channel === `session:${chat}`) socket = ws
          }
          server.send(message)
        })
      })
      await page.goto(url)
      await page.getByRole("button", { name: `Open ${name}`, exact: true }).click()
      const panel = page.getByTestId("sheet-side-panel")
      const frame = await acquire(page)
      await editCell(page, frame, "human seed", "A3")
      await page.keyboard.press("Control+s")
      await expect(panel.getByTestId("sheet-panel-state")).toHaveText(/^Saved · version \d+$/, { timeout: 15_000 })
      const base = Number((await panel.getByTestId("sheet-panel-state").innerText()).match(/\d+$/)![0])
      let simulatedHead = base
      const send = (version: number, command: ReturnType<typeof cellSet>) => {
        simulatedHead = version
        const id = `sheet-change-${version}`
        const emit = (type: string, content: string, metadata: Record<string, unknown> = {}) =>
          socket!.send(JSON.stringify({ type: "chat_event", channel: `session:${chat}`, payload: { type, content, metadata } }))
        emit("tool_call", "craft_sheet_apply", { tool_id: id, tool_name: "craft_sheet_apply" })
        emit("tool_result", JSON.stringify({ page_id: ref.pageId, slug: name, version: String(version), base_version: String(version - 1), content_hash: hash, commands: [command] }), { tool_use_id: id })
        emit("done", "")
      }
      await expect.poll(() => Boolean(socket), { message: "real chat WebSocket connected" }).toBe(true)
      const executes = () => page.evaluate(() => window.sheetTestEvents.filter(event => event.type === "gridcraft:executed"))
      const operation = cellSet(race === "other sheet" ? "'Other'!B2" : race === "other cell" ? "'Data'!C2" : "'Data'!B2", "agent")
      await page.route("**/craft/document/operations?**", route => {
        const from = new URL(route.request().url()).searchParams.get("from")!
        const head = String(simulatedHead)
        if (from === head) return route.fulfill({ json: { from, head, replayable: true, versions: [] } })
        return route.fulfill({ json: { from, head, head_content_hash: hash, replayable: true, versions: [{ version: head, base_version: from, engine_pin: pin, actor_kind: "agent", replayable: true, ops: [operation] }] } })
      })

      if (race === "saved revision") {
        send(base + 1, cellSet("'Data'!A2", "first agent change"))
        await expect.poll(async () => (await executes()).length, { message: "the first version was replayed by GridCraft" }).toBe(1)
        await editCell(page, frame, "human", "B2")
        send(base + 2, operation)
      } else {
        await page.evaluate(() => { window.sheetTestHoldPaint = true })
        send(base + 1, operation)
        await expect(page.getByTestId("agent-working")).toBeVisible()
        await editCell(page, frame, "human", "B2")
        await page.evaluate(() => window.sheetTestReleasePaint())
      }
      const conflicts = race === "saved revision" || race === "same cell" ? [{ sheet: "Data", cell: "B2", kept: "human", agent: "agent" }] : []
      await expect.poll(async () => (await executes()).at(-1), { message: "GridCraft replays over the saved baseline and preserves the user's cells" })
        .toMatchObject({ savedRevision: expect.any(String), conflicts })
      if (race === "other sheet") {
        const readSnapshot = () => page.evaluate(() => ({ count: window.sheetTestFiles.length, bytes: window.sheetTestFiles.at(-1) ?? [] }))
        const contains = (bytes: number[]) => ({ human: hasValue(Buffer.from(bytes), "human</t>"), agent: hasValue(Buffer.from(bytes), "agent</t>") })
        await expect.poll(async () => contains((await readSnapshot()).bytes)).toEqual({ human: true, agent: true })
        const { count } = await readSnapshot()
        await page.keyboard.press("Control+z")
        await page.keyboard.press("Control+s")
        await expect.poll(async () => {
          const snapshot = await readSnapshot()
          return snapshot.count > count ? contains(snapshot.bytes) : null
        }, { message: "GridCraft Undo removes the replayed edit and keeps the person's cell in the exported workbook" }).toEqual({ human: true, agent: false })
      }
    } finally {
      await context.close()
    }
  })
}

for (const transition of ["switch chat", "narrow window"] as const) {
  test(`Sheet draft survives ${transition} before autosave`, async ({ browser }) => {
    const context = await browser.newContext({ baseURL: origin, storageState: await loginStorageState(origin, account), viewport: native.VIEWPORT, acceptDownloads: true })
    try {
      const { name, craft, ref, chat, title, otherChat, otherTitle, url } = await fixture(context.request)
      const page = await context.newPage()
      await page.goto(url)
      const open = page.getByRole("button", { name: `Open ${name}`, exact: true })
      await open.click()
      const panel = page.getByTestId("sheet-side-panel")
      const frame = await acquire(page)
      const marker = "draft" + randomUUID().replaceAll("-", "")
      await editCell(page, frame, marker, "B2")

      if (transition === "switch chat") {
        await page.getByRole("button", { name: otherTitle, exact: true }).click()
        await expect(page).toHaveURL(new RegExp(`session=${otherChat}`))
        await expect(panel).toBeHidden()
        await page.getByRole("button", { name: title, exact: true }).click()
        await expect(page).toHaveURL(new RegExp(`session=${chat}`))
      } else {
        await page.setViewportSize({ width: 600, height: 1000 })
        await expect(panel).toBeHidden()
        await page.setViewportSize(native.VIEWPORT)
      }

      const stored = await readWorkbook(context.request, craft)
      expect(stored.version).toBe(ref.initVersion)
      expect(hasValue(stored.bytes, "saved base")).toBe(true)
      expect(hasValue(stored.bytes, marker)).toBe(false)
      if (await panel.isHidden()) await open.click()
      await expect(panel.getByTestId("sheet-panel-state")).toHaveText(`Viewing · version ${ref.initVersion}`)
      await acquire(page)
      const copy = panel.getByRole("button", { name: "Keep a copy", exact: true })
      await expect(copy, "the edit before autosave is available as a local draft").toBeVisible()
      const downloading = page.waitForEvent("download")
      await copy.click()
      const download = await downloading
      const stream = await download.createReadStream()
      expect(hasValue(Buffer.concat(await stream.toArray()), marker)).toBe(true)
    } finally {
      await context.close()
    }
  })
}
