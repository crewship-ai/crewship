// PRD §7 A (#4691, remainder of #4527): a whole-document agent task on a
// native Word document and Sheet that nobody holds. The agent takes the lease,
// edits through the craft_* tools, saves exactly one new version under its own
// session and releases the lease; the person who has the editor open read-only
// gets the agent's version on reload.
//
// craft-proposal covers the other branch (a person holds the editor, the agent
// gets 409 and proposes). Refusal of formulas and non-string Sheet input is a
// tool contract, covered by internal/api/craft_native_agent_engine_test.go.
//
// Full mode only: it needs the seeded agent, and the agent's crew must be on
// the workspace agent artifact policy (docs/development/prd-acceptance.md).
import { randomUUID } from "node:crypto"
import { findAgent, openChat, redact, sendInstruction, sheetInstruction, waitForTask, wordInstruction } from "./craft-proposal/agent"
import { createNativePage, deletePage, sheetCell, wordText } from "./craft-proposal/native"
import { expect, prdRequirement, test } from "./fixtures"

test.describe("craft agent edit", () => {
  for (const app of ["wordcraft", "gridcraft"] as const) {
    const label = app === "wordcraft" ? "Word" : "Sheet"
    const key = label.toLowerCase()
    test(
      `${label}: an agent edits a document nobody holds and saves exactly one version`,
      {
        annotation: [
          prdRequirement(`A.craft.agent.${key}.lease`, `${label}: an agent task on a document nobody holds takes the lease and releases it when done`),
          prdRequirement(
            `A.craft.agent.${key}.one-version`,
            `${label}: the agent's edit through the craft tools is saved as exactly one new version that carries the requested change`,
          ),
          prdRequirement(`A.craft.agent.${key}.visible`, `${label}: the person's open editor shows the agent's version after reload, with no draft or conflict`),
        ],
      },
      async ({ prd, request, page }) => {
        const seeded = prd.requireAgent()
        const agent = await findAgent(request, seeded.slug, seeded.workspaceId)
        const r = await createNativePage(request, agent.workspaceId, app, `PRD agent edit ${app} ${randomUUID().slice(0, 8)}`, { privateAudience: true })
        if ("unavailable" in r) {
          await prd.evidence(`native-unavailable-${app}.json`, { origin: prd.origin, status: r.unavailable, error: r.reason })
          test.skip(true, `origin has no native Craft engines (initialize ${app}: HTTP ${r.unavailable} ${r.reason})`)
          return
        }
        const doc = r.created.page
        try {
          expect((await doc.release(r.created.creator, r.created.initGeneration)).status, "release the creation lease").toBe(200)
          const before = await doc.head()
          const freeBefore = await doc.lease(await doc.session())
          expect(freeBefore.body.holder ?? null, "nobody holds the editor before the task").toBeNull()

          // The person watches read-only; they never take the editor.
          page.setDefaultTimeout(45_000)
          await page.goto(doc.editorPath())
          await page.getByRole("status").filter({ hasText: "Pouze čtení" }).waitFor({ timeout: 90_000 })

          const who = await request.get("/api/auth/session")
          const ownerUserId = ((await who.json()) as { user: { id: string } }).user.id
          const chatId = await openChat(request, agent)
          const marker = `PRDAGENT${label.toUpperCase()}_${randomUUID().slice(0, 8)}`
          const sent = await sendInstruction(page, request, agent, chatId, ownerUserId, app === "wordcraft" ? wordInstruction(marker) : sheetInstruction("B2", marker))
          await page.waitForTimeout(15_000)
          const start = await doc.startTask(chatId, sent.messageId)
          expect(start.status, `start native task: ${JSON.stringify(start.body).slice(0, 200)}`).toBeLessThan(300)
          const st = await waitForTask(doc, start.body.work_id!)

          const after = await doc.head()
          const leaseAfter = await doc.lease(await doc.session())
          const events = (st.events ?? []).map((e) => ({ operation: e.operation, status: e.status, holder_kind: e.holder_kind, error: e.error }))
          const has = app === "wordcraft" ? wordText(after.doc).includes(marker) : sheetCell(after.doc, 1, 1) === marker

          await page.reload()
          await page.getByRole("status").filter({ hasText: "Pouze čtení" }).waitFor({ timeout: 90_000 })
          const alerts = await page
            .getByRole("alert")
            .allInnerTexts()
            .catch(() => [] as string[])
          const draftOffered = await page.getByText(/Neuložený koncept|Koncept zůstává/).count()
          await prd.evidence(`${app}-agent-edit-reloaded.png`, await page.screenshot({ fullPage: true }))
          await prd.evidence(
            `${app}-agent-edit.json`,
            redact({
              app,
              workId: start.body.work_id,
              taskState: st.state,
              events,
              receipt: st.last_committed_receipt ?? null,
              before: before.version,
              after: after.version,
              hasMarker: has,
              leaseAfter: leaseAfter.body.holder ?? null,
              uiAlerts: alerts,
              draftOffered,
            }),
          )

          expect(st.state, `task state (events ${JSON.stringify(events)})`).toBe("succeeded")
          expect(events.some((e) => e.operation === "acquire" && e.status === 200), "the agent took the lease").toBe(true)
          expect(events.filter((e) => e.operation === "apply" && e.status === 200).length, "exactly one applied write").toBe(1)
          expect(events.some((e) => e.operation === "release" && e.status === 200), "the agent released the lease").toBe(true)
          expect(BigInt(after.version), "exactly one new version").toBe(BigInt(before.version) + 1n)
          expect(has, "the new version carries the requested change").toBe(true)
          expect(String(st.last_committed_receipt?.version), "the task's receipt names that version").toBe(after.version)
          expect(st.last_committed_receipt?.contentHash, "the receipt hash is the saved bytes").toBe(after.hash)
          expect(leaseAfter.body.holder ?? null, "nobody holds the editor after the task").toBeNull()
          expect(draftOffered, "the reloaded editor offers no draft").toBe(0)
          expect(alerts.join(" | "), "the reloaded editor shows no conflict").not.toMatch(/konflikt|změnil/i)
        } finally {
          await page.goto("about:blank").catch(() => {})
          const status = await deletePage(request, agent.workspaceId, doc.pageId)
          if (status >= 300) console.warn(`[prd] craft-agent-edit test page ${doc.pageId} not deleted (HTTP ${status})`)
        }
      },
    )
  }
})
