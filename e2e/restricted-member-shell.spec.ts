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
  /^GET \/api\/v1\/workspaces\/[^/]+\/restricted-projects\/[^/]+\/milestones(\/[^/]+)?$/,
  /^GET \/api\/v1\/workspaces\/[^/]+\/restricted-projects\/[^/]+$/,
  /^GET \/api\/v1\/workspaces\/[^/]+\/restricted-issue-projects$/,

  /^GET \/api\/v1\/workspaces\/[^/]+\/restricted-routine-library(\/[^/]+)?$/,
  /^GET \/api\/v1\/workspaces$/,
  /^GET \/api\/v1\/agents$/,
  /^GET \/api\/v1\/agents\/[^/]+$/,
  /^GET \/api\/v1\/workspaces\/[^/]+\/restricted-(pages|routines|routine-runs|issues)(\/[^/]+)?$/,
  /^GET \/api\/v1\/auth\/(sessions|cli-tokens)$/,
  /^GET \/api\/auth\/(session|csrf|providers)$/,
  /^POST \/api\/auth\/token\/refresh$/,
]

// As internal/api/restricted_directory.go answers a restricted account: every
// row carries the session's surfaces, derived from the allowlist. Without them
// the shell offers nothing (fail closed).
const SURFACES = ["chat", "routines", "pages", "account_security"]
const ROWS = [
  { id: "ws-a", name: "Alpha", slug: "alpha", currentUserRole: "MEMBER", currentUserAccessMode: "restricted", restricted_session: true, restricted_surfaces: SURFACES },
  { id: "ws-b", name: "Beta", slug: "beta", currentUserRole: "MEMBER", currentUserAccessMode: "trusted", restricted_session: true, restricted_surfaces: SURFACES },
]

for (const width of [1280, 390]) {
  test(`restricted member shell at ${width}px: no reconnect banner, no forbidden request`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    const forbidden: string[] = []
    const sockets: string[] = []
    // Next's dev-server HMR socket (/_next/…) exists only under `next dev`;
    // the assertion is about the application's realtime socket.
    page.on("websocket", (ws) => {
      if (!new URL(ws.url()).pathname.startsWith("/_next/")) sockets.push(ws.url())
    })

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

    // A screen outside the surfaces is never mounted, so it sends nothing.
    await page.goto("/issues")
    await expect(page.getByText("Not available with your access")).toBeVisible()
    await page.waitForTimeout(1_000)
    expect(forbidden).toEqual([])
  })
}

// The production grid stays read only while mobile navigation returns the
// selected source context to an actionable composer, without sending it.
test.describe("mobile artifact selection", () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
  test("Chat and Work uncover the composer; Ask selection preserves its draft", async ({ page }) => {
    let sendAttempts = 0
    const agent = { id: "entity-agent", slug: "_", name: "Jamie", status: "IDLE", crew_id: "crew", crew_name: "Platform", role: "lead", current_user_can_chat: true }
    await page.route("**/api/**", async (route) => {
      const request = route.request(), path = new URL(request.url()).pathname
      let body: unknown = [], status = 200
      if (request.method() === "POST" && path.includes("/chats/")) sendAttempts++
      if (path === "/api/auth/session") body = { user: { id: "viewer", name: "Pavel", email: "pavel@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "test-workspace-id", name: "Test", slug: "test", currentUserRole: "ADMIN" }]
      else if (path === "/api/v1/ws-token") { status = 404; body = {} }
      else if (path === "/api/v1/conversations") body = { conversations: [], next_offset: null }
      else if (path === "/api/v1/agents") body = [agent]
      else if (path === "/api/v1/crews") body = [{ id: "crew", name: "Platform", slug: "platform" }]
      else if (path === "/api/v1/agents/entity-agent") body = agent
      else if (path === "/api/v1/agents/entity-agent/chats") body = [{ id: "chat-1", title: "Table selection", mode: "private", kind: "direct", status: "active", message_count: 1, started_at: "2026-10-13T09:00:00Z", updated_at: "2026-10-13T09:00:00Z" }]
      else if (path.endsWith("/execution-profile")) body = { mode: "trusted" }
      else if (path.endsWith("/messages")) body = { messages: [{ id: "trigger-message", role: "user", content: "Read the output", ts: "2026-10-13T09:00:00Z" }] }
      else if (path.endsWith("/entity-refs")) body = { contract_version: 1, reset: true, snapshot_id: "empty", refs: [], has_more: false, snapshot_complete: true, next_cursor: "" }
      else if (path === "/api/v1/agents/entity-agent/files") body = [{ name: "selection.csv", path: "runs/msg_test/selection.csv", is_dir: false, size: 33 }]
      else if (path === "/api/v1/agents/entity-agent/files/download") return route.fulfill({ contentType: "application/octet-stream", body: "Region,Revenue\nNorth,10\nSouth,20\n" })
      await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/chat/_?session=chat-1")
    const composer = page.getByPlaceholder("Message Jamie...")
    await expect(composer).toBeVisible({ timeout: 30_000 })
    await composer.fill("Keep this existing draft.")
    const tabs = page.getByRole("tablist", { name: "Chat panel", exact: true })
    const artifacts = tabs.getByRole("tab", { name: "Artifacts", exact: true })
    const chat = tabs.getByRole("tab", { name: "Chat", exact: true })
    await artifacts.click()
    await page.getByRole("button", { name: /^selection\.csv/ }).click()
    const grid = page.getByRole("region", { name: "Spreadsheet preview", exact: true })
    await expect(grid).toBeVisible({ timeout: 30_000 })
    await chat.click()
    await expect(page.getByRole("complementary", { name: "Live artifact", exact: true })).toHaveCount(0)
    await composer.click({ trial: true })
    await expect(composer).toHaveValue("Keep this existing draft.")
    await artifacts.click()
    await expect(grid).toBeVisible()
    await tabs.getByRole("tab", { name: "Work", exact: true }).click()
    await expect(page.getByRole("complementary", { name: "Live artifact", exact: true })).toHaveCount(0)
    await artifacts.click()
    await expect(grid).toBeVisible()
    await grid.getByRole("checkbox", { name: "Select source row 2", exact: true }).check()
    await grid.getByRole("button", { name: "Ask about selection (1)", exact: true }).focus()
    await page.keyboard.press("Enter")
    await expect(chat).toHaveAttribute("aria-selected", "true")
    await expect(page.getByRole("complementary", { name: "Live artifact", exact: true })).toHaveCount(0)
    await composer.click({ trial: true })
    await expect(composer).toHaveValue(/Keep this existing draft\.[\s\S]*A2:B2/)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    const draft = await composer.inputValue()
    await page.reload()
    await expect(composer).toHaveValue(draft, { timeout: 30_000 })
    expect(sendAttempts).toBe(0)
  })
})

for (const width of [1280,390]) {
  test(`Guest project Issues at ${width}px show only the authorized projection`,async({page})=>{
    await page.setViewportSize({width,height:900})
    const forbidden:string[]=[]
    let revoked=false
    await page.route("**/api/**",async route=>{
      const request=route.request();const path=new URL(request.url()).pathname;const key=`${request.method()} ${path}`
      if(!ALLOWED.some(re=>re.test(key))){forbidden.push(key);return route.fulfill({status:404,contentType:"application/json",body:"{}"})}
      let body:unknown=[]
      if(path==="/api/auth/session")body={user:{id:"issue-guest",name:"Guest",email:"guest@example.test"},expires:"2099-01-01T00:00:00Z"}
      else if(path==="/api/v1/workspaces")body=[{...ROWS[0],restricted_surfaces:["issues","account_security"]}]
      else if(path.endsWith("/restricted-issues"))body=revoked?[]:[{id:"shared-issue",identifier:"SHARED-1",title:"Client project issue",status:"TODO",project_id:"project-1",project_name:"Client project",can_read:true}]
      await route.fulfill({status:200,contentType:"application/json",body:JSON.stringify(body)})
    })
    await page.goto("/issues")
    await expect(page.getByRole("heading",{name:"Issues shared with you"})).toBeVisible()
    await expect(page.getByRole("link",{name:"Client project issue"})).toBeVisible()
    expect(forbidden).toEqual([])
    revoked=true
    await page.getByRole("button",{name:"Refresh",exact:true}).click()
    await expect(page.getByRole("link",{name:"Client project issue"})).toHaveCount(0)
    await expect(page.getByRole("heading",{name:"No issues shared here"})).toBeVisible()
    expect(forbidden).toEqual([])
  })
}
