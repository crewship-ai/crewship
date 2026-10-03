import { test, expect } from "@playwright/test"

// #2861 — a restricted account's shell must stay inside the restricted
// allowlist (internal/api/restricted_access.go): no /ws-token, so no reconnect
// loop and no "Reconnecting…" banner, and no shell call the server refuses.
//
// Every /api request is answered here, so the spec needs the frontend only.
// Anything not on the allowlist answers 404, as the server does for this
// account, and is recorded as a failure.

// Mirrors lib/restricted-endpoints.ts (kept literal so the spec has no build
// dependency on app code); lib/__tests__/restricted-endpoints.test.ts pins that
// list to the Go source.
const ALLOWED: RegExp[] = [
  /^GET \/api\/v1\/workspaces$/,
  /^GET \/api\/v1\/agents$/,
  /^GET \/api\/v1\/agents\/[^/]+$/,
  /^GET \/api\/v1\/workspaces\/[^/]+\/restricted-(pages|routines|routine-runs)(\/[^/]+)?$/,
  /^GET \/api\/v1\/auth\/(sessions|cli-tokens)$/,
  /^GET \/api\/auth\/(session|csrf|providers)$/,
  /^POST \/api\/auth\/token\/refresh$/,
]

const ROWS = [
  { id: "ws-a", name: "Alpha", slug: "alpha", currentUserRole: "MEMBER", currentUserAccessMode: "restricted" },
  { id: "ws-b", name: "Beta", slug: "beta", currentUserRole: "MEMBER", currentUserAccessMode: "trusted" },
]

for (const width of [1280, 390]) {
  test(`restricted member shell at ${width}px: no reconnect banner, no forbidden request`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    const forbidden: string[] = []
    const sockets: string[] = []
    page.on("websocket", (ws) => sockets.push(ws.url()))

    await page.route("**/api/**", async (route) => {
      const request = route.request()
      const path = new URL(request.url()).pathname
      const key = `${request.method()} ${path}`
      if (!ALLOWED.some((re) => re.test(key))) {
        forbidden.push(key)
        return route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: "Resource not found" }) })
      }
      let body: unknown = []
      if (path === "/api/auth/session") body = { user: { id: "u-r", name: "Rita", email: "rita@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/auth/csrf") body = { csrfToken: "t" }
      else if (path === "/api/v1/workspaces") body = ROWS
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })

    await page.goto("/routines")
    await expect(page.getByRole("heading", { name: "Private routines" })).toBeVisible()
    // The realtime banner appears after 3 s of a dead socket; wait well past it.
    await page.waitForTimeout(5_000)
    await expect(page.getByText(/Reconnecting|Connection lost/)).toHaveCount(0)
    expect(sockets).toEqual([])
    expect(forbidden).toEqual([])
  })
}
