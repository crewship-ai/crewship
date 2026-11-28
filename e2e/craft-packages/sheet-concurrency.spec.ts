import { createHash, randomUUID } from "node:crypto"
import { expect, test, type APIRequestContext, type Frame, type Page, type WebSocketRoute } from "@playwright/test"
import { strToU8, unzipSync, strFromU8, zipSync } from "fflate"
import { loginStorageState } from "../acceptance/prd/auth"
import * as native from "../acceptance/prd/outage-lostack/native"

const origin = process.env.CRAFT_GATE_ORIGIN!
const workspace = process.env.CRAFT_GATE_WORKSPACE!
const account = { email: process.env.CRAFT_GATE_EMAIL!, password: process.env.CRAFT_GATE_PASSWORD! }
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

async function workbookCell(page: Page, bytes: Buffer, sheet: string, address: string, input?: string) {
  const files = unzipSync(bytes)
  const result = await page.evaluate(({ parts, sheet, address, input }) => {
    const parse = (xml: string) => new DOMParser().parseFromString(xml, "application/xml")
    const tags = (node: Document | Element, name: string) => Array.from(node.getElementsByTagNameNS("*", name))
    const workbook = parse(parts["xl/workbook.xml"])
    const namedSheet = tags(workbook, "sheet").find(node => node.getAttribute("name") === sheet)
    const id = namedSheet?.getAttributeNS("http://schemas.openxmlformats.org/officeDocument/2006/relationships", "id")
    const rels = parse(parts["xl/_rels/workbook.xml.rels"])
    const target = tags(rels, "Relationship").find(node => node.getAttribute("Id") === id)?.getAttribute("Target")
    if (!target) throw new Error("No workbook relationship for " + sheet)
    const path = new URL(target, "https://workbook.invalid/xl/workbook.xml").pathname.slice(1)
    if (!parts[path]) throw new Error("Missing worksheet part " + path)
    const worksheet = parse(parts[path])
    let cell = tags(worksheet, "c").find(node => node.getAttribute("r") === address)
    if (input !== undefined) {
      const create = (name: string) => worksheet.createElementNS(worksheet.documentElement.namespaceURI, name)
      if (!cell) {
        const rowNumber = Number(address.match(/\d+$/)![0])
        const data = tags(worksheet, "sheetData")[0]
        let row = tags(data, "row").find(node => Number(node.getAttribute("r")) === rowNumber)
        if (!row) {
          row = create("row")
          row.setAttribute("r", String(rowNumber))
          data.insertBefore(row, tags(data, "row").find(node => Number(node.getAttribute("r")) > rowNumber) ?? null)
        }
        cell = create("c")
        cell.setAttribute("r", address)
        // The fixture uses single-letter columns.
        row.insertBefore(cell, tags(row, "c").find(node => node.getAttribute("r")! > address) ?? null)
      }
      cell.setAttribute("t", "inlineStr")
      const inline = create("is"), text = create("t")
      text.textContent = input
      inline.append(text)
      cell.replaceChildren(inline)
      return { path, xml: new XMLSerializer().serializeToString(worksheet) }
    }
    const value = cell && tags(cell, "v")[0]?.textContent
    const text = (node?: Element) => node && tags(node, "t").map(tag => tag.textContent).join("")
    if (cell?.getAttribute("t") === "s" && value != null) {
      const strings = parse(parts["xl/sharedStrings.xml"])
      return { value: text(tags(strings, "si")[Number(value)]) }
    }
    return { value: cell?.getAttribute("t") === "inlineStr" ? text(cell) : value ?? undefined }
  }, { parts: Object.fromEntries(Object.entries(files).filter(([path]) => path.endsWith(".xml") || path.endsWith(".rels")).map(([path, content]) => [path, strFromU8(content)])), sheet, address, input })
  if ("xml" in result && result.xml !== undefined) {
    files[result.path] = strToU8(result.xml)
    return { bytes: Buffer.from(zipSync(files)) }
  }
  return { value: result.value }
}

async function createWorkbook(api: APIRequestContext, chatId: string) {
  const slug = `sheet-concurrency-${randomUUID()}`
  const name = slug
  const creation = await api.post(`/api/v1/pages?${query}`, { data: { name, slug, panels: [], allow_empty: true, created_by_test: true } })
  expect(creation.status(), await creation.text()).toBe(201)
  const { id: pageId } = await creation.json() as { id: string }
  const craft = (path: string) => `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/${path}?${query}`
  const init = await api.post(craft("initialize"), {
    headers: { "X-Craft-Chat": chatId },
    data: { application_id: "gridcraft", bytes: workbook().toString("base64") },
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
  const { bytes, version, enginePin, format } = await stored.json() as { bytes: string; version: string; enginePin: string; format: string }
  expect(format).toBe("xlsx")
  expect(version).toMatch(/^[1-9][0-9]*$/)
  expect(enginePin).toMatch(/^[a-f0-9]{64}$/)
  return { bytes: Buffer.from(bytes, "base64"), version, pin: enginePin }
}

test("Sheet reconciles a committed save whose reply was lost", async ({ browser }, info) => {
  const context = await browser.newContext({ baseURL: origin, storageState: await loginStorageState(origin, account), viewport: native.VIEWPORT, acceptDownloads: true })
  const page = await context.newPage()
  try {
    const { name, craft, url, ref } = await fixture(context.request)
    await watchEditor(page)
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
    const frame = await openEditor(page)
    await editCell(page, frame, "lost acknowledgement cell", "B2")
    const reconcile = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith("/craft/document/reconcile"))
    await page.keyboard.press("Control+s")
    expect((await reconcile).ok()).toBe(true)
    expect(lostVersion).toBe(String(Number(ref.initVersion) + 1))
    await expect.poll(() => page.evaluate(() => window.sheetTestEvents.find(event => event.type === "gridcraft:save-ack")?.version)).toBe(lostVersion)
    const panel = page.getByTestId("sheet-side-panel")
    await expect(panel.getByTestId("sheet-panel-state")).toHaveText("Saved")
    await expect(panel.getByTestId("sheet-panel-state")).toHaveAttribute("data-version", lostVersion!)
    expect(saves, "the browser asks about the original save without sending it again").toBe(1)

    await panel.getByRole("button", { name: "Close Sheet" }).click()
    await page.getByRole("button", { name: `Open ${name}`, exact: true }).click()
    await expect(panel.getByTestId("sheet-panel-state")).toHaveAttribute("data-version", lostVersion!)
    const head = await readWorkbook(context.request, craft)
    expect(head.version).toBe(lostVersion)
    expect(hasValue(head.bytes, "lost acknowledgement cell")).toBe(true)
    await panel.getByRole("button", { name: "More for this Sheet" }).click()
    const downloading = page.waitForEvent("download")
    await page.getByRole("menuitem", { name: /^Download Excel/ }).click()
    const exported = await (await downloading).createReadStream()
    expect(hasValue(Buffer.concat(await exported.toArray()), "lost acknowledgement cell")).toBe(true)

    const verifyHistory = async () => {
      const chatURL = page.url()
      await panel.getByRole("button", { name: "More for this Sheet" }).click()
      await page.getByRole("menuitem", { name: "Version history" }).click()
      const history = page.getByRole("dialog", { name: "Version history" })
      await expect(history.getByText("Latest", { exact: true })).toBeVisible()
      await expect(history).not.toContainText(/Take over editing|Hand back|Edit anyway|Keep reading/i)
      await expect(panel.getByRole("button", { name: /^(Take over editing|Edit anyway)$/i })).toHaveCount(0)
      await expect(page).toHaveURL(chatURL)
      await history.getByRole("button", {name: "Close", exact: true}).click()
    }
    await panel.getByRole("button", { name: "Expand Sheet" }).click()
    await verifyHistory()
    await page.reload()
    if (await panel.isHidden()) await page.getByRole("button", { name: `Open ${name}`, exact: true }).click()
    await openEditor(page)
    await verifyHistory()
  } finally {
    const events = await page.evaluate(() => window.sheetTestEvents).catch(() => [])
    await info.attach("editor revisions", { body: JSON.stringify(events ?? [], null, 2), contentType: "application/json" })
    await context.close()
  }
})

async function openEditor(page: Page) {
  // The editor starts read-only while software WebGL initializes and write access is verified.
  await expect(page.getByTestId("sheet-panel-state")).toHaveAttribute("data-editing", "true", { timeout: 30_000 })
  const frame = await native.frameOf(page, "gridcraft")
  await frame.locator("canvas").first().click({ position: { x: 54, y: 240 } })
  return frame
}

async function editCell(page: Page, frame: Frame, value: string, cell: "A3" | "B2" | "C2") {
  const position = cell === "A3" ? { x: 54, y: 255 } : { x: cell === "B2" ? 104 : 154, y: 239 }
  await frame.locator("canvas").first().click({ position })
  await page.keyboard.type(value)
  await frame.waitForFunction(() => document.activeElement?.tagName === "INPUT", {}, { timeout: 5_000 })
  await page.keyboard.press("Enter")
  await expect(page.getByTestId("sheet-panel-state")).toHaveAttribute("data-dirty", "true")
}

interface EditorEvent {
  type: string
  document?: string
  revision?: string
  savedRevision?: string
  baseRevision?: string
  code?: string
  version?: string
  keep?: string
  build?: string
  dirty?: boolean
  conflicts?: Array<{ sheet: string; cell: string }>
}

declare global {
  interface Window {
    sheetTestEvents: EditorEvent[]
    sheetTestFiles: Array<{ document?: string; revision?: string; bytes: number[] }>
    sheetTestHoldPaint: boolean
    sheetTestReleasePaint(): void
    sheetTestHasDeferredPaint(): boolean
  }
}

async function watchEditor(page: Page) {
  await page.addInitScript(() => {
    if (window !== window.top) return
    window.sheetTestEvents = []
    window.sheetTestFiles = []
    const channel = window.MessageChannel, outgoing = MessagePort.prototype.postMessage
    const remember = (message: EditorEvent & { bytes?: ArrayBuffer | Uint8Array }) => {
      if (/^gridcraft:(ready|execute|executed|opened|changed|save-ack|adopt-merged|merged-adopted)$/.test(message?.type)) window.sheetTestEvents.push(message)
      if (message?.type === "gridcraft:save" && message.bytes) {
        const bytes = message.bytes instanceof Uint8Array ? message.bytes : new Uint8Array(message.bytes)
        window.sheetTestFiles.push({ document: message.document, revision: message.revision, bytes: Array.from(bytes) })
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
    window.sheetTestHasDeferredPaint = () => deferred.length > 0
    window.sheetTestReleasePaint = () => {
      window.sheetTestHoldPaint = false
      for (const start of deferred.splice(0)) start()
    }
  })
}

const cellSet = (cell: string, input: string) => ({ command: "cell.set", params: { cell, input } })

for (const race of ["saved revision", "same cell", "other cell", "other sheet", "head-only change", "late head-only edit", "undeclared head edit"] as const) {
  test(`Sheet adopts canonical bytes for ${race}`, async ({ browser }) => {
    const context = await browser.newContext({ baseURL: origin, storageState: await loginStorageState(origin, account), viewport: native.VIEWPORT, acceptDownloads: true })
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
      const frame = await openEditor(page)
      await editCell(page, frame, "human seed", "A3")
      const firstSave = page.waitForResponse(response => response.url().includes("/craft/document/save?") && response.status() === 200)
      await page.keyboard.press("Control+s")
      await expect(panel.getByTestId("sheet-panel-state")).toHaveText("Saved", { timeout: 15_000 })
      const saved = await firstSave
      const binding = saved.request().postDataJSON() as Record<string, unknown>
      const session = await saved.request().headerValue("X-Craft-Session")
      expect(session).toBeTruthy()
      const headers = { "X-Craft-Session": session! }
      const { version: savedVersion } = await saved.json() as { version: string }
      const base = Number(savedVersion)
      const agentFootprints = new Map<number, { sheet: string; a1: string }>()
      const headFootprints = new Map<number, { sheet: string; a1: string }>()
      const headOnly = race === "head-only change" || race === "late head-only edit" || race === "undeclared head edit"
      let receivedStaleSave!: () => void, releaseStaleSave!: () => void
      const receivedCapture = new Promise<void>(resolve => { receivedStaleSave = resolve })
      const holdResponse = new Promise<void>(resolve => { releaseStaleSave = resolve })
      // Seed real versions while stubbing the agent role and the merge response in this browser test.
      // Go integration tests exercise the server's authorization and merge rules with an actual agent grant.
      const send = async (version: number, command: ReturnType<typeof cellSet>) => {
        const head = await readWorkbook(context.request, craft)
        expect(head.version).toBe(String(version - 1))
        const [, sheet, address] = /^'([^']+)'!([A-Z]+\d+)$/.exec(command.params.cell)!
        const { bytes } = await workbookCell(page, head.bytes, sheet, address, command.params.input)
        expect(bytes).toBeDefined()
        const hash = "sha256:" + createHash("sha256").update(bytes!).digest("hex")
        const commit = await context.request.post(craft("document/save"), {
          headers,
          data: { ...binding, baseVersion: head.version, editorRevision: "fixture:" + randomUUID(), attemptId: randomUUID(), bytes: bytes!.toString("base64"), contentHash: hash },
        })
        expect(commit.status(), await commit.text()).toBe(200)
        expect(await commit.json()).toMatchObject({ version: String(version), candidateHash: hash })
        if (headOnly) headFootprints.set(version, { sheet, a1: address })
        else agentFootprints.set(version, { sheet, a1: address })
        const id = `sheet-change-${version}`
        const emit = (type: string, content: string, metadata: Record<string, unknown> = {}) =>
          socket!.send(JSON.stringify({ type: "chat_event", channel: `session:${chat}`, payload: { type, content, metadata } }))
        emit("tool_call", "craft_sheet_apply", { tool_id: id, tool_name: "craft_sheet_apply" })
        emit("tool_result", JSON.stringify({ page_id: ref.pageId, slug: name, version: String(version), base_version: String(version - 1), content_hash: hash, commands: [command] }), { tool_use_id: id })
        emit("done", "")
      }
      await expect.poll(() => Boolean(socket), { message: "real chat WebSocket connected" }).toBe(true)
      const adoptions = () => page.evaluate(() => window.sheetTestEvents.filter(event => event.type === "gridcraft:merged-adopted"))
      const operation = cellSet(race === "other sheet" ? "'Other'!B2" : race === "other cell" || headOnly ? "'Data'!C2" : "'Data'!B2", "agent")
      await page.route("**/craft/document/save?**", async route => {
        const request = route.request().postDataJSON() as typeof binding & {
          attemptId: string; baseVersion: string; editorRevision: string; contentHash: string; bytes: string
        }
        const head = await readWorkbook(context.request, craft)
        if (head.version === request.baseVersion) return route.continue()
        const agentRanges = [...agentFootprints].filter(([version]) => version > Number(request.baseVersion)).map(([, range]) => range)
        const headRanges = race === "undeclared head edit" ? [] : [...headFootprints].filter(([version]) => version > Number(request.baseVersion)).map(([, range]) => range)
        if (race === "late head-only edit") { receivedStaleSave(); await holdResponse }
        const captured = await workbookCell(page, Buffer.from(request.bytes, "base64"), "Data", "B2")
        const human = captured.value === "human"
        const overlap = human ? agentRanges.filter(range => range.sheet === "Data" && range.a1 === "B2") : []
        let canonical = head.bytes, version = head.version
        if (human && overlap.length === 0) {
          const preserved = await workbookCell(page, canonical, "Data", "B2", "human")
          if (!preserved.bytes) throw Error("The fixture could not preserve the independent edit")
          canonical = preserved.bytes
          const hash = "sha256:" + createHash("sha256").update(canonical).digest("hex")
          const reply = await context.request.post(craft("document/save"), {
            headers, data: { ...request, baseVersion: head.version, attemptId: randomUUID(), bytes: canonical.toString("base64"), contentHash: hash },
          })
          expect(reply.status(), await reply.text()).toBe(200)
          version = (await reply.json() as { version: string }).version
        }
        const hash = "sha256:" + createHash("sha256").update(canonical).digest("hex")
        const { attemptId, bytes: _bytes, touched: _touched, ...scope } = request
        await route.fulfill({ json: {
          kind: "merged",
          submitted: { attempt_id: attemptId, base_version: request.baseVersion, editor_revision: request.editorRevision, content_hash: request.contentHash },
          receipt: { ...scope, baseVersion: head.version, version, ...(version === head.version ? { unchanged: true } : {}),
            contentHash: hash, candidateHash: hash, durability: "node", issuedBy: "crewship", phase: "committed" },
          head_content_hash: hash, bytes: canonical.toString("base64"), overlap, agent_ranges: agentRanges, head_ranges: headRanges,
        } })
      })

      if (race === "late head-only edit") {
        await send(base + 1, operation)
        await receivedCapture
        try { await editCell(page, frame, "late local value", "C2") }
        finally { releaseStaleSave() }
        await expect.poll(async () => (await adoptions()).at(-1)).toMatchObject({ code: "head_range_conflict" })
        await expect(panel.getByText("The same cell changed elsewhere. Your edit is still here.")).toBeVisible()
        const download = page.waitForEvent("download")
        await panel.getByRole("button", { name: "Download my changes" }).click()
        const stream = await (await download).createReadStream()
        expect((await workbookCell(page, Buffer.concat(await stream.toArray()), "Data", "C2")).value).toBe("late local value")
        return
      }
      if (race === "saved revision") {
        await send(base + 1, cellSet("'Data'!A2", "first agent change"))
        await expect.poll(async () => (await adoptions()).length).toBe(1)
        await editCell(page, frame, "human", "B2")
        await send(base + 2, operation)
      } else {
        await page.evaluate(() => { window.sheetTestHoldPaint = true })
        await send(base + 1, operation)
        await expect.poll(() => page.evaluate(() => window.sheetTestHasDeferredPaint())).toBe(true)
        await editCell(page, frame, "human", "B2")
        await page.evaluate(() => window.sheetTestReleasePaint())
      }
      if (race === "undeclared head edit") {
        await expect.poll(async () => (await adoptions()).at(-1)).toMatchObject({ code: "stale_base" })
        await expect(panel.getByText("Review changes", { exact: true })).toBeVisible()
        const download = page.waitForEvent("download")
        await panel.getByRole("button", { name: "Download my changes" }).click()
        const stream = await (await download).createReadStream()
        expect((await workbookCell(page, Buffer.concat(await stream.toArray()), "Data", "B2")).value).toBe("human")
        return
      }
      const overlaps = race === "saved revision" || race === "same cell"
      const finalVersion = String(base + (race === "same cell" ? 1 : 2))
      await expect.poll(async () => (await adoptions()).at(-1)).toMatchObject({ version: finalVersion, dirty: false })
      const head = await readWorkbook(context.request, craft)
      expect(head.version).toBe(finalVersion)
      expect((await workbookCell(page, head.bytes, "Data", "B2")).value).toBe(overlaps ? "agent" : "human")
      const [, sheet, address] = /^'([^']+)'!([A-Z]+\d+)$/.exec(operation.params.cell)!
      expect((await workbookCell(page, head.bytes, sheet, address)).value).toBe("agent")
      if (headOnly) {
        await panel.getByRole("button", { name: "Close Sheet" }).click()
        await page.getByTestId("sheet-chat-card").getByRole("button", { name: `Open ${name}`, exact: true }).click()
        await expect(panel.getByTestId("sheet-panel-state")).toHaveAttribute("data-version", finalVersion)
        const reopened = await readWorkbook(context.request, craft)
        expect((await workbookCell(page, reopened.bytes, "Data", "B2")).value).toBe("human")
        expect((await workbookCell(page, reopened.bytes, "Data", "C2")).value).toBe("agent")
      }
      if (overlaps) {
        await expect(panel.getByText("Agent edited Data!B2. Your copy is saved in this browser.")).toBeVisible()
        const download = page.waitForEvent("download")
        await panel.getByRole("button", { name: "Download my copy" }).click()
        const stream = await (await download).createReadStream()
        const copy = Buffer.concat(await stream.toArray())
        expect((await workbookCell(page, copy, "Data", "B2")).value).toBe("human")
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
      const frame = await openEditor(page)
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
      await openEditor(page)
      await expect(panel.getByTestId("sheet-panel-state")).toHaveAttribute("data-version", ref.initVersion)
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
