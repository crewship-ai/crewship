// PRD §7 A (#4480): Word and Sheet agent proposals while a person holds the
// editor. The agent's write gets 409 and it records a separate proposal over
// the version it read; the person compares, accepts (the new version is the
// proposal bytes) or rejects; a proposal over an older version goes stale; a
// reader cannot decide (403).
//
// Quick mode runs the agent-free API checks. The native checks need an origin
// with native Craft engines (initialization answers 503 elsewhere and the test
// is skipped with that reason). The agent tests need full mode and a seeded
// agent; their pages are created in PRD_AGENT_WORKSPACE_ID, the only workspace
// where that agent can act, as private test pages that are deleted afterwards.
import { randomUUID } from "node:crypto"
import type { APIRequestContext, Page, Response } from "@playwright/test"
import { apiAs, loginStorageState } from "./auth"
import { findAgent, openChat, redact, sendInstruction, sheetInstruction, waitForTask, wordInstruction, type SeededAgent } from "./craft-proposal/agent"
import {
  createNativePage,
  deletePage,
  NativePage,
  sheetCell,
  wordText,
  type CreatedNativePage,
  type NativeApp,
  type ProposalView,
} from "./craft-proposal/native"
import { frameOf, typeMarker } from "./drafts-crash/native"
import { expect, prdRequirement, test, type PrdScenario } from "./fixtures"

const UNKNOWN_PROPOSAL = "ncp_000000000000000000000000"
const STALE_MESSAGE = "Dokument se po přípravě návrhu změnil"

/** Creates a native page or skips the test when the origin has no native engines. */
async function nativeOrSkip(
  prd: PrdScenario,
  api: APIRequestContext,
  workspaceId: string,
  app: NativeApp,
  name: string,
  privateAudience = false,
): Promise<CreatedNativePage> {
  const r = await createNativePage(api, workspaceId, app, name, {
    privateAudience,
  })
  if ("unavailable" in r) {
    await prd.evidence(`native-unavailable-${app}.json`, {
      origin: prd.origin,
      status: r.unavailable,
      error: r.reason,
    })
    test.skip(true, `origin has no native Craft engines (initialize ${app}: HTTP ${r.unavailable} ${r.reason})`)
    throw new Error("unreachable")
  }
  return r.created
}

test.describe("craft proposal", () => {
  test(
    "proposal routes refuse callers without a native admission",
    {
      annotation: prdRequirement("A.craft.proposal.admission", "Proposal review and decisions need an admitted native session; nothing is decided without one"),
    },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      const q = `workspace_id=${encodeURIComponent(ws.id)}`
      const slug = `prd-qa-plain-${randomUUID().replaceAll("-", "").slice(0, 10)}`
      const created = await request.post(`/api/v1/pages?${q}`, {
        data: {
          name: "PRD proposal admission",
          slug,
          panels: [],
          allow_empty: true,
          created_by_test: true,
        },
      })
      expect(created.status()).toBe(201)
      const pageId = ((await created.json()) as { id: string }).id
      const base = `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/proposals`
      const calls = [
        { name: "list", res: await request.get(`${base}?${q}`) },
        {
          name: "read",
          res: await request.get(`${base}/${UNKNOWN_PROPOSAL}?${q}`),
        },
        {
          name: "accept",
          res: await request.post(`${base}/${UNKNOWN_PROPOSAL}/accept?${q}`, {
            data: { sessionId: "x", generation: "1" },
          }),
        },
        {
          name: "reject",
          res: await request.post(`${base}/${UNKNOWN_PROPOSAL}/reject?${q}`, {
            data: {},
          }),
        },
      ]
      const out: Record<string, { status: number; error: unknown }> = {}
      for (const c of calls) {
        const body = (await c.res.json().catch(() => null)) as {
          error?: unknown
        } | null
        out[c.name] = { status: c.res.status(), error: body?.error ?? null }
      }
      await prd.evidence("routes.json", {
        page: "ordinary test page, no native admission",
        calls: out,
      })
      for (const [name, r] of Object.entries(out)) {
        // A missing route answers the router's plain-text 404; the proposal
        // handlers answer a JSON error. Either way nothing may succeed.
        expect(typeof r.error, `${name}: proposal route must exist and answer a JSON error (HTTP ${r.status})`).toBe("string")
        expect(r.status, `${name} without a native admission`).toBeGreaterThanOrEqual(400)
      }
    },
  )

  test(
    "a person holding the editor blocks other writers and a reader cannot decide",
    {
      annotation: [
        prdRequirement(
          "A.lock",
          "While a person holds the editor, another writer's acquire gets 409 and the lease names the human holder (Word and Sheet)",
        ),
        prdRequirement("A.craft.proposal.reader-403", "A reader can list proposals but cannot accept or reject them (403); the document is unchanged"),
      ],
    },
    async ({ prd, request }) => {
      const ws = await prd.qaWorkspace()
      // People first: a membership change retires native sessions that are
      // already open, including the creating admission that holds the lease.
      const member = await prd.provision("MEMBER", "PRD proposal co-editor")
      const reader = await prd.provision("VIEWER", "PRD proposal reader")
      const memberApi = await apiAs(prd.origin, await loginStorageState(prd.origin, member.account))
      const readerApi = await apiAs(prd.origin, await loginStorageState(prd.origin, reader.account))
      const results: Record<string, unknown> = {}
      try {
        // One document at a time: initializing another native Page retires the
        // creating admission of the previous one.
        for (const app of ["wordcraft", "gridcraft"] as const) {
          const created = await nativeOrSkip(prd, request, ws.id, app, `PRD proposal rights ${app}`)
          const owner = created.page
          expect((await owner.release(created.creator, created.initGeneration)).status, `${app}: release the creation lease`).toBe(200)
          // New test Pages are private to their author; share them explicitly.
          for (const [userId, level] of [
            [member.userId, "write"],
            [reader.userId, "read"],
          ] as const) {
            const grant = await request.put(`/api/v1/pages/${encodeURIComponent("@" + owner.pageId)}/grants?workspace_id=${encodeURIComponent(ws.id)}`, {
              data: { subject_type: "user", subject: userId, level },
            })
            expect(grant.status(), `${app}: grant ${level}`).toBeLessThan(300)
          }
          // The person takes the editor through a fresh admission (sharing
          // changes the Page's authority, which retires older admissions).
          const holder = await owner.session()
          const held = await owner.acquire(holder)
          expect(held.status, `${app}: the person takes the editor`).toBe(200)
          const before = await owner.head(holder)
          const ownerLease = await owner.lease(holder)
          expect(ownerLease.body.holder?.kind, `${app}: a person holds the editor`).toBe("human")
          expect(ownerLease.body.mine).toBe(true)

          const asMember = new NativePage(memberApi, ws.id, owner.pageId)
          const m = await asMember.session()
          const memberAcquire = await asMember.acquire(m)
          const memberView = await asMember.lease(m)

          const asReader = new NativePage(readerApi, ws.id, owner.pageId)
          const readerSession = await asReader.admit()
          expect(readerSession.status, `${app}: a reader may open the document`).toBe(201)
          const r = readerSession.admission!
          const readerList = await asReader.proposals(r)
          const readerAccept = await asReader.accept(r, UNKNOWN_PROPOSAL, "1")
          const readerReject = await asReader.reject(r, UNKNOWN_PROPOSAL)
          const readerAcquire = await asReader.acquire(r)
          // Control: the owner reaches the proposal lookup, so the reader's
          // 403 is a rights decision and not a missing proposal.
          const ownerReject = await owner.reject(holder, UNKNOWN_PROPOSAL)
          const after = await owner.head(holder)

          results[app] = {
            pageVersion: {
              before: before.version,
              after: after.version,
              unchanged: before.hash === after.hash,
            },
            ownerLease: {
              holderKind: ownerLease.body.holder?.kind,
              mine: ownerLease.body.mine,
              generation: ownerLease.body.generation,
            },
            memberAcquire: {
              status: memberAcquire.status,
              leaseHolderSeen: memberView.body.holder?.kind ?? null,
              memberIsHolder: memberView.body.mine ?? null,
              error: memberAcquire.body.error ?? null,
            },
            reader: {
              list: readerList.status,
              accept: {
                status: readerAccept.status,
                error: readerAccept.body.error ?? null,
              },
              reject: {
                status: readerReject.status,
                error: readerReject.body.error ?? null,
              },
              acquire: readerAcquire.status,
            },
            ownerRejectUnknown: {
              status: ownerReject.status,
              error: ownerReject.body.error ?? null,
            },
          }
          await prd.evidence(`rights-${app}.json`, results[app])

          expect(memberAcquire.status, `${app}: co-editor acquire while a person holds the editor`).toBe(409)
          expect(memberView.body.holder?.kind, `${app}: the co-editor sees a human holder`).toBe("human")
          expect(memberView.body.mine).toBe(false)
          expect(readerList.status, `${app}: reader lists proposals`).toBe(200)
          expect(readerAccept.status, `${app}: reader accept`).toBe(403)
          expect(readerReject.status, `${app}: reader reject`).toBe(403)
          expect(readerAcquire.status, `${app}: reader acquire`).not.toBe(200)
          expect(ownerReject.status, `${app}: owner reject of an unknown proposal`).toBe(404)
          expect(after.hash, `${app}: document unchanged`).toBe(before.hash)
          expect(after.version).toBe(before.version)
          await owner.release(holder, String(held.body.generation))
        }
      } finally {
        await memberApi.dispose()
        await readerApi.dispose()
      }
    },
  )

  for (const app of ["wordcraft", "gridcraft"] as const) {
    const label = app === "wordcraft" ? "Word" : "Sheet"
    test(
      `${label}: agent proposes while a person holds the editor; the person compares and accepts`,
      {
        annotation: [
          prdRequirement(
            `A.craft.proposal.${label.toLowerCase()}.agent-409`,
            `${label}: the agent gets 409 from the person's lease and records a proposal over the version it read; the document is unchanged`,
          ),
          prdRequirement(`A.craft.proposal.${label.toLowerCase()}.compare`, `${label}: the person compares the proposal with the server version in the editor`),
          prdRequirement(
            `A.craft.proposal.${label.toLowerCase()}.accept`,
            `${label}: accepting saves exactly the proposal bytes as the person's own new version`,
          ),
        ],
      },
      async ({ prd, request, page }) => {
        const flow = await AgentFlow.start(prd, request, page, app)
        try {
          const marker = `PRD${label.toUpperCase()}_${randomUUID().slice(0, 8)}`
          const p = await flow.propose(app === "wordcraft" ? wordInstruction(marker) : sheetInstruction("B2", marker), "accept")
          const candidate = await flow.doc.proposal(await flow.doc.session(), p.proposal.id)
          expect(candidate.status).toBe(200)
          expect(candidate.contentHash, "proposal bytes match their recorded hash").toBe(candidate.computedHash)
          const has = (d: Record<string, unknown>) => (app === "wordcraft" ? wordText(d).includes(marker) : sheetCell(d, 1, 1) === marker)
          expect(has(candidate.doc!), "the proposal carries the requested change").toBe(true)

          const group = await flow.banner(p.before.version)
          await group.getByRole("button", { name: "Porovnat", exact: true }).click()
          const server = page.getByRole("textbox", { name: "Serverová verze" })
          await server.waitFor()
          const proposalBox = page.getByRole("textbox", {
            name: `Návrh ${flow.agent.name} nad verzí ${p.before.version}`,
            exact: true,
          })
          const compare = {
            server: (await server.inputValue()).includes(marker),
            proposal: (await proposalBox.inputValue()).includes(marker),
          }
          await prd.evidence(`${app}-compare.png`, await page.screenshot({ fullPage: true }))

          const acceptRes = page.waitForResponse((r) => r.url().includes(`/proposals/${p.proposal.id}/accept`), { timeout: 60_000 })
          await group.getByRole("button", { name: "Přijmout", exact: true }).click()
          const ar = await acceptRes
          const ab = (await ar.json().catch(() => ({}))) as {
            receipt?: { version?: string; sessionId?: string }
            error?: string
          }
          const post = await flow.doc.head()
          const fin = await flow.proposalState(p.proposal.id)
          const alert = await page
            .getByRole("alert")
            .allInnerTexts()
            .catch(() => [] as string[])
          await prd.evidence(`${app}-accept.json`, {
            ...p.summary,
            compare,
            accept: {
              status: ar.status(),
              receiptVersion: ab.receipt?.version ?? null,
              receiptIsHumanSession: flow.humanSessions.has(String(ab.receipt?.sessionId)),
              error: ab.error ?? null,
            },
            uiAlert: alert,
            head: {
              before: p.before.version,
              after: post.version,
              equalsProposal: post.hash === candidate.contentHash,
            },
            proposalAfter: fin,
          })
          expect(compare.server, "server side of the comparison does not have the change").toBe(false)
          expect(compare.proposal, "proposal side of the comparison has the change").toBe(true)
          expect(ar.status(), `UI Přijmout must apply the proposal (alert: ${alert.join(" | ")})`).toBe(200)
          expect(BigInt(post.version) > BigInt(p.before.version), "accept writes a new version").toBe(true)
          expect(post.hash, "new version is exactly the proposal bytes").toBe(candidate.contentHash)
          expect(ab.receipt?.version).toBe(post.version)
          expect(flow.humanSessions.has(String(ab.receipt?.sessionId)), "the accepted version is the person's own save, not the agent's").toBe(true)
          expect(fin?.state).toBe("accepted")
          expect(String(fin?.result_version)).toBe(post.version)
        } finally {
          await flow.dispose()
        }
      },
    )
  }

  for (const app of ["wordcraft", "gridcraft"] as const) {
    const label = app === "wordcraft" ? "Word" : "Sheet"
    const slug = label.toLowerCase()
    const instruct = (marker: string) => (app === "wordcraft" ? wordInstruction(marker) : sheetInstruction("B2", marker))
    // Sheet text lives in a cell: the person's value goes to A9, the proposal to B2.
    const has = (d: Record<string, unknown>, text: string) => (app === "wordcraft" ? wordText(d).includes(text) : sheetCell(d, 8, 0) === text || sheetCell(d, 1, 1) === text)

    test(
      `${label}: the person rejects a proposal`,
      { annotation: prdRequirement(`A.craft.proposal.${slug}.reject`, `${label}: rejecting a proposal leaves the document and its version unchanged`) },
      async ({ prd, request, page }) => {
        const flow = await AgentFlow.start(prd, request, page, app)
        try {
          const m1 = `PRD${label.toUpperCase()}REJECT_${randomUUID().slice(0, 8)}`
          const p1 = await flow.propose(instruct(m1), "reject")
          const g1 = await flow.banner(p1.before.version)
          const rejectRes = page.waitForResponse((r) => r.url().includes(`/proposals/${p1.proposal.id}/reject`), { timeout: 60_000 })
          await g1.getByRole("button", { name: "Odmítnout", exact: true }).click()
          const rr = await rejectRes
          const afterReject = await flow.doc.head()
          const fin1 = await flow.proposalState(p1.proposal.id)
          await expect(flow.group(p1.before.version)).toHaveCount(0, { timeout: 30_000 })
          await prd.evidence(`${slug}-reject.json`, {
            ...p1.summary,
            rejectStatus: rr.status(),
            headAfter: afterReject.version,
            unchanged: afterReject.hash === p1.before.hash,
            keepsProposal: has(afterReject.doc, m1),
            proposalAfter: fin1,
          })
          expect(rr.status()).toBe(200)
          expect(afterReject.version).toBe(p1.before.version)
          expect(afterReject.hash).toBe(p1.before.hash)
          expect(has(afterReject.doc, m1), "the rejected proposal is not applied").toBe(false)
          expect(fin1?.state).toBe("rejected")
        } finally {
          await flow.dispose()
        }
      },
    )

    test(
      `${label}: a proposal over an older version goes stale`,
      { annotation: prdRequirement(`A.craft.proposal.${slug}.stale`, `${label}: after the person saves, accepting an older proposal is refused as stale and nothing is applied`) },
      async ({ prd, request, page }) => {
        const flow = await AgentFlow.start(prd, request, page, app)
        try {
          // The person saves after the agent's base, then tries to accept.
          const m2 = `PRD${label.toUpperCase()}STALE_${randomUUID().slice(0, 8)}`
          const human = `PRDHUMAN_${randomUUID().slice(0, 8)}`
          const p2 = await flow.propose(instruct(m2), "stale")
          const humanSave = await flow.typeAndSave(human)
          expect(BigInt(humanSave.version) > BigInt(p2.before.version), "the person's save is a new version").toBe(true)
          expect(has(humanSave.doc, human)).toBe(true)
          const g2 = await flow.banner(p2.before.version)
          const acceptRes = page.waitForResponse((r) => r.url().includes(`/proposals/${p2.proposal.id}/accept`), { timeout: 60_000 })
          await g2.getByRole("button", { name: "Přijmout", exact: true }).click()
          const ar = await acceptRes
          const message = page.getByRole("alert").filter({ hasText: STALE_MESSAGE })
          await message.waitFor({ timeout: 20_000 })
          const post = await flow.doc.head()
          const fin2 = await flow.proposalState(p2.proposal.id)
          await prd.evidence(`${slug}-stale.json`, {
            ...p2.summary,
            humanSaveVersion: humanSave.version,
            acceptStatus: ar.status(),
            uiMessage: await message.innerText(),
            headAfter: post.version,
            keepsHumanEdit: has(post.doc, human),
            appliedProposal: has(post.doc, m2),
            proposalAfter: fin2,
          })
          await prd.evidence(`${slug}-stale.png`, await page.screenshot({ fullPage: true }))
          expect(ar.status()).toBe(409)
          expect(fin2?.state).toBe("stale")
          expect(has(post.doc, human), "the person's edit is kept").toBe(true)
          expect(has(post.doc, m2), "the stale proposal is not applied").toBe(false)
        } finally {
          await flow.dispose()
        }
      },
    )
  }
})

interface Proposed {
  before: { version: string; hash: string }
  proposal: ProposalView
  summary: Record<string, unknown>
}

/** One native page in the agent's workspace, held by the owner in the editor UI. */
class AgentFlow {
  readonly humanSessions = new Set<string>()
  private ownerUserId = ""

  private constructor(
    readonly prd: PrdScenario,
    readonly request: APIRequestContext,
    readonly page: Page,
    readonly app: NativeApp,
    readonly agent: SeededAgent,
    readonly doc: NativePage,
    readonly chatId: string,
  ) {}

  static async start(prd: PrdScenario, request: APIRequestContext, page: Page, app: NativeApp): Promise<AgentFlow> {
    const seeded = prd.requireAgent()
    const agent = await findAgent(request, seeded.slug, seeded.workspaceId)
    const created = await nativeOrSkip(prd, request, agent.workspaceId, app, `PRD proposal ${app} ${randomUUID().slice(0, 8)}`, true)
    const doc = created.page
    let flow: AgentFlow | undefined
    try {
      const released = await doc.release(created.creator, created.initGeneration)
      expect(released.status, "release the creation lease").toBe(200)
      const chatId = await openChat(request, agent)
      flow = new AgentFlow(prd, request, page, app, agent, doc, chatId)
      const who = await request.get("/api/auth/session")
      flow.ownerUserId = ((await who.json()) as { user: { id: string } }).user.id
      page.on("response", (r: Response) => {
        if (!r.url().includes("/craft/sessions") || r.status() !== 201 || r.request().method() !== "POST") return
        void r
          .json()
          .then((b: { session_id?: string }) => {
            if (b.session_id) flow!.humanSessions.add(b.session_id)
          })
          .catch(() => {})
      })
      await flow.hold()
      return flow
    } catch (e) {
      await deletePage(request, agent.workspaceId, doc.pageId)
      throw e
    }
  }

  private status(text: string) {
    return this.page.getByRole("status").filter({ hasText: text })
  }

  /** Opens the editor and takes the lease as the person. */
  async hold(): Promise<void> {
    this.page.setDefaultTimeout(45_000)
    await this.page.goto(this.doc.editorPath())
    await this.status("Pouze čtení").waitFor({ timeout: 90_000 })
    await this.page.getByRole("button", { name: "Převzít úpravy", exact: true }).click()
    await this.status("Připraveno k úpravám").waitFor()
  }

  group(baseVersion: string) {
    return this.page.getByRole("group", { name: `Návrh ${this.agent.name}` }).filter({ hasText: new RegExp(`nad verzí ${baseVersion}(?!\\d)`) })
  }

  async banner(baseVersion: string) {
    const g = this.group(baseVersion)
    await g.waitFor({ timeout: 30_000 })
    expect(await g.innerText()).toContain(`${this.agent.name} připravil návrh nad verzí ${baseVersion}`)
    return g
  }

  async proposalState(id: string): Promise<ProposalView | undefined> {
    const list = await this.doc.proposals(await this.doc.session())
    return list.body.proposals?.find((p) => p.id === id)
  }

  /**
   * Asks the agent for a whole-document change while the person holds the
   * editor and returns the new open proposal. Asserts the 409 → proposal path
   * and that the document did not change.
   */
  async propose(instruction: string, label: string): Promise<Proposed> {
    const before = await this.doc.head()
    const admission = await this.doc.session()
    const lease = await this.doc.lease(admission)
    expect(lease.body.holder?.kind, "the person holds the editor before the agent starts").toBe("human")
    const sent = await sendInstruction(this.page, this.request, this.agent, this.chatId, this.ownerUserId, instruction)
    // The chat turn acknowledges; the explicit native task does the edit.
    await this.page.waitForTimeout(15_000)
    const startedAt = new Date()
    const start = await this.doc.startTask(this.chatId, sent.messageId)
    expect(start.status, `start native task: ${JSON.stringify(start.body).slice(0, 200)}`).toBeLessThan(300)
    const workId = start.body.work_id!
    const st = await waitForTask(this.doc, workId)
    const after = await this.doc.head()
    const list = (await this.doc.proposals(await this.doc.session())).body.proposals ?? []
    const mine = list.filter(
      (p) => p.state === "open" && String(p.base_version) === before.version && new Date(p.created_at) >= new Date(startedAt.getTime() - 120_000),
    )
    const events = (st.events ?? []).map((e) => ({
      operation: e.operation,
      status: e.status,
      holder_kind: e.holder_kind,
      error: e.error,
    }))
    const summary = {
      label,
      app: this.app,
      workId,
      taskState: st.state,
      events,
      leaseHolderKind: lease.body.holder?.kind,
      before: before.version,
      afterAgent: after.version,
      proposal: mine.at(-1)
        ? {
            id: mine.at(-1)!.id,
            base: mine.at(-1)!.base_version,
            author: mine.at(-1)!.author.display_name,
            summary: mine.at(-1)!.summary,
          }
        : null,
    }
    await this.prd.evidence(`${this.app}-${label}-agent.json`, redact(summary))
    expect(
      events.some((e) => e.operation === "acquire" && e.status === 409),
      `the agent's acquire got 409 (events ${JSON.stringify(events)})`,
    ).toBe(true)
    expect(
      events.some((e) => e.operation === "apply" && e.status === 200),
      "the agent must not write while the person holds the editor",
    ).toBe(false)
    expect(after.version, "the agent wrote no version").toBe(before.version)
    expect(after.hash).toBe(before.hash)
    expect(mine.length, `an open proposal over version ${before.version} (task ${st.state}, events ${JSON.stringify(events)})`).toBeGreaterThan(0)
    expect(mine.at(-1)!.author.kind).toBe("agent")
    return {
      before: { version: before.version, hash: before.hash },
      proposal: mine.at(-1)!,
      summary,
    }
  }

  /** Types in the editor (end of the Word text, cell A9 of the Sheet) and saves with Ctrl+S. */
  async typeAndSave(text: string) {
    if (this.app === "wordcraft") {
      const frame = this.page.frameLocator('iframe[title="WordCraft editor"]')
      await frame
        .locator("canvas")
        .first()
        .click({ position: { x: 400, y: 340 } })
      await this.page.keyboard.press("Control+End")
      await this.page.keyboard.type(" " + text + " ", { delay: 20 })
    } else {
      await typeMarker(this.page, await frameOf(this.page, this.app), this.app, text, 9)
    }
    const saved = this.page.waitForResponse((r) => r.url().includes("/document/save") && r.request().method() === "POST" && r.status() === 200, {
      timeout: 60_000,
    })
    await this.page.keyboard.press("Control+s")
    await saved
    return this.doc.head()
  }

  async dispose(): Promise<void> {
    try {
      const release = this.page.getByRole("button", {
        name: "Uvolnit úpravy",
        exact: true,
      })
      if (await release.count()) {
        await release.click()
        await this.status("Pouze čtení").waitFor({ timeout: 30_000 })
      }
    } catch {
      // The page is deleted below either way.
    }
    await this.page.goto("about:blank").catch(() => {})
    const status = await deletePage(this.request, this.agent.workspaceId, this.doc.pageId)
    if (status >= 300) console.warn(`[prd] craft-proposal test page ${this.doc.pageId} not deleted (HTTP ${status})`)
  }
}
