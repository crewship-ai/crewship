import { test, expect } from "@playwright/test"

for (const width of [1280, 390]) {
  test(`account group editing at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    const pool = { id: "pool", name: "Team group", provider: "OPENAI", mode: "api_key", revision: 3, member_count: 1, allow_cross_owner: false, members: [{ credential_id: "account", priority: 1 }] }
    const account = { id: "account", name: "Team OpenAI", type: "API_KEY", provider: "OPENAI", status: "ACTIVE", tags: [], crew_ids: [], agent_names: [], last_used_ips: [], created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z", security_level: 1, login: { mode: "api_key", provider: "OPENAI", owner_email: "fixture@example.test", owner_user_id: "user", plan: null, expires_at: null, refresh: { supported: false, status: "none" }, quota: null, delivery: { kind: "env", target: "OPENAI_API_KEY" }, pays_for: { agents: 0, crews: 0 } } }
    let updated = false
    await page.route("**/api/**", async route => {
      const path = new URL(route.request().url()).pathname
      let body: unknown = []
      if (path === "/api/auth/session") body = { user: { id: "user", email: "fixture@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "ws", name: "Fixture", slug: "fixture", currentUserRole: "OWNER" }]
      else if (path === "/api/v1/credentials") body = [account]
      else if (path === "/api/v1/provider-logins/pools") body = { items: [pool], next_cursor: null }
      else if (path === "/api/v1/provider-logins/pools/pool") {
        if (route.request().method() === "PUT") {
          expect(route.request().headers()["if-match"]).toBe('"3"')
          expect(route.request().headers()["x-workspace-id"]).toBe("ws")
          expect(route.request().postDataJSON()).not.toHaveProperty("provider")
          updated = true
          return route.fulfill({ status: 204 })
        }
        body = pool
      } else if (/settings|config|health/.test(path)) body = {}
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/credentials")
    if (width > 640) await page.getByRole("button", { name: "Sidebar: hover", exact: true }).click()
    const titleBox = await page.getByRole("heading", { name: "Credentials", exact: true }).boundingBox()
    const actionBox = await page.getByRole("button", { name: "Account groups", exact: true }).boundingBox()
    expect(titleBox!.x + titleBox!.width).toBeLessThanOrEqual(actionBox!.x)
    await page.getByRole("button", { name: "Account groups", exact: true }).click()
    await page.getByRole("button", { name: "Edit Team group" }).click()
    await expect(page.getByRole("heading", { name: "Edit account group" })).toBeVisible()
    await page.getByLabel("Group name", { exact: true }).fill("Updated group")
    const dialog = page.getByRole("dialog")
    const box = await dialog.boundingBox()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(width + 1)
    await page.screenshot({ path: `/tmp/provider-pool-editor-${width}.png` })
    await page.getByRole("button", { name: "Save changes", exact: true }).click()
    await expect.poll(() => updated).toBe(true)
    await expect(page.getByRole("heading", { name: "Account groups", exact: true })).toBeVisible()
  })
}

for (const role of ["MANAGER", "MEMBER", "VIEWER"]) {
  test(`${role} cannot access account groups`, async ({ page }) => {
    let reads = 0
    await page.route("**/api/**", async route => {
      const path = new URL(route.request().url()).pathname
      let body: unknown = []
      if (path === "/api/auth/session") body = { user: { id: "user", email: "fixture@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "ws", name: "Fixture", slug: "fixture", currentUserRole: role }]
      else if (/settings|config|health/.test(path)) body = {}
      if (path.includes("provider-logins/pools")) reads++
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/credentials")
    await expect(page.getByText("No credentials yet", { exact: true })).toBeVisible()
    await expect(page.getByRole("button", { name: "Account groups" })).toHaveCount(0)
    expect(reads).toBe(0)
  })
}
