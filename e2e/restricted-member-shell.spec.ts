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


// CHAT19 visible flow uses the existing isolated static UI fixture. Backend,
// source authority and prompt recall are covered by real Go/CLI tests.
for (const width of [1280, 390]) {
  test(`CHAT19 explicit preferences at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    let feedback: { id: string; workspace_id: string; user_id: string; message_id: string; signal: string; reason?: string; revision: number; memory: { status: string; entry_id?: string } } | null = null
    let preferences: { id: string; revision: number; feedback_id: string; source_revision: number; chat_id: string; message_id: string; reason: string; value: string; status: string; source_type: string }[] = []
    let optedOut = false
    let processingReads = 0
    const posts: { reason?: string }[] = []
    const concise = "Next time, keep answers concise."
    const conclusion = "Next time, start with the conclusion."
    await page.addInitScript(() => localStorage.setItem("crewship.workspaceId:memory-user", "memory-ws"))
    await page.route("**/api/**", async route => {
      const req = route.request(), path = new URL(req.url()).pathname
      let body: unknown = []
      let status = 200
      if (path === "/api/auth/session") body = { user: { id: "memory-user", name: "Memory reader", email: "memory@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "memory-ws", slug: "memory", name: "Memory workspace", currentUserRole: "OWNER", currentUserAccessMode: "trusted" }]
      else if (path === "/api/v1/agents" || path === "/api/v1/agents/memory-agent") { const agent = { id: "memory-agent", name: "Jamie", slug: "_", status: "IDLE", crew_id: "memory-crew", agent_role: "AGENT", current_user_can_chat: true }; body = path.endsWith("/agents") ? [agent] : agent }
      else if (path === "/api/v1/crews") body = [{ id: "memory-crew", slug: "memory-crew", name: "Memory crew" }]
      else if (path.endsWith("/chats")) body = [{ id: "memory-chat", title: "Feedback preferences", status: "IDLE", mode: "CHAT", origin: "UI", created_by: "memory-user", visibility: "private", message_count: 1, started_at: "2026-10-15T08:00:00Z" }]
      else if (path.endsWith("/execution-profile")) body = { mode: "trusted", audience: "private" }
      else if (path === "/api/v1/chats/memory-chat/messages") body = { messages: [{ id: "memory-message", role: "assistant", content: "Here is the completed summary.", ts: "2026-10-15T08:00:00Z" }] }
      else if (path === "/api/v1/feedback") {
        if (req.method() === "POST") {
          const data = req.postDataJSON(); posts.push(data)
          feedback = { id: "feedback-1", workspace_id: "memory-ws", user_id: "memory-user", message_id: "memory-message", signal: data.signal, reason: data.reason, revision: data.reason ? 2 : 1, memory: { status: data.reason ? "processing" : "feedback_only", entry_id: data.reason ? "preference-1" : undefined } }
          if (data.reason) preferences = [{ id: "preference-1", revision: 1, feedback_id: feedback.id, source_revision: 2, chat_id: "memory-chat", message_id: "memory-message", reason: data.reason, value: "", status: "processing", source_type: "feedback_reason" }]
          body = feedback; status = 201
        } else {
          if (feedback?.memory.status === "processing" && ++processingReads >= 3) { feedback.memory.status = "saved"; preferences = preferences.map(p => ({ ...p, status: "saved", value: p.reason === concise ? "prefers concise answers" : "prefers the conclusion first" })) }
          body = { feedback: feedback ? [feedback] : [] }
        }
      } else if (path.endsWith("/peers")) body = { peers: [], preferences }
      else if (path.endsWith("/peers/memory-user")) {
        const params = new URL(req.url()).searchParams
        if (req.method() === "PATCH") { const data = req.postDataJSON(); expect(data).toMatchObject({ entry_id: "preference-1", revision: 1 }); preferences = preferences.map(p => ({ ...p, reason: data.reason, revision: 2, status: "processing" })); feedback = { ...feedback!, reason: data.reason, memory: { status: "processing" } }; processingReads = 0 }
        else { expect(params.get("entry_id")).toBe("preference-1"); expect(params.get("revision")).toBe("2"); preferences = []; feedback = { ...feedback!, memory: { status: "withdrawn" } } }
        status = 204; body = null
      } else if (path.endsWith("/peer-consent")) { if (req.method() === "PUT") optedOut = req.postDataJSON().opted_out; body = { opted_out: optedOut } }
      else if (path.endsWith("/conversations")) body = { conversations: [], next_offset: null }
      else if (path === "/api/v1/me/preferences") body = {}
      else if (path === "/api/v1/ws-token") { status = 404; body = {} }
      await route.fulfill({ status, contentType: "application/json", body: status === 204 ? "" : JSON.stringify(body) })
    })
    await page.goto("/chat/_?session=memory-chat")
    await expect(page.getByText("Here is the completed summary.")).toBeVisible()
    await page.getByRole("button", { name: "Good response", exact: true }).click()
    await expect(page.getByRole("button", { name: /What should change next time/ })).toBeVisible()
    expect(posts[0].reason).toBeUndefined()
    await page.getByRole("button", { name: /What should change next time/ }).click()
    await page.getByRole("button", { name: concise, exact: true }).click()
    await expect(page.getByRole("status").filter({ hasText: "Processing" })).toBeVisible()
    await expect(page.getByText("Remembered for your next private chat with this agent.")).toBeVisible()
    await page.getByRole("button", { name: "Manage my preferences", exact: true }).click()
    const panel = page.getByRole("region", { name: "Your preferences for this agent" })
    await expect(panel.getByText(concise, { exact: true })).toBeVisible()
    await panel.getByText("Source", { exact: true }).click()
    await expect(panel.getByRole("link", { name: "Open source conversation" })).toBeVisible()
    await panel.getByRole("button", { name: "Change preference" }).click()
    await panel.getByRole("button", { name: conclusion, exact: true }).click()
    await expect(panel.getByRole("paragraph").filter({ hasText: conclusion })).toBeVisible()
    await panel.getByRole("button", { name: "Forget", exact: true }).click()
    await page.getByRole("alertdialog").getByRole("button", { name: "Forget preference", exact: true }).click()
    await expect(panel.getByText("No preferences saved for this agent yet.")).toBeVisible()
    await panel.getByRole("button", { name: "Turn off and forget", exact: true }).click()
    await page.getByRole("alertdialog").getByRole("button", { name: "Turn off and forget", exact: true }).click()
    await expect(panel.getByText("Personal memory off", { exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
  })
}

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
