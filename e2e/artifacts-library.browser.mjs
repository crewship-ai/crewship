// Isolated production-component browser test of the Artifacts library: gallery of the kinds
// with a backend, kind filter, table, a project's version history with restore, and a native
// Word held by another person. Every Pages, artifact and Craft request goes to in-memory mocks;
// anything else fails the run. No live instance, provider or credentials are used.
// Run: crewship-heavy node e2e/artifacts-library.browser.mjs
// Screenshots: $ARTIFACTS_LIBRARY_SCREENS or a fresh temporary directory
import { build } from "esbuild"
import postcss from "postcss"
import tailwindcss from "@tailwindcss/postcss"
import { chromium } from "@playwright/test"
import assert from "node:assert/strict"
import { createServer } from "node:http"
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"

const SHOTS = process.env.ARTIFACTS_LIBRARY_SCREENS ?? await mkdtemp(join(tmpdir(), "crewship-artifacts-library-shots-"))
const VIEWPORTS = [
  { name: "desktop", width: 1280, height: 860 },
  { name: "desktop-dark", width: 1280, height: 860, dark: true },
  { name: "phone", width: 390, height: 844, mobile: true },
]
const TIMEOUT = 15_000

// The app shell around the library (auth, workspace, router) is replaced by inert stand-ins.
const STUBS = {
  "use-auth": 'export const useSessionSafe = () => ({ data: { user: { id: "fixture-human", name: "Fixture Human", email: "h@example.test" } }, status: "authenticated" }); export const useSession = useSessionSafe;',
  "use-workspace": 'const ws = { id: "ws-fixture", name: "Fixture" }; export const useCurrentWorkspaceId = () => ws.id; export const useWorkspace = () => ({ workspaces: [ws], workspace: ws, workspaceId: ws.id, loading: false, error: null, workspaceUnavailable: false, setWorkspaceId() {} }); export const bindWorkspaceSession = () => {}; export const useWorkspacePagesTheme = () => undefined; export const refreshWorkspaceSettings = async () => {};',
  "next-navigation": 'export const useRouter = () => ({ push() {}, replace() {}, prefetch() {}, back() {}, refresh() {} }); export const usePathname = () => "/artifacts"; export const useSearchParams = () => new URLSearchParams();',
}
const stubOf = path => path.includes("use-auth") ? "use-auth" : path.includes("use-workspace") ? "use-workspace" : path === "next/navigation" ? "next-navigation" : null

const output = await mkdtemp(join(tmpdir(), "crewship-artifacts-library-"))
let server, browser
const step = (name, detail = {}) => console.log(JSON.stringify({ step: name, ...detail }))

try {
  await mkdir(SHOTS, { recursive: true })
  const css = await postcss([tailwindcss()]).process(await readFile("app/globals.css", "utf8"), { from: "app/globals.css" })
  await build({
    entryPoints: ["e2e/artifacts-library.fixture.tsx"], bundle: true, outfile: join(output, "bundle.js"), platform: "browser", format: "esm", jsx: "automatic",
    alias: { "@": process.cwd() }, logLevel: "error",
    define: { "process.env": JSON.stringify({ NODE_ENV: "test", NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS: "true", NEXT_PUBLIC_CHAT_CRAFT_LEASE: "true", NEXT_PUBLIC_ARTIFACTS_LIBRARY: "true" }) },
    plugins: [{ name: "fixture-neighbours", setup(b) {
      b.onResolve({ filter: /(?:^@\/hooks\/use-auth$|\/hooks\/use-auth(?:\.tsx?)?$|^@\/hooks\/use-workspace$|^next\/navigation$)/ }, args => ({ path: stubOf(args.path), namespace: "fixture" }))
      b.onLoad({ filter: /.*/, namespace: "fixture" }, args => ({ loader: "js", contents: STUBS[args.path] }))
    } }],
  })
  server = createServer(async (request, response) => {
    const { pathname, searchParams } = new URL(request.url, "http://host")
    if (pathname === "/bundle.js") { response.writeHead(200, { "content-type": "text/javascript" }); return response.end(await readFile(join(output, "bundle.js"))) }
    if (pathname === "/app.css") { response.writeHead(200, { "content-type": "text/css" }); return response.end(css.css) }
    if (pathname !== "/") { response.writeHead(404); return response.end() }
    response.writeHead(200, { "content-type": "text/html" })
    response.end(`<!doctype html><html lang="en"${searchParams.has("dark") ? ' class="dark"' : ""}><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Artifacts library</title><link rel="stylesheet" href="/app.css"><body class="bg-background text-foreground antialiased"><div id="root"></div><script type="module" src="/bundle.js"></script></body></html>`)
  })
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve))
  browser = await chromium.launch({ headless: true })
  const shots = []

  for (const vp of VIEWPORTS) {
    const context = await browser.newContext({ viewport: { width: vp.width, height: vp.height }, hasTouch: !!vp.mobile, isMobile: !!vp.mobile, locale: "en-US", timezoneId: "Europe/Prague", colorScheme: vp.dark ? "dark" : "light" })
    const page = await context.newPage()
    page.setDefaultTimeout(TIMEOUT)
    const errors = []
    page.on("pageerror", error => { errors.push(error.message); console.error(`[${vp.name}] page error:`, error.message) })
    const shot = async name => { const path = join(SHOTS, `${vp.name}-${name}.png`); await page.screenshot({ path, animations: "disabled", fullPage: true }); shots.push(path) }
    await page.goto(`http://127.0.0.1:${server.address().port}/${vp.dark ? "?dark" : ""}`)
    const noSideways = async where => assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `${vp.name} ${where}: no sideways page scroll`)

    // 1. Gallery: only kinds with a backend, newest first; the app without a version head is left out.
    const gallery = page.getByRole("list", { name: "Artifacts" })
    await gallery.waitFor()
    const titles = await gallery.getByRole("heading").allTextContents()
    assert.deepEqual(titles, ["Pricing site", "Partner brief", "Price model", "Weekly competitor report", "Q3 board deck", "Q3 support summary"])
    const site = gallery.getByRole("listitem").filter({ hasText: "Pricing site" })
    await site.getByText("Shared v2").waitFor()
    await gallery.getByRole("listitem").filter({ hasText: "Partner brief" }).getByText("Not shared").waitFor()
    await gallery.getByRole("listitem").filter({ hasText: "Q3 support summary" }).getByText("Private").waitFor()
    await noSideways("gallery")
    await shot("01-gallery")

    // 2. Kind filter and the table view.
    await page.getByRole("button", { name: "Projects 2" }).click()
    assert.equal(await gallery.getByRole("listitem").count(), 2)
    await page.getByRole("button", { name: "All 6" }).click()
    await page.getByRole("radio", { name: "Table" }).click()
    await page.getByRole("table").waitFor()
    assert.equal(await page.getByRole("table").getByRole("row").count(), 7)
    await shot("02-table")
    await page.getByRole("radio", { name: "Gallery" }).click()

    // 3. Project detail opens on its version history; restore v1 as a new version.
    await page.getByRole("button", { name: "Open Pricing site" }).click()
    await page.getByRole("heading", { level: 1, name: "Pricing site" }).waitFor()
    const viewer = page.locator('section[aria-label="Artifact viewer"]')
    await viewer.waitFor()
    await viewer.getByText("Headline names every team").waitFor()
    await noSideways("project history")
    await shot("03-project-history")
    // History is newest first, so v1's restore is the last one.
    await viewer.getByRole("button", { name: "Restore as new version" }).last().click()
    await viewer.getByText("Restored v1 as v5 · nothing deleted").waitFor()
    await shot("04-project-restored")
    await page.getByRole("button", { name: "All artifacts" }).click()
    await gallery.waitFor()

    // 4. Native Word held by another person: the lock and Request edit, no Edit.
    await page.getByRole("button", { name: "Open Partner brief" }).click()
    await page.getByText("Editing: Jana Nováková").waitFor()
    await page.getByRole("button", { name: "Request edit" }).waitFor()
    assert.equal(await page.getByRole("button", { name: "Edit", exact: true }).count(), 0)
    await shot("05-word-locked")
    await page.getByRole("button", { name: "Request edit" }).click()
    await page.getByText("Request sent to Jana Nováková · they can hand over or decline").waitFor()
    await shot("06-word-requested")

    const unmocked = await page.evaluate(() => window.__unmocked)
    assert.deepEqual(unmocked, [], `requests outside the mocks: ${unmocked.join(", ")}`)
    assert.deepEqual(errors, [], "no page errors")
    step("viewport-pass", { viewport: vp.name })
    await context.close()
  }
  step("screenshots", { dir: SHOTS, count: shots.length })
  console.log(`PASS artifacts-library: ${VIEWPORTS.length} viewports, ${shots.length} screenshots`)
} finally {
  await browser?.close()
  server?.close()
  await rm(output, { recursive: true, force: true })
}
