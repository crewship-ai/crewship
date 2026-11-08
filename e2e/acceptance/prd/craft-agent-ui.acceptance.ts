// PRD §7 A (#5003): the browser control "Edit with an agent" on a native Word
// document and Sheet. Everything the person does goes through the page, with no
// chat socket of the harness: they open the control, pick the agent, write the
// instruction, tick the consent and start. On a free document the agent edits
// and the person sees who is editing and, after a reload, the new version. On a
// document the person holds the same start ends in a proposal they can accept.
// A person with Read never gets the control.
//
// Full mode and a seeded agent only. Agent pages live in PRD_AGENT_WORKSPACE_ID
// as private test pages that are deleted afterwards. The Read case needs no
// agent and no change to that shared workspace: agent availability follows the
// instance and the caller's right, so it runs in a disposable QA workspace, with
// a write grantee proving the control exists there. A Read person gets the shared
// preview, never the live editor, so it is checked on a published document too.
import { randomUUID } from "node:crypto"
import type { Page, Response } from "@playwright/test"
import { loginStorageState } from "./auth"
import { requireNative } from "./craft-lease/helpers"
import { NativePage as LeasePage } from "./craft-lease/native"
import { barSeen, findAgent, redact, sheetInstruction, waitForTask, watchBar, wordInstruction, type SeededAgent } from "./craft-proposal/agent"
import { createNativePage, deletePage, sheetCell, wordText, type NativeApp, type NativePage } from "./craft-proposal/native"
import { frameOf } from "./drafts-crash/native"
import { expect, prdRequirement, test } from "./fixtures"
import { agentControl, lockButton, lockControls } from "./craft-session/frame"

/** Goes through the whole control and returns the task the page started. Fails with what the control said. */
async function startFromControl(page: Page, agent: SeededAgent, instruction: string): Promise<{ workId: string; held: boolean }> {
  // The toggle above the editor on /pages/craft/editor; the Agent drawer's Edit mode in the Artifacts frame (#5132).
  const control = await agentControl(page, 60_000)
  const picker = control.getByRole("combobox", { name: "Agent" })
  await expect(picker.locator(`option[value="${agent.id}"]`), `the picker offers ${agent.name}`).toHaveCount(1, { timeout: 30_000 })
  await picker.selectOption(agent.id)
  await control.getByRole("textbox", { name: "Instruction for the agent" }).fill(instruction)
  const start = control.getByRole("button", { name: "Start agent", exact: true })
  await expect(start, "no start without consent").toBeDisabled()
  await control.getByRole("checkbox", { name: "The agent may edit the whole document" }).check()
  await expect(start, "the start opens with agent, instruction and consent").toBeEnabled()
  const held = await control.getByText("The agent will not change it; it prepares a proposal for review.").isVisible()

  const created = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith("/craft/tasks"), { timeout: 60_000 })
  const refused = control.getByRole("alert").waitFor({ timeout: 60_000 }).then(() => control.getByRole("alert").innerText())
  await start.click()
  const first = await Promise.race([created, refused])
  if (typeof first === "string") throw new Error(`the control refused the start: ${first}`)
  refused.catch(() => {})
  expect(first.status(), "the page started the task").toBe(202)
  const body = (await first.json()) as { work_id?: string }
  expect(body.work_id, "the task has an id").toBeTruthy()
  await expect.poll(() => new URL(page.url()).searchParams.get("craft_task_id"), { message: "the page tracks the task it started" }).toBe(body.work_id)
  return { workId: body.work_id!, held }
}

/** What a reload renders. The editors draw on a canvas, so the content is read from the document the editor fetched, and it counts only once that editor finished starting and is on screen. */
async function reloadAndRead(page: Page, app: NativeApp, has: (doc: Record<string, unknown>) => boolean) {
  const read = page.waitForResponse((r) => r.request().method() === "GET" && /\/craft\/document\?/.test(r.url()), { timeout: 90_000 })
  await page.reload()
  const loaded = await read
  const body = (await loaded.json().catch(() => ({}))) as { version?: string | number; bytes?: string }
  await page.getByRole("status").filter({ hasText: /Read-only|Ready to edit/ }).first().waitFor({ timeout: 90_000 })
  await frameOf(page, app)
  const frame = page.locator('iframe[title$=" editor"]')
  await expect(frame, "the reloaded editor is on screen").toBeVisible()
  await expect(frame, "the reloaded editor is not concealed").toHaveCSS("opacity", "1")
  return { status: loaded.status(), version: String(body.version), hasChange: has(JSON.parse(Buffer.from(body.bytes ?? "", "base64").toString("utf8") || "{}") as Record<string, unknown>) }
}

const eventsOf = (task: { events?: Array<{ operation?: string; status?: number; holder_kind?: string; error?: string }> }) =>
  (task.events ?? []).map((e) => ({ operation: e.operation, status: e.status, holder_kind: e.holder_kind, error: e.error }))

/** The panel the page shows for the task it started, or what stands in its place. */
async function taskPanel(page: Page, expected: string): Promise<{ text: string; alerts: string[] }> {
  const panel = page.getByRole("complementary", { name: "Agent task in the document" })
  await panel.getByText(expected).waitFor({ timeout: 20_000 }).catch(() => {})
  return { text: await panel.innerText({ timeout: 1_000 }).catch(() => ""), alerts: await alertsOn(page) }
}

const alertsOn = (page: Page) => page.getByRole("alert").allInnerTexts().then((all) => all.map((t) => t.trim()).filter(Boolean), () => [] as string[])

test.describe("craft-agent-ui", () => {
  for (const app of ["wordcraft", "gridcraft"] as const satisfies readonly NativeApp[]) {
    const label = app === "wordcraft" ? "Word" : "Sheet"
    const instruct = (marker: string) => (app === "wordcraft" ? wordInstruction(marker) : sheetInstruction("B2", marker))
    const carries = (marker: string) => (doc: Record<string, unknown>) => (app === "wordcraft" ? wordText(doc).includes(marker) : sheetCell(doc, 1, 1) === marker)

    test(
      `${label}: a person starts the agent from the page on a free document and sees the result after reload`,
      { annotation: prdRequirement(`A.craft.agent-ui.${label.toLowerCase()}.free`, `${label}: Edit with an agent on a free document: the status shows the agent editing and the page shows the agent's new version after reload`) },
      async ({ prd, request, page }) => {
        const seeded = prd.requireAgent()
        test.setTimeout(15 * 60_000)
        const agent = await findAgent(request, seeded.slug, seeded.workspaceId)
        const created = await createNativePage(request, agent.workspaceId, app, `PRD agent ui ${app} ${randomUUID().slice(0, 8)}`, { privateAudience: true })
        if ("unavailable" in created) {
          test.skip(true, `origin has no native Craft engines (initialize ${app}: HTTP ${created.unavailable})`)
          return
        }
        const doc: NativePage = created.created.page
        try {
          expect((await doc.release(created.created.creator, created.created.initGeneration)).status, "the document is free").toBe(200)
          const before = await doc.head()
          const marker = `PRDUI${label.toUpperCase()}_${randomUUID().slice(0, 8)}`

          page.setDefaultTimeout(60_000)
          await page.goto(doc.editorPath())
          const bar = await lockControls(page, 90_000)
          await expect(bar).toContainText("Free", { timeout: 90_000 })
          await watchBar(page)

          const { workId, held } = await startFromControl(page, agent, instruct(marker))
          const task = await waitForTask(doc, workId)
          const after = await doc.head()
          const events = eventsOf(task)
          await expect(bar, "the document is free again").toContainText("Free", { timeout: 60_000 })
          const seen = await barSeen(page)
          const panel = await taskPanel(page, "Agent task finished")
          await prd.evidence(`${app}-after-agent.png`, await page.screenshot({ fullPage: true }))
          const shown = await reloadAndRead(page, app, carries(marker))
          await expect(bar).toContainText("Free", { timeout: 90_000 })
          const alerts = await alertsOn(page)
          await prd.evidence(`${app}-reloaded.png`, await page.screenshot({ fullPage: true }))
          await prd.evidence(`${app}-free.json`, redact({
            app, mode: prd.mode, appSha: prd.appSha, agent: agent.name, workId, taskState: task.state, events, before: before.version, after: after.version,
            barTimeline: seen.map((s) => ({ atMs: s.at - seen[0].at, text: s.text })), panel, shown, alerts,
          }))

          expect(held, "a free document shows no proposal warning").toBe(false)
          expect(task.state, `the agent task ends well (events ${JSON.stringify(events)})`).toBe("succeeded")
          expect(events.some((e) => e.operation === "apply" && e.status === 200), "the agent wrote directly to the free document").toBe(true)
          expect(seen.some((s) => s.text.includes(`${agent.name} is editing`)), `the status showed the agent editing (saw ${JSON.stringify(seen.map((s) => s.text))})`).toBe(true)
          expect(BigInt(after.version), "the agent's edit is a new version").toBe(BigInt(before.version) + 1n)
          expect(carries(marker)(after.doc), "the new version carries the change").toBe(true)
          expect(shown.status, "the reloaded page reads the document").toBe(200)
          expect(shown.version, "the reloaded page shows the agent's version").toBe(after.version)
          expect(shown.hasChange, "the document the page renders carries the change").toBe(true)
          expect(alerts.join(" | "), "the reloaded page shows no alert").toBe("")
          expect(panel.alerts.join(" | "), "the page shows no alert about the finished task").toBe("")
          expect(panel.text, "the page says the task finished").toContain("Agent task finished")
        } finally {
          await page.goto("about:blank").catch(() => {})
          const status = await deletePage(request, agent.workspaceId, doc.pageId)
          if (status >= 300) console.warn(`[prd] agent-ui test page ${doc.pageId} not deleted (HTTP ${status})`)
        }
      },
    )

    test(
      `${label}: a person who holds the document starts the agent and accepts its proposal`,
      { annotation: prdRequirement(`A.craft.agent-ui.${label.toLowerCase()}.proposal`, `${label}: Edit with an agent on a document the person holds: the document stays unchanged, the proposal is shown and accepting it writes the new version`) },
      async ({ prd, request, page }) => {
        const seeded = prd.requireAgent()
        test.setTimeout(15 * 60_000)
        const agent = await findAgent(request, seeded.slug, seeded.workspaceId)
        const created = await createNativePage(request, agent.workspaceId, app, `PRD agent ui held ${app} ${randomUUID().slice(0, 8)}`, { privateAudience: true })
        if ("unavailable" in created) {
          test.skip(true, `origin has no native Craft engines (initialize ${app}: HTTP ${created.unavailable})`)
          return
        }
        const doc: NativePage = created.created.page
        const humanSessions = new Set<string>()
        page.on("response", (r: Response) => {
          if (!r.url().includes("/craft/sessions") || r.status() !== 201 || r.request().method() !== "POST") return
          void r.json().then((b: { session_id?: string }) => { if (b.session_id) humanSessions.add(b.session_id) }).catch(() => {})
        })
        try {
          expect((await doc.release(created.created.creator, created.created.initGeneration)).status, "release the creation lease").toBe(200)
          page.setDefaultTimeout(60_000)
          await page.goto(doc.editorPath())
          const status = (text: string) => page.getByRole("status").filter({ hasText: text })
          await status("Read-only").waitFor({ timeout: 90_000 })
          await (await lockButton(page, "Take over editing")).click()
          await status("Ready to edit").waitFor()

          const before = await doc.head()
          const marker = `PRDUIHELD${label.toUpperCase()}_${randomUUID().slice(0, 8)}`
          const { workId, held } = await startFromControl(page, agent, instruct(marker))
          const task = await waitForTask(doc, workId)
          const events = eventsOf(task)
          const unchanged = await doc.head()
          const proposals = (await doc.proposals(await doc.session())).body.proposals ?? []
          const proposal = proposals.find((p) => p.state === "open" && String(p.base_version) === before.version)
          await prd.evidence(`${app}-held-task.json`, redact({ app, mode: prd.mode, appSha: prd.appSha, agent: agent.name, workId, taskState: task.state, events, before: before.version, unchanged: unchanged.version, proposal: proposal ? { id: proposal.id, base: proposal.base_version } : null }))

          expect(held, "the control warned that the agent only proposes").toBe(true)
          expect(task.state, `the agent task ends well (events ${JSON.stringify(events)})`).toBe("succeeded")
          expect(events.some((e) => e.operation === "acquire" && e.status === 409), "the person's hold refused the agent").toBe(true)
          expect(unchanged.version, "the document stays unchanged until the person decides").toBe(before.version)
          expect(proposal, "the agent recorded a proposal over the version it read").toBeTruthy()

          const group = page.getByRole("group", { name: `Proposal by ${agent.name}` }).filter({ hasText: new RegExp(`on version ${before.version}(?!\\d)`) })
          await group.waitFor({ timeout: 60_000 })
          expect(await group.innerText()).toContain(`${agent.name} prepared a proposal on version ${before.version}`)
          const panel = await taskPanel(page, "The agent prepared a proposal for review")
          await prd.evidence(`${app}-proposal-shown.png`, await page.screenshot({ fullPage: true }))
          await group.getByRole("button", { name: "Compare", exact: true }).click()
          const compare = {
            server: (await page.getByRole("textbox", { name: "Server version" }).inputValue()).includes(marker),
            proposal: (await page.getByRole("textbox", { name: `Proposal by ${agent.name} on version ${before.version}`, exact: true }).inputValue()).includes(marker),
          }
          await prd.evidence(`${app}-compare.png`, await page.screenshot({ fullPage: true }))
          const accepted = page.waitForResponse((r) => r.url().includes(`/proposals/${proposal!.id}/accept`), { timeout: 60_000 })
          await group.getByRole("button", { name: "Accept", exact: true }).click()
          const answer = await accepted
          const receipt = (await answer.json().catch(() => ({}))) as { receipt?: { version?: string; sessionId?: string }; error?: string }
          const after = await doc.head()
          const alerts = await alertsOn(page)
          await expect(group, "the decided proposal leaves the page").toHaveCount(0, { timeout: 30_000 })
          const shown = await reloadAndRead(page, app, carries(marker))
          await prd.evidence(`${app}-accepted.png`, await page.screenshot({ fullPage: true }))
          await prd.evidence(`${app}-accepted.json`, redact({ compare, accept: { status: answer.status(), version: receipt.receipt?.version ?? null, error: receipt.error ?? null }, after: after.version, shown, panel, alerts }))

          expect(compare.server, "the server side of the comparison lacks the change").toBe(false)
          expect(compare.proposal, "the proposal side carries the change").toBe(true)
          expect(answer.status(), `the page accepts the proposal (alerts: ${alerts.join(" | ")})`).toBe(200)
          expect(BigInt(after.version) > BigInt(before.version), "accepting writes a new version").toBe(true)
          expect(carries(marker)(after.doc), "the new version carries the change").toBe(true)
          expect(receipt.receipt?.version, "the receipt names that version").toBe(after.version)
          expect(humanSessions.has(String(receipt.receipt?.sessionId)), "the version is the person's own save").toBe(true)
          expect(shown.version, "the reloaded page shows the accepted version").toBe(after.version)
          expect(shown.hasChange, "the document the page renders carries the change").toBe(true)
          // Last, so every result above still reports: the page has to say what became of the task it started.
          expect(panel.alerts.join(" | "), "the page shows no alert about the finished task").toBe("")
          expect(panel.text, "the page says the agent prepared a proposal").toContain("The agent prepared a proposal for review")
        } finally {
          const release = await lockButton(page, "Hand back", 5_000)
          if (await release.count().catch(() => 0)) await release.click().catch(() => {})
          await page.goto("about:blank").catch(() => {})
          const status = await deletePage(request, agent.workspaceId, doc.pageId)
          if (status >= 300) console.warn(`[prd] agent-ui test page ${doc.pageId} not deleted (HTTP ${status})`)
        }
      },
    )

    test(
      `${label}: a person with Read never gets the control, a person with Edit on the same document does`,
      { annotation: prdRequirement(`A.craft.agent-ui.${label.toLowerCase()}.read-absent`, `${label}: Edit with an agent is absent for a Read grantee and present for a write grantee on the same document`) },
      async ({ prd, request }) => {
        prd.requireFull("needs the seeded origin with agents enabled")
        const ws = await prd.qaWorkspace()
        // People first: a membership change retires open native sessions.
        const writer = await prd.provision("MEMBER", "PRD agent ui editor")
        const reader = await prd.provision("MEMBER", "PRD agent ui reader")
        const doc = await LeasePage.create(request, ws.id, app, `PRD agent ui read ${app}`)
        await requireNative(prd, request, doc)
        await doc.grant(request, writer.userId, "write")
        await doc.grant(request, reader.userId, "read")

        const person = async (account: Parameters<typeof loginStorageState>[1]) => {
          const context = await prd.browserContext(await loginStorageState(prd.origin, account))
          const opened = await context.newPage()
          opened.setDefaultTimeout(60_000)
          return opened
        }
        const availability = async (who: Page) =>
          (await (await who.context().request.get(`/api/v1/pages/${encodeURIComponent("@" + doc.pageId)}/craft/availability?workspace_id=${encodeURIComponent(ws.id)}`)).json()) as { agents_enabled?: boolean; can_edit?: boolean }
        const controlsOn = async (who: Page) => (await who.getByText("Edit with an agent").count()) + (await who.getByRole("group", { name: "Edit with an agent" }).count())

        // A Read person gets the shared preview, not the live editor. First the
        // state with nothing published, then the real document.
        const readerPage = await person(reader.account)
        await readerPage.goto(doc.editorPath)
        await expect(readerPage.getByText("The shared preview is not published yet.").first(), "nothing is published yet").toBeVisible({ timeout: 90_000 })
        const unpublished = await controlsOn(readerPage)

        const version = await doc.publishPreview(request)
        await readerPage.reload()
        await expect(readerPage.getByRole("status").filter({ hasText: `Shared preview · version ${version}` }).first(), "the Read person sees the published version").toBeVisible({ timeout: 90_000 })
        await expect(readerPage.locator("iframe"), "the document itself is on the page").toHaveCount(1, { timeout: 90_000 })

        // Only now the positive control, so the reader's wait outlasts the one that finds the control.
        const writerPage = await person(writer.account)
        await writerPage.goto(doc.editorPath)
        await writerPage.getByRole("status").filter({ hasText: "Read-only" }).waitFor({ timeout: 90_000 })
        await expect((await agentControl(writerPage, 60_000)).getByRole("combobox", { name: "Agent" }), "the write grantee has the control, so its absence below means something").toBeVisible({ timeout: 60_000 })

        const published = await controlsOn(readerPage)
        const takes = await (await lockButton(readerPage, "Take over editing", 10_000)).count()
        const access = { writer: await availability(writerPage), reader: await availability(readerPage) }
        await prd.evidence(`${app}-read.png`, await readerPage.screenshot({ fullPage: true }))
        await prd.evidence(`${app}-edit.png`, await writerPage.screenshot({ fullPage: true }))
        await prd.evidence(`${app}-read.json`, { app, mode: prd.mode, appSha: prd.appSha, version, access, controls: { unpublished, published }, takes })
        expect(access.writer, "agents are available to the write grantee").toMatchObject({ agents_enabled: true, can_edit: true })
        expect(access.reader, "the Read grantee can neither edit nor start an agent").toMatchObject({ agents_enabled: false, can_edit: false })
        expect(unpublished, "no control before anything is published").toBe(0)
        expect(published, "no control on the published document").toBe(0)
        expect(takes, "a Read person cannot take the editor").toBe(0)
      },
    )
  }
})
