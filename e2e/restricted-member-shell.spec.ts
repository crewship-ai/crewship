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


// #3330: browser pairing is account security for a verified Guest session.
// These fixtures exercise visible menu/dialog behavior; real session/token
// authority remains covered by the Router and deployed acceptance separately.
for (const width of [1280, 390]) {
  test(`Guest CLI pairing from the account menu at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    const forbidden: string[] = []
    let starts = 0
    let consumed = false
    await page.route("**/api/**", async route => {
      const request = route.request()
      const path = new URL(request.url()).pathname
      const key = `${request.method()} ${path}`
      let body: unknown = []
      if (key === "POST /api/v1/auth/pair/start") {
        starts++
        body = { code: "GUEST-PAIR", expires_at: new Date(Date.now() + 600_000).toISOString() }
      } else if (key === "GET /api/v1/auth/pair/poll") {
        expect(new URL(request.url()).searchParams.get("code")).toBe("GUEST-PAIR")
        body = { status: consumed ? "consumed" : "pending" }
      } else if (!ALLOWED.some(pattern => pattern.test(key))) {
        forbidden.push(key)
        return route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: "Resource not found" }) })
      } else if (path === "/api/auth/session") body = { user: { id: "u-r", name: "Rita", email: "rita@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/auth/csrf") body = { csrfToken: "t" }
      else if (path === "/api/v1/workspaces") body = ROWS
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/routines")
    await expect(page.getByRole("heading", { name: "Private routines" })).toBeVisible()
    await page.getByRole("button", { name: "User menu" }).click()
    await page.getByRole("menuitem", { name: "Connect CLI…" }).click()
    const dialog = page.getByRole("dialog", { name: "Connect the CLI" })
    await expect(dialog).toBeVisible()
    await expect(dialog.getByTestId("pair-command")).toHaveText(`crewship login --pair --code=GUEST-PAIR --server=${new URL(page.url()).origin}`)
    expect(starts).toBe(1)
    consumed = true
    await expect(dialog.getByRole("status")).toHaveText(/CLI connected/)
    expect(forbidden).toEqual([])
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
  })
}

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


// Production static export with real-handler projection fixtures. No provider,
// Docker runtime, persistent data or shared development instance is involved.
for (const width of [1280, 390]) {
  test(`durable issue card: edit, reload, keyboard detail and revocation at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    let revoked = false, revision = 1, writes = 0
    let status = "PLANNING"
    let hasReply = false, loseCommentAck = false
    let refsFailure: "network" | "server" | null = null
    const commentKeys = new Set<string>()
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
      else if (path.endsWith("/peers")) body = { peers: [], preferences: [] }
      else if (path.endsWith("/peer-consent")) body = { opted_out: false }
      else if (path === "/api/v1/feedback") body = request.method() === "POST"
        ? { id: "detail-feedback", workspace_id: "test-workspace-id", user_id: "viewer", message_id: "reply", signal: "helpful", revision: 1, memory: { status: "feedback_only" } }
        : { feedback: [] }
      else if (path === "/api/v1/agents/entity-agent/chats") body = [{ id: "chat-1", title: "Issue cards", mode: "private", kind: "direct", status: "active", message_count: 2, started_at: "2026-10-13T09:00:00Z", updated_at: "2026-10-13T09:01:00Z" }]
      else if (path.endsWith("/execution-profile")) body = { mode: "trusted" }
      else if (path.endsWith("/messages")) body = { messages: [
        { id: "trigger-message", role: "user", content: "Show the issue", ts: "2026-10-13T09:00:00Z" },
        ...(hasReply ? [{ id: "reply", role: "assistant", content: "Here is the result.", run_id: "entity-run", ts: "2026-10-13T09:01:00Z" }] : []),
      ] }
      else if (path.endsWith("/entity-refs")) {
        if (refsFailure === "network") return route.abort("failed")
        if (refsFailure === "server") return route.fulfill({ status: 500, contentType: "application/json", body: "{}" })
        if (revoked) { code = 404; body = {} }
        else body = { ...fixture, contract_version: 1, has_more: false, snapshot_complete: true, refs: [{ ...fixture.refs[0], anchor: { ...fixture.refs[0].anchor, run_status: hasReply ? "completed" : "failed" }, current: { ...fixture.refs[0].current, status, revision, aggregate_revision: revision, allowed_actions: issue().allowed_actions } }] }
      }
      else if (path === "/api/v1/crews/crew/issues/ENG-1/comments" && request.method() === "POST") {
        commentKeys.add(request.headers()["idempotency-key"])
        if (loseCommentAck) { loseCommentAck = false; return route.abort("failed") }
        body = { id: "comment-1" }
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
    await card.getByRole("button", { name: "Comment", exact: true }).click()
    const inlineComment = card.getByRole("textbox", { name: "Comment", exact: true })
    await inlineComment.fill("One durable comment")
    await inlineComment.focus()
    const refreshed = page.waitForResponse(response => response.url().includes("/entity-refs"))
    await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")))
    await refreshed
    await expect(inlineComment).toBeFocused()
    await expect(inlineComment).toHaveValue("One durable comment")
    loseCommentAck = true
    await card.getByRole("button", { name: "Save", exact: true }).click()
    await expect(card.getByText("The outcome is unknown. Your draft is kept.")).toBeVisible()
    await page.reload()
    await card.getByRole("button", { name: "Comment", exact: true }).click()
    await expect(card.getByText("The outcome is unknown. Your draft is kept.")).toBeVisible()
    await card.getByRole("button", { name: "Check and retry the same operation" }).click()
    await expect(card.getByRole("textbox", { name: "Comment", exact: true })).toHaveCount(0)
    expect(commentKeys.size).toBe(1)
    const opener = card.getByRole("button", { name: "Open on right" })
    await opener.focus(); await page.keyboard.press("Enter")
    await expect(page.getByRole("button", { name: "Back to Work" })).toBeVisible()
    await expect(page.getByRole("button", { name: "Change status" })).toBeVisible()
    await expect(page.getByRole("button", { name: "Change priority" })).toHaveCount(0)
    const panelComment = page.getByPlaceholder("Write a comment.  @ to bring in an agent.")
    const issuePanel = page.getByRole("button", { name: "Back to Work" }).locator("xpath=../..")
    await panelComment.fill("Comment survives a field save")
    await page.getByRole("button", { name: "Change status", exact: true }).click()
    await page.getByRole("button", { name: "Done", exact: true }).click()
    await page.getByRole("button", { name: "Save changes", exact: true }).click()
    await expect(page.getByRole("button", { name: "Save changes", exact: true })).toHaveCount(0)
    await expect(panelComment).toHaveValue("Comment survives a field save")

    // Real production DOM must survive a completed failed check, not only an
    // in-flight fetch. Both kinds of outage used to unmount this editor.
    for (const failure of ["server", "network"] as const) {
      refsFailure = failure
      await panelComment.focus()
      await panelComment.evaluate((el: HTMLTextAreaElement) => el.setSelectionRange(3, 9))
      const editor = await panelComment.elementHandle()
      const completed = failure === "server"
        ? page.waitForResponse((r) => new URL(r.url()).pathname.endsWith("/entity-refs") && r.status() === 500).then((r) => r.finished())
        : page.waitForEvent("requestfailed", { predicate: (r) => new URL(r.url()).pathname.endsWith("/entity-refs") })
      await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")))
      await completed
      await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
      await expect(issuePanel.getByText("Checking current access…", { exact: true })).toBeVisible()
      expect(await editor!.evaluate((el: HTMLTextAreaElement) => ({ same: el.isConnected && document.activeElement === el, start: el.selectionStart, end: el.selectionEnd, value: el.value }))).toEqual({ same: true, start: 3, end: 9, value: "Comment survives a field save" })
      refsFailure = null
      const recovered = page.waitForResponse((r) => new URL(r.url()).pathname.endsWith("/entity-refs") && r.ok())
      await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")))
      await recovered
      await expect(issuePanel.getByText("Checking current access…", { exact: true })).toHaveCount(0)
      await expect(panelComment).toHaveValue("Comment survives a field save")
    }

    if (width === 1280) {
      // An open Issue belongs to Work and must not cover other side panels.
      await page.getByRole("tab", { name: "Artifacts", exact: true }).click()
      await expect(page.getByText("No artifacts yet. Documents, sheets and pages the agent writes appear here.")).toBeVisible()
      await expect(page.getByRole("button", { name: "Back to Work" })).toHaveCount(0)
      await page.getByRole("tab", { name: "Work", exact: true }).click()
      await expect(page.getByRole("button", { name: "Back to Work" })).toBeVisible()
      await page.getByRole("tab", { name: "Memory", exact: true }).click()
      await expect(page.getByRole("region", { name: "Your preferences for this agent" })).toBeVisible()
      await expect(page.getByRole("button", { name: "Back to Work" })).toHaveCount(0)
      await page.keyboard.press("Escape")
      await page.getByRole("tab", { name: "Work", exact: true }).click()
      await expect(page.getByRole("button", { name: "Back to Work" })).toBeVisible()
      await page.getByRole("button", { name: "Good response", exact: true }).click()
      await page.getByRole("button", { name: /What should change next time/ }).click()
      await page.getByRole("button", { name: "Manage my preferences", exact: true }).click()
      await expect(page.getByRole("region", { name: "Your preferences for this agent" })).toBeVisible()
      await page.getByRole("tab", { name: "Work", exact: true }).focus()
      await page.keyboard.press("Enter")
      await expect(page.getByRole("button", { name: "Back to Work" })).toBeVisible()
      await expect(page.getByRole("button", { name: "Change status" })).toBeVisible()
    }
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
    else if (path.endsWith("/restricted-files") || path.endsWith("/project-input-options")) body = { files: [], has_more: false }
    else if (path.endsWith("/restricted-context")) body = { entries: [] }
    else if (path.endsWith("/execution-profile")) body = { mode: "restricted" }
    else if (path.endsWith("/messages")) body = { messages: [{ id: "trigger-message", role: "user", content: "Show the issue", ts: "2026-10-13T09:00:00Z" }] }
    await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) })
  })
  await page.goto("/chat/_?session=chat-1")
  await expect(page.getByText("Show the issue", { exact: true })).toBeVisible()
  await expect(page.getByText("Unavailable", { exact: true })).toBeVisible()
  await expect(page.getByRole("heading", { name: "Something went wrong", exact: true })).toHaveCount(0)
  expect(protectedRequests).toEqual([])
  await expect(page.getByText("ENG-1", { exact: true })).toHaveCount(0)
  await expect(page.getByText("Synthetic title", { exact: true })).toHaveCount(0)
})
