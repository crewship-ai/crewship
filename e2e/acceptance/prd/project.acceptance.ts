// PRD §7 milestone B, "project": a Project of three linked screens, a comment
// on one element, a new version, sharing a specific version with a reader and
// restoring an older version as a new one (survives reload).
//
// Quick mode drives the same product paths without a model: the owner creates
// the three screens and answers the comment through the public API, as the
// agent's tools would. Full mode adds the real seeded agent: it creates the
// screens from a chat message and publishes v2 from an element comment.
// The "agent creates ... publishes v2" test is the chat-first path
// (B.project.from-chat): it starts in the browser chat, the person opens the
// Project in /artifacts, comments there and the agent answers with a new
// version. The quick "element comment" test creates its Project through the
// API and does not start in the chat. Concurrent saves and the server restart
// are in project-rows.acceptance.ts.
import { loginStorageState } from "./auth"
import { expect, prdRequirement, test } from "./fixtures"
import {
  HERO, SCREENS, commentOnHero, createProject, deletePage, enableProjects, grantRead, heroOf, historyRow, linkedScreens, openHistory,
  FatalWait, openProject, pinWorkspace, preview, projectSlug, publish, reply, snapshot, threeScreens, trySnapshot, waitFor, wsQuery,
} from "./project/project-api"
import { trackPage } from "./created"

const V1 = "Welcome to the PRD cafe"
const V2 = "Welcome to the PRD cafe, open 8-18"

/** Every HTML screen links to both others, and the entry screen carries the hero element. */
function expectThreeLinkedScreens(snap: Awaited<ReturnType<typeof snapshot>>) {
  const links = linkedScreens(snap)
  expect(Object.keys(links).sort(), "exactly the three screens").toEqual([...SCREENS].sort())
  expect(snap.manifest.entrypoint).toBe("index.html")
  for (const screen of SCREENS) {
    for (const other of SCREENS.filter((s) => s !== screen)) {
      expect(links[screen], `${screen} links to ${other}`).toContain(other)
    }
  }
  const index = snap.manifest.files.find((f) => f.path === "index.html")?.content ?? ""
  expect(index, "index.html has the hero element with a stable id").toContain(HERO.slice(1))
  return links
}

test.describe("project", () => {
  test(
    "element comment is answered by a new version",
    {
      annotation: [
        prdRequirement("B.project.element-comment", "A comment is anchored to the selected element and answered by a new version"),
      ],
    },
    async ({ prd, page, request, context }) => {
      const ws = await prd.qaWorkspace()
      await enableProjects(request, ws.id)
      const slug = projectSlug()
      const v1 = await createProject(request, ws.id, slug, threeScreens(V1))
      const links = expectThreeLinkedScreens(v1)
      await pinWorkspace(context, ws.id)

      await openProject(page, ws.id, slug)
      const frame = preview(page)
      await expect(frame.locator(HERO)).toHaveText(V1)
      // The screens navigate inside the preview through their own links.
      await frame.getByRole("link", { name: "Menu" }).first().click()
      await expect(frame.locator("#artifact-menu-heading")).toBeVisible()
      await frame.getByRole("link", { name: "Contact" }).first().click()
      await expect(frame.locator("#artifact-contact-heading")).toBeVisible()
      await frame.getByRole("link", { name: "Home" }).first().click()
      await expect(frame.locator(HERO)).toHaveText(V1)

      // Comment on the exact element. Without a source chat the comment is saved
      // for the agent; full mode sends it to a real agent (see the agent test).
      const text = `Change this heading to "${V2}"`
      const anchor = await commentOnHero(page, text, 1)
      const saved = page.waitForResponse((r) => r.url().includes("/artifact/comments") && r.request().method() === "POST")
      const form = page.getByRole("form", { name: "New comment" })
      await form.getByRole("button", { name: /^(Save comment|Add to queue|Send to agent)$/ }).first().click()
      const savedRes = await saved
      expect(savedRes.status(), await savedRes.text()).toBe(201)
      const comment = (await savedRes.json()) as { id: string }

      const afterComment = await snapshot(request, ws.id, slug)
      const stored = afterComment.comments.find((c) => c.id === comment.id)
      expect(stored, "the comment is stored on the project").toBeTruthy()
      expect(stored).toMatchObject({ version: 1, path: "index.html", selector: HERO, text })
      expect(stored?.quote ?? "").toContain(V1)

      // The new version answers the comment (quick mode: as the agent's tools would).
      const res = await publish(request, ws.id, slug, 1, threeScreens(V2), "Address the heading comment")
      expect(res.status(), await res.text()).toBe(200)
      await reply(request, ws.id, slug, comment.id, 2, "Updated the heading in v2.")
      // A comment pinned to an element of an older version is refused once the
      // project moved on, instead of landing on a different element.
      const stale = await request.post(`/api/v1/pages/${encodeURIComponent(slug)}/artifact/comments?${wsQuery(ws.id)}`, {
        data: { version: 1, path: "index.html", selector: HERO, quote: V1, text: "stale target", send_to_agent: false },
      })
      expect(stale.status(), "a comment on a stale element is a conflict").toBe(409)

      await page.reload()
      await expect(preview(page).locator(HERO)).toHaveText(V2, { timeout: 30_000 })
      const v2 = await snapshot(request, ws.id, slug)
      expect(v2.latest_version).toBe(2)
      expect(heroOf(v2)).toBe(V2)
      expect(v2.comments.find((c) => c.id === comment.id)?.replies?.map((r) => r.version)).toContain(2)

      await prd.evidence("result.json", {
        mode: prd.mode,
        appSha: prd.appSha,
        workspace: ws.id,
        slug,
        screens: links,
        anchor,
        comment: { id: comment.id, version: stored?.version, selector: stored?.selector, quote: stored?.quote },
        staleCommentStatus: stale.status(),
        v2: { latest: v2.latest_version, hero: heroOf(v2), replies: v2.comments.find((c) => c.id === comment.id)?.replies },
      })
      await page.screenshot({ path: test.info().outputPath("v2.png"), fullPage: true })
    },
  )

  test(
    "a shared version stays pinned and read-only for the reader",
    { annotation: prdRequirement("B.project.share-version", "Sharing pins a specific version; the reader sees it and cannot write") },
    async ({ prd, page, request, context }) => {
      const ws = await prd.qaWorkspace()
      await enableProjects(request, ws.id)
      const slug = projectSlug()
      await createProject(request, ws.id, slug, threeScreens(V1))
      expect((await publish(request, ws.id, slug, 1, threeScreens(V2), "v2")).status()).toBe(200)
      const reader = await prd.provision("VIEWER", "PRD reader")
      await pinWorkspace(context, ws.id)

      // The owner views v1 from the history and shares exactly that version.
      await openProject(page, ws.id, slug)
      await openHistory(page)
      await historyRow(page, 1).getByRole("button", { name: "View" }).click()
      await page.getByRole("tab", { name: "Preview" }).click()
      await expect(preview(page).locator(HERO)).toHaveText(V1)
      // The viewer's own Share pins a version; the library header's Share manages people.
      await page.locator('section[aria-label="Artifact viewer"]').getByRole("button", { name: "Share", exact: true }).click()
      const shared = page.waitForResponse((r) => r.url().includes("/artifact/share") && r.request().method() === "POST")
      await page.getByRole("button", { name: "Share v1", exact: true }).click()
      const sharedRes = await shared
      expect(sharedRes.status(), await sharedRes.text()).toBe(200)
      expect(((await sharedRes.json()) as { shared_version: number }).shared_version).toBe(1)
      await grantRead(request, ws.id, slug, reader.userId)
      const owner = await snapshot(request, ws.id, slug)
      expect({ latest: owner.latest_version, shared: owner.shared_version }).toEqual({ latest: 2, shared: 1 })

      // The reader sees v1 only and has no write controls.
      const readerCtx = await prd.browserContext(await loginStorageState(prd.origin, reader.account))
      await pinWorkspace(readerCtx, ws.id)
      const readerPage = await readerCtx.newPage()
      await openProject(readerPage, ws.id, slug)
      await expect(preview(readerPage).locator(HERO)).toHaveText(V1)
      for (const name of ["Select", "Restore as new version", "Send to agent", /^Share v\d+$/]) {
        await expect(readerPage.getByRole("button", { name, exact: typeof name === "string" })).toHaveCount(0)
      }
      await readerPage.screenshot({ path: test.info().outputPath("reader.png"), fullPage: true })

      const api = readerCtx.request
      const read = await snapshot(api, ws.id, slug)
      expect(read.versions.map((v) => v.version), "the reader lists only the shared version").toEqual([1])
      expect(read.can_write ?? false).toBe(false)
      expect(heroOf(read)).toBe(V1)
      const base = `/api/v1/pages/${encodeURIComponent(slug)}`
      const q = wsQuery(ws.id)
      const attempts = {
        readV2: (await api.get(`${base}/artifact?${q}&version=2`)).status(),
        publish: (await api.post(`${base}/artifact/versions?${q}`, { data: { base_version: 2, entrypoint: "index.html", files: threeScreens("reader write"), message: "reader" } })).status(),
        restore: (await api.post(`${base}/artifact/restore?${q}`, { data: { base_version: 2, version: 1 } })).status(),
        share: (await api.post(`${base}/artifact/share?${q}`, { data: { base_version: 2, version: 2 } })).status(),
        comment: (await api.post(`${base}/artifact/comments?${q}`, { data: { version: 1, path: "index.html", selector: HERO, quote: V1, text: "reader", send_to_agent: false } })).status(),
        grant: (await api.put(`${base}/grants?${q}`, { data: { subject_type: "workspace", level: "write" } })).status(),
      }
      for (const [what, status] of Object.entries(attempts)) expect([403, 404], `reader ${what} → ${status}`).toContain(status)
      const after = await snapshot(request, ws.id, slug)
      expect({ latest: after.latest_version, shared: after.shared_version, hero: heroOf(after) }).toEqual({ latest: 2, shared: 1, hero: V2 })

      await prd.evidence("result.json", {
        mode: prd.mode,
        appSha: prd.appSha,
        workspace: ws.id,
        slug,
        owner: { latest: owner.latest_version, shared: owner.shared_version },
        reader: { versions: read.versions.map((v) => v.version), canWrite: read.can_write ?? false, hero: heroOf(read) },
        readerAttempts: attempts,
      })
    },
  )

  test(
    "restoring an older version makes a new version that survives reload",
    { annotation: prdRequirement("B.project.restore", "Restoring an old version creates a new version, deletes nothing and survives reload") },
    async ({ prd, page, request, context }) => {
      const ws = await prd.qaWorkspace()
      await enableProjects(request, ws.id)
      const slug = projectSlug()
      await createProject(request, ws.id, slug, threeScreens(V1))
      expect((await publish(request, ws.id, slug, 1, threeScreens(V2), "v2")).status()).toBe(200)
      await pinWorkspace(context, ws.id)

      await openProject(page, ws.id, slug)
      await expect(preview(page).locator(HERO)).toHaveText(V2)
      await openHistory(page)
      const restored = page.waitForResponse((r) => r.url().includes("/artifact/restore") && r.request().method() === "POST")
      await historyRow(page, 1).getByRole("button", { name: "Restore as new version" }).click()
      const restoredRes = await restored
      expect(restoredRes.status(), await restoredRes.text()).toBe(200)
      await expect(page.getByText(/Restored v1 as v3/)).toBeVisible()

      const after = await snapshot(request, ws.id, slug)
      expect(after.latest_version).toBe(3)
      expect(after.versions.map((v) => v.version).sort()).toEqual([1, 2, 3])
      expect(after.versions.find((v) => v.version === 3)?.restored_from).toBe(1)
      expect(heroOf(after)).toBe(V1)
      // v2 is still there, unchanged.
      expect(heroOf(await snapshot(request, ws.id, slug, 2))).toBe(V2)

      // A writer still holding v2 as its base gets a conflict, not an overwrite.
      const stale = await publish(request, ws.id, slug, 2, threeScreens("stale writer"), "stale")
      expect(stale.status(), "a publish on a stale base is a conflict").toBe(409)

      await page.reload()
      await openHistory(page)
      await expect(historyRow(page, 3)).toContainText("Restored from v1")
      await expect(historyRow(page, 2)).toBeVisible()
      await expect(historyRow(page, 1)).toBeVisible()
      await page.getByRole("tab", { name: "Preview" }).click()
      await expect(preview(page).locator(HERO)).toHaveText(V1)
      await page.screenshot({ path: test.info().outputPath("restored.png"), fullPage: true })
      const reloaded = await snapshot(request, ws.id, slug)
      expect(reloaded.latest_version).toBe(3)

      await prd.evidence("result.json", {
        mode: prd.mode,
        appSha: prd.appSha,
        workspace: ws.id,
        slug,
        versions: reloaded.versions.map((v) => ({ version: v.version, restored_from: v.restored_from ?? null })),
        hero: heroOf(reloaded),
        stalePublishStatus: stale.status(),
        restart: "not covered: the suite never restarts a deployment",
      })
    },
  )

  test(
    "agent creates three screens and publishes v2 from an element comment",
    {
      annotation: [
        prdRequirement("B.project.agent-screens", "A real agent creates a Project with three linked screens from chat"),
        prdRequirement("B.project.agent-version", "A comment on an element sends the agent to publish a new version"),
        prdRequirement("B.project.agent-restore", "The agent's earlier version can be restored as a new version after reload"),
        prdRequirement("B.project.from-chat", "From the browser chat: the person asks the agent for 3 screens, opens the Project in Artifacts, comments on an element, the agent publishes a new version"),
      ],
    },
    async ({ prd, page, request, context }) => {
      const agent = prd.requireAgent()
      // The agent works only in its own workspace, so its project lives there.
      // The test adds no members and deletes the project afterwards.
      const wsId = agent.workspaceId
      const slug = projectSlug("prd-agent-project")
      const token = slug.slice(-6)
      const evidence: Record<string, unknown> = { mode: prd.mode, appSha: prd.appSha, agent: agent.slug, slug }
      // The agent creates this project; remove it even if the test fails.
      trackPage(wsId, slug)
      try {
        const list = await request.get(`/api/v1/agents?${wsQuery(wsId)}`)
        expect(list.status()).toBe(200)
        const raw = (await list.json()) as unknown
        const agents = (Array.isArray(raw) ? raw : ((raw as { agents?: unknown[] }).agents ?? [])) as Array<{ id?: string; slug?: string; name?: string }>
        const seeded = agents.find((a) => a.slug === agent.slug)
        const name = seeded?.name
        expect(name, `agent ${agent.slug} in PRD_AGENT_WORKSPACE_ID`).toBeTruthy()
        // Only runs this test started count: the agent workspace is shared, and an
        // unrelated run of the same agent may stay RUNNING for days. A run without
        // a creation time still counts.
        let runsSince = Number.POSITIVE_INFINITY
        const runningRuns = async () => {
          const res = await request.get(`/api/v1/agents/${encodeURIComponent(seeded?.id ?? "")}/runs?${wsQuery(wsId)}`)
          expect(res.status(), "agent runs").toBe(200)
          const runs = (await res.json()) as Array<{ id: string; status: string; chat_id?: string | null; created_at?: string }>
          return runs.filter((r) => (r.status === "RUNNING" || r.status === "PENDING" || r.status === "QUEUED") && !(Date.parse(r.created_at ?? "") < runsSince))
        }
        await pinWorkspace(context, wsId)

        // 1. A chat message with "Create artifact project" on.
        await page.goto(`/chat?agent=${encodeURIComponent(agent.slug)}&${wsQuery(wsId)}`)
        await page.getByRole("button", { name: `New session with ${name}` }).first().click()
        // With NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS the composer has a "+ > New artifact"
        // menu and a removable "New Project" chip; without it the old toggle button.
        // Detect which one this build renders instead of assuming.
        const toggle = page.getByRole("button", { name: "Create artifact project" })
        const menu = page.getByRole("button", { name: "New artifact", exact: true })
        const chip = page.getByRole("button", { name: "Remove New Project" })
        const control = await waitFor("a project creation control in the composer", 60_000, 1_000, async () => {
          if (await menu.isVisible()) return "menu" as const
          if (await toggle.isVisible()) return "toggle" as const
          return null
        })
        evidence.composerControl = control
        await expect(control === "menu" ? menu : toggle).toBeEnabled({ timeout: 60_000 })
        const selectProject = async () => {
          if (control === "toggle") {
            if ((await toggle.getAttribute("aria-pressed")) !== "true") await toggle.click()
            await expect(toggle).toHaveAttribute("aria-pressed", "true")
            return
          }
          if (!(await chip.isVisible())) {
            await menu.click()
            const panel = page.getByRole("dialog", { name: "New artifact" })
            await panel.getByRole("button", { name: /^Project/ }).first().click()
            await panel.getByRole("list", { name: "Project starter prompts" }).getByRole("button").first().click()
          }
          // The consent chip: private project, sharing needs approval.
          await expect(chip).toBeVisible()
          await expect(page.getByText("Private project · Sharing requires your approval")).toBeVisible()
        }
        const prompt =
          `Create an artifact Project named "PRD project ${token}" with slug ${slug} and exactly three linked HTML screens: ` +
          `index.html (entrypoint), menu.html and kontakt.html. Every screen has a <nav> with relative links to the other two screens. ` +
          `index.html: <h1 id="artifact-hero-heading">${V1}</h1>; menu.html: a list of three coffees with prices; kontakt.html: an address and an email. ` +
          `Give the main elements stable artifact- IDs. Publish it and reply with the slug and version number.`
        const sentAt = new Date().toISOString()
        // A minute of slack for the server clock.
        runsSince = Date.parse(sentAt) - 60_000
        // The agent may still be busy with an earlier run; try again for a few minutes.
        for (let attempt = 0; ; attempt++) {
          await selectProject()
          await page.locator("textarea").last().fill(prompt)
          const busyBefore = await page.getByText("The agent is busy with another run right now").count()
          await page.getByRole("button", { name: "Submit" }).click()
          await page.waitForTimeout(6_000)
          if ((await page.getByText("The agent is busy with another run right now").count()) <= busyBefore) break
          if (attempt >= 8) throw new Error("the agent stayed busy for 4 minutes")
          await page.waitForTimeout(25_000)
        }
        evidence.chatUrl = new URL(page.url()).pathname + new URL(page.url()).search
        evidence.sentAt = sentAt

        const v1 = await waitFor("the agent's project v1", 10 * 60_000, 10_000, async () => {
          const snap = await trySnapshot(request, wsId, slug)
          return snap && snap.latest_version >= 1 ? snap : null
        })
        evidence.v1 = { at: new Date().toISOString(), latest: v1.latest_version, screens: linkedScreens(v1), hero: heroOf(v1) }
        expectThreeLinkedScreens(v1)
        expect(v1.can_send_to_agent, "the project made from chat can send comments back to its agent").toBe(true)

        // The creation run ends with the agent's reply; a person comments after
        // reading it, so the comment does not race the agent's own first run.
        await waitFor("the agent's creation run to finish", 5 * 60_000, 5_000, async () => ((await runningRuns()).length === 0 ? true : null))
        evidence.agentIdleAt = new Date().toISOString()

        // 2. A comment on the hero element, sent to the agent from the library.
        await openProject(page, wsId, slug)
        const text = `Change this heading to exactly "${V2} ${token}" and keep everything else.`
        const anchor = await commentOnHero(page, text, v1.latest_version)
        const saved = page.waitForResponse((r) => r.url().includes("/artifact/comments") && r.request().method() === "POST")
        await page.getByRole("form", { name: "New comment" }).getByRole("button", { name: "Send to agent" }).click()
        const savedRes = await saved
        expect(savedRes.status(), await savedRes.text()).toBe(201)
        const comment = (await savedRes.json()) as { id: string; task_id?: string }
        expect(comment.task_id, "the comment enters the agent's queue").toBeTruthy()
        evidence.comment = { id: comment.id, anchor, at: new Date().toISOString() }

        // 3. The agent publishes v2 and answers the comment. "The agent did not
        // start" while the agent is idle is a product failure, not a wait.
        const notStarted = page.getByText("The agent did not start")
        const v2 = await waitFor("the agent's v2 and reply", 10 * 60_000, 10_000, async () => {
          if (await notStarted.isVisible()) {
            evidence.dispatch = { notStarted: true, at: new Date().toISOString(), running: (await runningRuns()).map((r) => r.id) }
            throw new FatalWait("the comment was saved but the UI reports: The agent did not start")
          }
          const snap = await snapshot(request, wsId, slug)
          const answered = snap.comments.find((c) => c.id === comment.id)?.replies?.length
          return snap.latest_version > v1.latest_version && answered ? snap : null
        })
        evidence.v2 = {
          at: new Date().toISOString(),
          latest: v2.latest_version,
          hero: heroOf(v2),
          replies: v2.comments.find((c) => c.id === comment.id)?.replies,
        }
        expect(heroOf(v2) ?? "", "v2 carries the requested heading").toContain(token)
        expectThreeLinkedScreens(v2)
        await page.reload()
        await expect(preview(page).locator(HERO)).toContainText(token, { timeout: 60_000 })

        // 4. Restore the agent's v1 as a new version; it survives reload.
        await openHistory(page)
        const restored = page.waitForResponse((r) => r.url().includes("/artifact/restore") && r.request().method() === "POST")
        await historyRow(page, v1.latest_version).getByRole("button", { name: "Restore as new version" }).click()
        expect((await restored).status()).toBe(200)
        await page.reload()
        await openHistory(page)
        const after = await snapshot(request, wsId, slug)
        const newest = after.versions.find((v) => v.version === after.latest_version)
        expect(after.latest_version).toBe(v2.latest_version + 1)
        expect(newest?.restored_from).toBe(v1.latest_version)
        expect(heroOf(after)).toBe(heroOf(v1))
        await expect(historyRow(page, after.latest_version)).toContainText(`Restored from v${v1.latest_version}`)
        await page.getByRole("tab", { name: "Preview" }).click()
        await expect(preview(page).locator(HERO)).toHaveText(heroOf(v1) ?? V1)
        evidence.restore = { latest: after.latest_version, restored_from: newest?.restored_from, versions: after.versions.map((v) => v.version) }
        await page.screenshot({ path: test.info().outputPath("agent-restored.png"), fullPage: true })
      } finally {
        await prd.evidence("result.json", evidence)
        if (process.env.PRD_KEEP_WORKSPACES !== "1") {
          const status = await deletePage(request, wsId, slug).catch(() => 0)
          if (status >= 300 && status !== 404) console.warn(`[prd] project ${slug} not deleted (HTTP ${status})`)
        }
      }
    },
  )
})
