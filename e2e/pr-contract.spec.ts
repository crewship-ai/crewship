import { test, expect } from "./fixtures/auth"

// One browser context is intentional. NextAuth rotates the session cookie on
// some authenticated mutations; six independent contexts would reuse the
// global-setup snapshot and eventually redirect to /login (or hit the login
// rate limit if each spec tried to repair that itself).
test("PR browser contract subset", async ({ page }) => {
  await test.step("login flow", async () => {
    await page.goto("/crews")
    await expect(page).toHaveURL(/\/crews/)
    await expect(page.getByRole("heading", { name: "Your fleet" })).toBeVisible()
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
    await page.getByRole("button", { name: /Empty crew/ }).click()
    // Two Continues, not three. Lineup lands on Container now — the Runtime
    // step it used to pass through folded into it.
    await page.getByRole("button", { name: /Continue/ }).click()
    await page.getByRole("button", { name: /Skip to defaults/ }).click()
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
})
