// A new chat, a workbook through the paperclip, and a message sent right away
// What the server stored is checked,
// not what the agent did: there is no model on this throwaway server, so the
// agent never answers and never calls the Sheet (that is the stub: no run).
//
// Checked:
// - the chat is started on the server before the Sheet: the upload carries
//   X-Craft-Chat = that chat (network);
// - the first message carries artifact_refs naming the Sheet (the frame the
//   browser sent, and the server's own GET /chats/{id}/artifact-refs, which is
//   read from artifact_message_refs);
// - the chat is named after the file (the agent's chat list on the server);
// - the composer shows the workbook once (the Sheet's chip), not also as an
//   attachment;
// - server.log has no "chat run check 100". With no run this cannot appear
//   anyway: the report says so, and a run with a real agent covers it.
//
// Two ways of "right away": "immediately" sends the moment the file is chosen,
// "when the Sheet shows" sends once the Sheet is open beside the chat.
// The server needs the workspace flag artifact_agent_access_modes (the runner's
// SHEET_E2E_FLAGS), and the workspace's artifact policy must let the agent's crew
// change artifacts (set below through the admin API, as a workspace admin would;
// the policy as found is recorded). Without either the server refuses the Sheet
// for the agent and no message may carry it.
import { expect, test, type Page } from "@playwright/test"
import * as H from "./helpers"
import { twoSheetWorkbook } from "./workbooks"

type Seen = { at: number; what: string }

function watch(page: Page) {
  const seen: Seen[] = []
  const sentFrames: string[] = []
  const uploads: Array<{ url: string; chat: string | undefined }> = []
  const posts: string[] = []
  page.on("request", request => {
    const url = request.url()
    if (request.method() !== "GET" && /\/api\/v1\//.test(url)) {
      const path = new URL(url).pathname
      posts.push(`${request.method()} ${path}`)
      seen.push({ at: Date.now(), what: `${request.method()} ${path}` })
      const chat = request.headers()["x-craft-chat"]
      if (/\/craft\//.test(path) && request.method() === "POST" && (chat !== undefined || /init|import|document/.test(path))) uploads.push({ url: path, chat })
      const body = request.postData() ?? ""
      if (body.includes("artifact_refs")) sentFrames.push(`http ${path}: ${body.slice(0, 600)}`)
    }
  })
  page.on("websocket", ws => ws.on("framesent", frame => {
    const text = typeof frame.payload === "string" ? frame.payload : frame.payload.toString("utf8")
    if (/"(type|kind)"\s*:\s*"(user_message|message|send)/.test(text) || text.includes("artifact_refs")) {
      sentFrames.push(`ws: ${text.slice(0, 600)}`)
      seen.push({ at: Date.now(), what: "ws message sent" })
    }
  }))
  // The server's advice the composer reads before a send (reason and detail of a refusal).
  const access: string[] = []
  // Lease calls of the browser: does the release right after initialize go through?
  const leases: string[] = []
  page.on("response", async response => {
    const u = response.url()
    if (!/\/craft\/(lease\/|initialize|sessions)/.test(u) || response.request().method() !== "POST") return
    const body = await response.text().catch(() => "")
    leases.push(`${Date.now()} ${new URL(u).pathname.replace(/^.*\/craft\//, "")} ${response.status()} ${body.replace(/"(session_token|token)":"[^"]+"/g, '"$1":"…"').slice(0, 300)}`)
  })
  // Every finished lease call with its status, recorded at once (the body may never be readable).
  page.on("requestfinished", async request => {
    const u = request.url()
    if (!/\/craft\/lease\//.test(u)) return
    const response = await request.response().catch(() => null)
    leases.push(`${Date.now()} ${new URL(u).pathname.replace(/^.*\/craft\//, "")} finished ${response?.status() ?? "no response"}`)
    const body = response ? await Promise.race([response.text().catch(() => ""), new Promise<string>(r => setTimeout(() => r("(body not read)"), 3_000))]) : ""
    leases.push(`  ${new URL(u).pathname.replace(/^.*\/craft\//, "")} body ${body.replace(/"(session_token|token)":"[^"]+"/g, '"$1":"…"').slice(0, 300)}`)
  })
  page.on("requestfailed", request => {
    const u = request.url()
    if (/\/craft\/(lease\/|initialize|sessions)/.test(u)) leases.push(`${Date.now()} ${new URL(u).pathname.replace(/^.*\/craft\//, "")} FAILED ${request.failure()?.errorText ?? ""}`)
  })
  page.on("response", async response => {
    if (!/\/craft\/agent-access\?/.test(response.url())) return
    const body = await response.text().catch(() => "")
    access.push(`${Date.now()} ${response.status()} ${body.slice(0, 400)}`)
    seen.push({ at: Date.now(), what: `agent-access ${response.status()}` })
  })
  return { seen, sentFrames, uploads, posts, access, leases }
}

type Api = Awaited<ReturnType<typeof H.ownerApi>>
async function agentRow(api: Api): Promise<{ id: string; slug?: string; crew_id?: string }> {
  const r = await api.get(`/api/v1/agents?workspace_id=${encodeURIComponent(H.WORKSPACE)}`)
  const body = (await r.json()) as unknown
  const list = (Array.isArray(body) ? body : ((body as { agents?: unknown[]; items?: unknown[] }).agents ?? (body as { items?: unknown[] }).items ?? [])) as Array<{ id: string; slug?: string; crew_id?: string; crewId?: string }>
  const row = list.find(a => a.slug === H.AGENT)
  return row ? { ...row, crew_id: row.crew_id ?? row.crewId } : { id: H.AGENT }
}
const agentId = async (api: Api) => (await agentRow(api)).id

test.beforeAll(async () => {
  if (!H.AGENT) return
  const api = await H.ownerApi()
  try {
    const at = `/api/v1/workspaces/${encodeURIComponent(H.WORKSPACE)}/artifact-creation-policy?workspace_id=${encodeURIComponent(H.WORKSPACE)}`
    const found = await api.get(at)
    const policy = (await found.json()) as { enabled?: boolean; crew_ids?: string[]; version?: number; scoped_default?: boolean }
    test.info().annotations.push({ type: "artifact policy as found", description: `${found.status()} ${JSON.stringify(policy)}` })
    const crew = (await agentRow(api)).crew_id
    expect(crew, "the agent's crew").toBeTruthy()
    if (policy.enabled && policy.crew_ids?.includes(crew!)) return
    const put = await api.put(at, { data: { enabled: true, crew_ids: [...new Set([...(policy.crew_ids ?? []), crew!])], base_version: policy.version ?? 0 } })
    expect(put.status(), `the policy lets the agent's crew change artifacts: ${(await put.text()).slice(0, 200)}`).toBe(200)
  } finally {
    await api.dispose()
  }
})

for (const when of ["immediately", "when the Sheet shows"] as const) {
  test(`new chat + workbook + send ${when}: the first message carries the Sheet, the chat is named after the file, one chip`, async ({ browser }) => {
    H.requireAgent()
    test.setTimeout(10 * 60_000)
    const slug = when === "immediately" ? "hned" : "po-otevreni"
    const book = twoSheetWorkbook(3, 4, `novy-chat-${slug}-syntetika.xlsx`)
    const base = book.name.replace(/\.xlsx$/, "")
    const ctx = await H.clientContext(browser)
    const page = await ctx.newPage()
    const api = await H.ownerApi()
    const w = watch(page)
    let pageId = ""
    try {
      await H.newChat(page)
      const started = Date.now()
      await H.attach(page, book.name, book.bytes)
      const field = page.getByPlaceholder(/^Message /)
      const words = `Podívej se na ${book.name}`
      if (when === "when the Sheet shows") await H.waitViewing(page, started)
      await field.fill(words)
      await field.press("Enter")
      const sentAt = Date.now()
      test.info().annotations.push({ type: "sent after the file was chosen (ms)", description: String(sentAt - started) })
      // The words stay in the conversation as the person's message.
      await expect(page.getByText(words, { exact: true }).first(), "the message is in the conversation").toBeVisible({ timeout: 60_000 })
      if (when === "immediately") await H.waitViewing(page, started)
      pageId = await H.pageIdOf(page)
      await H.shot(page, `60-${slug}-sent`)

      // The chat id: the URL names it once the chat is on the server.
      await expect.poll(() => new URL(page.url()).searchParams.get("session") ?? w.uploads.find(u => u.chat)?.chat, { timeout: 60_000, message: "the chat is on the server" }).toBeTruthy()
      const chat = new URL(page.url()).searchParams.get("session") ?? w.uploads.find(u => u.chat)!.chat!
      test.info().annotations.push({ type: "chat", description: chat }, { type: "requests", description: w.posts.join(" | ").slice(0, 1500) })

      // The upload was tied to the chat from version 1.
      const tied = w.uploads.filter(u => u.chat !== undefined)
      test.info().annotations.push({ type: "upload requests", description: JSON.stringify(w.uploads) })
      expect(tied.map(u => u.chat), "the workbook's upload names the chat (X-Craft-Chat)").toContain(chat)

      // What the browser sent with the first message.
      expect.soft(w.sentFrames.some(f => f.includes("artifact_refs") && f.includes(pageId)), "the first message the browser sent carries artifact_refs naming the Sheet").toBe(true)

      // What the server stored: the chat's artifact refs (artifact_message_refs).
      const refsAt = `/api/v1/chats/${encodeURIComponent(chat)}/artifact-refs?workspace_id=${encodeURIComponent(H.WORKSPACE)}&limit=100`
      let refs: Array<{ message_id: string; page_id: string; source: string; name: string }> = []
      await expect.soft.poll(async () => {
        const r = await api.get(refsAt)
        if (!r.ok()) return `${r.status()}`
        refs = ((await r.json()) as { refs: typeof refs }).refs ?? []
        return refs.filter(r => r.page_id === pageId && r.source === "message").length
      }, { timeout: 30_000, message: "the server stored the Sheet on a message of this chat" }).toBe(1)
      test.info().annotations.push({ type: "server artifact-refs", description: JSON.stringify(refs) })
      const messageRef = refs.find(r => r.page_id === pageId && r.source === "message")
      // ...and that message is the first one the person sent in this chat.
      const m = await api.get(`/api/v1/chats/${encodeURIComponent(chat)}/messages?workspace_id=${encodeURIComponent(H.WORKSPACE)}`)
      const messages = (((await m.json()) as { messages?: Array<{ id: string; role?: string; content?: unknown; metadata?: unknown }> }).messages ?? [])
      const mine = messages.filter(x => x.role === "user")
      test.info().annotations.push({ type: "server messages (user)", description: JSON.stringify(mine.map(x => ({ id: x.id, metadata: x.metadata }))).slice(0, 1500) })
      expect.soft(mine.length, "one message from the person").toBe(1)
      expect.soft(messageRef?.message_id, "the Sheet is on the first message").toBe(mine[0]?.id)
      // Why the composer did or did not attach it: the page as the server keeps it.
      const rec = await api.get(`/api/v1/pages/${encodeURIComponent("@" + pageId)}?workspace_id=${encodeURIComponent(H.WORKSPACE)}`)
      const recBody = (await rec.json().catch(() => ({}))) as Record<string, unknown>
      test.info().annotations.push({ type: "page record", description: JSON.stringify(Object.fromEntries(Object.entries(recBody).filter(([k]) => /private|audience|folder|owner|visib|share/i.test(k)))) })

      // The chat is named after the file.
      const agent = await agentId(api)
      let title = ""
      await expect.poll(async () => {
        const r = await api.get(`/api/v1/agents/${encodeURIComponent(agent)}/chats?workspace_id=${encodeURIComponent(H.WORKSPACE)}&limit=50`)
        const body = (await r.json()) as unknown
        const list = (Array.isArray(body) ? body : ((body as { chats?: unknown[]; items?: unknown[] }).chats ?? (body as { items?: unknown[] }).items ?? [])) as Array<{ id: string; title?: string | null }>
        title = list.find(c => c.id === chat)?.title ?? ""
        return title
      }, { timeout: 30_000, message: "the chat's title on the server" }).toContain(base)
      test.info().annotations.push({ type: "chat title", description: title })

      // The composer shows the workbook once: the Sheet's chip, no attachment beside it.
      const composer = page.locator("form").filter({ has: field })
      const inComposer = await composer.getByText(book.name).count()
      test.info().annotations.push({ type: "file name in the composer", description: String(inComposer) })
      expect(inComposer, "the workbook is not also an attachment in the composer").toBeLessThanOrEqual(1)
      // The sent message shows the file once too.
      const bubble = page.getByText(words, { exact: true }).first().locator("xpath=ancestor::*[@data-message-id or @data-role='user'][1]")
      if (await bubble.count()) test.info().annotations.push({ type: "file name in the sent message", description: String(await bubble.getByText(book.name).count()) })
      await H.shot(page, `61-${slug}-composer`)

      // A click in the grid beside the chat (data-edit-attempt says what the host did with it).
      const before = await page.locator("[data-take-over-notice]").evaluateAll(els => els.map(e => `${e.getAttribute("data-take-over-notice")}|${e.getAttribute("data-edit-attempt") ?? "(none)"}`))
      await (await H.gridFrame(page)).locator("canvas").first().click({ position: { x: 300, y: 260 } })
      await page.waitForTimeout(8_000)
      const after = await page.locator("[data-take-over-notice]").evaluateAll(els => els.map(e => `${e.getAttribute("data-take-over-notice")}|${e.getAttribute("data-edit-attempt") ?? "(none)"}`))
      const state = await H.stateOf(page).innerText().catch(() => "")
      test.info().annotations.push({ type: "click in the panel: notice|data-edit-attempt", description: `before ${JSON.stringify(before)} after ${JSON.stringify(after)} state "${state}"` })
      await H.shot(page, `62-${slug}-after-click`)
    } finally {
      test.info().annotations.push({ type: "craft lease/initialize/sessions POSTs", description: w.leases.join(" || ").slice(0, 2500) })
      test.info().annotations.push({ type: "message frames sent", description: w.sentFrames.join(" || ").slice(0, 1500) })
      test.info().annotations.push({ type: "agent-access answers", description: w.access.join(" || ").slice(0, 2500) })
      test.info().annotations.push({ type: "timeline", description: w.seen.map(s => `${s.at}: ${s.what}`).join(" | ").slice(0, 2000) })
      await H.deletePage(api, pageId)
      await ctx.close()
      await api.dispose()
    }
  })
}
