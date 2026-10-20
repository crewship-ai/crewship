import { test, expect, type Page, type Route } from "@playwright/test"

// Chat <-> artifacts context in a real browser, against a real backend for the
// chat itself. The artifact endpoints are a PROPOSED contract that no backend
// serves yet (FE-REQ-F2-1, -3-UPDATE, -4, -5-UPDATE, -6), so those, and the
// pages they point at, are answered with page.route. Everything else (login,
// workspace, agent, the chat row, the websocket send) is the real server.
//
// The surfaces exist only when the frontend was started with
// NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT=true. That is a build-time value, so the
// spec runs only when E2E_CHAT_ARTIFACT_CONTEXT=true says it was.
test.skip(process.env.E2E_CHAT_ARTIFACT_CONTEXT !== "true", "needs a frontend built with NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT=true")

const TIMEOUT = 20_000
const doc = (text: string) => ({ type: "doc", content: [{ type: "paragraph", attrs: { id: "p1" }, content: [{ type: "text", text }] }] })

interface Mock {
  /** Every URL the page requested for the artifact contract, in order. */
  requests: string[]
  previewStatus: number
  checkpointStatus: number
  served: boolean
  restrictedCard: boolean
}

async function mockContract(page: Page, options: Partial<Mock> = {}): Promise<Mock> {
  const mock: Mock = { requests: [], previewStatus: 200, checkpointStatus: 200, served: true, restrictedCard: false, ...options }
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })
  await page.route("**/api/v1/**", async route => {
    const url = new URL(route.request().url())
    const path = decodeURIComponent(url.pathname)
    const contract = /\/chats\/[^/]+\/(work-cards|links|library|artifact-context\/preview)/.test(path)
    const mockedPage = /^\/api\/v1\/pages\/@page-[ab](\/|$)/.test(path)
    if (!contract && !mockedPage) return route.fallback()
    mock.requests.push(path + url.search)
    if (contract && !mock.served) return route.fulfill({ status: 404, contentType: "text/plain", body: "404 page not found" })
    if (path.endsWith("/work-cards")) {
      return json(route, { cards: [{
        id: "work-1", run_state: "applied", saved: { revision: 2, checkpoint_id: "cp-2" }, freshness: "current", problem: null, page_id: "page-a",
        access: mock.restrictedCard ? "restricted" : { title: "Quarterly plan", type: "Document" }, executor: "Riley", on_behalf_of_user_id: "someone-else",
        can: { open: true, retry: true, cancel: true, compare: true },
      }], cursor: "c1" })
    }
    if (path.endsWith("/links")) {
      return json(route, { links: [
        { link_id: "l1", kind: "artifact", page_id: "page-a", revision: 2, checkpoint_id: "cp-2", name: "Quarterly plan", accessible: true },
        { link_id: "l2", kind: "artifact", page_id: "page-b", name: "Roadmap", accessible: true },
        { link_id: "l3", kind: "artifact", page_id: "page-c", accessible: false },
      ] })
    }
    if (path.endsWith("/library")) {
      return json(route, { items: [
        { link_id: "l1", kind: "artifact", page_id: "page-a", revision: 2, checkpoint_id: "cp-2", name: "Quarterly plan" },
        { link_id: "l2", kind: "artifact", page_id: "page-b", name: "Roadmap" },
        { link_id: "l9", restricted: true },
      ] })
    }
    if (path.endsWith("/artifact-context/preview")) {
      if (mock.previewStatus !== 200) return json(route, { error: "not found" }, mock.previewStatus)
      return json(route, { create: { audience: { kind: "personal" }, result_type: "Document" },
        update: { audience: { kind: "folder", name: "Finance", people: 4 }, result_type: "Document", expected: { schema: 2, rev: 7, generation: 3 } } })
    }
    if (path.includes("/document/checkpoints/cp-2")) {
      if (mock.checkpointStatus !== 200) return json(route, { error: "not found" }, mock.checkpointStatus)
      return json(route, { page_id: "page-a", id: "cp-2", rev: 2, schema: 2, content: doc("Body at revision two") })
    }
    if (path.endsWith("/document")) {
      const id = path.includes("page-b") ? "page-b" : "page-a"
      return json(route, { page_id: id, schema: 2, rev: 7, content: doc(id === "page-b" ? "Roadmap body at latest" : "Body at latest") })
    }
    const id = path.includes("page-b") ? "page-b" : "page-a"
    return json(route, { id, name: id === "page-b" ? "Roadmap" : "Quarterly plan", has_document: true })
  })
  return mock
}

/** A real chat row for the demo agent, so the panel verifies read scope as it does for any existing conversation. */
async function openChat(page: Page) {
  await page.goto("/")
  const target = await page.evaluate(async () => {
    const workspaces = await (await fetch("/api/v1/workspaces")).json()
    for (const workspace of Array.isArray(workspaces) ? workspaces : []) {
      const agents = await (await fetch(`/api/v1/agents?workspace_id=${workspace.id}`)).json()
      const agent = (Array.isArray(agents) ? agents : []).find((item: { slug?: string; id?: string }) => item?.slug && item?.id)
      if (!agent) continue
      const sessionId = crypto.randomUUID()
      const created = await fetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspace.id}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ session_id: sessionId, origin: "UI" }) })
      if (created.ok) return { workspaceId: workspace.id as string, slug: agent.slug as string, sessionId }
    }
    return null
  })
  expect(target, "no workspace with an agent on this instance; seed one first").not.toBeNull()
  await page.evaluate(id => window.localStorage.setItem("crewship.workspaceId", id), target!.workspaceId)
  await page.goto(`/chat/${target!.slug}?session=${target!.sessionId}`)
}

function captureSends(page: Page) {
  const sent: { content?: string; metadata?: Record<string, unknown> }[] = []
  page.on("websocket", socket => socket.on("framesent", frame => {
    try {
      const envelope = JSON.parse(String(frame.payload))
      if (envelope.type !== "send_message") return
      sent.push(JSON.parse(envelope.payload))
    } catch { /* not a JSON frame */ }
  }))
  return sent
}

const noOverflow = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)
const composer = (page: Page) => page.getByRole("textbox").last()

for (const width of [1280, 390]) {
  test.describe(`chat artifact context at ${width}px`, () => {
    test.beforeEach(async ({ page }) => { await page.setViewportSize({ width, height: 900 }) })

    test("result card, exact revision, linked switch, composer target, send and library", async ({ page }) => {
      const sent = captureSends(page)
      const mock = await mockContract(page)
      await openChat(page)

      // 1. A work result card with its three separate facts.
      const card = page.getByRole("article", { name: "Quarterly plan" })
      await expect(card).toBeVisible({ timeout: TIMEOUT })
      await expect(card).toContainText("Saved revision 2")
      await expect(card).toContainText("On behalf of a human collaborator")
      await expect(card.getByRole("button", { name: "Compare" })).toHaveCount(0)

      // 2. Open the exact revision: through the checkpoint reader, never ?revision=.
      await card.getByRole("button", { name: "Open" }).click()
      const viewer = page.getByRole("region", { name: "Document preview" })
      await expect(viewer.getByText("Body at revision two")).toBeVisible({ timeout: TIMEOUT })
      await expect(viewer.getByTestId("viewer-target-label")).toHaveText("Revision 2")
      expect(mock.requests.some(request => request.includes("/document/checkpoints/cp-2"))).toBe(true)
      expect(mock.requests.some(request => request.includes("revision="))).toBe(false)
      expect(mock.requests.some(request => request.endsWith("/document") || request.includes("/document?"))).toBe(false)

      // 3. Switch the linked viewer to the other linked item. The inaccessible link is not offered.
      const switcher = viewer.getByLabel("Linked items")
      await expect(switcher.locator("option")).toHaveText(["Quarterly plan · Revision 2", "Roadmap · Latest"])
      await switcher.selectOption({ label: "Roadmap · Latest" })
      await expect(viewer.getByText("Roadmap body at latest")).toBeVisible({ timeout: TIMEOUT })
      await expect(viewer.getByTestId("viewer-target-label")).toHaveText("Latest")
      expect(await noOverflow(page)).toBe(true)

      // 4. Use it as the target of the next message. On a phone the viewer gives way to the composer.
      await viewer.getByRole("button", { name: "Use in message" }).click()
      const strip = page.getByRole("region", { name: "Artifact context" })
      await expect(strip).toContainText("Roadmap", { timeout: TIMEOUT })
      if (width < 600) await expect(page.getByRole("region", { name: "Document preview" })).toHaveCount(0)
      await expect(strip.getByText("Choose what the agent should do")).toBeVisible()
      await strip.getByLabel("Update attached").check()
      await expect(strip).toContainText("Saved to Finance. 4 people can see it.", { timeout: TIMEOUT })
      await expect(strip).toContainText("Executor")

      // 5. The send carries the target and the expected revision, once, with a stable submission id.
      await composer(page).fill("Add a risks section")
      await composer(page).press("Enter")
      await expect.poll(() => sent.length, { timeout: TIMEOUT }).toBe(1)
      expect(sent[0].content).toBe("Add a risks section")
      const submission = sent[0].metadata?.artifact_submission as { submission_id: string; edit_mode: string; update_page_id: string; context: { page_id: string; expected: unknown }[] }
      expect(submission).toMatchObject({ edit_mode: "update", update_page_id: "page-b", context: [{ page_id: "page-b", expected: { schema: 2, rev: 7, generation: 3 } }] })
      expect(submission.submission_id).toMatch(/^[0-9a-f-]{36}$/)
      await expect(page.getByRole("region", { name: "Artifact context" })).toHaveCount(0)

      // 6. The library lists what is linked, a restricted link without a name.
      await page.getByRole("button", { name: "Library", exact: true }).click()
      const library = page.getByRole("region", { name: "Conversation library" })
      await expect(library.getByRole("button", { name: "Roadmap" })).toBeVisible({ timeout: TIMEOUT })
      await expect(library.getByRole("button", { name: "Quarterly plan" })).toBeVisible()
      await expect(library.getByText("Restricted item")).toBeVisible()
      await expect(library.getByRole("button", { name: "All (3)" })).toBeVisible()
      await library.getByRole("button", { name: "Roadmap" }).click()
      await expect(page.getByRole("region", { name: "Document preview" }).getByText("Roadmap body at latest")).toBeVisible({ timeout: TIMEOUT })
      expect(await noOverflow(page)).toBe(true)
    })

    test("lost access: the revision is unavailable, the item leaves the composer and nothing is described", async ({ page }) => {
      const sent = captureSends(page)
      const mock = await mockContract(page, { checkpointStatus: 404, previewStatus: 404 })
      await openChat(page)
      await page.getByRole("article", { name: "Quarterly plan" }).getByRole("button", { name: "Open" }).click()
      const viewer = page.getByRole("region", { name: "Document preview" })
      await expect(viewer.getByText("This revision is not available.")).toBeVisible({ timeout: TIMEOUT })
      // A missing exact revision never shows the latest body instead.
      await expect(page.getByText("Body at latest")).toHaveCount(0)
      expect(mock.requests.some(request => request.endsWith("/document") || request.includes("/document?"))).toBe(false)

      // An unverified revision names nothing and cannot be attached.
      await expect(viewer.getByRole("button", { name: "Use in message" })).toHaveCount(0)
      await expect(viewer).not.toContainText("Quarterly plan")
      await expect(viewer).not.toContainText("Saved revision")

      // A readable item the server later answers 404 for leaves the composer.
      await viewer.getByRole("button", { name: width < 600 ? "Back to chat" : "Close document preview" }).click()
      await page.getByRole("button", { name: "Library", exact: true }).click()
      await page.getByRole("region", { name: "Conversation library" }).getByRole("button", { name: "Roadmap" }).click()
      await expect(page.getByRole("region", { name: "Document preview" }).getByText("Roadmap body at latest")).toBeVisible({ timeout: TIMEOUT })
      await page.getByRole("region", { name: "Document preview" }).getByRole("button", { name: "Use in message" }).click()
      await expect.poll(() => mock.requests.some(request => request.includes("/artifact-context/preview")), { timeout: TIMEOUT }).toBe(true)
      await expect(page.getByRole("region", { name: "Artifact context" })).toHaveCount(0)

      // With the item gone a plain message goes out untouched.
      await composer(page).fill("Never mind")
      await composer(page).press("Enter")
      await expect.poll(() => sent.length, { timeout: TIMEOUT }).toBe(1)
      expect(sent[0].metadata?.artifact_submission).toBeUndefined()
    })

    test("a restricted card shows no name, type or executor", async ({ page }) => {
      await mockContract(page, { restrictedCard: true })
      await openChat(page)
      const card = page.getByRole("article", { name: "Restricted item" })
      await expect(card).toBeVisible({ timeout: TIMEOUT })
      await expect(card).not.toContainText("Quarterly plan")
      await expect(card).not.toContainText("Riley")
      await expect(card.getByRole("button", { name: "Open" })).toHaveCount(0)
    })

    test("endpoints that are not served: unavailable library, no cards, chat unchanged", async ({ page }) => {
      const sent = captureSends(page)
      await mockContract(page, { served: false })
      await openChat(page)
      await expect(composer(page)).toBeVisible({ timeout: TIMEOUT })
      await expect(page.getByRole("article")).toHaveCount(0)
      await page.getByRole("button", { name: "Library", exact: true }).click()
      await expect(page.getByText(/The library could not be refreshed/)).toBeVisible({ timeout: TIMEOUT })
      await expect(page.getByRole("button", { name: "Try again" })).toBeVisible()
      await composer(page).fill("Hello")
      await composer(page).press("Enter")
      await expect.poll(() => sent.length, { timeout: TIMEOUT }).toBe(1)
      expect(sent[0].metadata?.artifact_submission).toBeUndefined()
      expect(await noOverflow(page)).toBe(true)
    })
  })
}
