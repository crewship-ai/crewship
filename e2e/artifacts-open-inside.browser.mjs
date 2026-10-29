// Isolated browser test of opening artifacts inside Artifacts: a click in the library opens the
// artifact at /artifacts/<id> with its content on screen, Back returns to the list, a reload of
// that address opens it again, and a link from before IDs (/artifacts?item=<slug>) and an old /pages/<slug> link move there.
// The real route code runs over in-memory mocks; anything unmocked fails the run.
// Run: crewship-heavy node e2e/artifacts-open-inside.browser.mjs
import { build } from "esbuild"
import postcss from "postcss"
import tailwindcss from "@tailwindcss/postcss"
import { chromium } from "@playwright/test"
import assert from "node:assert/strict"
import { createServer } from "node:http"
import { mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"

const TIMEOUT = 15_000
// The app shell is replaced by stand-ins; next/navigation follows the real address bar.
const STUBS = {
  "use-auth": 'export const useSessionSafe = () => ({ data: { user: { id: "fixture-human", name: "Fixture Human", email: "h@example.test" } }, status: "authenticated" }); export const useSession = useSessionSafe;',
  "use-workspace": 'const ws = { id: "ws-fixture", name: "Fixture" }; export const useCurrentWorkspaceId = () => ws.id; export const useWorkspace = () => ({ workspaces: [ws], workspace: ws, workspaceId: ws.id, role: "OWNER", capabilities: [], loading: false, error: null, workspaceUnavailable: false, setWorkspaceId() {} }); export const bindWorkspaceSession = () => {}; export const useWorkspacePagesTheme = () => undefined; export const refreshWorkspaceSettings = async () => {};',
  "use-page-workspace": 'export const usePageWorkspace = () => ({ workspaceId: "ws-fixture", loading: false, linkUnavailable: false });',
  // useSyncExternalStore re-checks the address on subscribe, so a child's redirect during the first commit is never missed.
  "next-navigation": `import { useSyncExternalStore } from "react"
const listeners = new Set(); const notify = () => listeners.forEach(f => f())
addEventListener("popstate", notify)
const subscribe = f => { listeners.add(f); return () => { listeners.delete(f) } }
const useAddress = () => useSyncExternalStore(subscribe, () => location.href)
export const useRouter = () => ({ push(u) { history.pushState(null, "", u); notify() }, replace(u) { history.replaceState(null, "", u); notify() }, back() { history.back() }, prefetch() {}, refresh() {} })
export const useSearchParams = () => { useAddress(); return new URLSearchParams(location.search) }
export const usePathname = () => { useAddress(); return location.pathname }
export const useParams = () => { useAddress(); const m = /^\\/artifacts\\/([^/]+)/.exec(location.pathname); return m ? { id: m[1] } : {} }`,
}
const stubOf = path => path.includes("use-page-workspace") ? "use-page-workspace" : path.includes("use-auth") ? "use-auth" : path.includes("use-workspace") ? "use-workspace" : path === "next/navigation" ? "next-navigation" : null

const output = await mkdtemp(join(tmpdir(), "crewship-artifacts-open-"))
let server, browser
const step = (name, detail = {}) => console.log(JSON.stringify({ step: name, ...detail }))

try {
  const css = await postcss([tailwindcss()]).process(await readFile("app/globals.css", "utf8"), { from: "app/globals.css" })
  await build({
    entryPoints: ["e2e/artifacts-open-inside.fixture.tsx"], bundle: true, outfile: join(output, "bundle.js"), platform: "browser", format: "esm", jsx: "automatic",
    alias: { "@": process.cwd() }, logLevel: "error", external: ["/fonts/*"],
    define: { "process.env": JSON.stringify({ NODE_ENV: "test", NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS: "true", NEXT_PUBLIC_CHAT_CRAFT_LEASE: "true", NEXT_PUBLIC_ARTIFACTS_LIBRARY: "true", NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS: "true" }) },
    plugins: [{ name: "fixture-neighbours", setup(b) {
      b.onResolve({ filter: /(?:^@\/hooks\/use-auth$|\/hooks\/use-auth(?:\.tsx?)?$|^@\/hooks\/use-workspace$|^@\/hooks\/use-page-workspace$|^next\/navigation$)/ }, args => ({ path: stubOf(args.path), namespace: "fixture" }))
      b.onLoad({ filter: /.*/, namespace: "fixture" }, args => ({ loader: "js", contents: STUBS[args.path], resolveDir: process.cwd() }))
    } }],
  })
  server = createServer(async (request, response) => {
    const { pathname } = new URL(request.url, "http://host")
    if (pathname === "/bundle.js") { response.writeHead(200, { "content-type": "text/javascript" }); return response.end(await readFile(join(output, "bundle.js"))) }
    if (pathname === "/bundle.css") { response.writeHead(200, { "content-type": "text/css" }); return response.end(await readFile(join(output, "bundle.css")).catch(() => "")) }
    if (pathname === "/app.css") { response.writeHead(200, { "content-type": "text/css" }); return response.end(css.css) }
    if (pathname !== "/artifacts" && !pathname.startsWith("/artifacts/") && !pathname.startsWith("/pages/")) { response.writeHead(404); return response.end() }
    // Like the Go binary serving artifacts/_.html: every /artifacts address gets the same shell.
    response.writeHead(200, { "content-type": "text/html" })
    response.end(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Artifacts</title><link rel="stylesheet" href="/app.css"><link rel="stylesheet" href="/bundle.css"><body class="bg-background text-foreground antialiased"><div id="root"></div><script type="module" src="/bundle.js"></script></body></html>`)
  })
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve))
  const origin = `http://127.0.0.1:${server.address().port}`
  browser = await chromium.launch({ headless: true })
  const page = await (await browser.newContext({ viewport: { width: 1280, height: 860 }, locale: "en-US" })).newPage()
  page.setDefaultTimeout(TIMEOUT)
  const errors = []
  page.on("pageerror", error => errors.push(error.message))
  const path = () => new URL(page.url()).pathname
  const list = page.getByRole("list", { name: "Artifacts" })
  const projectContent = () => page.frameLocator('iframe[title^="Preview of"]').getByRole("heading", { name: "Simple pricing" }).waitFor()

  // 1. A click in the library opens the project at its own address, with its content.
  await page.goto(`${origin}/artifacts?view=all`)
  await list.getByRole("button", { name: "Open Pricing site" }).click()
  await page.getByRole("complementary", { name: "Preview" }).getByRole("button", { name: "Open", exact: true }).click()
  await projectContent()
  assert.equal(path(), "/artifacts/page-site")
  assert.equal(new URL(page.url()).searchParams.get("view"), "all", "the view it was opened from stays")
  step("project opened", { url: page.url() })

  // 2. Back returns to the same list; Forward opens the project again.
  await page.goBack()
  await list.getByRole("button", { name: "Open Pricing site" }).waitFor()
  assert.equal(path(), "/artifacts")
  await page.goForward()
  await projectContent()
  step("back and forward")

  // 3. The address alone opens it: reload, and a Word from a fresh tab.
  await page.reload()
  await projectContent()
  await page.goto(`${origin}/artifacts/page-brief`)
  await page.getByText("Three partners, one launch date.").waitFor()
  assert.equal(path(), "/artifacts/page-brief")
  step("reload and direct address")

  // 4. A link from before IDs moves to the artifact's address and opens it.
  await page.goto(`${origin}/artifacts?item=pricing-site`)
  await projectContent()
  assert.equal(path(), "/artifacts/pricing-site")
  step("legacy ?item= link")

  // 5. Bookmarks and shared links to /pages/<slug> open the artifact inside Artifacts; Back skips Pages.
  await page.goto(`${origin}/artifacts`)
  await page.goto(`${origin}/pages/%40page-site?workspace_id=ws-fixture&artifact=1`)
  await projectContent()
  assert.equal(path(), "/artifacts/page-site")
  await page.goto(`${origin}/pages/partner-brief`)
  await page.getByText("Three partners, one launch date.").waitFor()
  assert.equal(path(), "/artifacts/partner-brief")
  await page.goBack()
  await projectContent()
  assert.equal(path(), "/artifacts/page-site", "Back does not return to /pages")
  step("old /pages links")

  // 6. Leaving the detail returns to the library at /artifacts.
  await page.getByRole("button", { name: "All artifacts" }).first().click()
  await list.waitFor()
  assert.equal(path(), "/artifacts")

  assert.deepEqual(errors, [], "no page errors")
  assert.deepEqual(await page.evaluate(() => window.__unmocked), [], "every request was mocked")
  step("done")
} finally {
  await browser?.close()
  server?.close()
  await rm(output, { recursive: true, force: true })
}
