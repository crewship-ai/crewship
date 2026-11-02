// PRD §7 A4: an agent edits a free (unlocked) Word or Sheet document. A person
// who has the same document open in the browser sees who is editing
// ("Upravuje <agent>") while the agent holds the editor, and once the agent has
// finished the person sees the change: the editor reads the new version and the
// bar is free again.
//
// Full mode and a seeded agent only. The page lives in PRD_AGENT_WORKSPACE_ID,
// the one workspace where that agent can act, as a private test page that is
// deleted afterwards (the same setup as the proposal scenarios).
import { randomUUID } from "node:crypto"
import type { Page } from "@playwright/test"
import { findAgent, openChat, redact, sendInstruction, sheetInstruction, waitForTask, wordInstruction } from "./craft-proposal/agent"
import { createNativePage, deletePage, sheetCell, wordText } from "./craft-proposal/native"
import { expect, prdRequirement, test } from "./fixtures"

const REQ = {
  wordcraft: prdRequirement("A.craft.agent.free-edit-word", "Word: an agent edits a free document; the person in the browser sees \"Upravuje <agent>\", then the change and the new version"),
  gridcraft: prdRequirement("A.craft.agent.free-edit-sheet", "Sheet: an agent edits a free document; the person in the browser sees \"Upravuje <agent>\", then the change and the new version"),
} as const

interface Seen {
  at: number
  text: string
}

/** Records every text the lease bar shows, with timestamps, inside the page. */
async function watchBar(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as unknown as { __bar: Array<{ at: number; text: string }> }
    w.__bar = []
    const bar = document.querySelector('[role="toolbar"][aria-label="Úpravy dokumentu"]')
    if (!bar) throw new Error("lease bar not found")
    const note = () => {
      const text = (bar.textContent ?? "").trim()
      if (w.__bar.at(-1)?.text !== text) w.__bar.push({ at: Date.now(), text })
    }
    note()
    new MutationObserver(note).observe(bar, { subtree: true, childList: true, characterData: true })
  })
}

const barSeen = (page: Page) => page.evaluate(() => (window as unknown as { __bar: Seen[] }).__bar)

for (const app of ["wordcraft", "gridcraft"] as const) {
  const label = app === "wordcraft" ? "Word" : "Sheet"
  test.describe(`${label} agent edits a free document`, () => {
    test(
      `${label}: the agent edits an unlocked document and the person sees who edits and the result`,
      { annotation: REQ[app] },
      async ({ prd, request, page }) => {
        const seeded = prd.requireAgent()
        test.setTimeout(15 * 60_000)
        const agent = await findAgent(request, seeded.slug, seeded.workspaceId)
        const created = await createNativePage(request, agent.workspaceId, app, `PRD free edit ${app} ${randomUUID().slice(0, 8)}`, { privateAudience: true })
        if ("unavailable" in created) {
          test.skip(true, `origin has no native Craft engines (initialize ${app}: HTTP ${created.unavailable})`)
          return
        }
        const doc = created.created.page
        try {
          const released = await doc.release(created.created.creator, created.created.initGeneration)
          expect(released.status, "release the creation lease; the document is free").toBe(200)
          const ownerId = ((await (await request.get("/api/auth/session")).json()) as { user: { id: string } }).user.id
          const chatId = await openChat(request, agent)
          const before = await doc.head()
          const marker = `PRDFREE${label.toUpperCase()}_${randomUUID().slice(0, 8)}`

          // The person opens the document and only watches: they do not take the editor.
          page.setDefaultTimeout(60_000)
          const reads: Array<{ at: number; version: string; hasMarker: boolean }> = []
          page.on("response", (r) => {
            if (r.request().method() !== "GET" || !new URL(r.url()).pathname.endsWith("/craft/document") || r.status() !== 200) return
            void r
              .json()
              .then((b: { version?: unknown; bytes?: string }) => {
                reads.push({ at: Date.now(), version: String(b.version), hasMarker: Buffer.from(b.bytes ?? "", "base64").toString("utf8").includes(marker) })
              })
              .catch(() => {})
          })
          await page.goto(doc.editorPath())
          const bar = page.getByRole("toolbar", { name: "Úpravy dokumentu" })
          await expect(bar).toContainText("Volné", { timeout: 90_000 })
          await watchBar(page)

          const instruction = app === "wordcraft" ? wordInstruction(marker) : sheetInstruction("B2", marker)
          const sent = await sendInstruction(page, request, agent, chatId, ownerId, instruction)
          await page.waitForTimeout(15_000)
          const started = await doc.startTask(chatId, sent.messageId)
          expect(started.status, `start native task: ${JSON.stringify(started.body).slice(0, 200)}`).toBeLessThan(300)
          const task = await waitForTask(doc, started.body.work_id!)
          const after = await doc.head()
          const has = (d: Record<string, unknown>) => (app === "wordcraft" ? wordText(d).includes(marker) : sheetCell(d, 1, 1) === marker)
          const events = (task.events ?? []).map((e) => ({ operation: e.operation, status: e.status, holder_kind: e.holder_kind, error: e.error }))

          // The person's bar went free -> "Upravuje <agent>" -> free, and the editor read the new version.
          await expect(bar).toContainText("Volné", { timeout: 60_000 })
          await expect.poll(() => reads.some((r) => r.version === after.version && r.hasMarker), { timeout: 60_000, message: "the person's editor reads the new version" }).toBe(true)
          const seen = await barSeen(page)
          const holderTexts = seen.filter((s) => s.text.includes("Upravuje")).map((s) => s.text)
          await prd.evidence(`${app}-free-edit.json`, redact({
            app, mode: prd.mode, appSha: prd.appSha, agent: agent.name, taskState: task.state, events,
            before: before.version, after: after.version, barTimeline: seen.map((s) => ({ atMs: s.at - seen[0].at, text: s.text })),
            editorReads: reads.map((r) => ({ version: r.version, hasMarker: r.hasMarker })),
          }))
          await prd.evidence(`${app}-free-edit.png`, await page.screenshot({ fullPage: true }))

          expect(task.state, `the agent task ends well (events ${JSON.stringify(events)})`).toBe("succeeded")
          expect(events.some((e) => e.operation === "apply" && e.status === 200), "the agent wrote directly to the free document").toBe(true)
          expect(events.some((e) => e.operation === "acquire" && e.status === 409), "the free document needed no proposal").toBe(false)
          expect(BigInt(after.version) > BigInt(before.version), "the agent's edit is a new version").toBe(true)
          expect(has(after.doc), "the agent's change is in the document").toBe(true)
          expect(holderTexts.some((t) => t.includes(`Upravuje ${agent.name}`)), `the person saw "Upravuje ${agent.name}" (saw: ${JSON.stringify(seen.map((s) => s.text))})`).toBe(true)
        } finally {
          await page.goto("about:blank").catch(() => {})
          const status = await deletePage(request, agent.workspaceId, doc.pageId)
          if (status >= 300) console.warn(`[prd] free-edit test page ${doc.pageId} not deleted (HTTP ${status})`)
        }
      },
    )
  })
}
