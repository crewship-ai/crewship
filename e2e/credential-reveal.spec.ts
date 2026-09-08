import { test, expect } from "@playwright/test"

test("Static export helper serves root and denies traversal", async ({ request }) => {
  expect((await request.get("/")).status()).toBe(200)
  expect((await request.get("/%2e%2e%2fpackage.json")).status()).toBe(403)
  expect((await request.get("/missing-fixture-2461")).status()).toBe(404)
})

for (const scenario of [
  { role: "OWNER", capability: true, enabled: true, sealed: false, allowed: true },
  { role: "ADMIN", capability: true, enabled: true, sealed: false, allowed: true },
  { role: "MEMBER", capability: true, enabled: true, sealed: false, allowed: false },
  { role: "VIEWER", capability: true, enabled: true, sealed: false, allowed: false },
  { role: "ADMIN", capability: false, enabled: true, sealed: false, allowed: false },
  { role: "OWNER", capability: true, enabled: false, sealed: false, allowed: false },
  { role: "OWNER", capability: true, enabled: true, sealed: true, allowed: false },
]) {
  const { role, capability, enabled, sealed, allowed } = scenario
  test(`${role} reveal capability=${capability} policy=${enabled} sealed=${sealed}`, async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 })
    await page.clock.install()
    let reveals = 0
    const credential = { id: "demo", name: "Demo file", type: "GENERIC_SECRET", provider: "GCP", status: "ACTIVE", scope: "WORKSPACE", tags: ["demo"], crew_ids: [], agent_names: [], last_used_ips: [], created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z", security_level: 2, sensitivity: sealed ? "SEALED" : "STANDARD", _count_agent_credentials: 0 }
    await page.route("**/api/**", async route => {
      const path = new URL(route.request().url()).pathname
      let body: unknown = []
      if (path === "/api/auth/session") body = { user: { id: "user", email: "demo@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "ws", name: "Fixture", slug: "fixture", currentUserRole: role, currentUserCapabilities: capability ? ["credentials:reveal"] : [] }]
      else if (path === "/api/v1/credentials") body = [credential]
      else if (path === "/api/v1/credentials/reveal-policy") body = { enabled }
      else if (path === "/api/v1/credentials/demo/sensitivity") body = { sensitivity: credential.sensitivity }
      else if (path === "/api/v1/credentials/demo") body = credential
      else if (path === "/api/v1/credentials/demo/reveal") { reveals++; body = { value: "dummy-not-a-real-secret-browser" } }
      else if (/settings|config|health/.test(path)) body = {}
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/credentials")
    await page.getByRole("button", { name: "Sidebar: hover", exact: true }).click()
    await page.getByText("Demo file", { exact: true }).first().click()
    await expect(page.getByText("Back to credentials", { exact: true })).toBeVisible()
    const reveal = page.getByRole("button", { name: "Reveal the existing value…", exact: true })
    if (!allowed) {
      await expect(reveal).toHaveCount(0)
      expect(reveals).toBe(0)
      return
    }
    await reveal.click()
    await page.getByLabel("Reason", { exact: true }).fill("Inspecting the inert demo credential for acceptance")
    await page.getByRole("button", { name: "Reveal the existing value", exact: true }).click()
    await expect(page.getByTestId("revealed-value")).toHaveText("dummy-not-a-real-secret-browser")
    await page.screenshot({ path: "/tmp/credential-reveal-desktop.png" })
    await page.clock.fastForward(30001)
    await expect(page.getByTestId("revealed-value")).toHaveCount(0)
    await expect(page.getByLabel("Reason", { exact: true })).toHaveValue("")
    expect(reveals).toBe(1)
  })
}
