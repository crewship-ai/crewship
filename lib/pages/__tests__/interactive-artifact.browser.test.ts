// @vitest-environment node
import { existsSync, readFileSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium, type Frame, type Page, type Route } from "@playwright/test"
import { expect, it } from "vitest"

const STUDIO = "https://studio.interactive.test"
const RUNTIME = "https://runtime.interactive.example"
const BOOTSTRAP = `${RUNTIME}/api/v1/pages/runtime/bootstrap`

// The runtime document is built from the Go server's own source: the same
// CSP, HTML skeleton and bootstrap.js that internal/pagebuild serves.
function runtimeDocument(): { html: string; csp: string } {
  const source = readFileSync(`${process.cwd()}/internal/pagebuild/runtime.go`, "utf8")
  const bootstrap = readFileSync(`${process.cwd()}/internal/pagebuild/bootstrap.js`, "utf8")
  const csp = /"Content-Security-Policy", fmt\.Sprintf\("([^"]+)"/.exec(source)?.[1]
  const template = /fmt\.Fprintf\(w, `([^`]+)`/.exec(source)?.[1]
  if (!csp || !template) throw new Error("runtime.go layout changed; update this fixture")
  const nonce = "bootnonce" + Math.random().toString(36).slice(2)
  const values = [nonce, JSON.stringify(STUDIO), JSON.stringify(nonce), bootstrap]
  return { csp: csp.replace("%s", nonce).replace("%s", STUDIO), html: template.replace(/%[sq]/g, () => values.shift()!) }
}

const POLL = `<title>Lunch poll</title>
<style>#count{font-weight:bold}</style>
<h1 id="artifact-title">Lunch poll</h1>
<p>Viewer: <span id="who"></span></p>
<p>Pizza: <span id="count">loading</span></p>
<button id="vote" type="button">Vote pizza</button>
<a id="away" href="details.html">Details</a>
<p id="error"></p>
<pre id="probe"></pre>
<script>
  const probe = {}
  try { probe.cookie = document.cookie } catch (e) { probe.cookie = "blocked:" + e.name }
  try { probe.parentDom = parent.document.getElementById("secret").textContent } catch (e) { probe.parentDom = "blocked:" + e.name }
  try { probe.topHref = top.location.href } catch (e) { probe.topHref = "blocked:" + e.name }
  try { probe.storage = typeof localStorage.getItem("x") } catch (e) { probe.storage = "blocked:" + e.name }
  probe.origin = self.origin
  probe.port = typeof window.__crewshipPagesPort
  const show = () => { document.getElementById("probe").textContent = JSON.stringify(probe) }
  fetch("${STUDIO}/api/v1/auth/me", { credentials: "include" }).then(() => { probe.fetch = "reached" }, e => { probe.fetch = "blocked:" + e.name }).finally(show)
  async function refresh() {
    const votes = await crewship.data.get("votes")
    const pizza = votes.documents.find(doc => doc.id === "pizza")
    document.getElementById("count").textContent = String(pizza ? pizza.value.count : 0)
    return pizza
  }
  document.getElementById("vote").addEventListener("click", async () => {
    const pizza = await refresh()
    try {
      await crewship.data.batch("votes", [{ id: "pizza", ifVersion: pizza ? pizza.version : 0, value: { count: (pizza ? pizza.value.count : 0) + 1 } }])
      document.getElementById("error").textContent = "saved"
    } catch (e) { document.getElementById("error").textContent = e.code + ": " + e.message }
    await refresh()
  })
  crewship.viewer().then(viewer => { document.getElementById("who").textContent = viewer.name })
  refresh()
</script>`

type Doc = { version: number; value: { count: number } }
interface Fixture { page: Page; store: Map<string, Doc>; apiCookies: string[]; frameApiCalls: number }

async function withStudio(run: (fixture: Fixture) => Promise<void>) {
  expect(existsSync(chromium.executablePath())).toBe(true)
  const module = buildSync({ entryPoints: [`${process.cwd()}/lib/pages/interactive-artifact.ts`], bundle: true, write: false, format: "iife", globalName: "interactive", platform: "browser", tsconfig: `${process.cwd()}/tsconfig.json` }).outputFiles[0].text
  const runtime = runtimeDocument()
  const browser = await chromium.launch({ headless: true })
  const fixture: Fixture = { page: undefined as unknown as Page, store: new Map(), apiCookies: [], frameApiCalls: 0 }
  try {
    const context = await browser.newContext()
    await context.route(`${RUNTIME}/**`, route => route.request().url() === BOOTSTRAP
      ? route.fulfill({ body: runtime.html, contentType: "text/html; charset=utf-8", headers: { "Content-Security-Policy": runtime.csp, "Cross-Origin-Embedder-Policy": "credentialless", "Origin-Agent-Cluster": "?1", "Referrer-Policy": "no-referrer" } })
      : route.fulfill({ status: 404 }))
    // A stand-in for the data endpoints with the same wire shape and ifVersion rule.
    await context.route(`${STUDIO}/api/**`, async (route: Route) => {
      const request = route.request(), url = new URL(request.url())
      if (request.frame() !== fixture.page.mainFrame()) fixture.frameApiCalls++
      fixture.apiCookies.push((await request.allHeaders()).cookie ?? "")
      const match = /^\/api\/v1\/pages\/@pg1\/artifact\/data\/(votes)$/.exec(decodeURIComponent(url.pathname))
      if (!match) return route.fulfill({ status: 404, json: { error: "not found" } })
      if (request.method() === "POST") {
        const body = request.postDataJSON() as { base_version: number; documents: { id: string; ifVersion: number; value: { count: number } }[] }
        if (body.base_version !== 1 || body.documents.some(doc => (fixture.store.get(doc.id)?.version ?? 0) !== doc.ifVersion)) return route.fulfill({ status: 409, json: { code: "artifact_data_conflict" } })
        for (const doc of body.documents) fixture.store.set(doc.id, { version: doc.ifVersion + 1, value: doc.value })
      } else if (url.searchParams.get("version") !== "1" || url.searchParams.get("workspace_id") !== "ws1") return route.fulfill({ status: 400, json: {} })
      return route.fulfill({ json: { collection: "votes", code_version: 1, documents: [...fixture.store].map(([id, doc]) => ({ id, ...doc, updated_at: "2026-10-31T10:00:00Z", source_label: "Contributor", source_kind: "human" })) } })
    })
    await context.route(`${STUDIO}/`, route => route.fulfill({ body: `<!doctype html><title>Studio</title><p id="secret">parent-secret</p>`, contentType: "text/html", headers: { "Set-Cookie": "crewship_session=s3cret; Path=/; Secure; SameSite=Lax" } }))
    const page = fixture.page = await context.newPage()
    await page.goto(`${STUDIO}/`)
    await page.addScriptTag({ content: module })
    await page.evaluate(({ poll, bootstrap }) => {
      const api = (window as unknown as { interactive: typeof import("../interactive-artifact") }).interactive
      const files = [{ path: "index.html", media_type: "text/html", content: poll }]
      ;(window as unknown as { mount: (name: string, viewer: string, canWrite: boolean) => void }).mount = (name, viewer, canWrite) => {
        document.querySelector(`iframe[name="${name}"]`)?.remove()
        api.validateInteractiveRuntime(bootstrap, location.href, false)
        const access = api.createInteractiveDataAccess({ fetcher: (input, init) => fetch(input, { ...init, credentials: "include" }), workspaceId: "ws1", pageId: "pg1", codeVersion: 1, viewerName: () => viewer })
        const session = new api.InteractiveFrameSession(api.interactiveArtifactSource(files, "index.html"), access, { collections: ["votes"], canWrite, onReady: () => frame.setAttribute("data-ready", "1") })
        const frame = document.createElement("iframe")
        frame.name = name
        frame.sandbox.add("allow-scripts")
        frame.referrerPolicy = "no-referrer"
        frame.onload = () => { if (!session.load(frame.contentWindow)) frame.setAttribute("data-ended", "1") }
        frame.src = bootstrap
        document.body.append(frame)
      }
    }, { poll: POLL, bootstrap: BOOTSTRAP })
    await run(fixture)
  } finally { await browser.close() }
}

async function mount(page: Page, name: string, viewer: string, canWrite: boolean): Promise<Frame> {
  await page.evaluate(args => (window as unknown as { mount: (...a: unknown[]) => void }).mount(...args), [name, viewer, canWrite] as const)
  await page.waitForSelector(`iframe[name="${name}"][data-ready="1"]`)
  const frame = page.frame({ name })
  if (!frame) throw new Error("frame missing")
  return frame
}

it("runs artifact script in the isolated runtime, shares data through the broker and keeps the parent out of reach", () => withStudio(async fixture => {
  const { page, store, apiCookies } = fixture
  const owner = await mount(page, "owner", "Ada Lovelace", true)
  expect(new URL(owner.url()).origin).toBe(RUNTIME)
  await expect.poll(() => owner.textContent("#who")).toBe("Ada Lovelace")
  await expect.poll(() => owner.textContent("#count")).toBe("0")

  // Script ran in an opaque, credential-less document.
  await expect.poll(async () => (await owner.textContent("#probe")) ?? "").toContain("fetch")
  const probe = JSON.parse((await owner.textContent("#probe"))!)
  expect(probe).toEqual({
    cookie: "blocked:SecurityError", parentDom: "blocked:SecurityError", topHref: "blocked:SecurityError", storage: "blocked:SecurityError",
    origin: "null", port: "undefined", fetch: "blocked:TypeError",
  })

  // A link cannot replace the verified document.
  await owner.click("#away")
  await page.waitForTimeout(300)
  expect(await page.getAttribute('iframe[name="owner"]', "data-ended")).toBeNull()
  expect(new URL(owner.url()).pathname).toBe("/api/v1/pages/runtime/bootstrap")

  // Write through the broker with the viewer's session.
  await owner.click("#vote")
  await expect.poll(() => owner.textContent("#error")).toBe("saved")
  await expect.poll(() => owner.textContent("#count")).toBe("1")
  expect(store.get("pizza")).toEqual({ version: 1, value: { count: 1 } })

  // A reload and a second viewer see the same shared state; the reader cannot write.
  const reloaded = await mount(page, "owner", "Ada Lovelace", true)
  await expect.poll(() => reloaded.textContent("#count")).toBe("1")
  const reader = await mount(page, "reader", "Ben Reader", false)
  await expect.poll(() => reader.textContent("#count")).toBe("1")
  await reader.click("#vote")
  await expect.poll(() => reader.textContent("#error")).toBe("forbidden: You can read this artifact but not change its data.")
  expect(store.get("pizza")).toEqual({ version: 1, value: { count: 1 } })

  // Every data request was the parent's own, carrying the session; none came from a frame.
  expect(apiCookies.length).toBeGreaterThan(0)
  expect(apiCookies.every(cookie => cookie.includes("crewship_session=s3cret"))).toBe(true)
  expect(fixture.frameApiCalls).toBe(0)
  expect(await page.textContent("#secret")).toBe("parent-secret")
}), 60_000)

it("ends the session when the frame navigates away from the verified bootstrap", () => withStudio(async ({ page }) => {
  const frame = await mount(page, "owner", "Ada", true)
  await frame.evaluate(() => { location.href = location.href + "#x"; location.reload() }).catch(() => {})
  await page.waitForSelector(`iframe[name="owner"][data-ended="1"]`)
}), 60_000)
