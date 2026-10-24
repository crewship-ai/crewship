// Isolated production-component browser test of the embedded workbook editor host.
// Two local origins: the host page (127.0.0.1) and a mock editor origin (localhost) serving a
// minimal fake editor with the editor origin's sandbox headers. The workbook API is an in-memory
// mock with per-cell conflicts; an agent edit is a commit appended to the mocked commits feed.
// No live instance, provider or credentials are used.
// Run: crewship-heavy node e2e/workbook-editor-host.browser.mjs
import { build } from "esbuild"
import postcss from "postcss"
import tailwindcss from "@tailwindcss/postcss"
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
  cells: new Map([["A1", "Item"], ["B2", "10"]]),
  /** rev → commit; rev 1 is the import. */
  commits: [{ rev: 1, kind: "snapshot" }],
  /** cell → { rev, writer } of its last write. */
  writes: new Map(),
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
  for (const op of ops) {
    if (typeof op.cell !== "string" || !/^[A-Z]{1,3}[1-9][0-9]{0,6}$/.test(op.cell)) throw new Error("Invalid fixture cell address")
  }
  api.rev++
  for (const op of ops) { api.cells.set(op.cell, op.input); api.writes.set(op.cell, { rev: api.rev, writer }) }
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
  for (const [cell, input] of api.cells) {
    const { row, col } = address(cell)
    ;(rows[row] ??= {})[col] = { value: { t: "Text", v: input }, formula: null, style: 0 }
  }
  return { sheets: [{ name: "Sheet1", cells: { bands: { 0: { rows } }, len: api.cells.size } }], active_sheet: 0 }
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
      const last = api.writes.get(op.cell)
      return last && last.rev > edit.base_rev && last.writer !== edit.session_id ? [{ cell: op.cell, input: api.cells.get(op.cell), rev: last.rev, actor_label: last.writer.startsWith("agent:") ? "Agent Ada" : "Owner" }] : []
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
  // Only static markup selects a fixture mode; no data is interpolated into JavaScript.
  const modes = {
    normal: '<meta name="fixture-mode" content="normal">',
    "bad-digest": '<meta name="fixture-mode" content="bad-digest">',
    "prefixed-digest": '<meta name="fixture-mode" content="prefixed-digest">',
    "early-hello": '<meta name="fixture-mode" content="early-hello">',
    renavigate: '<meta name="fixture-mode" content="renavigate">',
    silent: '<meta name="fixture-mode" content="silent">',
  }
  assert.ok(Object.hasOwn(modes, mode), "known static editor fixture mode")
  return '<!doctype html><meta charset="utf-8">' + modes[mode] + `<title>Fake editor</title>
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
const fixtureMode = document.querySelector('meta[name="fixture-mode"]').content
const hello = () => parent.postMessage({ type: "gridcraft:hello", protocol: 1,
  bundle: fixtureMode === "bad-digest" ? "e".repeat(64) : fixtureMode === "prefixed-digest" ? "sha256:" + "c".repeat(64) : "c".repeat(64),
}, parentOrigin)
if (fixtureMode === "early-hello") hello()
else if (fixtureMode === "renavigate") addEventListener("load", () => setTimeout(() => { location.href = "next.html" }, 0))
else if (fixtureMode !== "silent") addEventListener("load", () => setTimeout(hello, 0))
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
  const css = await postcss([tailwindcss()]).process(await readFile("app/globals.css", "utf8"), { from: "app/globals.css" })
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
    if (url.pathname === "/host.css") { response.writeHead(200, { "content-type": "text/css" }); return response.end(css.css) }
    const workbook = /^\/api\/v1\/pages\/(?:%40|@)page-1\/workbook(.*)$/.exec(url.pathname)
    if (workbook) return workbookApi(request, response, workbook[1], url.searchParams)
    if (url.pathname === "/api/v1/feature-flags") return json(response, 200, [{ id: "f1", key: "gridcraft_editor", description: null, enabled: api.flag, percentage: 100, created_at: "", updated_at: "" }])
    // The sign-in is valid: an access-token refresh succeeds. The editor must never need it for its
    // own session ending; the count proves that.
    if (url.pathname === "/api/auth/token/refresh" && request.method === "POST") { api.refreshes++; return json(response, 200, { ok: true }) }
    if (url.pathname.startsWith("/api/")) return json(response, 404, { error: "not mocked" })
    response.writeHead(200, { "content-type": "text/html", ...studioHeaders })
    response.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Host</title><link rel="stylesheet" href="/host.css"><div id="root"></div><script type="module" src="/bundle.js"></script>')
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

  const contextA = await browser.newContext({ viewport: { width: 320, height: 640 } })
  const contextB = await browser.newContext()
  const a = await open(contextA, "A")
  const b = await open(contextB, "B")

  // 1. The live DOM element carries exactly the required attributes.
  for (const [label, page] of [["A", a], ["B", b]]) {
    await page.locator("iframe").waitFor()
    const dom = await page.locator("iframe").evaluate(frame => ({
      sandbox: frame.getAttribute("sandbox"), tokens: [...frame.sandbox], credentialless: frame.credentialless,
      credentiallessAttribute: frame.hasAttribute("credentialless"), referrerPolicy: frame.referrerPolicy, referrerpolicy: frame.getAttribute("referrerpolicy"), src: frame.src,
    }))
    assert.equal(dom.sandbox, "allow-scripts")
    assert.deepEqual(dom.tokens, ["allow-scripts"])
    assert.equal(dom.credentialless, true)
    assert.equal(dom.credentiallessAttribute, true)
    assert.equal(dom.referrerPolicy, "no-referrer")
    assert.equal(dom.referrerpolicy, "no-referrer")
    assert.equal(new URL(dom.src).origin, editorOrigin)
    assert.ok(!dom.src.includes("ws_mock"), "no session id in the frame URL")
    const { state } = await waitEditor(page, s => s.connects === 1 && s.rev === 1)
    // The editor document runs in an opaque origin: no cookies, no reach into the host.
    assert.equal(state.probes.origin, "null")
    assert.match(state.probes.cookie, /^blocked:/)
    assert.match(state.probes.parent, /^blocked:/)
    assert.equal(state.cells.B2, "10")
    step("frame-attributes", { context: label, ...dom, src: undefined, probes: state.probes })
  }
  assert.equal(await status(a), "No unsaved changes")

  // 2. A commits B3=21: "Saving…" while the server holds the edit, "Saved" only after its receipt.
  let release
  api.hold = new Promise(resolve => { release = resolve })
  await commitCell(a, "B3", "21")
  await a.getByText("Saving…").waitFor()
  assert.equal(await unsent(a), "true")
  const guarded = await a.evaluate(() => { const event = new Event("beforeunload", { cancelable: true }); dispatchEvent(event); return event.defaultPrevented })
  assert.equal(guarded, true)
  assert.notEqual(await status(a), "Saved")
  const committedAt = Date.now()
  api.hold = undefined
  release()
  await a.getByText("Saved", { exact: true }).waitFor()
  assert.equal(await unsent(a), "false")
  const { state: ackedA } = await waitEditor(a, s => s.acks.includes(1))
  assert.equal(ackedA.rev, 2)
  const seenByB = await waitEditor(b, s => s.cells.B3 === "21", 3000)
  const bLatency = Date.now() - committedAt
  assert.ok(bLatency <= 3000, `B saw the commit after ${bLatency} ms`)
  step("save-state-and-leave-guard", { savingWhileHeld: true, leaveGuardWhilePending: guarded, savedAfterReceipt: true, ackRev: ackedA.rev, bSeesB3Ms: bLatency, bRev: seenByB.state.rev })

  // 3. Agent edit while A has a draft open: both windows see it, A's draft and focus survive.
  const frameA = a.frameLocator("iframe")
  await frameA.locator("#draft").fill("draft")
  await frameA.locator("#draft").focus()
  const agentAt = Date.now()
  const agentRev = agentCommit("B2", "99")
  const inA = await waitEditor(a, s => s.cells.B2 === "99", 3000)
  const inB = await waitEditor(b, s => s.cells.B2 === "99", 3000)
  assert.equal(await frameA.locator("#draft").inputValue(), "draft")
  assert.equal(await frameA.locator("#draft").evaluate(input => document.activeElement === input), true)
  assert.deepEqual(inA.state.committed.at(-1), { rev: agentRev, actor: "Agent Ada" })
  step("agent-edit", { rev: agentRev, aMs: inA.ms, bMsAfterA: inB.ms, totalMs: Date.now() - agentAt, draftKept: true, focusKept: true })

  // 4. A edits B2 from an older revision while the agent changes it: refused with the server value.
  await frameA.locator("#cell").fill("B2")
  await frameA.locator("#value").focus() // the edit starts at the current revision
  await frameA.locator("#value").fill("mine")
  const frameBeforeConflict = await a.locator("iframe").boundingBox()
  const conflictRev = agentCommit("B2", "7")
  // The person is warned while the draft is still open, before any commit; the draft stays.
  const warning = a.getByTestId("workbook-draft-conflict")
  await warning.getByText("Changed by Agent Ada while you were editing").waitFor({ timeout: 5000 })
  assert.deepEqual(await a.locator("iframe").boundingBox(), frameBeforeConflict, "draft warning must not move or resize the editor under a pointer gesture")
  assert.equal(await frameA.locator("#value").inputValue(), "mine")
  await frameA.locator("#commit").click()
  const refused = await waitEditor(a, s => s.rejected.length === 1, 3000)
  assert.equal(refused.state.rejected[0].code, "precondition_failed")
  assert.deepEqual(refused.state.rejected[0].cells, [{ cell: "B2", input: "7", rev: conflictRev, actor_label: "Agent Ada" }])
  assert.equal(refused.state.cells.B2, "7")
  await a.getByText("Not saved · a change was refused").waitFor()
  // At commit the refusal names the writer and keeps the person's value until they choose.
  await warning.getByRole("button", { name: "Keep mine for Sheet1!B2" }).waitFor()
  assert.match(await warning.textContent(), /Yours: .?mine.? · Theirs: .?7/)
  assert.deepEqual(await a.locator("iframe").boundingBox(), frameBeforeConflict, "refusal must not move or resize the editor while the next input arrives")
  // Until the person decides, their value exists only in the page: leaving is guarded.
  assert.equal(await unsent(a), "true")
  await warning.getByRole("button", { name: "Take theirs for Sheet1!B2" }).click()
  await warning.waitFor({ state: "detached" })
  // The leave guard follows the save state one render later.
  await a.getByLabel("Unsent edits").filter({ hasText: "false" }).waitFor({ timeout: 3000 })
  await waitEditor(b, s => s.cells.B2 === "7", 3000)
  step("conflict", { refused: refused.state.rejected[0], aStatus: await status(a), warnedBeforeCommit: true, keepOrTakeOffered: true })

  // 5. B's frame navigates itself after the handshake: frame removed, session closed, port not re-sent.
  const closedBefore = api.closed.length
  await b.frameLocator("iframe").locator("#away").click()
  await b.locator("iframe").waitFor({ state: "detached" })
  await b.getByTestId("workbook-editor-notice").waitFor()
  await b.getByRole("button", { name: "Open editor again" }).waitFor()
  await b.waitForFunction(() => document.querySelectorAll("iframe").length === 0)
  for (let i = 0; i < 40 && api.closed.length === closedBefore; i++) await b.waitForTimeout(50)
  assert.equal(api.closed.length, closedBefore + 1)
  assert.equal(await b.getByTestId("workbook-editor-notice").getAttribute("data-reason"), "navigation")
  assert.ok(editorHits.includes(`/b/${DIGEST}/next.html`), "the frame did navigate")
  step("navigation-kills", { reason: "navigation", frameRemoved: true, sessionClosed: api.closed.at(-1), notice: await b.getByTestId("workbook-editor-notice").textContent() })

  // 6. Hellos the host must refuse, each in a fresh page: the frame is removed and never connected.
  for (const [mode, reason] of [["bad-digest", "bundle"], ["prefixed-digest", "bundle"], ["early-hello", "handshake"], ["renavigate", "navigation"]]) {
    editorMode = mode
    const page = await open(contextA, mode)
    await page.getByTestId("workbook-editor-notice").waitFor()
    assert.equal(await page.locator("iframe").count(), 0)
    assert.equal(await page.getByTestId("workbook-editor-notice").getAttribute("data-reason"), reason)
    step("refused-handshake", { mode, reason, frameRemoved: true })
    await page.close()
  }
  editorMode = "normal"

  const notice = page => page.getByTestId("workbook-editor-notice")
  const reasonOf = page => notice(page).getAttribute("data-reason")
  const waitClosed = async (before, timeout = 3000) => {
    for (const started = Date.now(); api.closed.length === before && Date.now() - started < timeout;) await new Promise(resolve => setTimeout(resolve, 50))
    return api.closed.length - before
  }
  const editsFor = cell => api.editLog.filter(edit => edit.ops.some(op => op.cell === cell))

  // 8. The snapshot cannot be read at connect: preview with a notice and a reopen button, never a
  //    blank "Connecting…"; reopening works once the server answers again.
  {
    api.snapshotFails = true
    const closedBefore = api.closed.length
    const page = await open(contextA, "snapshot-failure")
    await notice(page).waitFor()
    assert.equal(await reasonOf(page), "load")
    assert.equal(await page.locator("iframe").count(), 0)
    assert.equal(await page.getByTestId("workbook-save-state").count(), 0, "no editor status line is left behind")
    assert.equal(await waitClosed(closedBefore), 1)
    const text = await notice(page).textContent()
    api.snapshotFails = false
    await page.getByRole("button", { name: "Open editor again" }).click()
    const { state } = await waitEditor(page, s => s.connects === 1 && s.rev === api.rev)
    step("snapshot-failure-falls-back", { reason: "load", notice: text, sessionClosed: true, reopenedRev: state.rev })
    await page.close()
  }

  // 9. The editor never says hello: the handshake deadline ends the session and shows the preview.
  {
    editorMode = "silent"
    const closedBefore = api.closed.length
    const started = Date.now()
    const page = await open(contextA, "silent")
    await page.locator("iframe").waitFor()
    await notice(page).waitFor({ timeout: 25_000 })
    const ms = Date.now() - started
    assert.equal(await reasonOf(page), "timeout")
    assert.equal(await page.locator("iframe").count(), 0)
    assert.equal(await waitClosed(closedBefore), 1)
    assert.ok(ms >= 14_000, `timed out after ${ms} ms`)
    await page.getByRole("button", { name: "Open editor again" }).waitFor()
    await page.getByRole("table", { name: "Sheet Sheet1" }).waitFor()
    step("handshake-timeout", { reason: "timeout", ms, frames: 0, sessionClosed: true })
    await page.close()
    editorMode = "normal"
  }

  // 10. The editor is turned off while open: an edit answered 503 editor-disabled is not retried,
  //     and the head poll finds it on its own; both kill the frame and show the preview.
  for (const [scope, cell] of [["edits", "C1"], ["all", null]]) {
    const page = await open(contextA, `disabled-${scope}`)
    await waitEditor(page, s => s.connects === 1)
    const closedBefore = api.closed.length
    api.disabled = scope
    if (cell) await commitCell(page, cell, "x")
    await notice(page).waitFor({ timeout: 5000 })
    assert.equal(await reasonOf(page), "disabled")
    assert.equal(await page.locator("iframe").count(), 0)
    assert.equal(await page.getByRole("button", { name: "Open editor again" }).count(), 0)
    await page.waitForTimeout(1500)
    const attempts = cell ? editsFor(cell).length : 0
    if (cell) assert.equal(attempts, 1, "a disabled editor is not retried")
    await waitClosed(closedBefore)
    step("editor-disabled-kills", { scope, reason: "disabled", attempts, frames: 0, notice: await notice(page).textContent() })
    api.disabled = undefined
    await page.close()
  }

  // 11. The bundle is revoked while open: the recheck on focus kills the frame.
  {
    const page = await open(contextA, "bundle-revoked")
    await waitEditor(page, s => s.connects === 1)
    api.bundle = "f".repeat(64)
    for (let i = 0; i < 160 && await notice(page).count() === 0; i++) {
      await page.evaluate(() => dispatchEvent(new Event("focus")))
      await page.waitForTimeout(50)
    }
    assert.equal(await reasonOf(page), "disabled")
    assert.equal(await page.locator("iframe").count(), 0)
    step("bundle-revoked-kills", { reason: "disabled", frames: 0 })
    api.bundle = BUNDLE
    await page.close()
  }

  // 12. 200 without a durable receipt: a non-durable receipt is settled by the commits feed; an
  //     unreadable answer is retried with the same mutation, then refused, and later edits still save.
  {
    const page = await open(contextA, "unconfirmed")
    await waitEditor(page, s => s.connects === 1)
    api.editMode = "memory"
    await commitCell(page, "C2", "memory")
    await page.getByText("Saved", { exact: true }).waitFor({ timeout: 8000 })
    await waitEditor(page, s => s.acks.includes(1), 3000)
    assert.equal(api.cells.get("C2"), "memory")

    api.editMode = "unreadable"
    await commitCell(page, "C3", "lost")
    const { state } = await waitEditor(page, s => s.rejected.some(r => r.code === "not_confirmed"), 15_000)
    await page.getByText("Not saved · the server did not confirm a change").waitFor()
    const c3 = editsFor("C3")
    assert.equal(c3.length, 5)
    assert.equal(new Set(c3.map(edit => edit.mutation_id)).size, 1, "every retry is the same mutation")

    api.editMode = "normal"
    await commitCell(page, "C4", "later")
    await waitEditor(page, s => s.acks.length >= 2, 5000)
    assert.equal(api.cells.get("C4"), "later")
    // The later edit is saved, but the unconfirmed one still waits for the person: never "Saved" meanwhile.
    await page.getByText("Not saved · a change was refused").waitFor({ timeout: 5000 })
    await page.getByRole("button", { name: "Keep mine for Sheet1!C3" }).click()
    await page.getByText("Saved", { exact: true }).waitFor({ timeout: 5000 })
    assert.equal(api.cells.get("C3"), "lost")
    assert.equal(await page.locator("iframe").count(), 1)
    step("unconfirmed-bounded", { memorySettledByFeed: true, unreadableAttempts: c3.length, rejected: state.rejected.at(-1).code, laterEditSaved: true, refusalKeptUntilDecided: true })
    await page.close()
  }

  // 13. The session expires while an edit is pending: the host opens a new session, sends the edit
  //     again under it and keeps the frame; the heartbeat finding it expired does the same.
  {
    const page = await open(contextA, "session-expired")
    await waitEditor(page, s => s.connects === 1)
    const first = api.lastSession
    api.sessions.get(first).open = false
    const openedBefore = api.opened, refreshesBefore = api.refreshes
    await commitCell(page, "D1", "after-expiry")
    await page.getByText("Saved", { exact: true }).waitFor({ timeout: 5000 })
    const d1 = editsFor("D1")
    assert.equal(api.cells.get("D1"), "after-expiry")
    assert.equal(api.opened, openedBefore + 1)
    // The editor's own expiry never reaches the app-wide sign-in handling: no token refresh, no replay.
    assert.deepEqual(d1.map(edit => edit.session_id), [first, api.lastSession])
    assert.equal(api.refreshes, refreshesBefore)
    assert.deepEqual(d1.map(edit => edit.mutation_id.split(":")[1]), ["1", "1"], "same frame seq under the new session")
    assert.equal(await page.locator("iframe").count(), 1)
    assert.equal(await notice(page).count(), 0)
    const second = api.lastSession
    api.sessions.get(second).open = false
    for (let i = 0; i < 160 && api.lastSession === second; i++) {
      await page.evaluate(() => dispatchEvent(new Event("focus")))
      await page.waitForTimeout(50)
    }
    assert.notEqual(api.lastSession, second, "the heartbeat opened a new session")
    assert.equal(await page.locator("iframe").count(), 1)
    step("session-expired-reopens", { edits: d1.map(edit => edit.mutation_id.replace(/^ws_mock\d+/, "ws_…")), reopenedByHeartbeat: true, frames: 1 })
    await page.close()
  }

  // 7. Flag off: the read-only preview, no session, no frame.
  api.flag = false
  const openedBefore = api.opened
  const preview = await open(contextB, "preview")
  await preview.getByText("Read-only preview. Edit by importing a file or through an admitted agent.").waitFor()
  await preview.getByRole("table", { name: "Sheet Sheet1" }).waitFor()
  await preview.waitForTimeout(300)
  assert.equal(await preview.locator("iframe").count(), 0)
  assert.equal(api.opened, openedBefore)
  step("flag-off-preview", { frames: 0, sessionsOpened: api.opened - openedBefore })

  assert.deepEqual(errors, [])
  console.log(JSON.stringify({ result: "PASS", steps: results.length }))
} finally {
  await browser?.close()
  await new Promise(resolve => hostServer ? hostServer.close(resolve) : resolve())
  await new Promise(resolve => editorServer.listening ? editorServer.close(resolve) : resolve())
  await rm(output, { recursive: true, force: true })
}
