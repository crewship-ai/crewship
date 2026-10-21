import { test, expect } from "@playwright/test"

// Production static UI; synthetic HTTP responses only. Real Router authorization
// and CAS remain covered by the separate known-project acceptance harness.
// MEMBER is valid OrgRole; restricted_session controls Guest shell gating.
// Empty surfaces deliberately provide no List; the exact known-ID route is separate.
test.use({ storageState: { cookies: [], origins: [] }, serviceWorkers: "block" })
for (const width of [1280, 390]) {
  test(`PR browser contract subset — known project without List at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.addInitScript(() => localStorage.setItem("crewship.workspaceId:known-guest", "known-ws"))
    const base = "/api/v1/workspaces/known-ws", projectPath = `${base}/restricted-projects/known`
    let project = { id: "known", name: "Known client project", description: "Shared project", version: "a".repeat(64), can_manage: true }
    const milestone = { id: "milestone", project_id: "known", name: "Reviewed milestone", description: "", target_date: "", status: "active", position: 0, version: "c".repeat(64), can_manage: true, can_delete: true }
    let milestones: typeof milestone[] = [], denied = false, transient = false, staleDelete = true
    let deletionVersion = "d".repeat(64), foreignReads = 0
    const writes: { method: string; path: string; body: unknown }[] = [], unexpected: string[] = []
    await page.route("**/api/**", async route => {
      const request = route.request(), path = new URL(request.url()).pathname, method = request.method()
      let body: unknown = {}, status = 200
      const headers: Record<string, string> = { "Content-Type": "application/json", "Cache-Control": "no-store" }
      if (method === "GET" && path === "/api/auth/session") body = { user: { id: "known-guest", name: "Known Guest", email: "guest@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (method === "GET" && path === "/api/auth/csrf") body = { csrfToken: "synthetic-csrf" }
      else if (method === "GET" && path === "/api/v1/workspaces") body = [{ id: "known-ws", name: "Known workspace", slug: "known", currentUserRole: "MEMBER", currentUserAccessMode: "restricted", restricted_session: true, restricted_surfaces: [] }]
      else if (path === projectPath && method === "GET") { body = denied ? { error: "Project unavailable" } : project; status = denied ? (transient ? 500 : 404) : 200 }
      else if (path === projectPath && method === "PUT") {
        const data = request.postDataJSON()
        expect(data).toEqual({ name: project.name, description: "Reviewed project edit", version: project.version })
        writes.push({ method, path, body: data }); project = { ...project, description: data.description, version: "b".repeat(64) }; body = project
      } else if (path === `${projectPath}/milestones` && method === "GET") {
        body = milestones; headers["X-Total-Count"] = String(milestones.length); headers["X-Can-Manage"] = "true"
      } else if (path === `${projectPath}/milestones` && method === "POST") {
        const data = request.postDataJSON()
        expect(data).toEqual({ name: milestone.name, description: "", target_date: "", status: "active" })
        writes.push({ method, path, body: data }); milestones = [milestone]; body = milestone; status = 201
      } else if (path === `${base}/restricted-issues/known-issue/deletion` && method === "GET") {
        body = { id: "known-issue", identifier: "KNOWN-1", title: "Reviewed issue", project_id: "known", status: "BACKLOG", version: deletionVersion, can_delete: true }
      } else if (path === `${base}/restricted-issues/known-issue` && method === "DELETE") {
        const data = request.postDataJSON()
        expect(data).toEqual({ project_id: "known", version: deletionVersion, confirm: true })
        writes.push({ method, path, body: data })
        if (staleDelete) { staleDelete = false; deletionVersion = "e".repeat(64); status = 409; body = { error: "Issue changed" } }
        else { status = 204; body = null }
      } else if (path === `${base}/restricted-projects/foreign` && method === "GET") { foreignReads++; status = 404; body = { error: "Project unavailable" } }
      else { unexpected.push(`${method} ${path}`); status = 404; body = { error: "Unexpected discovery request" } }
      await route.fulfill({ status, headers, body: status === 204 ? "" : JSON.stringify(body) })
    })
    const link = "/issues/manage?project=known"
    await page.goto(link)
    const details = page.getByRole("region", { name: "Shared project details", exact: true })
    await expect(details.getByLabel("Project name", { exact: true })).toHaveValue(project.name)
    await expect(page.getByRole("link", { name: "Issues", exact: true })).toHaveCount(0)
    await expect(page.getByLabel("Shared project", { exact: true })).toHaveCount(0)
    await details.getByLabel("Project description", { exact: true }).fill("Reviewed project edit")
    await details.getByRole("button", { name: "Save project details", exact: true }).click()
    await expect.poll(() => writes.filter(write => write.method === "PUT").length).toBe(1)
    await expect(details.getByRole("button", { name: "Save project details", exact: true })).toBeEnabled()
    const milestonePanel = page.getByRole("region", { name: "Shared project milestones", exact: true })
    const create = milestonePanel.getByLabel("New milestone", { exact: true })
    await create.getByText("New milestone", { exact: true }).click()
    await create.getByLabel("Milestone name", { exact: true }).fill(milestone.name)
    await create.getByRole("button", { name: "Create milestone", exact: true }).click()
    await expect(milestonePanel.getByRole("article", { name: `Milestone ${milestone.name}`, exact: true })).toBeVisible()
    await expect.poll(() => writes.filter(write => write.method === "POST").length).toBe(1)
    await page.goto(`${link}&issue_id=known-issue`)
    await page.getByRole("button", { name: "Delete issue", exact: true }).click()
    const confirmation = page.getByRole("alertdialog", { name: "Delete KNOWN-1?", exact: true })
    await expect(confirmation.getByText("Reviewed issue", { exact: true })).toBeVisible()
    await confirmation.getByRole("button", { name: "Permanently delete issue", exact: true }).click()
    await expect(confirmation.getByRole("alert")).toContainText("changed")
    await expect(confirmation.getByRole("button", { name: "Permanently delete issue", exact: true })).toBeDisabled()
    await expect.poll(() => writes.filter(write => write.method === "DELETE").length).toBe(1)
    await confirmation.getByRole("button", { name: "Keep issue", exact: true }).click()
    await page.getByRole("button", { name: "Delete issue", exact: true }).click()
    await confirmation.getByRole("button", { name: "Permanently delete issue", exact: true }).click()
    await expect(page.getByText("Known issue deleted.", { exact: true })).toBeVisible()
    await expect(details.getByLabel("Project description", { exact: true })).toHaveValue("Reviewed project edit")
    await expect(page).toHaveURL(/\/issues\/manage\?project=known&issue_id=known-issue$/)
    denied = true
    await details.getByRole("button", { name: "Refresh project details", exact: true }).click()
    await expect(details.getByRole("alert")).toContainText("unavailable")
    await expect(page.getByLabel("Project name", { exact: true })).toHaveCount(0)
    await expect(milestonePanel).toHaveCount(0)
    await expect(page.getByRole("button", { name: "Delete issue", exact: true })).toHaveCount(0)
    transient = true
    await page.evaluate(() => localStorage.setItem("crewshipDebug", "1"))
    const failedRead = page.waitForEvent("console", message => message.type() === "warning" && message.text().includes("[crewship] 500 from") && message.text().includes(projectPath))
    const failedRefresh = page.waitForResponse(response => new URL(response.url()).pathname === projectPath && response.status() === 500)
    await details.getByRole("button", { name: "Refresh project details", exact: true }).click()
    await failedRefresh
    await failedRead
    // apiFetch processed the 500; flush query notification and paint as well.
    await page.evaluate(() => new Promise<void>(done => requestAnimationFrame(() => requestAnimationFrame(() => done()))))
    await expect(details.getByRole("alert")).toContainText("unavailable")
    await expect(page.getByLabel("Project name", { exact: true })).toHaveCount(0)
    await expect(milestonePanel).toHaveCount(0)
    await expect(page.getByRole("button", { name: "Delete issue", exact: true })).toHaveCount(0)
    await page.goto("/issues/manage?project=foreign")
    await expect(page.getByRole("region", { name: "Shared project details", exact: true }).getByRole("alert")).toContainText("unavailable")
    await expect(page.getByLabel("Project name", { exact: true })).toHaveCount(0)
    await expect(page.getByRole("region", { name: "Shared project milestones", exact: true })).toHaveCount(0)
    expect(foreignReads).toBeGreaterThan(0)
    expect(writes.map(write => write.method)).toEqual(["PUT", "POST", "DELETE", "DELETE"])
    expect(unexpected).toEqual([]) // No project catalog, issue collection/count, trusted route, or runtime request.
  })
}
