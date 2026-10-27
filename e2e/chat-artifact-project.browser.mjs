// Isolated production-component browser test of the chat artifact project flow: the real
// AssistantTurn with artifact tool cards, the side viewer (screens, anchored comment, agent
// publish, restore). Every artifact request goes to the in-memory mock server; anything else
// fails the run.
// No live instance, provider or credentials are used.
// Run: crewship-heavy node e2e/chat-artifact-project.browser.mjs
// Screenshots: $CHAT_ARTIFACT_SCREENS or a fresh temporary directory
import { build } from "esbuild"
import postcss from "postcss"
import tailwindcss from "@tailwindcss/postcss"
import { chromium } from "@playwright/test"
import assert from "node:assert/strict"
import { createServer } from "node:http"
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"

const SHOTS = process.env.CHAT_ARTIFACT_SCREENS ?? await mkdtemp(join(tmpdir(), "crewship-chat-artifact-shots-"))
const VIEWPORTS = [
  { name: "desktop", width: 1280, height: 860 },
  { name: "desktop-dark", width: 1280, height: 860, dark: true },
  { name: "tablet", width: 834, height: 1112 },
  { name: "phone", width: 390, height: 844, mobile: true },
]
const TIMEOUT = 15_000

// Neighbours of AssistantTurn that need an app shell (auth, workspace, router,
// reactions feed). They are replaced by inert stand-ins; the components under test stay real.
const STUBS = {
  "use-auth": 'export const useSessionSafe = () => ({ data: { user: { id: "fixture-human", name: "Fixture Human", email: "h@example.test" } }, status: "authenticated" }); export const useSession = useSessionSafe;',
  "use-workspace": 'const ws = { id: "ws-fixture", name: "Fixture" }; export const useCurrentWorkspaceId = () => ws.id; export const useWorkspace = () => ({ workspaces: [ws], workspace: ws, workspaceId: ws.id, loading: false, error: null, workspaceUnavailable: false, setWorkspaceId() {} }); export const bindWorkspaceSession = () => {}; export const useWorkspacePagesTheme = () => undefined; export const refreshWorkspaceSettings = async () => {};',
  "next-navigation": 'export const useRouter = () => ({ push() {}, replace() {}, prefetch() {}, back() {}, refresh() {} }); export const usePathname = () => "/chat"; export const useSearchParams = () => new URLSearchParams();',
  "turn-reactions": "export const TurnReactions = () => null;",
}
const stubOf = path => path.includes("use-auth") ? "use-auth" : path.includes("use-workspace") ? "use-workspace" : path === "next/navigation" ? "next-navigation" : path.includes("turn-reactions") ? "turn-reactions" : null

const output = await mkdtemp(join(tmpdir(), "crewship-chat-artifact-"))
let server, browser, current
const step = (name, detail = {}) => console.log(JSON.stringify({ step: name, ...detail }))

try {
  await mkdir(SHOTS, { recursive: true })
  const css = await postcss([tailwindcss()]).process(await readFile("app/globals.css", "utf8"), { from: "app/globals.css" })
  await build({
    entryPoints: ["e2e/chat-artifact-project.fixture.tsx"], bundle: true, outfile: join(output, "bundle.js"), platform: "browser", format: "esm", jsx: "automatic",
    alias: { "@": process.cwd() }, logLevel: "error",
    define: { "process.env": JSON.stringify({ NODE_ENV: "test", NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS: "true" }) },
    plugins: [{ name: "fixture-neighbours", setup(b) {
      b.onResolve({ filter: /(?:^@\/hooks\/use-auth$|\/hooks\/use-auth(?:\.tsx?)?$|^@\/hooks\/use-workspace$|^next\/navigation$|\/reactions\/turn-reactions$)/ }, args => ({ path: stubOf(args.path), namespace: "fixture" }))
      b.onLoad({ filter: /.*/, namespace: "fixture" }, args => ({ loader: "js", contents: STUBS[args.path] }))
    } }],
  })
  server = createServer(async (request, response) => {
    const { pathname, searchParams } = new URL(request.url, "http://host")
    if (pathname === "/bundle.js") { response.writeHead(200, { "content-type": "text/javascript" }); return response.end(await readFile(join(output, "bundle.js"))) }
    if (pathname === "/app.css") { response.writeHead(200, { "content-type": "text/css" }); return response.end(css.css) }
    if (pathname !== "/") { response.writeHead(404); return response.end() }
    response.writeHead(200, { "content-type": "text/html" })
    response.end(`<!doctype html><html lang="en"${searchParams.has("dark") ? ' class="dark"' : ""}><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Chat artifact project</title><link rel="stylesheet" href="/app.css"><body class="bg-background text-foreground antialiased"><div id="root"></div><script type="module" src="/bundle.js"></script></body></html>`)
  })
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve))
  browser = await chromium.launch({ headless: true })
  const shots = []

  for (const vp of VIEWPORTS) {
    const context = await browser.newContext({ viewport: { width: vp.width, height: vp.height }, hasTouch: !!vp.mobile, isMobile: !!vp.mobile, locale: "en-US", timezoneId: "Europe/Prague", colorScheme: vp.dark ? "dark" : "light" })
    const page = await context.newPage()
    current = { page, name: vp.name }
    page.setDefaultTimeout(TIMEOUT)
    const errors = []
    page.on("pageerror", error => { errors.push(error.message); console.error(`[${vp.name}] page error:`, error.message) })
    page.on("console", message => { if (message.type() === "error") console.error(`[${vp.name}] console error:`, message.text()) })
    const shot = async (name, target = page) => {
      const path = join(SHOTS, `${vp.name}-${name}.png`)
      await target.screenshot({ path, animations: "disabled" })
      shots.push(path)
    }
    // Dark mode as the app applies it: the "dark" class on <html> (app/globals.css @custom-variant dark).
    await page.goto(`http://127.0.0.1:${server.address().port}/${vp.dark ? "?dark" : ""}`)
    assert.equal(await page.evaluate(() => document.documentElement.classList.contains("dark")), !!vp.dark)

    const viewer = page.locator('section[aria-label="Artifact viewer"]')
    const card = page.getByRole("article", { name: "Pricing site" })
    const backToChat = async () => { if (vp.mobile) { await viewer.getByRole("button", { name: "Back to chat" }).click(); await card.first().waitFor() } }
    const openViewer = async () => { await card.first().getByRole("button", { name: "Open Pricing site" }).click(); await viewer.getByRole("heading", { name: "Pricing site" }).waitFor() }
    const frame = screen => viewer.frameLocator(`iframe[title="Preview of ${screen}"]`)
    const chooseScreen = async (label, path) => {
      const segmented = viewer.getByRole("radiogroup", { name: "Screen" })
      if (await segmented.isVisible()) await segmented.getByRole("radio", { name: label }).click()
      else await viewer.getByRole("combobox", { name: "Screen (compact)" }).selectOption(path)
      await frame(label).locator("body").waitFor()
    }

    // 1. The agent's create call is still running.
    await page.getByText("Creating project…").waitFor()
    await page.getByRole("progressbar", { name: "Creating project" }).waitFor()
    assert.equal(await card.count(), 0, "no project card while the create call runs")
    await shot("01-running")

    // 2. The create call returns: progress becomes "v1 ready" plus the project card.
    await page.getByRole("button", { name: "Agent finishes create" }).click()
    await card.waitFor()
    await card.getByText("v1", { exact: true }).waitFor()
    await card.getByText("Draft", { exact: true }).waitFor()
    await page.getByText("v1 ready").waitFor()
    await shot("02-card")

    // 3. Open the viewer; three screens, switch to Compare.
    await openViewer()
    await frame("Plans").locator("#headline").waitFor()
    const segmentedVisible = await viewer.getByRole("radiogroup", { name: "Screen" }).isVisible()
    const labels = segmentedVisible
      ? await viewer.getByRole("radiogroup", { name: "Screen" }).getByRole("radio").allTextContents()
      : await viewer.getByRole("combobox", { name: "Screen (compact)" }).locator("option").allTextContents()
    assert.deepEqual(labels, ["Plans", "Compare", "FAQ"], "screen switch lists the three html files by <title>, entrypoint first")
    assert.ok(segmentedVisible, "three screens keep the segmented control at every width")
    // Create and publish each show a card for the same project; only the one that opened the viewer is ringed.
    const ringed = await page.locator('article[aria-label="Pricing site"][data-open]').count()
    assert.ok(vp.mobile ? ringed <= 1 : ringed === 1, `one card ringed as open, got ${ringed}`)
    step("screens", { viewport: vp.name, control: segmentedVisible ? "segmented" : "compact-select", labels })
    await shot("03-viewer")
    await chooseScreen("Compare", "compare.html")
    await frame("Compare").locator("#cmp-table").waitFor()
    await shot("04-compare")

    // 4. Back on Plans, Select, pick #plan-team inside the sandboxed frame, send the comment.
    await chooseScreen("Plans", "plans.html")
    const select = viewer.getByRole("button", { name: "Select" })
    await select.click()
    assert.equal(await select.getAttribute("aria-pressed"), "true")
    await viewer.getByText("Click a part of the screen. The comment stays anchored to that element and version.", { exact: true }).waitFor()
    await frame("Plans").locator("#plan-team h2").click()
    const pin = viewer.getByRole("form", { name: "New comment" })
    await pin.getByText("On #plan-team", { exact: false }).waitFor()
    await pin.getByRole("textbox", { name: "Comment" }).fill("Make the Team plan stand out")
    await pin.getByRole("button", { name: "Send to agent" }).click()
    const queue = viewer.getByRole("region", { name: "Comment queue" })
    await queue.getByText("Make the Team plan stand out").waitFor()
    await queue.getByText("Waiting", { exact: true }).waitFor()
    await viewer.getByText("Comment sent to the agent").waitFor()
    const posted = await page.evaluate(() => window.__mock.state.comments.map(c => ({ selector: c.selector, quote: c.quote, status: c.status, path: c.path, version: c.version })))
    assert.deepEqual(posted, [{ selector: "#plan-team", quote: "Team$24Unlimited agents, shared crews", status: "pending", path: "plans.html", version: 1 }])
    const dispatched = await page.evaluate(() => window.__dispatched)
    assert.equal(dispatched.length, 1, "Send to agent starts the agent once")
    assert.equal(dispatched[0].artifact_project_intent.base_version, 1)
    await queue.scrollIntoViewIfNeeded()
    await shot("05-comment")

    // 5. The agent publishes v2 and answers the comment.
    await backToChat()
    await page.getByRole("button", { name: "Agent publishes v2" }).click()
    await page.getByText("v2 ready").waitFor()
    await page.getByRole("button", { name: "Restore v1" }).waitFor()
    await card.first().getByText("v2", { exact: true }).waitFor()
    if (!vp.mobile) {
      await queue.getByText("Done → v2").waitFor()
      await frame("Plans").getByText("Most popular").waitFor()
      assert.equal(await viewer.getByText("Comment sent to the agent").count(), 0, "the sent notice goes once a new version is shown")
    }
    await shot("06-v2")
    if (vp.mobile) {
      await openViewer()
      await queue.getByText("Done → v2").waitFor()
      await frame("Plans").getByText("Most popular").waitFor()
      await queue.scrollIntoViewIfNeeded()
      await shot("06b-v2-queue")
      await backToChat()
    }

    // 6. Restore v1 from the step row: a new v3, nothing deleted.
    await page.getByRole("button", { name: "Restore v1" }).click()
    await page.getByText("Restored v1 as a new version · nothing deleted").waitFor()
    await card.first().getByText("v3", { exact: true }).waitFor()
    if (vp.mobile) { await shot("07a-restore-notice"); await openViewer() }
    const history = viewer.getByRole("button", { name: "History" })
    if (await history.getAttribute("aria-pressed") !== "true") await history.click()
    const versions = viewer.getByRole("region", { name: "Version history" })
    const v3 = versions.getByRole("listitem").filter({ hasText: "v3" }).first()
    await v3.getByText("Restored from v1", { exact: false }).waitFor()
    await v3.getByText("Latest", { exact: true }).waitFor()
    const latest = await page.evaluate(() => window.__mock.state.versions.at(-1))
    assert.equal(latest.version, 3); assert.equal(latest.restored_from, 1)
    // The agent's version carries its run id; it must not widen the viewer.
    assert.equal(await versions.getByText(/msg_|cmuxw/).count(), 0, "no raw actor id in the history")
    const overflow = await viewer.evaluate(el => [...el.querySelectorAll("*")].filter(n => n.scrollWidth > n.clientWidth + 1 && /^(auto|scroll)$/.test(getComputedStyle(n).overflowX)).map(n => n.className.toString().slice(0, 60)))
    assert.deepEqual(overflow, [], "the viewer does not scroll sideways")
    await versions.scrollIntoViewIfNeeded()
    await shot("07-restored")

    const unmocked = await page.evaluate(() => window.__unmocked)
    assert.deepEqual(unmocked, [], `requests outside the mocked artifact routes: ${unmocked.join(", ")}`)
    assert.deepEqual(errors, [], `page errors: ${errors.join(" | ")}`)
    const requests = await page.evaluate(() => [...new Set(window.__mock.state.requests)])
    step("viewport-pass", { viewport: vp.name, requests })
    await context.close()
  }
  step("screenshots", { dir: SHOTS, files: shots.map(path => path.slice(SHOTS.length + 1)) })
  console.log(`PASS chat-artifact-project: ${VIEWPORTS.length} viewports, ${shots.length} screenshots`)
} catch (error) {
  console.error("FAIL chat-artifact-project:", error)
  if (current) {
    // Evidence for the failing viewport: what the page showed and what reached the mock.
    const path = join(SHOTS, `${current.name}-FAILED.png`)
    await current.page.screenshot({ path }).catch(() => {})
    const state = await current.page.evaluate(() => ({ requests: window.__mock?.state.requests, comments: window.__mock?.state.comments, unmocked: window.__unmocked })).catch(() => null)
    console.error(JSON.stringify({ failed_viewport: current.name, screenshot: path, ...state }))
  }
  process.exitCode = 1
} finally {
  await browser?.close()
  server?.close()
  await rm(output, { recursive: true, force: true })
}
