import { test, expect } from "@playwright/test"

// Production static export with synthetic Guest API responses only. Backend
// quota/authority and real CLI-to-Router behavior are verified independently.
for (const width of [1280, 390]) {
  test(`shared issue search cooldown keeps drafts and recovers at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.clock.install()
    let limited = false, recovered = false, lists = 0
    await page.addInitScript(() => localStorage.setItem("crewship.workspaceId:search-guest", "search-ws"))
    await page.route("**/api/**", async route => {
      const path = new URL(route.request().url()).pathname
      let body: unknown = [], status = 200
      const headers: Record<string, string> = {}
      if (path === "/api/auth/session") body = { user: { id: "search-guest", name: "Guest", email: "guest@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "search-ws", name: "Shared workspace", slug: "shared", currentUserRole: "VIEWER", currentUserAccessMode: "restricted", restricted_session: true, restricted_surfaces: ["issues", "account_security"] }]
      else if (path.endsWith("/restricted-issues")) {
        lists++
        if (limited && !recovered) { status = 429; headers["Retry-After"] = "30"; body = { error: "Shared issue searches are temporarily limited. Try again in 30s." } }
        else body = [{ id: "issue-1", identifier: "CLIENT-1", title: recovered ? "Recovered issue" : "Shared issue", status: "TODO", project_id: "project-1", project_name: "Shared project", can_read: false, can_contribute: false }]
      }
      await route.fulfill({ status, headers, contentType: "application/json", body: JSON.stringify(body) })
    })
    const searchForm = page.locator("form").filter({ has: page.getByLabel("Search shared issues") })
    await page.goto("/issues")
    await expect(page.getByText("Shared issue", { exact: true })).toBeVisible()
    await page.getByLabel("Search shared issues").fill("a useful search")
    await searchForm.getByRole("button", { name: "Search", exact: true }).click()
    await expect.poll(() => lists).toBeGreaterThan(1)
    await page.getByLabel("Search shared issues").fill("Keep my draft")
    limited = true
    await page.getByRole("button", { name: "Refresh", exact: true }).click()
    await expect(page.getByText(/Shared issue searches are temporarily limited/)).toBeVisible()
    await expect(page.getByText("Shared issue", { exact: true })).toBeVisible()
    await expect(page.getByLabel("Search shared issues")).toHaveValue("Keep my draft")
    await expect(searchForm.getByRole("button", { name: "Search", exact: true })).toBeDisabled()
    const before = lists
    await page.clock.runFor(15_000)
    expect(lists).toBe(before)
    recovered = true
    await page.clock.runFor(16_000)
    await expect(page.getByText("Recovered issue", { exact: true })).toBeVisible()
    await expect(searchForm.getByRole("button", { name: "Search", exact: true })).toBeEnabled()
    await expect(page.getByLabel("Search shared issues")).toHaveValue("Keep my draft")
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
  })
}
