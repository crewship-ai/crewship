// Native walk (docs/development/prd-acceptance.md, "Native walk"): a short real working cycle in
// the native Word and Sheet editors in full headed Chromium against a deployed origin, on a
// disposable workspace deleted at the end. Run it with `pnpm acceptance:native-walk`; verdict.mjs
// decides whether the run counts.
//
// A violation is recorded and the walk goes on, so one run shows every broken rule; the test
// fails at the end on any violation. Only a walk that cannot continue aborts.
import { chromium, type Frame, type Page } from "@playwright/test"
import { readFacts } from "../prd/craft-lease/sheet-workbook"
import { holdsMarker, isUnconfirmedStatus, labelVersion, pendingReplies } from "../prd/craft-session/confirm"
import { walk, wordFacts } from "../prd/craft-session/facts"
import { readAppBuild, type AppBuild } from "../prd/app-sha"
import { apiAs, loginStorageState } from "../prd/auth"
import { expect, prdRequirement, test, type PrdScenario } from "../prd/fixtures"
import { disposeActors, isolatedActor } from "../prd/outage-lostack/actor"
import * as N from "../prd/outage-lostack/native"
import { EVIDENCE_FILE, HISTORY_NOT_ENABLED, SCENARIOS } from "./verdict.mjs"

const CONFIRM_MS = 30_000 // a save the status must confirm
const RECOVER_MS = 60_000 // an unconfirmed result must clear by itself within this
const QUIET_MS = 5_000 // watched after a press or a recovery for a late version
const SETTLE_QUIET_MS = 1_500 // no save sent for this long before a step counts the versions
const STEP_MS = 3 * 60_000
const WALK_MS = 9 * 60_000
const RENDER_MS = 15_000
const LEASE_MS = 90_000 // the server caps a lease TTL at one minute
const BLANK_DOMINANT = 0.998 // share of the commonest colour at which the area counts as blank
const VERIFY = "Ověřit výsledek"
// At the 1440x1000 viewport, in the editor frame.
const WORD_UI = { homeTab: { x: 76, y: 53 }, heading1: { x: 918, y: 94 }, noSpacing: { x: 844, y: 94 } }
const BODY = { wordcraft: { x: 200, y: 215, width: 800, height: 480 }, gridcraft: { x: 60, y: 215, width: 800, height: 300 } }

type Row = {
  name: string; at: string; inputMs: number | null; confirmationMs: number | null; longestStallMs: number; verifyPresses: number
  versionBefore: string | null; versionAfter: string | null; status: string; saveRequests: number; acks: number; statusTrail: string[]; note?: unknown
}
type PortMsg = { at: number; type: string }
type Probe = { __gaps: { at: number; ms: number }[]; __port: PortMsg[]; __status: { at: number; s: string }[] }

// Runs in every frame: render gaps, and (type only, never content) the save messages on the editor port.
const spy = () => {
  const w = window as unknown as Probe
  w.__gaps = []; w.__port = []; w.__status = []
  let last = performance.now()
  const tick = () => { const now = performance.now(); if (now - last > 100) w.__gaps.push({ at: Date.now(), ms: Math.round(now - last) }); last = now; requestAnimationFrame(tick) }
  requestAnimationFrame(tick)
  const post = MessagePort.prototype.postMessage as (...a: unknown[]) => void
  MessagePort.prototype.postMessage = function (this: MessagePort, ...a: unknown[]) {
    const type = (a[0] as { type?: unknown } | null)?.type
    if (typeof type === "string" && /:(save-request|saved|save-ack|save-failed)$/.test(type)) w.__port.push({ at: Date.now(), type })
    return post.apply(this, a)
  } as typeof MessagePort.prototype.postMessage
  if (window.top !== window) return
  let prev = ""
  const log = () => {
    const el = document.querySelector('[role="status"]')
    const s = el ? `${(el.textContent ?? "").slice(0, 40)}|${el.getAttribute("data-save-phase") ?? ""}` : ""
    if (s !== prev) { prev = s; w.__status.push({ at: Date.now(), s }) }
  }
  new MutationObserver(log).observe(document, { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ["data-save-phase"] })
}

/** The build the origin serves now, through a login of its own (same source as global setup). */
async function deployedBuild(prd: PrdScenario): Promise<AppBuild> {
  try {
    const owner = prd.env.accounts.owner
    if (!owner) return { sha: "unknown", version: null, dirty: null, error: "owner account missing" }
    const api = await apiAs(prd.origin, await loginStorageState(prd.origin, owner))
    try {
      return await readAppBuild(api)
    } finally {
      await api.dispose()
    }
  } catch (err) {
    return { sha: "unknown", version: null, dirty: null, error: String(err).slice(0, 200) }
  }
}

const bounded = <T>(what: string, ms: number, p: Promise<T>) => {
  let t: NodeJS.Timeout
  return Promise.race([p, new Promise<never>((_, no) => { t = setTimeout(() => no(new Error(`${what}: stalled past ${ms} ms`)), ms) })]).finally(() => clearTimeout(t))
}

/** Share of the commonest colour in the area, measured from a real screenshot of it. */
async function uniformity(page: Page, png: Buffer): Promise<{ dominant: number; colours: number }> {
  return page.evaluate(async (b64) => {
    const bin = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))
    const bmp = await createImageBitmap(new Blob([bin], { type: "image/png" }))
    const c = new OffscreenCanvas(bmp.width, bmp.height).getContext("2d")!
    c.drawImage(bmp, 0, 0)
    const d = c.getImageData(0, 0, bmp.width, bmp.height).data, counts = new Map<number, number>()
    for (let i = 0; i < d.length; i += 4) { const k = (d[i] << 16) | (d[i + 1] << 8) | d[i + 2]; counts.set(k, (counts.get(k) ?? 0) + 1) }
    return { dominant: Math.max(...counts.values()) / (d.length / 4), colours: counts.size }
  }, png.toString("base64"))
}

for (const scenario of SCENARIOS) {
  const app = scenario.app as N.NativeApp
  const label = N.APP_LABEL[app]
  test(scenario.test, { annotation: prdRequirement(scenario.id, scenario.title) }, async ({ prd }) => {
    test.setTimeout(WALK_MS + 90_000)
    const exe = process.env.NATIVE_WALK_CHROME
    if (!exe) throw new Error("NATIVE_WALK_CHROME is not set: run through `pnpm acceptance:native-walk`")
    const browser = await chromium.launch({ headless: false, executablePath: exe })
    const violations: { step: string; rule: string }[] = []
    const steps: Row[] = []
    const lines: string[] = []
    const out: Record<string, unknown> = { app, label, chromium: { path: exe, version: browser.version(), headless: false }, appSha: prd.appSha, origin: prd.origin, runId: prd.run.runId, startedAt: new Date().toISOString() }
    const opsRef: { ops: N.Op[] } = { ops: [] }
    let failure: string | undefined
    // A deploy between global setup and this walk, or during it, must not be stamped with the RC.
    const build: { start: AppBuild; end: AppBuild | null } = { start: await deployedBuild(prd), end: null }
    const pageErrors: { at: string; step: string; message: string }[] = []
    let releaseStatus: string | null = null // "ok", or why the release failed; null when the walk never got there
    let proofAt: number | null = null // ops recorded when the version proof was taken
    try {
      await bounded(`${label} walk`, WALK_MS, (async () => {
        const { context, page, api, ws } = await isolatedActor(prd, browser, `native walk ${label}`, { viewport: N.VIEWPORT })
        page.setDefaultTimeout(60_000)
        await context.addInitScript(spy)
        expect((await N.nativeEnabled(api, ws.id)).enabled, "native editing is on").toBe(true)
        const ref = await N.createNative(api, ws.id, app, `PRD native walk ${label}`)
        out.initVersion = ref.initVersion
        const ops = (opsRef.ops = N.instrument(context))
        page.on("pageerror", (e) => pageErrors.push({ at: new Date().toISOString(), step: steps.at(-1)?.name ?? "before the first step", message: String(e).slice(0, 240) }))
        page.on("console", (m) => { if (m.type() === "error") lines.push(`${new Date().toISOString()} error ${m.text().slice(0, 200)}`) })

        const tag = Math.random().toString(36).slice(2, 7)
        const kb = page.keyboard
        const marks: string[] = []
        const mark = () => { const m = `${String.fromCharCode(65 + marks.length)}WALK${tag}`; marks.push(m); return m }
        const nextCell = () => `A${marks.length + 1}`
        const missing = (text: string) => marks.filter((m) => !holdsMarker(text, m))
        let f: Frame
        let row: Row
        let presses = 0
        const violate = (rule: string) => { violations.push({ step: row.name, rule }); console.log(`[walk] VIOLATION ${label} / ${row.name}: ${rule}`) }
        const check = (ok: boolean, rule: string) => { if (!ok) violate(rule) }

        const drain = async () => {
          const frames = page.frames()
          const parts = await Promise.all(frames.map((x) => bounded("drain", 30_000, x.evaluate(() => {
            const w = window as unknown as Partial<Probe>
            const r = { gaps: w.__gaps ?? [], port: w.__port ?? [], status: w.__status ?? [] }
            w.__gaps = []; w.__port = []; w.__status = []
            return r
          })).catch(() => ({ gaps: [], port: [] as PortMsg[], status: [] as Probe["__status"] }))))
          return { gaps: parts.flatMap((p) => p.gaps), port: parts.flatMap((p) => p.port).sort((a, b) => a.at - b.at), status: parts.flatMap((p) => p.status).sort((a, b) => a.at - b.at) }
        }
        type Drained = Awaited<ReturnType<typeof drain>>
        let seen: Drained = { gaps: [], port: [], status: [] }
        const collect = async () => { const d = await drain(); seen = { gaps: [...seen.gaps, ...d.gaps], port: [...seen.port, ...d.port], status: [...seen.status, ...d.status] }; return seen }

        const step = async (name: string, fn: (r: Row) => Promise<void>) => {
          row = { name, at: new Date().toISOString(), inputMs: null, confirmationMs: null, longestStallMs: 0, verifyPresses: 0, versionBefore: null, versionAfter: null, status: "", saveRequests: 0, acks: 0, statusTrail: [] }
          steps.push(row)
          const r = row
          console.log(`[walk] ${label} / ${name}`)
          await collect()
          seen = { gaps: [], port: [], status: [] }
          r.versionBefore = (await N.readHead(api, ref)).version
          const before = presses
          try {
            await bounded(`${label} / ${name}`, STEP_MS, fn(r))
          } finally {
            const t = Date.now()
            const d = await collect().catch(() => seen)
            r.longestStallMs = Math.max(Date.now() - t, ...d.gaps.map((g) => g.ms))
            r.saveRequests = d.port.filter((p) => /:save-request$/.test(p.type)).length
            r.acks = d.port.filter((p) => /:(saved|save-ack)$/.test(p.type)).length
            r.statusTrail = d.status.map((s) => s.s)
            r.verifyPresses = presses - before
            r.status = (await N.status(page).catch(() => "?")).slice(0, 60)
            r.versionAfter = await N.readHead(api, ref).then((h) => h.version).catch(() => null)
          }
        }
        /** Time spent on input, up to the frame answering again. */
        const input = async (r: Row, fn: () => Promise<void>) => {
          const s = Date.now()
          await fn()
          await f.evaluate(() => 0)
          r.inputMs = (r.inputMs ?? 0) + Date.now() - s
          return Date.now()
        }
        const press = async () => {
          const button = page.getByRole("button", { name: VERIFY, exact: true })
          await button.click({ timeout: 5_000 })
          presses++
          violate(`pressed "${VERIFY}": the result did not clear by itself within ${RECOVER_MS} ms`)
        }
        /** Waits for a saved label newer than `after`. Never presses until the recovery budget has passed. */
        const confirm = async (r: Row, from: number, after: string, budget = CONFIRM_MS): Promise<string | undefined> => {
          let end = Date.now() + budget, unsure = false, pressed = false
          while (Date.now() < end) {
            const st = await N.status(page)
            const v = labelVersion(st)
            if (v && BigInt(v) > BigInt(after)) { r.confirmationMs = Date.now() - from; return v }
            if (isUnconfirmedStatus(st) && !unsure) { unsure = true; end = Math.max(end, Date.now() + RECOVER_MS) }
            if (unsure && !pressed && Date.now() >= end - 500) { pressed = true; await press(); end = Date.now() + 15_000 }
            await N.pause(50)
          }
          return undefined
        }
        /** Saved, nothing on the wire, head equal to the label, and no save for a moment. */
        const settle = async (timeout = CONFIRM_MS) => {
          const end = Date.now() + timeout
          let st = ""
          for (; Date.now() < end; await N.pause(300)) {
            st = await N.status(page)
            const v = labelVersion(st), last = ops.at(-1)
            if (!v || pendingReplies(ops).length || (last && Date.now() - Date.parse(last.sentAt) < SETTLE_QUIET_MS)) continue
            const h = await N.readHead(api, ref)
            if (h.version === v) return h
          }
          throw new Error(`not settled within ${timeout} ms: status ${st.slice(0, 60)}, pending ${pendingReplies(ops).join(",")}`)
        }
        const committed = (from: number) => ops.slice(from).filter((o) => o.kind === "save" && o.status === 200 && o.version !== undefined)
        const pressSeen = async (at: number) => (await collect()).port.some((p) => p.at >= at && /:save-request$/.test(p.type))
        const ackAt = async (at: number) => (await collect()).port.find((p) => p.at >= at && /:(saved|save-ack)$/.test(p.type))?.at
        const type = () => { const cell = nextCell(); const m = mark(); return { m, go: () => N.typeMarker(page, f, app, m, cell) } }
        const cellOf = (m: string) => `A${marks.indexOf(m) + 1}`
        const inCell = (raw: string, m: string) => {
          const facts = readFacts(Buffer.from(raw))
          return facts.cells[`${facts.sheets[0]}!${cellOf(m)}`]?.value === m
        }
        const heldAt = (h: N.Head, r: string) => check(!missing(h.text).length, `${r}: head v${h.version} lacks ${missing(h.text).join(", ")}`)

        await step("open and take the editor", async (r) => {
          const s = Date.now()
          await N.openEditor(page, ref)
          f = await N.acquire(page, app)
          r.inputMs = Date.now() - s
        })

        // Content: everything here autosaves as it likes; the versions are read after.
        let lastInput = 0
        let wordCheck: (raw: string) => { heading: boolean; bold: boolean; style: string } = () => ({ heading: false, bold: false, style: "" })
        if (app === "wordcraft") {
          const canvas = () => f.locator("canvas").first()
          const click = async (at: { x: number; y: number }) => { await canvas().click({ position: at }); await page.waitForTimeout(700) }
          const m = { text: mark(), more: mark(), heading: mark(), style: mark(), bold: mark() }
          await step("home tab", async (r) => { lastInput = await input(r, async () => { await kb.press("Control+End"); await click(WORD_UI.homeTab); await kb.press("Control+End") }) })
          await step("type text", async (r) => { lastInput = await input(r, () => kb.type(m.text)) })
          await step("enter, more text", async (r) => { lastInput = await input(r, async () => { await kb.press("Enter"); await kb.type(m.more) }) })
          await step("heading from the ribbon", async (r) => { lastInput = await input(r, async () => { await kb.press("Enter"); await kb.type(m.heading); await click(WORD_UI.heading1); await kb.press("End"); await kb.press("Enter") }) })
          await step("style from the ribbon", async (r) => { lastInput = await input(r, async () => { await kb.type(m.style); await click(WORD_UI.noSpacing); await kb.press("End"); await kb.press("Enter") }) })
          await step("bold", async (r) => { lastInput = await input(r, async () => { await kb.press("Control+b"); await kb.type(m.bold); await kb.press("Control+b") }) })
          wordCheck = (raw: string) => {
            const facts = wordFacts(raw, { heading: m.heading, bold: m.bold, cell: "-", font: "-" })
            const paras = [...walk((JSON.parse(raw) as { body?: unknown }).body ?? [])].filter((n) => n.kind === "para")
            const para = paras.find((p) => String(p.text ?? "").includes(m.style))
            return { heading: facts.heading, bold: facts.bold, style: String((para?.props as Record<string, unknown> | undefined)?.style ?? "") }
          }
        } else {
          const first = type()
          await step("type in a cell, enter", async (r) => { lastInput = await input(r, first.go) })
          const second = mark()
          await step("next cell after enter", async (r) => { lastInput = await input(r, async () => { await kb.type(second); await kb.press("Enter") }) })
          const third = mark()
          await step("third cell after enter", async (r) => { lastInput = await input(r, async () => { await kb.type(third); await kb.press("Enter") }) })
        }

        const checkContent = (h: N.Head, where: string) => {
          heldAt(h, where)
          if (app === "gridcraft") for (const m of marks) check(inCell(h.text, m), `${where}: ${m} is not in ${cellOf(m)}`)
          else {
            const c = wordCheck(h.text)
            check(c.heading, `${where}: no heading style on the heading paragraph`)
            check(c.bold, `${where}: no bold run on the bold paragraph`)
            check(/^no ?spacing/i.test(c.style), `${where}: the style paragraph has style "${c.style}", not No Spacing`)
          }
        }

        await step("autosave", async (r) => {
          let h = await N.readHead(api, ref)
          for (const end = Date.now() + CONFIRM_MS; missing(h.text).length && Date.now() < end; h = await N.readHead(api, ref)) await N.pause(100)
          r.confirmationMs = Date.now() - lastInput
          check(!missing(h.text).length, `autosave did not bring the typing to the server within ${CONFIRM_MS} ms`)
          h = await settle()
          r.note = { serverVersion: h.version }
          out.autosaveVersion = h.version
          checkContent(h, "after autosave")
        })

        // The change is the last keystroke, sent with the press: nothing is left for the 300 ms autosave to
        // capture first, so one change and one press must make exactly one version.
        await step("ctrl+s with a change", async (r) => {
          const m = mark()
          if (app === "wordcraft") {
            await input(r, () => N.typeMarker(page, f, app, m.slice(0, -1), cellOf(m)))
            await settle()
          }
          const h0 = await settle()
          const n0 = ops.length
          if (app === "gridcraft") await input(r, () => kb.type(m))
          let pressAt = 0
          const inputEnd = await input(r, async () => { pressAt = Date.now(); await Promise.all([kb.press(app === "wordcraft" ? m.slice(-1) : "Enter"), kb.press("Control+s")]) })
          const v = await confirm(r, inputEnd, h0.version)
          check(v !== undefined, `no confirmation within ${CONFIRM_MS} ms`)
          await N.pause(QUIET_MS - SETTLE_QUIET_MS)
          const h1 = await settle()
          r.note = { label: v, base: h0.version, head: h1.version, saves: ops.slice(n0).map((o) => ({ kind: o.kind, sentAt: o.sentAt, base: o.baseVersion, version: o.version, status: o.status, hash: o.contentHash?.slice(7, 15) })), pressAt: new Date(pressAt).toISOString() }
          check(await pressSeen(pressAt), "the press never reached the editor")
          check(BigInt(h1.version) === BigInt(h0.version) + 1n, `exactly one new version: v${h0.version} became v${h1.version}`)
          check(committed(n0).length === 1, `exactly one committed save, saw ${committed(n0).length}`)
          check(v === h1.version, `status said v${v}, head is v${h1.version}`)
          checkContent(h1, "after ctrl+s")
        })

        await step("ctrl+s without a change", async (r) => {
          const h0 = await settle()
          const n0 = ops.length
          let pressAt = 0
          await input(r, async () => { pressAt = Date.now(); await kb.press("Control+s") })
          await N.pause(QUIET_MS)
          const ack = await ackAt(pressAt)
          const st = await N.status(page)
          const h1 = await settle()
          const shown = labelVersion(st)
          const said = (await collect()).status.find((x) => x.at >= pressAt && /^Uloženo/.test(x.s))
          r.confirmationMs = ack ? ack - pressAt : said ? said.at - pressAt : null
          r.note = { before: h0.version, head: h1.version, label: shown ?? null, ack: ack !== undefined, sent: ops.slice(n0).map((o) => ({ kind: o.kind, status: o.status, version: o.version, base: o.baseVersion })) }
          check(await pressSeen(pressAt), "the press never reached the editor")
          check(h1.version === h0.version, `no new version on ctrl+s without a change: v${h0.version} became v${h1.version}`)
          check(shown === h1.version, `status not confirmed: "${st.slice(0, 50)}" at head v${h1.version}`)
        })

        await step("reload", async (r) => {
          const h0 = await settle()
          const s = Date.now()
          await page.reload({ waitUntil: "domcontentloaded" })
          await page.getByRole("status").filter({ hasText: /Pouze čtení|Připraveno k úpravám/ }).first().waitFor()
          const h1 = await N.readHead(api, ref)
          check(h1.version === h0.version && h1.hash === h0.hash, `reload changed the server document: v${h0.version} to v${h1.version}`)
          checkContent(h1, "after reload")
          // The closed page's lease outlives it for up to its TTL; the lease bar says so while it counts down.
          const take = page.getByRole("button", { name: "Převzít úpravy", exact: true })
          const waiting = page.getByRole("status").filter({ hasText: /Pokud jste ji zavřeli|Čekám na uvolnění/ })
          const ready = () => page.getByRole("status").filter({ hasText: /^Připraveno k úpravám/ }).count()
          for (const end = Date.now() + LEASE_MS; Date.now() < end && !(await ready()) && ((await waiting.count()) > 0 || !(await take.count())); ) await N.pause(200)
          const waitedMs = Date.now() - s
          if (await ready()) {
            f = await N.frameOf(page, app)
            await f.locator("canvas").first().click({ position: app === "wordcraft" ? { x: 678, y: 324 } : { x: 54, y: 240 } })
          } else f = await N.acquire(page, app)
          const st = await N.status(page)
          check(/^Připraveno k úpravám/.test(st), `status after reload is "${st.slice(0, 50)}", not ready`)
          r.confirmationMs = Date.now() - s
          const box = await page.locator(`iframe[title="${app === "wordcraft" ? "WordCraft" : "GridCraft"} editor"]`).boundingBox()
          if (!box) throw new Error("no editor frame box")
          const clip = { ...BODY[app], x: box.x + BODY[app].x, y: box.y + BODY[app].y }
          let png: Buffer = Buffer.alloc(0), u = { dominant: 1, colours: 1 }
          const t = Date.now()
          for (const end = t + RENDER_MS; Date.now() < end; await N.pause(300)) {
            png = await page.screenshot({ clip })
            u = await uniformity(page, png)
            if (u.dominant < BLANK_DOMINANT) break
          }
          await prd.evidence(`${app}-after-reload.png`, png)
          r.note = { ...u, renderedAfterMs: Date.now() - t, clip, leaseWaitMs: waitedMs }
          check(u.dominant < BLANK_DOMINANT, `rendered area blank after reload: ${(u.dominant * 100).toFixed(2)} % one colour after ${RENDER_MS} ms`)
        })

        await step("keep working after the reload", async (r) => {
          const n0 = ops.length
          const h0 = await N.readHead(api, ref)
          const t = type()
          const inputEnd = await input(r, t.go)
          const v = await confirm(r, inputEnd, h0.version)
          check(v !== undefined, `no confirmation within ${CONFIRM_MS} ms`)
          const h1 = await settle()
          r.note = { base: h0.version, head: h1.version }
          check(committed(n0)[0]?.baseVersion === h0.version && committed(n0)[0]?.version === String(BigInt(h0.version) + 1n), `first save after reload not contiguous with v${h0.version}: ${JSON.stringify(committed(n0)[0] ?? null)}`)
          checkContent(h1, "after work following reload")
        })

        const lost: { serverStatus: number; serverVersion?: string; serverHash?: string; at: number }[] = []
        let armed = false
        await step("lost reply", async (r) => {
          const h0 = await settle()
          await context.route("**/craft/document/save**", async (route) => {
            if (!armed) return route.continue()
            armed = false
            const res = await route.fetch({ maxRetries: 0, timeout: 30_000 })
            const j = (await res.json().catch(() => null)) as { version?: unknown; contentHash?: string } | null
            lost.push({ serverStatus: res.status(), serverVersion: j?.version === undefined ? undefined : String(j.version), serverHash: j?.contentHash, at: Date.now() })
            await route.abort("connectionreset")
          })
          armed = true
          const t = type()
          await input(r, t.go)
          await expect.poll(() => lost.length, { timeout: 20_000 }).toBe(1)
          const n0 = ops.length
          const loss = lost[0]
          await context.unroute("**/craft/document/save**")
          check(loss.serverStatus === 200, `the dropped save answered ${loss.serverStatus}`)
          const lostV = loss.serverVersion ?? String(BigInt(h0.version) + 1n)
          check(lostV === String(BigInt(h0.version) + 1n), `the dropped save committed v${lostV} on v${h0.version}`)
          const v = await confirm(r, loss.at, h0.version, RECOVER_MS)
          if (v === undefined && (await page.getByRole("button", { name: VERIFY, exact: true }).count())) { await press(); await confirm(r, loss.at, h0.version, 15_000) }
          check(v !== undefined, `did not recover by itself within ${RECOVER_MS} ms: "${(await N.status(page)).slice(0, 50)}"`)
          check(v === undefined || v === lostV, `recovered to v${v}, the server committed v${lostV}`)
          await N.pause(QUIET_MS)
          const h1 = await settle()
          const sent = ops.slice(n0)
          r.note = { base: h0.version, lostVersion: lostV, head: h1.version, recoveryMs: r.confirmationMs, sent: sent.map((o) => ({ kind: o.kind, status: o.status, version: o.version, phase: o.phase, failed: o.failed })) }
          check(h1.version === lostV, `no duplicate version: head v${h1.version}, lost save v${lostV}`)
          check(!sent.some((o) => o.kind === "save"), `re-saved after the lost reply: ${sent.filter((o) => o.kind === "save").length} saves`)
          checkContent(h1, "after the lost reply")
        })

        await step("next save after the lost reply", async (r) => {
          const h0 = await settle()
          const n0 = ops.length
          const t = type()
          const inputEnd = await input(r, t.go)
          const v = await confirm(r, inputEnd, h0.version)
          check(v !== undefined, `no confirmation within ${CONFIRM_MS} ms`)
          const h1 = await settle()
          const first = committed(n0)[0]
          r.note = { base: h0.version, head: h1.version, first: first ? { base: first.baseVersion, version: first.version } : null }
          check(first?.baseVersion === h0.version && first?.version === String(BigInt(h0.version) + 1n), `next save not contiguous with v${h0.version}: ${JSON.stringify(first ?? null)}`)
          check(BigInt(h1.version) === BigInt(h0.version) + BigInt(committed(n0).length), `head v${h1.version} after ${committed(n0).length} saves on v${h0.version}`)
          checkContent(h1, "after the next save")
        })

        // Save receipts: every committed save the browser saw, plus the one whose reply was dropped.
        const receiptsNow = () => [...ops.filter((o) => o.kind === "save" && o.status === 200 && o.version).map((o) => o.version as string), ...lost.map((x) => x.serverVersion ?? "?")].sort((a, b) => Number(a) - Number(b))
        const sameVersions = (a: unknown, b: string[]) => Array.isArray(a) && a.length === b.length && a.every((v, i) => typeof v === "string" && /^\d+$/.test(v) && v === b[i])
        let headAfterReload: string | null = null
        await step("final", async () => {
          const h = await settle()
          checkContent(h, "final")
          // The history listing: 200 must list exactly the observed versions, the flag-off 404 is
          // recorded as not enabled (NOT VERIFIED in the verdict), anything else is a violation.
          const pageRef = encodeURIComponent("@" + ref.pageId), wsq = `workspace_id=${encodeURIComponent(ref.workspaceId)}`
          const s = await api.post(`/api/v1/pages/${pageRef}/craft/sessions?${wsq}`, { data: {} })
          check(s.status() === 201, `read admission for the history listing answered ${s.status()}`)
          const tok = s.status() === 201 ? ((await s.json()) as { session_token: string }).session_token : ""
          const l = await api.get(`/api/v1/pages/${pageRef}/craft/document/versions?${wsq}`, { headers: { "X-Craft-Session": tok } })
          const text = await l.text()
          type Listing = { error?: unknown; versions?: { version: unknown }[]; truncated?: unknown }
          let body: Listing | null = null
          try { body = JSON.parse(text) as Listing } catch { /* recorded as the raw text below */ }
          const observed = [ref.initVersion, ...receiptsNow()]
          const listing = l.status() === 200 && Array.isArray(body?.versions) ? body.versions.map((v) => v?.version).reverse() : null
          const error = typeof body?.error === "string" ? body.error : l.status() === 200 ? null : text.slice(0, 200)
          let history: "verified" | "not-enabled" | "mismatch" | "error"
          if (l.status() === 404 && error === HISTORY_NOT_ENABLED) history = "not-enabled"
          else if (l.status() !== 200) history = "error"
          else history = body?.truncated === false && sameVersions(listing, observed) ? "verified" : "mismatch"
          out.history = { result: history, listingStatus: l.status(), error, listing, truncated: body?.truncated ?? null, observed }
          check(history !== "error", `version history listing answered ${l.status()}: ${error}`)
          check(history !== "mismatch", `version history listing ${JSON.stringify(listing)} (truncated ${String(body?.truncated)}) is not the observed ${observed.join(",")}`)
          await page.screenshot({ path: test.info().outputPath(`${app}-end.png`), timeout: 30_000 }).catch(() => {})
          releaseStatus = await N.release(page).then(() => "ok", (e: unknown) => `failed: ${String(e).slice(0, 200)}`)
          check(releaseStatus === "ok", `release ${releaseStatus}`)
          // A release of a saved document must leave no unsaved label and no draft to offer (#5089).
          await page.waitForTimeout(5_000)
          const after = (await page.getByRole("status").allInnerTexts().catch(() => [] as string[])).join(" ")
          out.statusAfterRelease = after.slice(0, 200)
          check(!after.includes("Neuložený koncept"), `status after a release of a saved document: ${after.slice(0, 120)}`)
          await page.reload({ waitUntil: "domcontentloaded" })
          await page.waitForTimeout(25_000)
          const offers = await page.getByText("Místní koncept", { exact: false }).count().catch(() => -1)
          out.draftOffersAfterRelease = offers
          check(offers === 0, `draft offered after a release of a saved document: ${offers}`)
          headAfterReload = (await N.readHead(api, ref)).version
        })

        // The version proof, taken after the last step (release and the reload included) and
        // independent of the history listing, which stays behind a flag until it is accepted.
        const receipts = receiptsNow()
        proofAt = ops.length
        const stepRow = (name: string) => steps.find((x) => x.name === name)
        const unchanged = stepRow("ctrl+s without a change"), lostStep = stepRow("lost reply"), finalStep = stepRow("final")
        const last = receipts.at(-1)
        const versionProblems: string[] = []
        receipts.forEach((v, i) => { if (v !== String(BigInt(ref.initVersion) + BigInt(i) + 1n)) versionProblems.push(`receipt ${i + 1} is v${v}, expected v${BigInt(ref.initVersion) + BigInt(i) + 1n}`) })
        if (!receipts.length) versionProblems.push("no save receipt")
        if (finalStep?.versionAfter !== last) versionProblems.push(`final head v${finalStep?.versionAfter}, last receipt v${last}`)
        if (headAfterReload !== last) versionProblems.push(`head after the final reload v${headAfterReload}, last receipt v${last}`)
        if (!unchanged || unchanged.versionBefore !== unchanged.versionAfter) versionProblems.push(`ctrl+s without a change: v${unchanged?.versionBefore} to v${unchanged?.versionAfter}`)
        if (!lostStep?.versionBefore || lostStep.versionAfter !== String(BigInt(lostStep.versionBefore) + 1n)) versionProblems.push(`lost reply: v${lostStep?.versionBefore} to v${lostStep?.versionAfter}`)
        out.versions = {
          start: ref.initVersion, receipts, head: finalStep?.versionAfter ?? null, headAfterReload,
          lostVersions: lost.map((x) => x.serverVersion ?? "?"), opsAtProof: proofAt,
          unchangedSave: { before: unchanged?.versionBefore ?? null, after: unchanged?.versionAfter ?? null },
          lostReply: { before: lostStep?.versionBefore ?? null, after: lostStep?.versionAfter ?? null },
          contiguous: versionProblems.length === 0, problems: versionProblems,
        }
        for (const p of versionProblems) violate(`version sequence: ${p}`)
        out.marks = marks.length
        const names = steps.map((x) => x.name)
        check(names.join("|") === scenario.steps.join("|"), `steps ${names.join(", ")} are not the expected ${scenario.steps.join(", ")}`)
        await context.close()
      })())
    } catch (err) {
      failure = String(err).slice(0, 600)
    } finally {
      build.end = await deployedBuild(prd)
      for (const [when, b] of [["start", build.start], ["end", build.end]] as const) {
        if (b.sha !== prd.appSha || b.dirty !== false) violations.push({ step: `build at the ${when}`, rule: `the origin served ${b.sha} (dirty ${String(b.dirty)}${b.error ? `, ${b.error}` : ""}), not ${prd.appSha} clean` })
      }
      // Nothing may save after the proof; a page error anywhere in the walk fails it.
      const savesAfterProof = proofAt === null ? null : opsRef.ops.slice(proofAt).filter((o) => o.kind === "save").length
      if (savesAfterProof) violations.push({ step: "after the walk", rule: `${savesAfterProof} saves after the version proof` })
      for (const e of pageErrors) violations.push({ step: e.step, rule: `page error: ${e.message}` })
      Object.assign(out, { finishedAt: new Date().toISOString(), failure: failure ?? null, violations, steps, ops: opsRef.ops, savesAfterProof, releaseStatus, pageErrors, build, lines: lines.slice(-40) })
      await prd.evidence(EVIDENCE_FILE, out)
      await disposeActors()
      await browser.close()
    }
    expect(failure ?? null, "the walk ran to its end").toBeNull()
    expect(violations).toEqual([])
  })
}
