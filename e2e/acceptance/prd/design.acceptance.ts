// PRD §0.14 Design acceptance: two people on one live canvas, edits made
// through the Design tool routes, and a real agent editing while both watch.
//
// Quick mode runs the first test against the local test server in a disposable
// QA workspace. The agent test needs the seeded agent and an existing second
// member of the agent's workspace, so it only runs in full mode.
import { randomUUID } from "node:crypto"
import { loginStorageState } from "./auth"
import { expect, prdRequirement, test } from "./fixtures"
import {
  applyOperations,
  canvasTexts,
  chatToolCalls,
  createDesign,
  deletePage,
  enableDesign,
  expectOnCanvas,
  findAgent,
  getDesign,
  grantWrite,
  listVersions,
  openCanvas,
  revokeWrite,
  scoped,
  selectWorkspace,
  sessionUserId,
  snapshot,
  waitAgentIdle,
  type DesignNode,
} from "./design/canvas"
import type { BrowserContext, Page } from "@playwright/test"
import type { PrdScenario } from "./fixtures"
import { capacityHint, trackPage } from "./created"

/**
 * The owner in a fresh session of its own. The fixture's owner cookies are
 * shared by every test of the run; a browser that refreshes them rotates the
 * refresh token, and the next test presenting the old one has the whole
 * session revoked by reuse detection. A per-test login keeps tests independent.
 */
async function ownerSession(prd: PrdScenario): Promise<{ context: BrowserContext; page: Page; request: BrowserContext["request"] }> {
  const owner = prd.env.accounts.owner
  if (!owner) throw new Error("owner account is not configured")
  const context = await prd.browserContext(await loginStorageState(prd.origin, owner))
  return { context, page: await context.newPage(), request: context.request }
}

const tag = () => randomUUID().replaceAll("-", "").slice(0, 8)

/** Adds a Screen and a Text layer through the editor toolbar and types `text`. */
async function addScreenWithText(page: Page, text: string): Promise<void> {
  const tools = page.getByLabel("Design tools")
  await tools.getByRole("button", { name: "Screen" }).click()
  await tools.getByRole("button", { name: "Text", exact: true }).click()
  await page.getByLabel("Text content").fill(text)
  await expect.poll(() => canvasTexts(page)).toContain(text)
}

async function selectTextLayer(page: Page): Promise<void> {
  await page.getByLabel("Layers").getByRole("button", { name: "Text", exact: true }).click()
  await expect(page.getByLabel("Text content")).toBeVisible()
}

async function expectHistoryShows(page: Page, version: number): Promise<void> {
  await page.getByRole("button", { name: /^History/ }).click()
  await expect(page.getByLabel("Version history").getByText(`Version ${version}`, { exact: true })).toBeVisible({ timeout: 20_000 })
}

test.describe("design", () => {
  test(
    "two people and the design tool routes edit one live canvas",
    {
      annotation: [
        prdRequirement("B.design.two-humans", "§0.14 Design: two people edit one canvas both ways, with presence and cursor, without reload"),
        prdRequirement("B.design.tool-route-live", "§0.14 Design: an edit through design get/apply (CLI and agent tool route) reaches both open canvases live"),
        prdRequirement("B.design.save-reload", "§0.14 Design: a saved version holds the change and reload shows the persisted canvas"),
      ],
    },
    async ({ prd }) => {
      const { page, context, request } = await ownerSession(prd)
      const ws = await prd.qaWorkspace()
      await enableDesign(request, ws.id)

      const second = await prd.provision("MEMBER", "PRD Design Second")
      const secondCtx = await prd.browserContext(await loginStorageState(prd.origin, second.account))
      const ownerId = await sessionUserId(request)
      await selectWorkspace(context, ownerId, ws.id)
      await selectWorkspace(secondCtx, second.userId, ws.id)

      const slug = `prd-design-${tag()}`
      const design = await createDesign(request, ws.id, slug, `PRD design ${slug}`)
      expect(design.can_edit, "the creator can edit the Design").toBe(true)
      await grantWrite(request, ws.id, design.page_id, second.userId)

      const secondPage = await secondCtx.newPage()
      const ownerUi = await openCanvas(page, slug, ws.id)
      if (!ownerUi) {
        // A build without the client-side Design flag renders no editor at all.
        test.skip(prd.mode === "quick", "this build ships without the Design UI (NEXT_PUBLIC_ARTIFACTS_DESIGN)")
        throw new Error("the deployed build renders 'Design is not enabled.'; Design UI is required in full mode")
      }
      expect(await openCanvas(secondPage, slug, ws.id), "second person opens the live canvas").toBe(true)
      const secondMeta = await getDesign(secondCtx.request, ws.id, slug)
      expect(secondMeta.can_edit, "Edit grant gives the second person write access").toBe(true)

      // Presence: each sees the other on the canvas.
      for (const p of [page, secondPage]) {
        await expect(p.getByLabel("People on this canvas").locator("span")).toHaveCount(1, { timeout: 20_000 })
      }

      // Owner → second, live.
      const ownerText = `Owner text ${tag()}`
      await addScreenWithText(page, ownerText)
      const ownerToSecondMs = await expectOnCanvas(secondPage, ownerText)

      // Second → owner, live (the other direction).
      const secondText = `Second text ${tag()}`
      await selectTextLayer(secondPage)
      await secondPage.getByLabel("Text content").fill(secondText)
      const secondToOwnerMs = await expectOnCanvas(page, secondText)

      // Cursor: the second person's pointer is drawn on the owner's canvas.
      const box = await secondPage.locator('svg[aria-label="Design canvas"]').boundingBox()
      expect(box, "canvas is laid out").toBeTruthy()
      await secondPage.mouse.move(box!.x + 300, box!.y + 200)
      await secondPage.mouse.move(box!.x + 340, box!.y + 230, { steps: 5 })
      await expect(page.locator('svg[aria-label="Design canvas"] g[aria-label$=" cursor"]')).toHaveCount(1, { timeout: 15_000 })

      // The design tool route: read (design_get), change (design_apply), save (design_snapshot).
      const before = await getDesign(request, ws.id, slug)
      const textNode = before.nodes.find((n) => n.type === "text")
      const frame = before.nodes.find((n) => n.type === "frame")
      expect(textNode?.text, "design get returns the people's edit").toBe(secondText)
      expect(frame, "design get returns the screen").toBeTruthy()
      const toolText = `Tool route ${tag()}`
      const rect: DesignNode = { id: `prd-rect-${tag()}`, type: "rect", name: "PRD rectangle", parentId: frame!.id, x: 40, y: 40, width: 120, height: 60, fill: "#3366ff" }
      const sentAt = Date.now()
      const applied = await applyOperations(request, ws.id, slug, [
        { op: "upsert", node: { ...textNode!, text: toolText } },
        { op: "upsert", node: rect },
      ], "PRD acceptance: tool route edit")
      expect(applied.nodes.find((n) => n.id === textNode!.id)?.text).toBe(toolText)
      const seenLive: Record<string, number> = {}
      await Promise.all(
        ([["owner", page], ["second", secondPage]] as const).map(async ([who, p]) => {
          await expectOnCanvas(p, toolText)
          await expect(p.locator(`[data-design-node="${rect.id}"]`)).toHaveCount(1)
          seenLive[who] = Date.now() - sentAt
        }),
      )

      const saved = await snapshot(request, ws.id, slug, "PRD acceptance: saved after tool route edit")
      expect(saved.version).toBeGreaterThan(before.latest_version ?? before.version)
      const versions = await listVersions(request, ws.id, slug)
      expect(versions.versions.map((v) => v.version)).toContain(saved.version)
      const retained = await getDesign(request, ws.id, slug, saved.version)
      expect(retained.nodes.find((n) => n.id === textNode!.id)?.text, "the retained version holds the change").toBe(toolText)
      expect(retained.nodes.some((n) => n.id === rect.id)).toBe(true)

      // Reload both: the persisted canvas comes back from the server.
      for (const p of [page, secondPage]) {
        await p.reload()
        await expect(p.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 45_000 })
        await expectOnCanvas(p, toolText)
        await expect(p.locator(`[data-design-node="${rect.id}"]`)).toHaveCount(1)
      }
      await expectHistoryShows(page, saved.version)

      await prd.evidence("result.json", {
        appSha: prd.appSha,
        mode: prd.mode,
        workspace: ws.id,
        design: { slug, pageId: design.page_id },
        liveMs: { ownerToSecond: ownerToSecondMs, secondToOwner: secondToOwnerMs, toolRoute: seenLive },
        presence: "each person sees one peer",
        cursor: "second person's cursor drawn on the owner's canvas",
        savedVersion: saved.version,
        versions: versions.versions.map((v) => ({ version: v.version, message: v.message })),
        reload: "both canvases show the tool-route text and rectangle after reload",
      })
      await prd.evidence("owner-after-reload.png", await page.screenshot())
    },
  )

  test(
    "a real agent edits the canvas while two people watch",
    {
      annotation: [
        prdRequirement("B.design.agent-live", "§0.14 Design: a real agent edits the canvas and both connected people see it without reload"),
        prdRequirement("B.design.agent-tools", "§0.14 Design: the agent works through design_get and design_apply, without a redundant design_snapshot"),
        prdRequirement("B.design.agent-version", "§0.14 Design: the agent's change is a retained version and survives reload"),
        prdRequirement("B.design.agent-receipt", "§0.14 Design: Send to agent reports a delivered comment as sent"),
      ],
    },
    async ({ prd }) => {
      const agent = prd.requireAgent()
      const memberAccount = prd.requireAccount("member")
      const { page, context, request } = await ownerSession(prd)
      // A seeded agent's chats live in its own workspace, so this test works
      // there. It changes no membership or flag: it creates one test Design
      // (deleted afterwards) and one private chat, and gives the existing
      // member an Edit grant on that Design only (revoked afterwards).
      const wsId = agent.workspaceId
      // Fresh member session too, for the same reason as ownerSession.
      const memberCtx = await prd.browserContext(await loginStorageState(prd.origin, memberAccount))
      const memberApi = memberCtx.request
      const ownerId = await sessionUserId(request)
      const memberId = await sessionUserId(memberApi)
      await selectWorkspace(context, ownerId, wsId)
      await selectWorkspace(memberCtx, memberId, wsId)

      const report: Record<string, unknown> = { appSha: prd.appSha, mode: prd.mode, workspace: wsId, agent: agent.slug, observations: [] as unknown[] }
      const observe = (name: string, data: Record<string, unknown> = {}) => (report.observations as unknown[]).push({ name, at: new Date().toISOString(), ...data })

      const agentRow = await findAgent(request, wsId, agent.slug)
      const chatRes = await request.post(scoped(`/api/v1/agents/${encodeURIComponent(agentRow.id)}/chats`, wsId), { data: { origin: "UI" } })
      expect(chatRes.status(), `create chat: ${(await chatRes.text()).slice(0, 300)}`).toBeLessThan(300)
      const chatId = ((await chatRes.json()) as { id: string }).id
      const profile = await request.get(scoped(`/api/v1/chats/${encodeURIComponent(chatId)}/execution-profile`, wsId))
      const profileBody = (await profile.json().catch(() => ({}))) as { mode?: string; audience?: string }
      report.chat = { id: chatId, profile: profileBody }
      expect(profileBody.mode, "the owner's private chat with the agent is trusted").toBe("trusted")

      const slug = `prd-design-agent-${tag()}`
      let pageId = ""
      let granted = false
      try {
        trackPage(wsId, slug)
        const res = await request.post(scoped("/api/v1/pages/design", wsId), { data: { slug, name: `PRD design agent ${slug}`, created_by_test: true } })
        const created = (await res.text()).slice(0, 200)
        expect(res.status(), `Design must be enabled in PRD_AGENT_WORKSPACE_ID: ${created}${capacityHint(res.status(), created)}`).toBeLessThan(300)
        pageId = (await getDesign(request, wsId, slug)).page_id
        report.design = { slug, pageId }

        expect(await openCanvas(page, slug, wsId), "the deployed build renders the Design editor").toBe(true)
        const originalText = `Owner text ${tag()}`
        await addScreenWithText(page, originalText)
        const savedResponse = page.waitForResponse((r) => r.url().includes("/design/snapshots") && r.request().method() === "POST")
        await page.getByRole("button", { name: "Save version", exact: true }).click()
        expect((await savedResponse).ok(), "owner saved a version").toBe(true)
        const base = await getDesign(request, wsId, slug)
        const textNode = base.nodes.find((n) => n.type === "text")
        expect(textNode?.text).toBe(originalText)
        const baseLatest = base.latest_version ?? base.version

        await page.getByLabel("Agent chat ID").fill(chatId)
        await page.getByRole("button", { name: "Connect agent chat" }).click()
        await expect(page.getByRole("link", { name: "Open agent chat" })).toBeVisible({ timeout: 20_000 })
        expect((await getDesign(request, wsId, slug)).origin?.chat_id, "the chat is the Design's agent origin").toBe(chatId)

        await grantWrite(request, wsId, pageId, memberId)
        granted = true
        const memberPage = await memberCtx.newPage()
        expect(await openCanvas(memberPage, slug, wsId), "the member opens the live canvas").toBe(true)
        await expectOnCanvas(memberPage, originalText)
        for (const p of [page, memberPage]) {
          await expect(p.getByLabel("People on this canvas").locator("span")).toHaveCount(1, { timeout: 20_000 })
        }

        // Comment on the text node and send it to the agent.
        const marker = `Agent edit ${tag()}`
        await selectTextLayer(page)
        await page.getByLabel("Comment for agent").fill(`Change the text of this selected text node to exactly "${marker}". Do not change anything else.`)
        observe("agent status before send", { status: await waitAgentIdle(request, wsId, agent.slug, 10 * 60_000) })
        const sendAt = Date.now()
        await page.getByRole("button", { name: "Send to agent", exact: true }).click()

        // Delivery status. "Sent to agent" or the agent's "Done · vN" reply is a
        // truthful receipt; "Comment saved. Retry …" while the agent then does
        // the work is a false negative. A busy agent is retried once idle.
        const statusLine = page.getByLabel("Properties").locator("article p").filter({ hasText: /Sent to agent|Done · v\d+|Comment saved\./ })
        const receipts: string[] = []
        for (let attempt = 0; attempt < 5; attempt++) {
          await expect(statusLine.first()).toBeVisible({ timeout: 45_000 })
          const status = (await statusLine.first().textContent())?.trim() ?? ""
          receipts.push(status)
          observe("delivery status", { status, msAfterSend: Date.now() - sendAt })
          if (!/agent is busy/.test(status)) break
          await waitAgentIdle(request, wsId, agent.slug, 10 * 60_000)
          await page.getByRole("button", { name: "Retry delivery" }).first().click()
          await expect(statusLine.filter({ hasNotText: /agent is busy/ }).first()).toBeVisible({ timeout: 45_000 })
        }
        report.receipts = receipts
        expect.soft(receipts.at(-1), "Send to agent must confirm a delivered comment (no false 'Comment saved. Retry …')").toMatch(/Sent to agent|Done · v\d+/)

        // Both people see the agent's change live, without reload.
        const seen: Record<string, number> = {}
        await Promise.all(
          ([["owner", page], ["member", memberPage]] as const).map(async ([who, p]) => {
            await expectOnCanvas(p, marker, 7 * 60_000)
            seen[who] = Date.now() - sendAt
          }),
        )
        report.agentEditSeenMs = seen

        // A retained version by the agent (design_apply saves it).
        let agentVersion = 0
        let agentVersions = 0
        await expect
          .poll(async () => {
            const v = await listVersions(request, wsId, slug)
            report.versions = v.versions.map((x) => ({ version: x.version, message: x.message, actor: x.actor?.split(":").slice(0, 2).join(":") }))
            const mine = v.versions.filter((x) => x.version > baseLatest && x.actor?.startsWith("agent:"))
            agentVersions = mine.length
            agentVersion = mine.reduce((m, x) => Math.max(m, x.version), 0)
            return agentVersion
          }, { timeout: 3 * 60_000, intervals: [2_000, 5_000] })
          .toBeGreaterThan(baseLatest)

        // The agent used the Design tools.
        let calls: string[] = []
        await expect
          .poll(async () => {
            calls = await chatToolCalls(request, wsId, chatId)
            return ["design_get", "design_apply"].filter((n) => calls.includes(n))
          }, { timeout: 2 * 60_000, intervals: [3_000, 5_000] })
          .toEqual(["design_get", "design_apply"])
        report.toolCalls = calls
        // design_apply already retained the version; a snapshot would only duplicate it.
        expect(calls).not.toContain("design_snapshot")
        expect(agentVersions).toBe(1)

        // Reload both: persisted; the retained version holds the same node.
        for (const p of [page, memberPage]) {
          await p.reload()
          await expect(p.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 45_000 })
          await expectOnCanvas(p, marker)
        }
        const retained = await getDesign(request, wsId, slug, agentVersion)
        expect(retained.nodes.find((n) => n.id === textNode!.id)?.text, "the agent version changed the same node").toBe(marker)
        await expectHistoryShows(page, agentVersion)
        report.agentVersion = agentVersion
        report.reload = "both canvases show the agent text after reload"
      } finally {
        if (granted) report.cleanupRevoke = await revokeWrite(request, wsId, pageId, memberId).catch((e: unknown) => String(e))
        if (pageId) report.cleanupDelete = await deletePage(request, wsId, pageId).catch((e: unknown) => String(e))
        await prd.evidence("result.json", report)
      }
    },
  )
})
