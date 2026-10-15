import { test, expect } from "@playwright/test"
import { readFileSync } from "node:fs"
const fixture = JSON.parse(readFileSync(new URL("../internal/api/testdata/chat-entity-refs/page.json", import.meta.url), "utf8"))

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


// Production static export with real-handler projection fixtures. No provider,
// Docker runtime, persistent data or shared development instance is involved.
for (const width of [1280, 390]) {
  test(`durable issue card: edit, reload, keyboard detail and revocation at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    let revoked = false, revision = 1, writes = 0
    let status = "PLANNING"
    let hasReply = false
    const agent = { id: "entity-agent", slug: "_", name: "Jamie", status: "IDLE", crew_id: "crew", crew_name: "Platform", role: "lead", current_user_can_chat: true }
    const issue = () => ({ id: "entity-1", identifier: "ENG-1", workspace_id: "test-workspace-id", crew_id: "crew", title: "Synthetic title", status, priority: "none", revision, aggregate_revision: revision, allowed_actions: ["view", "comment", "change_status"], labels: [], created_at: "2026-10-13T09:00:00Z", updated_at: "2026-10-13T09:00:00Z" })
    await page.route("**/api/**", async (route) => {
      const request = route.request(), path = new URL(request.url()).pathname
      let body: unknown = []
      let code = 200
      if (path === "/api/auth/session") body = { user: { id: "viewer", name: "Pavel", email: "pavel@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "test-workspace-id", name: "Test", slug: "test", currentUserRole: "ADMIN" }]
      else if (path === "/api/v1/ws-token") { code = 404; body = {} }
      else if (path === "/api/v1/conversations") body = { conversations: [], next_offset: null }
      else if (path === "/api/v1/agents") body = [agent]
      else if (path === "/api/v1/crews") body = [{ id: "crew", name: "Platform", slug: "platform" }]
      else if (path === "/api/v1/agents/entity-agent") body = agent
      else if (path === "/api/v1/agents/entity-agent/chats") body = [{ id: "chat-1", title: "Issue cards", mode: "private", kind: "direct", status: "active", message_count: 2, started_at: "2026-10-13T09:00:00Z", updated_at: "2026-10-13T09:01:00Z" }]
      else if (path.endsWith("/execution-profile")) body = { mode: "trusted" }
      else if (path.endsWith("/messages")) body = { messages: [
        { id: "trigger-message", role: "user", content: "Show the issue", ts: "2026-10-13T09:00:00Z" },
        ...(hasReply ? [{ id: "reply", role: "assistant", content: "Here is the result.", run_id: "entity-run", ts: "2026-10-13T09:01:00Z" }] : []),
      ] }
      else if (path.endsWith("/entity-refs")) {
        if (revoked) { code = 404; body = {} }
        else body = { ...fixture, contract_version: 1, has_more: false, snapshot_complete: true, refs: [{ ...fixture.refs[0], anchor: { ...fixture.refs[0].anchor, run_status: hasReply ? "completed" : "failed" }, current: { ...fixture.refs[0].current, status, revision, aggregate_revision: revision, allowed_actions: issue().allowed_actions } }] }
      }
      else if (path === "/api/v1/issues/ENG-1") body = issue()
      else if (path === "/api/v1/crews/crew/issues/ENG-1" && request.method() === "PATCH") {
        const edit = request.postDataJSON()
        expect(edit.expected_revision).toBe(revision)
        expect(request.headers()["idempotency-key"]).toBeTruthy()
        writes++; revision++; status = edit.status; body = issue()
      }
      await route.fulfill({ status: code, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/chat/_?session=chat-1")
    const card = page.locator("section").filter({ has: page.getByRole("heading", { name: "Synthetic title", exact: true }) })
    // Cold static-export hydration includes profile, history and reference requests.
    // Keep this startup budget separate from the default bounded mutation/revocation checks.
    await expect(card).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('[data-turn-id="trigger-message"] + div').getByText("ENG-1")).toBeVisible()
    await expect(page.getByText("Run ended without a reply · failed")).toBeVisible()
    // Permission controls come from the viewer projection, including in detail.
    await expect(card.getByRole("button", { name: "Edit", exact: true })).toBeVisible()
    await card.getByRole("button", { name: "Edit", exact: true }).focus()
    await page.keyboard.press("Enter")
    await expect(card.getByRole("combobox", { name: "Status" })).toBeVisible()
    await expect(card.getByRole("textbox", { name: "Title" })).toHaveCount(0)
    await card.getByRole("combobox", { name: "Status" }).selectOption("IN_PROGRESS")
    await card.getByRole("button", { name: "Save", exact: true }).click()
    await expect(card.getByText("In Progress", { exact: true })).toBeVisible()
    expect(writes).toBe(1)
    hasReply = true
    await page.reload()
    await expect(page.locator('[data-turn-id="reply"] + div').getByText("ENG-1")).toBeVisible()
    await expect(card.getByText("In Progress", { exact: true })).toBeVisible()
    const opener = card.getByRole("button", { name: "Open on right" })
    await opener.focus(); await page.keyboard.press("Enter")
    await expect(page.getByRole("button", { name: "Back to Work" })).toBeVisible()
    await expect(page.getByRole("button", { name: "Change status" })).toBeVisible()
    await expect(page.getByRole("button", { name: "Change priority" })).toHaveCount(0)
    if (width === 390) {
      await expect(page.getByRole("dialog", { name: "Issue detail" })).toBeVisible()
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
      await page.keyboard.press("Escape")
      await expect(page.getByRole("dialog", { name: "Issue detail" })).toHaveCount(0)
      await opener.click()
    }
    revoked = true
    await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")))
    await expect(page.getByRole("heading", { name: "Synthetic title", exact: true })).toHaveCount(0)
    await expect(page.getByText("ENG-1", { exact: true })).toHaveCount(0)
    await expect(page.getByRole("button", { name: "Back to Work" })).toHaveCount(0)
    await expect(page.getByText("Unavailable", { exact: true })).toBeVisible()
  })
}

test("Guest card capability is generic and never fetches identity or object data", async ({ page }) => {
  const protectedRequests: string[] = []
  const agent = { id: "entity-agent", slug: "_", name: "Jamie", status: "IDLE", crew_id: "crew", current_user_can_chat: true }
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith("/entity-refs") || path.includes("/issues/")) protectedRequests.push(path)
    let body: unknown = []
    if (path === "/api/auth/session") body = { user: { id: "guest", name: "Guest", email: "guest@example.test" }, expires: "2099-01-01T00:00:00Z" }
    else if (path === "/api/v1/workspaces") body = [{ id: "test-workspace-id", name: "Test", slug: "test", currentUserRole: "MEMBER", currentUserAccessMode: "restricted", restricted_session: true, restricted_surfaces: ["chat", "account_security"] }]
    else if (path === "/api/v1/conversations") body = { conversations: [], next_offset: null }
      else if (path === "/api/v1/agents") body = [agent]
    else if (path === "/api/v1/agents/entity-agent") body = agent
    else if (path === "/api/v1/agents/entity-agent/chats") body = [{ id: "chat-1", title: "Chat", kind: "direct", status: "active", message_count: 1, started_at: "2026-10-13T09:00:00Z" }]
    else if (path.endsWith("/execution-profile")) body = { mode: "restricted" }
    else if (path.endsWith("/messages")) body = { messages: [{ id: "trigger-message", role: "user", content: "Show the issue", ts: "2026-10-13T09:00:00Z" }] }
    await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) })
  })
  await page.goto("/chat/_?session=chat-1")
  await expect(page.getByText("Unavailable", { exact: true })).toBeVisible()
  expect(protectedRequests).toEqual([])
  await expect(page.getByText("ENG-1", { exact: true })).toHaveCount(0)
  await expect(page.getByText("Synthetic title", { exact: true })).toHaveCount(0)
})
