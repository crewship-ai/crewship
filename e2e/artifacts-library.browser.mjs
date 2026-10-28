// Isolated production-component browser test of the Artifacts library (Harbor 2): Home, the
// panel's spaces, search operators and a facet (a bottom sheet on a phone), every kind's detail,
// a project's comment, new version and restore, a native Word held by another person, and the
// New artifact dialog. Every Pages, artifact and Craft request goes to in-memory mocks;
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
  "use-workspace": 'const ws = { id: "ws-fixture", name: "Fixture" }; export const useCurrentWorkspaceId = () => ws.id; export const useWorkspace = () => ({ workspaces: [ws], workspace: ws, workspaceId: ws.id, role: "OWNER", capabilities: [], loading: false, error: null, workspaceUnavailable: false, setWorkspaceId() {} }); export const bindWorkspaceSession = () => {}; export const useWorkspacePagesTheme = () => undefined; export const refreshWorkspaceSettings = async () => {};',
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
    alias: { "@": process.cwd() }, logLevel: "error", external: ["/fonts/*"],
    define: { "process.env": JSON.stringify({ NODE_ENV: "test", NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS: "true", NEXT_PUBLIC_CHAT_CRAFT_LEASE: "true", NEXT_PUBLIC_ARTIFACTS_LIBRARY: "true" }) },
    plugins: [{ name: "fixture-neighbours", setup(b) {
      b.onResolve({ filter: /(?:^@\/hooks\/use-auth$|\/hooks\/use-auth(?:\.tsx?)?$|^@\/hooks\/use-workspace$|^next\/navigation$)/ }, args => ({ path: stubOf(args.path), namespace: "fixture" }))
      b.onLoad({ filter: /.*/, namespace: "fixture" }, args => ({ loader: "js", contents: STUBS[args.path] }))
    } }],
  })
  server = createServer(async (request, response) => {
    const { pathname, searchParams } = new URL(request.url, "http://host")
    if (pathname === "/bundle.js") { response.writeHead(200, { "content-type": "text/javascript" }); return response.end(await readFile(join(output, "bundle.js"))) }
    if (pathname === "/bundle.css") { response.writeHead(200, { "content-type": "text/css" }); return response.end(await readFile(join(output, "bundle.css")).catch(() => "")) }
    if (/^\/fonts\/[A-Za-z0-9_./-]+\.(woff2|ttf)$/.test(pathname) && !pathname.includes("..")) { response.writeHead(200, { "content-type": pathname.endsWith(".woff2") ? "font/woff2" : "font/ttf" }); return response.end(await readFile(join("public", pathname))) }
    if (pathname === "/app.css") { response.writeHead(200, { "content-type": "text/css" }); return response.end(css.css) }
    if (pathname !== "/") { response.writeHead(404); return response.end() }
    response.writeHead(200, { "content-type": "text/html" })
    response.end(`<!doctype html><html lang="en"${searchParams.has("dark") ? ' class="dark"' : ""}><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Artifacts library</title><link rel="stylesheet" href="/app.css"><link rel="stylesheet" href="/bundle.css"><body class="bg-background text-foreground antialiased"><div id="root"></div><script type="module" src="/bundle.js"></script></body></html>`)
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

    // The panel is docked on a desktop; on a phone it is the SecondaryPanel bottom sheet, opened
    // from the top bar's panel button (the fixture's stand-in carries the same accessible name).
    const sheet = page.locator('[data-slot="secondary-panel-sheet"]')
    const openPanel = async () => {
      if (!vp.mobile) { const docked = page.locator('aside[data-slot="secondary-panel"][aria-label="Artifacts"]'); await docked.waitFor(); return docked }
      await page.getByRole("button", { name: "Artifacts", exact: true }).click()
      await sheet.waitFor()
      return sheet
    }
    const closePanel = async () => {
      if (!vp.mobile) return
      await sheet.getByRole("button", { name: "Close", exact: true }).click()
      await sheet.waitFor({ state: "detached" })
    }
    const list = page.getByRole("list", { name: "Artifacts" })
    const listed = () => list.getByRole("button").evaluateAll(buttons => buttons.map(b => b.getAttribute("aria-label")))
    const noEmptyPanels = async where => assert.equal(await page.getByText(/declares no panels/i).count(), 0, `${vp.name} ${where}: never "This page declares no panels"`)

    // 1. Home by default: Make something, Not shared yet, Recent; the empty Page is hidden. Then All artifacts.
    await page.getByRole("heading", { level: 1, name: "Artifacts" }).waitFor()
    await page.getByRole("heading", { name: "Make something" }).waitFor()
    await page.getByRole("button", { name: "New Project" }).waitFor()
    const attention = page.getByRole("region", { name: "Not shared yet" })
    assert.deepEqual(await attention.getByRole("button").evaluateAll(b => b.map(x => x.getAttribute("aria-label"))), ["Open Partner brief", "Open Q3 support summary"])
    const recent = page.getByRole("region", { name: "Recent" })
    assert.deepEqual(await recent.getByRole("button").evaluateAll(b => b.map(x => x.getAttribute("aria-label"))),
      ["Open Pricing site", "Open Price model", "Open Sales pipeline", "Open Weekly competitor report", "Open Q3 board deck"])
    assert.equal(await page.getByText("Pricing calculator").count(), 0, "an empty Page is not on Home")
    await noSideways("home")
    await shot("01-home")
    let panel = await openPanel()
    const spaces = panel.getByRole("tablist", { name: "Artifacts" })
    assert.equal(await spaces.getByRole("tab", { name: /^Home/ }).getAttribute("aria-selected"), "true")
    await panel.getByRole("button", { name: /^Empty/ }).waitFor()
    assert.equal(await panel.getByRole("button", { name: /Repeated runs/ }).count(), 0, "a view with nothing in it is not listed")
    await panel.getByRole("button", { name: /Research/ }).waitFor()
    if (vp.mobile) await shot("01b-panel-sheet")
    await spaces.getByRole("tab", { name: /^All artifacts/ }).click()
    assert.equal(await spaces.getByRole("tab", { name: /^All artifacts/ }).getAttribute("aria-selected"), "true")
    await closePanel()
    await list.waitFor()
    const ALL = ["Open Pricing site", "Open Partner brief", "Open Price model", "Open Sales pipeline", "Open Weekly competitor report", "Open Q3 board deck", "Open Q3 support summary"]
    assert.deepEqual(await listed(), ALL)
    await page.getByRole("toolbar", { name: "Display" }).getByText("7 artifacts").waitFor()
    // A legacy Page with panels is a Page, not a Dashboard.
    assert.match(await list.getByRole("button", { name: "Open Weekly competitor report" }).innerText(), /\bPage\b/)
    assert.doesNotMatch(await list.getByRole("button", { name: "Open Weekly competitor report" }).innerText(), /Dashboard/)
    assert.match(await list.getByRole("button", { name: "Open Sales pipeline" }).innerText(), /\bDashboard\b/)
    await noSideways("all artifacts")
    await shot("02-all")

    // 2. Search with an operator, then one facet (Who can open: Private) from the folded Filter block.
    panel = await openPanel()
    const search = panel.getByRole("textbox", { name: "Search artifacts" })
    await search.fill("kind:project")
    await closePanel()
    await page.getByRole("toolbar", { name: "Display" }).getByText("2 artifacts").waitFor()
    assert.deepEqual(await listed(), ["Open Pricing site", "Open Q3 support summary"])
    panel = await openPanel()
    const filterButton = panel.getByRole("button", { name: /^Filter/ })
    await filterButton.click()
    const facets = panel.getByRole("group", { name: "Filter artifacts" })
    await facets.waitFor()
    for (const facet of ["Kind", "Who can open", "Version", "Owner"])
      assert.equal(await facets.getByRole("button", { name: facet, exact: true }).getAttribute("aria-expanded"), "false", `${facet} starts folded`)
    await facets.getByRole("button", { name: "Who can open", exact: true }).click()
    const privateOption = facets.getByRole("group", { name: "Who can open" }).getByRole("button", { name: /^Private/ })
    await privateOption.click()
    assert.equal(await privateOption.getAttribute("aria-pressed"), "true")
    await filterButton.click()
    await panel.getByText("Who can open: Private").waitFor()
    if (vp.mobile) await shot("03b-facet-chip")
    await closePanel()
    await page.getByRole("toolbar", { name: "Display" }).getByText("1 artifact").waitFor()
    assert.deepEqual(await listed(), ["Open Q3 support summary"])
    await shot("03-search-facet")
    panel = await openPanel()
    await panel.getByRole("button", { name: "Remove filter Who can open: Private" }).click()
    await panel.getByRole("textbox", { name: "Search artifacts" }).fill("")
    await closePanel()
    await page.getByRole("toolbar", { name: "Display" }).getByText("7 artifacts").waitFor()
    assert.deepEqual(await listed(), ALL)

    // 3 + 4. Open every kind. The project opens in its library viewer: comment, a new version in
    // History, restore. The others land on their Open in Pages / Open page link (Word with its lock).
    const back = async () => { await page.getByRole("button", { name: "All artifacts", exact: true }).click(); await list.waitFor() }
    const openItem = async (name, kind) => {
      await list.getByRole("button", { name: `Open ${name}` }).click()
      // Exactly one title: the library's h1, or the viewer's own header where it opens in the viewer.
      const title = page.getByRole("heading", { level: 1, name }).or(page.locator('section[aria-label="Artifact viewer"]').getByRole("heading", { level: 2, name }))
      await title.waitFor()
      assert.equal(await page.getByRole("heading", { name, exact: true }).count(), 1, `${name} is titled once`)
      assert.ok((await title.locator("xpath=..").innerText()).includes(kind), `${name} is a ${kind}`)
    }
    const viewer = page.locator('section[aria-label="Artifact viewer"]')

    await openItem("Pricing site", "Project")
    await viewer.waitFor()
    assert.equal(await viewer.getByRole("tab", { name: "Preview" }).getAttribute("aria-selected"), "true")
    assert.equal(await page.getByRole("link", { name: /Open in Pages|Open page/ }).count(), 0, "a project opens in its viewer, not a link")
    await noEmptyPanels("project")
    const preview = viewer.frameLocator('iframe[title^="Preview of"]')
    await preview.locator("#headline").getByText("Simple pricing for every team").waitFor()
    // Comment: Select, pick #headline in the sandboxed preview, send it for the agent.
    await viewer.getByRole("button", { name: "Select", exact: true }).click()
    await preview.locator("#headline").click()
    const pin = viewer.getByRole("form", { name: "New comment" })
    await pin.getByText("On #headline", { exact: false }).waitFor()
    await pin.getByRole("textbox", { name: "Comment" }).fill("Name the Enterprise plan in the headline")
    await pin.getByRole("button", { name: "Send to agent" }).click()
    await viewer.getByText("Comment saved for the agent").waitFor()
    const posted = await page.evaluate(() => window.__project.state.comments.map(c => ({ selector: c.selector, path: c.path, version: c.version, status: c.status, text: c.text })))
    assert.deepEqual(posted, [{ selector: "#headline", path: "plans.html", version: 4, status: "pending", text: "Name the Enterprise plan in the headline" }])
    await viewer.getByRole("tab", { name: "Comments", exact: true }).click()
    const queue = viewer.getByRole("region", { name: "Comment queue" })
    await queue.getByText("Name the Enterprise plan in the headline").waitFor()
    await noSideways("project comment")
    await shot("04-project-comment")
    // The agent answers with v5: History shows it as Latest and the comment is answered.
    assert.equal(await page.evaluate(() => window.__agentPublish()), 5)
    await viewer.getByRole("tab", { name: "History", exact: true }).click()
    const versions = viewer.getByRole("region", { name: "Version history" })
    const v5 = versions.getByRole("listitem").filter({ hasText: "Headline answers the comment" })
    await v5.getByText("v5", { exact: true }).waitFor()
    await v5.getByText("Latest", { exact: true }).waitFor()
    assert.equal(await versions.getByRole("listitem").count(), 5)
    await viewer.getByRole("tab", { name: "Comments", exact: true }).click()
    await queue.getByText("Reworded the headline.").waitFor()
    await viewer.getByRole("tab", { name: "History", exact: true }).click()
    await noSideways("project history")
    await shot("05-project-history")
    // Restore v1 (newest first, so the last restore button): v6, nothing deleted.
    await versions.getByRole("button", { name: "Restore as new version" }).last().click()
    await viewer.getByText("Restored v1 as v6 · nothing deleted").waitFor()
    const v6 = versions.getByRole("listitem").filter({ hasText: "v6" }).first()
    await v6.getByText("Restored from v1", { exact: false }).waitFor()
    await v6.getByText("Latest", { exact: true }).waitFor()
    assert.equal(await versions.getByRole("listitem").count(), 6, "restore deletes nothing")
    const last = await page.evaluate(() => window.__project.state.versions.at(-1))
    assert.equal(last.version, 6); assert.equal(last.restored_from, 1)
    await viewer.getByRole("tab", { name: "Preview" }).click()
    await preview.locator("#headline").getByText("Simple pricing", { exact: true }).waitFor()
    await shot("06-project-restored")
    await back()

    const linked = [
      { name: "Sales pipeline", kind: "Dashboard", link: "Open in Pages", href: "/pages/%40page-pipeline?workspace_id=ws-fixture&artifact=1" },
      { name: "Weekly competitor report", kind: "Page", link: "Open page", href: "/pages/weekly-competitor-report?workspace_id=ws-fixture" },
      { name: "Partner brief", kind: "Word", link: "Open in Pages", href: "/pages/partner-brief?workspace_id=ws-fixture" },
      { name: "Price model", kind: "Sheet", link: "Open in Pages", href: "/pages/price-model?workspace_id=ws-fixture" },
      { name: "Q3 board deck", kind: "Slides", link: "Open in Pages", href: "/pages/q3-board-deck?workspace_id=ws-fixture" },
    ]
    for (const entry of linked) {
      await openItem(entry.name, entry.kind)
      const link = page.getByRole("link", { name: entry.link })
      await link.waitFor()
      assert.equal(await link.getAttribute("href"), entry.href, `${entry.name} links to its Page`)
      assert.equal(await viewer.count(), 0, `${entry.name} has no project viewer`)
      await noEmptyPanels(entry.name)
      if (entry.kind === "Word") {
        // Native Word held by another person: the lock and Request edit, no Edit.
        await page.getByText("Editing: Jana Nováková").waitFor()
        assert.equal(await page.getByRole("button", { name: "Edit", exact: true }).count(), 0)
        await page.getByRole("button", { name: "Request edit" }).click()
        await page.getByText("Request sent to Jana Nováková · they can hand over or decline").waitFor()
      }
      await noSideways(entry.name)
      await shot(`07-${entry.kind.toLowerCase()}`)
      await back()
    }

    // 5. New artifact from the SubBar: the dialog with its kinds, then Cancel.
    await page.getByRole("button", { name: "New artifact", exact: true }).click()
    const dialog = page.getByRole("dialog", { name: "New artifact" })
    await dialog.waitFor()
    const kinds = await dialog.getByRole("group", { name: "Kind" }).locator("[data-kind]").evaluateAll(tiles => tiles.map(t => t.getAttribute("data-kind")))
    assert.ok(kinds.includes("project") && kinds.includes("page") && kinds.includes("word"), `creatable kinds: ${kinds.join(", ")}`)
    await dialog.getByLabel("Name").waitFor()
    await noSideways("new artifact")
    await shot("08-new-artifact")
    await dialog.getByRole("button", { name: "Cancel" }).click()
    await dialog.waitFor({ state: "detached" })

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
