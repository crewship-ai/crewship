/**
 * Standalone smoke test — login + WebSocket end-to-end through a real
 * headless Chromium. Run via `pnpm smoke` (or with a different target via
 * SMOKE_URL=…). Bypasses playwright's globalSetup so it works against
 * remote dev/prod URLs where the global-setup auth flow doesn't.
 *
 * Each step prints ✓/✗ and a one-line detail; exits non-zero on any
 * failure so CI / `&& echo ok` chaining works.
 */
import pkg from "playwright"
const { chromium } = pkg

const URL = process.env.SMOKE_URL ?? "http://localhost:3011"
const EMAIL = process.env.SMOKE_EMAIL ?? "demo@crewship.ai"
const PASSWORD = process.env.SMOKE_PASSWORD ?? "password123"

const steps = []
function step(name, ok, details = "") {
  steps.push({ name, ok, details })
  console.log(`${ok ? "✓" : "✗"} ${name}${details ? ` — ${details}` : ""}`)
}

console.log(`smoke target: ${URL}\n`)

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext()
const page = await ctx.newPage()

const apiResponses = []
const wsConnections = []
const pageErrors = []

page.on("response", (res) => {
  if (res.url().includes("/api/")) {
    apiResponses.push({ url: res.url(), status: res.status() })
  }
})
const allWsAttempts = []
page.on("websocket", (ws) => {
  allWsAttempts.push(ws.url())
  // /_next/webpack-hmr is the dev HMR socket — not what we care about.
  if (ws.url().includes("/_next/")) return
  const log = { url: ws.url(), frames: 0, closed: false, closeCode: null, closeReason: null }
  wsConnections.push(log)
  ws.on("framereceived", () => { log.frames++ })
  ws.on("close", () => { log.closed = true })
  // Also capture explicit close event with code/reason via CDP-level
  // listener if available — Playwright's high-level API doesn't expose
  // close codes, so we approximate via a frame-watcher fallback.
  ws.on("socketerror", (err) => { log.closeReason = String(err) })
})
page.on("pageerror", (err) => pageErrors.push(err))

async function pollForResponse(predicate, timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const hit = apiResponses.find(predicate)
    if (hit) return hit
    await page.waitForTimeout(200)
  }
  return null
}

let exitCode = 0
try {
  const nav = await page.goto(`${URL}/login`, {
    waitUntil: "domcontentloaded",
    timeout: 15000,
  })
  step("GET /login", nav?.ok() ?? false, `HTTP ${nav?.status() ?? "?"}`)

  // Wait for React hydration to fully attach event handlers. The
  // raw "button visible & not disabled" check passes too early in
  // dev mode — the DOM is there but React's onClick is still wiring
  // up. Wait for a known post-hydration side effect instead: the
  // useEffect on the login page fires `/api/v1/auth/google/status`
  // once on mount.
  await pollForResponse((r) => r.url.endsWith("/api/v1/auth/google/status"), 10000)
  // Tiny extra settle so the form's React onSubmit handler is bound.
  await page.waitForTimeout(300)

  await page.fill("#email", EMAIL)
  await page.fill("#password", PASSWORD)
  await page.locator("button[type=submit]").click()

  const csrfRes = await pollForResponse((r) => r.url.endsWith("/api/auth/csrf"), 10000)
  step(
    "GET /api/auth/csrf",
    !!csrfRes && csrfRes.status === 200,
    csrfRes ? `HTTP ${csrfRes.status}` : "not called within 10s after submit",
  )
  const loginRes = await pollForResponse((r) => r.url.endsWith("/api/auth/callback/credentials"), 15000)
  step(
    "POST /api/auth/callback/credentials",
    !!loginRes && loginRes.status === 200,
    loginRes ? `HTTP ${loginRes.status}` : "not called within 10s after submit",
  )

  await page.waitForURL((url) => !url.pathname.startsWith("/login"), {
    timeout: 10000,
    // "load" event can lag indefinitely behind navigation in dev mode
    // when HMR / RSC streaming is still wiring up. We just need the
    // URL to flip — "commit" fires as soon as the new document starts.
    waitUntil: "commit",
  })
  step("redirect off /login", true, `landed on ${page.url()}`)

  const workspacesRes = await pollForResponse((r) => r.url.includes("/api/v1/workspaces"))
  step(
    "GET /api/v1/workspaces",
    !!workspacesRes && workspacesRes.status === 200,
    workspacesRes ? `HTTP ${workspacesRes.status}` : "not called within 15s",
  )

  const tokenRes = await pollForResponse((r) => r.url.includes("/api/v1/ws-token"))
  step(
    "GET /api/v1/ws-token",
    !!tokenRes && tokenRes.status === 200,
    tokenRes ? `HTTP ${tokenRes.status}` : "not called within 15s",
  )

  // Wait up to 10s for our app's /ws connection to actually open.
  const deadline = Date.now() + 10000
  while (Date.now() < deadline && wsConnections.length === 0) {
    await page.waitForTimeout(200)
  }
  const ws = wsConnections[0]
  step("WS connection opened", !!ws, ws?.url ?? "no /ws connection within 10s")

  // Give backend a few seconds to confirm the connection sticks. Backend
  // pings every 30s, so we don't expect frames in this short window
  // unless something explicitly broadcasts — keep frames as info, gate
  // pass/fail on the connection staying open.
  if (ws) {
    await page.waitForTimeout(4000)
    step("WS still alive", !ws.closed, ws.closed ? "closed mid-session" : `open, ${ws.frames} frame(s) in 4s`)
  }

  step("no uncaught JS errors", pageErrors.length === 0, pageErrors.length === 0 ? "" : pageErrors.map((e) => `${e.name}: ${e.message}`).join(" | "))

  // Sanity-check that we landed somewhere with content (not a blank
  // hydration error). 5 == arbitrary "more than just the loading
  // spinner" baseline.
  const linkCount = await page.locator("a").count()
  step("dashboard rendered", linkCount > 5, `${linkCount} <a> elements`)
} catch (err) {
  step("UNCAUGHT exception", false, err instanceof Error ? `${err.name}: ${err.message}` : String(err))
  exitCode = 1
} finally {
  await browser.close()
}

const failed = steps.filter((s) => !s.ok)
console.log(`\n${steps.length - failed.length}/${steps.length} passed`)
if (failed.length > 0) {
  console.log("FAILED:")
  for (const s of failed) console.log(`  ✗ ${s.name}${s.details ? ` — ${s.details}` : ""}`)
  exitCode = 1
}

const non200 = apiResponses.filter((r) => r.status >= 400)
if (non200.length > 0) {
  console.log("\nNon-2xx API responses:")
  for (const r of non200) console.log(`  ${r.status} ${r.url}`)
}

if (failed.length > 0) {
  console.log(`\nAll API responses captured (${apiResponses.length}):`)
  // Dedupe by URL so the noise of polling doesn't drown the signal.
  const seen = new Set()
  for (const r of apiResponses) {
    const key = `${r.status} ${r.url.replace(/[?&]workspace_id=[^&]+/, "")}`
    if (seen.has(key)) continue
    seen.add(key)
    console.log(`  ${r.status} ${r.url}`)
  }
  console.log(`\nWebSocket connections (${wsConnections.length}):`)
  for (const w of wsConnections) {
    console.log(`  ${w.url} — frames: ${w.frames}, ${w.closed ? "closed" : "open"}${w.closeReason ? ` (${w.closeReason})` : ""}`)
  }
  console.log(`\nAll WS attempts (${allWsAttempts.length}):`)
  for (const u of allWsAttempts) console.log(`  ${u}`)
}

process.exit(exitCode)
