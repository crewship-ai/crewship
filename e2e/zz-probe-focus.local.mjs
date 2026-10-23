// Isolated production-component browser test of the embedded workbook editor host.
// Two local origins: the host page (127.0.0.1) and a mock editor origin (localhost) serving a
// minimal fake editor with the editor origin's sandbox headers. The workbook API is an in-memory
// mock with per-cell conflicts; an agent edit is a commit appended to the mocked commits feed.
// No live instance, provider or credentials are used.
// Run: crewship-heavy node e2e/workbook-editor-host.browser.mjs
import { build } from "esbuild"
import { chromium } from "@playwright/test"
import assert from "node:assert/strict"
import { createServer } from "node:http"
import { mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"

// The bundle pin: plain lowercase hex SHA-256 of the manifest bytes, the same in the session's
// bundle_digest, the editor URL path and the editor's hello (as the server and editor origin emit it).
const DIGEST = "c".repeat(64)
const BUNDLE = DIGEST
const PAGE = "page-1"

// ----- mock workbook API -------------------------------------------------------------------------

const api = {
  flag: true,
  rev: 1,
  cells: { A1: "Item", B2: "10" },
  /** rev → commit; rev 1 is the import. */
  commits: [{ rev: 1, kind: "snapshot" }],
  /** cell → { rev, writer } of its last write. */
  writes: {},
  receipts: new Map(),
  sessions: new Map(),
  opened: 0,
  closed: [],
  /** While set, edits wait for it before answering. */
  hold: undefined,
  /** The bundle the server currently qualifies; a renewal names it. */
  bundle: BUNDLE,
  /** "edits": edits answer 503 editor disabled; "all": every live-editing endpoint does. */
  disabled: undefined,
  /** The snapshot read answers 500. */
  snapshotFails: false,
  /** "memory": commit, but answer a non-durable receipt; "unreadable": commit nothing, answer 200 with no body. */
  editMode: "normal",
  /** Access-token refreshes the host's fetch layer asked for. */
  refreshes: 0,
  /** Every edit request as received. */
  editLog: [],
}
let sessionCount = 0

function commit(writer, actor, ops, extra = {}) {
  api.rev++
  for (const op of ops) { api.cells[op.cell] = op.input; api.writes[op.cell] = { rev: api.rev, writer } }
  api.commits.push({ rev: api.rev, kind: "cells", actor_label: actor, ops: ops.map(({ sheet, cell, input }) => ({ sheet, cell, input })), writer, ...extra })
  return api.rev
}
/** An agent's committed edit, as the commits feed shows it to every session. */
const agentCommit = (cell, input) => commit("agent:ada", "Agent Ada", [{ sheet: "Sheet1", cell, input }])

function address(cell) {
  const [, letters, digits] = /^([A-Z]+)(\d+)$/.exec(cell)
  return { row: Number(digits) - 1, col: [...letters].reduce((n, l) => n * 26 + l.charCodeAt(0) - 64, 0) - 1 }
}
function gridcraftSnapshot() {
  const rows = {}
  for (const [cell, input] of Object.entries(api.cells)) {
    const { row, col } = address(cell)
    ;(rows[row] ??= {})[col] = { value: { t: "Text", v: input }, formula: null, style: 0 }
  }
  return { sheets: [{ name: "Sheet1", cells: { bands: { 0: { rows } }, len: Object.keys(api.cells).length } }], active_sheet: 0 }
}

const json = (response, status, body) => { response.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" }); response.end(body === undefined ? "" : JSON.stringify(body)) }
const readBody = request => new Promise(resolve => { let data = ""; request.on("data", chunk => { data += chunk }); request.on("end", () => resolve(data)) })
const DISABLED = { error: "gridcraft_editor_disabled" }
const SESSION_OVER = { error: "editor_session_expired" }

async function workbookApi(request, response, path, query) {
  if (path === "" && request.method === "GET") {
    if (api.snapshotFails) return json(response, 500, { error: "Workbook operation failed" })
    return json(response, 200, { schema_version: 1, rev: api.rev, generation: 1, snapshot: gridcraftSnapshot() })
  }
  if (api.disabled === "all" && (path.startsWith("/sessions") || path === "/edits" || path === "/commits")) return json(response, 503, DISABLED)
  if (path === "/sessions" && request.method === "POST") {
    const renew = JSON.parse((await readBody(request)) || "{}").session_id
    if (renew) {
      // The heartbeat: same session, and the bundle the server qualifies now.
      if (!api.sessions.has(renew)) return json(response, 404, { error: "Workbook not found" })
      if (!api.sessions.get(renew).open) return json(response, 409, SESSION_OVER)
      return json(response, 200, { session_id: renew, actor_label: "Owner", rev: api.rev, generation: 1, protocol: 1, bundle_digest: api.bundle, editor_url: `${editorOrigin}/b/${DIGEST}/index.html`, expires_at: "2099-01-01T00:00:00Z" })
    }
    const id = `ws_mock${++sessionCount}`
    api.sessions.set(id, { open: true })
    api.lastSession = id
    api.opened++
    return json(response, 201, { session_id: id, actor_label: "Owner", rev: api.rev, generation: 1, protocol: 1, bundle_digest: BUNDLE, editor_url: `${editorOrigin}/b/${DIGEST}/index.html`, expires_at: "2099-01-01T00:00:00Z" })
  }
  const close = /^\/sessions\/([^/]+)$/.exec(path)
  if (close && request.method === "DELETE") {
    const id = decodeURIComponent(close[1])
    api.closed.push(id)
    if (api.sessions.has(id)) api.sessions.get(id).open = false
    return json(response, 200, { status: "closed", session_id: id })
  }
  if (path === "/edits" && request.method === "POST") {
    const raw = await readBody(request)
    const edit = JSON.parse(raw)
    api.editLog.push(edit)
    // As the server: a session never opened is 404; an expired or closed one answers the editor's
    // own code, which is not the app-wide sign-in expiry (`session_expired`).
    if (!api.sessions.has(edit.session_id)) return json(response, 404, { error: "Workbook not found" })
    if (!api.sessions.get(edit.session_id).open) return json(response, 409, SESSION_OVER)
    if (api.disabled === "edits") return json(response, 503, DISABLED)
    if (api.editMode === "unreadable") { response.writeHead(200, { "content-type": "application/json" }); return response.end() }
    if (api.hold) await api.hold
    const known = api.receipts.get(edit.mutation_id)
    if (known) return json(response, 200, known)
    const conflicts = edit.ops.flatMap(op => {
      const last = api.writes[op.cell]
      return last && last.rev > edit.base_rev && last.writer !== edit.session_id ? [{ cell: op.cell, input: api.cells[op.cell], rev: last.rev, actor_label: last.writer.startsWith("agent:") ? "Agent Ada" : "Owner" }] : []
    })
    if (conflicts.length) return json(response, 409, { error: "precondition_failed", head_rev: api.rev, conflicts })
    const rev = commit(edit.session_id, "Owner", edit.ops, { own: true, session_id: edit.session_id, mutation_id: edit.mutation_id })
    const receipt = { operation_key: { artifact_id: PAGE, authority_id: "user:owner", mutation_id: edit.mutation_id }, outcome: "applied", rev, durability: api.editMode === "memory" ? "memory" : "node", at: new Date().toISOString() }
    api.receipts.set(edit.mutation_id, receipt)
    return json(response, 200, receipt)
  }
  if (path === "/commits" && request.method === "GET") {
    const since = Number(query.get("since")), limit = Number(query.get("limit"))
    const commits = api.commits.filter(c => c.rev > since).slice(0, limit).map(({ writer, ...c }) => c.kind === "snapshot" ? c : { ...c, own: writer.startsWith("ws_") })
    return json(response, 200, { head_rev: api.rev, generation: 1, commits })
  }
  const operation = /^\/operations\/(.+)$/.exec(path)
  if (operation) {
    const receipt = api.receipts.get(decodeURIComponent(operation[1]))
    return receipt ? json(response, 200, { state: "committed", receipt }) : json(response, 404, { error: "not found" })
  }
  return json(response, 404, { error: "not found" })
}

// ----- mock editor origin ------------------------------------------------------------------------

let editorMode = "normal"
const editorHits = []

/** A stand-in for the editor bundle: the bridge side of protocol 1 and a tiny cell form. */
function fakeEditor(mode) {
  const hello = `parent.postMessage({ type: "gridcraft:hello", protocol: 1, bundle: ${JSON.stringify(mode === "bad-digest" ? "e".repeat(64) : mode === "prefixed-digest" ? `sha256:${BUNDLE}` : BUNDLE)} }, parentOrigin)`
  return `<!doctype html><meta charset="utf-8"><title>Fake editor</title>
<label>Cell <input id="cell" value="B3"></label> <label>Value <input id="value"></label> <button id="commit">Commit</button>
<label>Draft <input id="draft"></label> <button id="away">Navigate away</button>
<pre id="state"></pre>
<script>
const parentOrigin = new URLSearchParams(location.search).get("parent_origin")
let port, rev = 0, seq = 0, base = null
const cells = {}
const log = { connects: 0, acks: [], rejected: [], committed: [], probes: {} }
try { log.probes.origin = self.origin } catch (e) { log.probes.origin = "error:" + e.name }
try { document.cookie = "tossed=1; Path=/"; log.probes.cookie = document.cookie } catch (e) { log.probes.cookie = "blocked:" + e.name }
try { log.probes.parent = parent.document.title } catch (e) { log.probes.parent = "blocked:" + e.name }
const render = () => { document.getElementById("state").textContent = JSON.stringify({ rev, cells, ...log }) }
function load(snapshot) {
  for (const key of Object.keys(cells)) delete cells[key]
  for (const band of Object.values(snapshot.sheets[0].cells.bands)) for (const [row, cols] of Object.entries(band.rows)) for (const [col, cell] of Object.entries(cols)) cells[String.fromCharCode(65 + Number(col)) + (Number(row) + 1)] = String(cell.value.v)
}
function handle(message) {
  if (message.type === "snapshot") { rev = message.rev; load(message.snapshot) }
  if (message.type === "committed") { rev = message.rev; for (const op of message.ops) cells[op.cell] = op.input; log.committed.push({ rev: message.rev, actor: message.actor_label }) }
  if (message.type === "ack") { rev = message.rev; log.acks.push(message.seq) }
  if (message.type === "rejected") { log.rejected.push({ seq: message.seq, code: message.code, cells: message.cells }); for (const c of message.cells) cells[c.cell] = c.input }
  render()
}
addEventListener("message", event => {
  if (event.source !== parent || event.origin !== parentOrigin || !event.data || event.data.type !== "gridcraft:connect" || port) return
  log.connects++
  port = event.ports[0]
  port.onmessage = m => handle(JSON.parse(m.data))
  render()
})
document.getElementById("value").addEventListener("focus", () => { if (base === null) base = rev })
// The open cell editor's address (never its text), as the editor reports an open draft.
const draft = cells => port && port.postMessage(JSON.stringify({ type: "draft", protocol: 1, cells }))
document.getElementById("value").addEventListener("input", () => draft([{ sheet: "Sheet1", cell: document.getElementById("cell").value }]))
document.getElementById("commit").addEventListener("click", () => {
  const cell = document.getElementById("cell").value, input = document.getElementById("value").value
  seq++
  port.postMessage(JSON.stringify({ type: "mutation", protocol: 1, seq, base_rev: base ?? rev, ops: [{ op: "cell.set", sheet: "Sheet1", cell, input }] }))
  cells[cell] = input
  base = null
  draft([])
  render()
})
document.getElementById("away").addEventListener("click", () => { location.href = "next.html" })
render()
${mode === "silent" ? "" : mode === "early-hello" ? hello : mode === "renavigate" ? `addEventListener("load", () => setTimeout(() => { location.href = "next.html" }, 0))` : `addEventListener("load", () => setTimeout(() => { ${hello} }, 0))`}
</script>`
}

let hostOrigin, editorOrigin
const editorServer = createServer((request, response) => {
  const url = new URL(request.url, "http://editor")
  editorHits.push(url.pathname)
  const page = url.pathname === `/b/${DIGEST}/index.html` ? fakeEditor(editorMode) : url.pathname === `/b/${DIGEST}/next.html` ? fakeEditor("normal") : null
  if (!page) { response.writeHead(404); return response.end() }
  // The editor origin's document headers (ADR §4.1), reduced to what this fake needs.
  response.writeHead(200, {
    "content-type": "text/html; charset=utf-8",
    "cache-control": "no-store",
    "content-security-policy": `default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; form-action 'none'; base-uri 'none'; frame-ancestors ${hostOrigin}; sandbox allow-scripts`,
    "cross-origin-resource-policy": "same-origin",
    "referrer-policy": "no-referrer",
    "x-content-type-options": "nosniff",
  })
  response.end(page)
})

// ----- host --------------------------------------------------------------------------------------

const output = await mkdtemp(join(tmpdir(), "crewship-workbook-editor-"))
let hostServer, browser
const results = []
const step = (name, detail = {}) => { results.push({ step: name, ...detail }); console.log(JSON.stringify({ step: name, ...detail })) }

try {
  await build({
    entryPoints: ["e2e/workbook-editor-host.fixture.tsx"], bundle: true, outfile: join(output, "bundle.js"), platform: "browser", format: "esm", jsx: "automatic",
    alias: { "@": process.cwd() }, define: { "process.env": '{"NODE_ENV":"test"}' }, logLevel: "error",
  })
  hostServer = createServer(async (request, response) => {
    const url = new URL(request.url, "http://host")
    const studioHeaders = {
      // Studio sends COEP credentialless and frames only the editor origin: a frame without the
      // credentialless attribute cannot load the editor document (CORP same-origin).
      "cross-origin-embedder-policy": "credentialless",
      "content-security-policy": `frame-src ${editorOrigin}; frame-ancestors 'none'`,
      "set-cookie": "sid=host-session; Path=/; SameSite=Lax; HttpOnly",
    }
    if (url.pathname === "/bundle.js") { response.writeHead(200, { "content-type": "text/javascript" }); return response.end(await readFile(join(output, "bundle.js"))) }
    if (url.pathname === "/negative") {
      // Harness control: the same editor URL framed by hand, with and without credentialless.
      response.writeHead(200, { "content-type": "text/html", ...studioHeaders })
      return response.end(`<title>Negative</title><script>
        window.frameLoads = (credentialless) => new Promise(done => {
          const f = document.createElement("iframe"); f.setAttribute("sandbox", "allow-scripts"); f.setAttribute("referrerpolicy", "no-referrer")
          if (credentialless) f.setAttribute("credentialless", "")
          let hello = false; addEventListener("message", e => { if (e.source === f.contentWindow) hello = true })
          f.src = ${JSON.stringify("EDITOR")}.replace("EDITOR", window.EDITOR) + "?host=authority&parent_origin=" + encodeURIComponent(location.origin) + "&v=1"
          document.body.append(f); setTimeout(() => done(hello), 2500)
        })</script>`)
    }
    const workbook = /^\/api\/v1\/pages\/(?:%40|@)page-1\/workbook(.*)$/.exec(url.pathname)
    if (workbook) return workbookApi(request, response, workbook[1], url.searchParams)
    if (url.pathname === "/api/v1/feature-flags") return json(response, 200, [{ id: "f1", key: "gridcraft_editor", description: null, enabled: api.flag, percentage: 100, created_at: "", updated_at: "" }])
    // The sign-in is valid: an access-token refresh succeeds. The editor must never need it for its
    // own session ending; the count proves that.
    if (url.pathname === "/api/auth/token/refresh" && request.method === "POST") { api.refreshes++; return json(response, 200, { ok: true }) }
    if (url.pathname.startsWith("/api/")) return json(response, 404, { error: "not mocked" })
    response.writeHead(200, { "content-type": "text/html", ...studioHeaders })
    response.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Host</title><div id="root"></div><script type="module" src="/bundle.js"></script>')
  })
  await new Promise(resolve => hostServer.listen(0, "127.0.0.1", resolve))
  await new Promise(resolve => editorServer.listen(0, "127.0.0.1", resolve))
  hostOrigin = `http://127.0.0.1:${hostServer.address().port}`
  // Another host name: a different origin, like the editor subdomain.
  editorOrigin = `http://localhost:${editorServer.address().port}`

  browser = await chromium.launch({ headless: true })
  const errors = []
  async function open(context, label) {
    const page = await context.newPage()
    page.on("pageerror", error => { errors.push(`${label}: ${error.message}`); console.error(`${label} page error:`, error.message) })
    await page.goto(hostOrigin)
    return page
  }
  const editorState = async page => JSON.parse(await page.frameLocator("iframe").locator("#state").textContent())
  const waitEditor = async (page, predicate, timeout = 5000) => {
    const started = Date.now()
    for (;;) {
      const state = await editorState(page).catch(() => null)
      if (state && predicate(state)) return { state, ms: Date.now() - started }
      if (Date.now() - started > timeout) throw new Error(`editor state not reached: ${JSON.stringify(state)}`)
      await page.waitForTimeout(50)
    }
  }
  const status = page => page.getByTestId("workbook-save-state").textContent()
  const unsent = page => page.getByLabel("Unsent edits").textContent()
  const commitCell = async (page, cell, value) => {
    const frame = page.frameLocator("iframe")
    await frame.locator("#cell").fill(cell)
    await frame.locator("#value").fill(value)
    await frame.locator("#commit").click()
  }

  // 0. Harness control: without the credentialless attribute the editor document cannot load here,
  //    so the attribute checks below are load-bearing, not decorative.
  {
    const page = await browser.newPage()
    await page.goto(`${hostOrigin}/negative`)
    await page.evaluate(origin => { window.EDITOR = origin }, `${editorOrigin}/b/${DIGEST}/index.html`)
    const without = await page.evaluate(() => window.frameLoads(false))
    const withAttribute = await page.evaluate(() => window.frameLoads(true))
    assert.equal(without, false)
    assert.equal(withAttribute, true)
    step("harness-control", { helloWithoutCredentialless: without, helloWithCredentialless: withAttribute })
    await page.close()
  }

  // PROBE: does focus leave the frame when a refusal arrives?
  const ctx = await browser.newContext()
  const a = await open(ctx, "A")
  await a.locator("iframe").waitFor()
  await waitEditor(a, s => s.connects === 1 && s.rev >= 1)
  await a.evaluate(() => {
    window.__focus = []
    const t0 = performance.now()
    addEventListener("focus", () => window.__focus.push(["window-focus", Math.round(performance.now() - t0), document.activeElement?.tagName]))
    addEventListener("blur", () => window.__focus.push(["window-blur", Math.round(performance.now() - t0)]))
    document.addEventListener("focusin", e => window.__focus.push(["focusin", Math.round(performance.now() - t0), e.target.tagName, e.target.getAttribute?.("data-kind") ?? e.target.textContent?.slice(0, 20)]))
  })
  const frameA = a.frameLocator("iframe")
  await frameA.locator("#cell").fill("B2")
  await frameA.locator("#value").focus()
  await frameA.locator("#value").fill("mine")
  agentCommit("B2", "7")
  await a.getByTestId("workbook-draft-conflict").getByText("Changed by Agent Ada while you were editing").waitFor({ timeout: 5000 })
  await frameA.locator("#commit").click()
  await waitEditor(a, s => s.rejected.length === 1, 3000)
  await a.getByText("Not saved · a change was refused").waitFor()
  await a.waitForTimeout(500)
  const active = await a.evaluate(() => ({ active: document.activeElement?.tagName, hasFocus: document.hasFocus(), log: window.__focus }))
  const frameFocus = await frameA.locator("#commit").evaluate(() => ({ active: document.activeElement?.id, hasFocus: document.hasFocus() }))
  console.log("PROBE", JSON.stringify({ host: active, frame: frameFocus }))
} finally {
  await browser?.close()
  await new Promise(resolve => hostServer ? hostServer.close(resolve) : resolve())
  await new Promise(resolve => editorServer.listening ? editorServer.close(resolve) : resolve())
  await rm(output, { recursive: true, force: true })
}
