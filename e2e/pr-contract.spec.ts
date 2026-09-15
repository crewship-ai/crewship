import { devices } from "@playwright/test"

import { test, expect } from "./fixtures/auth"
import { incomingWebhookFlow } from "./incoming-webhook-flow"

// One browser context is intentional. NextAuth rotates the session cookie on
// some authenticated mutations; six independent contexts would reuse the
// global-setup snapshot and eventually redirect to /login (or hit the login
// rate limit if each spec tried to repair that itself).
test("PR browser contract subset", async ({ page }) => {
  // Includes the real HTTP Incoming lifecycle and its disposable routine setup.
  test.setTimeout(90_000)
  await test.step("login flow", async () => {
    await page.goto("/crews")
    await expect(page).toHaveURL(/\/crews/)
    // A cold embedded export hydrates after the workspace request. The CI trace
    // showed the correct heading just after the former five-second deadline.
    await expect(page.getByRole("heading", { name: "Crews & agents", exact: true })).toBeVisible({ timeout: 20_000 })
  })

  await test.step("agent create dialog is reachable", async () => {
    await page.goto("/crews")
    await page.getByRole("button", { name: /^Agent$/ }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await expect(page.getByText(/^New agent$/)).toBeVisible()
    await page.getByRole("button", { name: /Cancel|Close/i }).first().click()
  })

  await test.step("create crew via wizard", async () => {
    const slug = `e2e-crew-${Date.now().toString(36)}`
    await page.goto("/crews")
    await page.getByRole("button", { name: /^Crew$/ }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await page.getByPlaceholder("Engineering", { exact: true }).fill(`E2E Crew ${slug}`)
    await page.getByPlaceholder("engineering", { exact: true }).fill(slug)
    await page.getByRole("button", { name: /Continue/ }).click()
    await page.getByRole("button", { name: /Start empty/ }).click()
    // Purpose → team → review. Environment settings are optional in review.
    await page.getByRole("button", { name: /Continue/ }).click()
    await page.getByRole("button", { name: /Create crew/ }).click()
    await expect(page.getByRole("dialog")).not.toBeVisible()

    const workspaces = await (await page.request.get("/api/v1/workspaces")).json()
    const workspaceID = Array.isArray(workspaces) ? workspaces[0]?.id : workspaces.id
    const crews = await (await page.request.get(`/api/v1/crews?workspace_id=${workspaceID}`)).json()
    const created = crews.find((crew: { id?: string; slug?: string }) => crew.slug === slug)
    expect(created?.id).toBeTruthy()
    // The server is disposable for this gate. Keep the created crew alive for
    // the following issue-create step; deleting it here can leave the UI's
    // workspace roster stale while the next modal is opening.
  })

  await test.step("cancel create-crew dialog", async () => {
    await page.goto("/crews")
    await page.getByRole("button", { name: /^Crew$/ }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await page.getByRole("button", { name: /Cancel|Close/i }).first().click()
    await expect(page.getByRole("dialog")).not.toBeVisible()
  })

  await test.step("create issue and remove the fixture", async () => {
    const title = `E2E issue ${Date.now()}`
    await page.goto("/issues")
    await page.getByRole("button", { name: "New Issue", exact: true }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
    // Selecting explicitly avoids relying on the modal's asynchronous
    // auto-select when the workspace roster is still loading.
    await page.getByRole("dialog").getByRole("button").first().click()
    await page.getByRole("option", { name: "Engineering", exact: true }).click()
    await page.getByPlaceholder("Issue title").fill(title)
    const createIssue = page.getByRole("button", { name: "Create issue", exact: true })
    await expect(createIssue).toBeEnabled({ timeout: 15_000 })
    const createResponse = page.waitForResponse(
      (response) => response.request().method() === "POST" && /\/issues(?:\?|$)/.test(response.url()),
    )
    await createIssue.click()
    const issueResponse = await createResponse
    expect(issueResponse.status(), await issueResponse.text()).toBe(201)
    const workspaces = await (await page.request.get("/api/v1/workspaces")).json()
    const workspaceID = Array.isArray(workspaces) ? workspaces[0]?.id : workspaces.id
    await expect.poll(async () => {
      const data = await (await page.request.get(`/api/v1/issues?workspace_id=${workspaceID}`)).json()
      const rows = Array.isArray(data) ? data : data.rows ?? data.data ?? []
      return rows.find((issue: { title?: string; identifier?: string }) => issue.title === title)?.identifier ?? ""
    }, { timeout: 10_000 }).not.toBe("")
    const data = await (await page.request.get(`/api/v1/issues?workspace_id=${workspaceID}`)).json()
    const rows = Array.isArray(data) ? data : data.rows ?? data.data ?? []
    const identifier = rows.find((issue: { title?: string; identifier?: string }) => issue.title === title)?.identifier
    expect(identifier).toBeTruthy()
    await page.request.delete(
      `/api/v1/issues/${encodeURIComponent(identifier)}?workspace_id=${encodeURIComponent(workspaceID)}`,
    )
  })

  // #2398 / #2403 — a run_needs_human card is acted on from /inbox, against
  // the real server, and the receipt is read back off the issue.
  //
  // The card is written only when a run reports outcome NEEDS_HUMAN, which
  // needs a provider-backed agent this gate does not have (see the header of
  // playwright.pr.config.ts). Until #2403 this step route-mocked the inbox
  // API and could prove only the web half of the contract. Now the server
  // is started with CREWSHIP_E2E_FIXTURES=1 (ci.yml, the browser server
  // step), which registers a seed door — POST /api/v1/e2e/fixtures/
  // run-needs-human — that plants what a NEEDS_HUMAN run leaves behind (the
  // session parked awaiting input, the run that reported it) and raises the
  // card through the same producer the real path uses. Nothing here is
  // mocked: the list, the act door and the issue's event log are all the
  // real server's. What is NOT proven is that a NEEDS_HUMAN run raises the
  // card in the first place — that is B6's Go test, not a browser concern.
  await test.step("inbox: answer a run_needs_human card; the receipt lands on the issue's event log", async () => {
    const workspaces = await (await page.request.get("/api/v1/workspaces")).json()
    const workspaceID: string = Array.isArray(workspaces) ? workspaces[0]?.id : workspaces.id
    expect(workspaceID).toBeTruthy()
    const crews = await (await page.request.get(`/api/v1/crews?workspace_id=${workspaceID}`)).json()
    const engineering = crews.find((crew: { slug?: string }) => crew.slug === "engineering")
    expect(engineering?.id, "the seed's engineering crew").toBeTruthy()

    // An issue of its own, so the event log read at the end is unambiguous.
    const title = `E2E needs-human ${Date.now()}`
    const createResponse = await page.request.post(
      `/api/v1/crews/${engineering.id}/issues?workspace_id=${encodeURIComponent(workspaceID)}`,
      { data: { title, priority: "high" } },
    )
    expect(createResponse.status(), await createResponse.text()).toBe(201)
    const issue = await createResponse.json()
    const identifier: string = issue.identifier
    expect(identifier).toBeTruthy()

    // The seed door. A 404 here means the server was started without
    // CREWSHIP_E2E_FIXTURES — the route does not exist on a normal instance.
    const seeded = await page.request.post(
      `/api/v1/e2e/fixtures/run-needs-human?workspace_id=${encodeURIComponent(workspaceID)}`,
      { data: { issue_identifier: identifier, reason: "Which bucket should the export go to — staging or prod?" } },
    )
    expect(seeded.status(), await seeded.text()).toBe(201)
    const fixture = await seeded.json()
    const cardID: string = fixture.inbox_item_id
    expect(cardID).toBeTruthy()
    expect(fixture.session_id).toBeTruthy()

    await page.goto(`/inbox?item=${cardID}`)
    const pane = page.getByRole("main", { name: "Inbox detail" })
    await expect(pane.getByText(`needs your input on ${identifier}`)).toBeVisible()
    await expect(pane.getByText("Which bucket should the export go to — staging or prod?")).toBeVisible()
    // The §12 badge, and the three actions B6 puts on the card.
    await expect(pane.getByTestId("attention-badge")).toHaveText("Input needed")
    await expect(pane.getByRole("button", { name: "Take over" })).toBeVisible()
    await expect(pane.getByRole("button", { name: "Dismiss" })).toBeVisible()

    await pane.getByRole("button", { name: "Answer" }).click()
    const send = pane.getByRole("button", { name: "Send" })
    await expect(send).toBeDisabled()
    await pane.getByRole("textbox", { name: "Your answer" }).fill("Use the staging bucket, not prod.")
    await expect(send).toBeEnabled()
    const actResponse = page.waitForResponse(
      (response) => response.request().method() === "POST" && response.url().includes(`/api/v1/inbox/${cardID}/act`),
    )
    await send.click()
    const acted = await actResponse
    expect(acted.status(), await acted.text()).toBe(200)
    const receipt = (await acted.json()).receipt
    expect(acted.request().postDataJSON()).toEqual({ action: "answer", input: "Use the staging bucket, not prod." })
    // The answer reached the session that asked: a delivery, dispatched as a
    // new run, and a receipt with its position on the issue's event log.
    expect(receipt.session_id).toBe(fixture.session_id)
    expect(receipt.dispatch_state).toBe("dispatched")
    expect(receipt.run_id).toBeTruthy()
    expect(receipt.run_id).not.toBe(fixture.assignment_id)
    expect(receipt.seq).toBeGreaterThan(0)

    // Resolved in place, with the receipt — no navigation, no reload.
    const rec = pane.getByTestId("act-receipt")
    await expect(rec).toBeVisible()
    await expect(rec).toContainText(`run ${receipt.run_id}`)
    await expect(rec).toContainText(`event #${receipt.seq}`)
    await expect(pane.getByRole("button", { name: "Send" })).toHaveCount(0)
    await expect(pane.getByText(/^Resolved /)).toBeVisible()
    expect(page.url()).toContain("/inbox")

    // The same card, resolved on the server — not just in the pane.
    const after = await (await page.request.get(`/api/v1/inbox/${cardID}?workspace_id=${encodeURIComponent(workspaceID)}`)).json()
    expect(after.state).toBe("resolved")
    expect(after.resolved_action).toBe("answer")

    // The receipt on the issue's event log, read the way a person reads it:
    // the issue's History tab shows the inbox_acted row.
    await page.goto(`/issues/${encodeURIComponent(identifier)}`)
    await expect(page.getByRole("heading", { name: title })).toBeVisible({ timeout: 15_000 })
    await page.getByRole("button", { name: /^history$/i }).click()
    await expect(page.getByText(/inbox acted/)).toBeVisible()
    // And as the CLI reads it (`crewship issue events`): the same row, the
    // same seq the receipt named.
    const events = await (await page.request.get(
      `/api/v1/crews/${engineering.id}/issues/${encodeURIComponent(identifier)}/events?workspace_id=${encodeURIComponent(workspaceID)}&after_seq=0`,
    )).json()
    const rows = Array.isArray(events) ? events : events.events ?? events.rows ?? []
    const actedRow = rows.find((event: { action?: string }) => event.action === "inbox_acted")
    expect(actedRow, JSON.stringify(rows)).toBeTruthy()
    expect(actedRow.seq).toBe(receipt.seq)
  })

  await test.step("Incoming: create GitHub endpoint, reveal, overview, detail, disable and refresh (real HTTP)", async () => {
    await incomingWebhookFlow(page)
  })

})

// ── The phone contract ──────────────────────────────────────────────────────
//
// Nothing in the PR gate ran at a phone width before #2483, which is how a
// navigation missing seven of fifteen destinations, an Admin Console with no
// mobile layout, and a search field that made iOS zoom the page and stay
// zoomed all shipped green.
//
// A real device profile, not `setViewportSize`: the touch work in that change
// keys on `@media (pointer: coarse)`, and resizing a Desktop Chrome context
// leaves `hasTouch` false, so a width-only test would assert the 16px rule
// against a page where the rule never applied.
//
// The title carries the config's grep phrase so this runs without widening it.
// It is read-only — no mutations — so the second context cannot rotate the
// session cookie the way the header of the test above warns about.
test.describe("PR browser contract subset — phone", () => {
  // The profile minus `defaultBrowserType`: that one key forces a new worker,
  // which Playwright ≥1.5x refuses inside a describe, and the project is
  // Chromium already. Viewport, touch, scale and user agent all carry over.
  const { defaultBrowserType: _browser, ...iphone } = devices["iPhone 13"]
  test.use(iphone)

  test("PR browser contract subset — the phone contract", async ({ page }) => {
    await page.goto("/")
    await page.waitForLoadState("networkidle")

    // The rule the touch work is built on has to be in force, or everything
    // below tests a desktop page that happens to be narrow.
    expect(
      await page.evaluate(() => window.matchMedia("(pointer: coarse)").matches),
      "not a coarse pointer — the touch rules under test do not apply here",
    ).toBe(true)

    const sideways = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth + 1,
    )
    expect(sideways, "the page scrolls sideways at 390px").toBe(false)

    const tabs = page.getByRole("navigation", { name: "Primary" })
    await expect(tabs).toBeVisible()
    for (const label of ["Dashboard", "Inbox", "Chat", "More"]) {
      await expect(tabs.getByText(label, { exact: true })).toBeVisible()
    }

    // More offers every destination the rail carries — the property that drifted.
    await tabs.getByRole("button", { name: "More" }).click()
    const sheet = page.getByRole("dialog")
    await expect(sheet).toBeVisible()
    for (const label of ["Inbox", "Issues", "Routines", "Pages", "Activity", "Journal", "Integrations"]) {
      await expect(sheet.getByText(label, { exact: true })).toBeVisible()
    }

    // Back closes it in place rather than leaving the page underneath.
    const before = new URL(page.url()).pathname
    await page.goBack()
    await expect(sheet).toBeHidden()
    expect(new URL(page.url()).pathname, "back left the page instead of closing the sheet").toBe(before)

    // Nothing you can type into renders under 16px, which is what makes iOS
    // zoom the page on focus and never zoom back out.
    await page.goto("/settings")
    await page.waitForLoadState("networkidle")
    const small = await page.evaluate(() =>
      [...document.querySelectorAll<HTMLElement>(
        "input:not([type=checkbox]):not([type=radio]):not([type=range]):not([type=color]), textarea, select",
      )]
        .filter((el) => el.getBoundingClientRect().width > 0)
        .filter((el) => parseFloat(getComputedStyle(el).fontSize) < 16)
        .map((el) => el.getAttribute("aria-label") ?? el.getAttribute("placeholder") ?? el.tagName),
    )
    expect(small, "fields under 16px make iOS zoom the page and stay zoomed").toEqual([])

    await page.goto("/integrations?tab=incoming")
    await page.getByRole("button", { name: "Expand sidebar", exact: true }).click()
    await expect(page.getByRole("textbox", { name: "Search incoming targets" })).toBeVisible()
    await page.getByRole("textbox", { name: "Search incoming targets" }).fill("no-such-target-e2e")
    await expect(page.getByText("No matching targets. Create a routine, agent or Page first.")).toBeVisible()
    await page.getByRole("button", { name: "Close the integrations list" }).click({ position: { x: 330, y: 300 } })
    await expect(page.getByRole("button", { name: "Expand sidebar", exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)).toBe(false)

  })
})
