// Isolated production-component browser test of the Artifacts library (Harbor 2 v87): Home with
// Needs attention, kind chips, grid/table and the New popover; the panel's spaces, search operators
// and a facet (a bottom sheet on a phone); every kind's body inside the ArtifactFrame (#5132: Docs,
// Dashboard, App static and interactive, Slides, Design, Word, Sheet) with its title menu, status chip,
// the lock in the chip's popover (Request edit, Take over editing), the Agent drawer offering only Ask,
// and the Info drawer; a project's comment, new version and restore; and the New artifact dialog.
// Flags follow the release build profile (e2e/build-profile.ts RELEASE_FLAGS and ALWAYS_ON_FLAGS). Every Pages, artifact,
// document and Craft request goes to in-memory mocks; anything else fails the run. Two hooks are
// stand-ins: the Design socket (artifacts-library.design-fixture.ts) and the native Craft host
// (artifacts-library.craft-host-fixture.tsx). No live instance, provider or credentials are used.
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
  { name: "desktop", width: 1440, height: 900 },
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
// The Design canvas's collaboration hook is replaced by a fixed, connected Yjs document (no socket).
const DESIGN_FIXTURE = join(process.cwd(), "e2e/artifacts-library.design-fixture.ts")
// The native Craft host is replaced by a stand-in that keeps its contract with the frame (status, real lock controls).
const CRAFT_HOST_FIXTURE = join(process.cwd(), "e2e/artifacts-library.craft-host-fixture.tsx")

const output = await mkdtemp(join(tmpdir(), "crewship-artifacts-library-"))
let server, browser
const step = (name, detail = {}) => console.log(JSON.stringify({ step: name, ...detail }))

try {
  await mkdir(SHOTS, { recursive: true })
  const css = await postcss([tailwindcss()]).process(await readFile("app/globals.css", "utf8"), { from: "app/globals.css" })
  await build({
    entryPoints: ["e2e/artifacts-library.fixture.tsx"], bundle: true, outfile: join(output, "bundle.js"), platform: "browser", format: "esm", jsx: "automatic",
    alias: { "@": process.cwd() }, logLevel: "error", external: ["/fonts/*"],
    define: { "process.env": JSON.stringify({ NODE_ENV: "test", NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS: "true", NEXT_PUBLIC_CHAT_CRAFT_LEASE: "true", NEXT_PUBLIC_ARTIFACTS_LIBRARY: "true", NEXT_PUBLIC_ARTIFACTS_DESIGN: "true", NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM: "true", NEXT_PUBLIC_ARTIFACTS_SHARING_MOCK: "true" }) },
    plugins: [{ name: "fixture-neighbours", setup(b) {
      b.onResolve({ filter: /(?:^@\/hooks\/use-auth$|\/hooks\/use-auth(?:\.tsx?)?$|^@\/hooks\/use-workspace$|^next\/navigation$)/ }, args => ({ path: stubOf(args.path), namespace: "fixture" }))
      b.onLoad({ filter: /.*/, namespace: "fixture" }, args => ({ loader: "js", contents: STUBS[args.path] }))
      b.onResolve({ filter: /^\.\/use-design$/ }, args => args.importer.includes("artifacts-design") ? { path: DESIGN_FIXTURE } : undefined)
      b.onResolve({ filter: /^@\/components\/features\/pages\/native-craft-editor-host$/ }, args => args.importer.endsWith("artifacts-library/native-editor.tsx") ? { path: CRAFT_HOST_FIXTURE } : undefined)
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
    const peek = page.getByRole("complementary", { name: "Preview" })
    // A click peeks on the right on a desktop (Open in the peek opens the detail); a phone opens it directly.
    const openRow = async name => {
      await list.getByRole("button", { name: `Open ${name}` }).click()
      if (vp.mobile) return
      await peek.getByRole("heading", { name, exact: true }).waitFor()
      await peek.getByRole("button", { name: "Open", exact: true }).click()
    }
    const listed = () => list.getByRole("button").evaluateAll(buttons => buttons.map(b => b.getAttribute("aria-label")))
    const noEmptyPanels = async where => assert.equal(await page.getByText(/declares no panels/i).count(), 0, `${vp.name} ${where}: never "This page declares no panels"`)

    // 1. Home by default: Needs attention by the fixed rule (only the owner's stale panels here, never
    // "unshared"), every artifact once with kind chips and a grid/table toggle, the New popover. Then All.
    await page.getByRole("heading", { level: 1, name: "Artifacts" }).waitFor({ state: "attached" })
    const attention = page.getByRole("region", { name: /^Needs attention/ })
    assert.deepEqual(await attention.getByRole("button").evaluateAll(b => b.map(x => x.getAttribute("aria-label"))), ["Data is stale: Weekly competitor report"])
    const HOME = ["Open Pricing site", "Open Partner brief", "Open Price model", "Open Sales pipeline", "Open Weekly competitor report", "Open Q3 board deck", "Open Q3 support summary", "Open Launch notes", "Open Field survey", "Open Launch landing", "Open Template Crewship Design System"]
    assert.deepEqual((await listed()).sort(), [...HOME].sort())
    assert.equal(await page.getByText("Pricing calculator").count(), 0, "an empty Page is not on Home")
    assert.equal(await page.getByText(/\bPages?\b/).count(), 0, "\"Page\" is never a label")
    await noSideways("home")
    await shot("01-home")
    const chips = page.getByRole("group", { name: "Kind" })
    await chips.getByRole("button", { name: /^Sheets/ }).click()
    assert.deepEqual(await listed(), ["Open Price model"])
    await chips.getByRole("button", { name: /^All/ }).click()
    await page.getByRole("radio", { name: "Table" }).click()
    await page.getByRole("table").waitFor()
    await page.getByRole("radio", { name: "Grid" }).click()
    await list.waitFor()
    await page.getByRole("button", { name: "New artifact", exact: true }).click()
    const newMenu = page.getByRole("dialog", { name: "New artifact" })
    await newMenu.getByRole("region", { name: "Artifacts" }).getByRole("button", { name: "New App" }).waitFor()
    await newMenu.getByRole("region", { name: "Craft · one editor at a time" }).getByRole("button", { name: "New Word" }).waitFor()
    await noSideways("new popover")
    await shot("01c-new-popover")
    await page.keyboard.press("Escape")
    await newMenu.waitFor({ state: "detached" })
    let panel = await openPanel()
    const spaces = panel.getByRole("tablist", { name: "Artifacts" })
    assert.equal(await spaces.getByRole("tab", { name: /^Home/ }).getAttribute("aria-selected"), "true")
    await panel.getByRole("button", { name: /^Empty/ }).waitFor()
    assert.equal(await panel.getByRole("button", { name: /Clean up/ }).count(), 0, "a view with nothing in it is not listed")
    await panel.getByRole("button", { name: /Research/ }).waitFor()
    if (vp.mobile) await shot("01b-panel-sheet")
    await spaces.getByRole("tab", { name: /^All artifacts/ }).click()
    assert.equal(await spaces.getByRole("tab", { name: /^All artifacts/ }).getAttribute("aria-selected"), "true")
    await closePanel()
    await list.waitFor()
    const ALL = ["Open Pricing site", "Open Partner brief", "Open Price model", "Open Sales pipeline", "Open Weekly competitor report", "Open Q3 board deck", "Open Q3 support summary", "Open Launch notes", "Open Field survey", "Open Launch landing", "Open Template Crewship Design System"]
    assert.deepEqual(await listed(), ALL)
    await page.getByRole("toolbar", { name: "Display" }).getByText("11 artifacts").waitFor()
    // A legacy Page of data panels without an application is a Dashboard (kinds.ts), never "Page" and not an App.
    assert.match(await list.getByRole("button", { name: "Open Weekly competitor report" }).innerText(), /\bDashboard\b/)
    assert.doesNotMatch(await list.getByRole("button", { name: "Open Weekly competitor report" }).innerText(), /\bPage\b/)
    assert.doesNotMatch(await list.getByRole("button", { name: "Open Weekly competitor report" }).innerText(), /\bApp\b/)
    assert.match(await list.getByRole("button", { name: "Open Sales pipeline" }).innerText(), /\bDashboard\b/)
    await noSideways("all artifacts")
    await shot("02-all")

    // 2. Search with an operator, then one facet (Who can open: Private) from the folded Filter block.
    panel = await openPanel()
    const search = panel.getByRole("textbox", { name: "Search artifacts" })
    await search.fill("kind:project")
    await closePanel()
    await page.getByRole("toolbar", { name: "Display" }).getByText("4 artifacts").waitFor()
    assert.deepEqual(await listed(), ["Open Pricing site", "Open Q3 support summary", "Open Field survey", "Open Template Crewship Design System"])
    if (!vp.mobile) {
      // 2b. The peek: the list stays with its filter; J/K move the peek, Escape closes it, Enter opens.
      await list.getByRole("button", { name: "Open Pricing site" }).click()
      await peek.getByRole("heading", { name: "Pricing site", exact: true }).waitFor()
      assert.deepEqual(await listed(), ["Open Pricing site", "Open Q3 support summary", "Open Field survey", "Open Template Crewship Design System"], "the list keeps its filter under the peek")
      assert.equal(await list.getByRole("button", { name: "Open Pricing site" }).getAttribute("aria-current"), "true")
      await page.keyboard.press("j")
      await peek.getByRole("heading", { name: "Q3 support summary", exact: true }).waitFor()
      await noSideways("peek")
      await shot("02b-peek")
      await page.keyboard.press("k")
      await peek.getByRole("heading", { name: "Pricing site", exact: true }).waitFor()
      await page.keyboard.press("Escape")
      await peek.waitFor({ state: "detached" })
      assert.deepEqual(await listed(), ["Open Pricing site", "Open Q3 support summary", "Open Field survey", "Open Template Crewship Design System"])
    }
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
    await page.getByRole("toolbar", { name: "Display" }).getByText("11 artifacts").waitFor()
    assert.deepEqual(await listed(), ALL)

    // 3 + 4. Open every kind inside the ArtifactFrame. The project opens in its library viewer:
    // comment, a new version in History, restore. Every other kind opens its own body in the frame.
    // The frame header's way back (the crumb, or a phone's back button); a body may carry its own.
    const back = async () => { await page.getByRole("group", { name: "Artifact" }).getByRole("button", { name: "All artifacts", exact: true }).first().click(); await list.waitFor() }
    const frame = page.locator("[data-artifact-frame]")
    const chip = page.getByRole("button", { name: /^Status: / })
    // `status` is the chip a real signal backs (stale panels, the document session, the lease, the
    // runtime, the design socket); null means the item has none and the frame must not invent one.
    const openItem = async (name, kind, status = null) => {
      await openRow(name)
      // The frame names it once, in the header's title menu, and says which UI kind it is.
      await page.getByRole("button", { name: `${name}, artifact menu` }).waitFor()
      // A design system is known only once its project is read; the frame then names it a Design.
      await page.locator(`[data-artifact-frame="${kind}"]`).waitFor().catch(() => {})
      assert.equal(await frame.getAttribute("data-artifact-frame"), kind, `${name} is a ${kind}`)
      assert.ok(await page.getByRole("heading", { name, exact: true }).count() <= 1, `${name} is titled at most once in the body`)
      if (status) await page.getByRole("button", { name: `Status: ${status}`, exact: true }).waitFor()
      else assert.equal(await chip.count(), 0, `${name}: no invented status`)
    }
    // A project is an App in the frame (#5132): view, output, viewport and tools slots, its queue and history in the drawer.
    const stage = page.locator("[data-app-stage]")
    const viewer = page.locator('section[aria-label="Artifact viewer"], [data-app-stage], [data-dashboard-stage]')
    const header = page.getByRole("group", { name: "Artifact" })
    const openDrawer = async name => {
      if (vp.mobile && name === "History") {
        await header.getByRole("button", { name: "Artifact menu", exact: true }).click()
        await page.getByRole("menuitem", { name: "Version history" }).click()
      } else await header.getByRole("button", { name, exact: true }).click()
    }

    await openItem("Pricing site", "app")
    await stage.waitFor()
    await page.getByRole("group", { name: "Capabilities" }).getByText("Static · no capabilities").waitFor()
    await stage.frameLocator('iframe[title^="Preview of"]').locator("#headline").waitFor()
    await shot("07-app-static")
    await page.getByRole("button", { name: /^Version: v4/ }).waitFor()
    assert.equal(await page.getByRole("link", { name: /Open in Pages|Open page/ }).count(), 0, "a project opens in its viewer, not a link")
    await noEmptyPanels("project")
    const preview = stage.frameLocator('iframe[title^="Preview of"]')
    await preview.locator("#headline").getByText("Simple pricing for every team").waitFor()
    // Comment: the tools slot's Comment, pick #headline in the sandboxed preview, send it for the agent.
    await page.getByRole("button", { name: "Add a comment" }).click()
    await preview.locator("#headline").click()
    const pin = page.getByRole("form", { name: "New comment" })
    await pin.getByText("On #headline", { exact: false }).waitFor()
    await pin.getByRole("textbox", { name: "Comment" }).fill("Name the Enterprise plan in the headline")
    await pin.getByRole("button", { name: "Send to agent" }).click()
    await stage.getByText("Comment saved for the agent").waitFor()
    const posted = await page.evaluate(() => window.__project.state.comments.map(c => ({ selector: c.selector, path: c.path, version: c.version, status: c.status, text: c.text })))
    assert.deepEqual(posted, [{ selector: "#headline", path: "plans.html", version: 4, status: "pending", text: "Name the Enterprise plan in the headline" }])
    await openDrawer("Comments")
    const queue = page.getByRole("region", { name: "Comment queue" })
    await queue.getByText("Name the Enterprise plan in the headline").waitFor()
    await noSideways("project comment")
    await shot("04-project-comment")
    // The agent answers with v5: History shows it as Latest and the comment is answered.
    assert.equal(await page.evaluate(() => window.__agentPublish()), 5)
    await openDrawer("History")
    const versions = page.getByRole("region", { name: "Version history" })
    const v5 = versions.getByRole("listitem").filter({ hasText: "Headline answers the comment" })
    await v5.getByText("v5", { exact: true }).waitFor()
    await v5.getByText("Latest", { exact: true }).waitFor()
    assert.equal(await versions.getByRole("listitem").count(), 5)
    await openDrawer("Comments")
    await queue.getByText("Reworded the headline.").waitFor()
    await openDrawer("History")
    await noSideways("project history")
    await shot("05-project-history")
    // Restore v1 (newest first, so the last restore button): v6, nothing deleted.
    await versions.getByRole("button", { name: "Restore as new version" }).last().click()
    await stage.getByText("Restored v1 as v6 · nothing deleted").waitFor()
    const v6 = versions.getByRole("listitem").filter({ hasText: "v6" }).first()
    await v6.getByText("Restored from v1", { exact: false }).waitFor()
    await v6.getByText("Latest", { exact: true }).waitFor()
    assert.equal(await versions.getByRole("listitem").count(), 6, "restore deletes nothing")
    const last = await page.evaluate(() => window.__project.state.versions.at(-1))
    assert.equal(last.version, 6); assert.equal(last.restored_from, 1)
    await page.getByRole("button", { name: "Close panel" }).click()
    await preview.locator("#headline").getByText("Simple pricing", { exact: true }).waitFor()
    await shot("06-project-restored")
    await back()

    // The Agent drawer offers only what has a backend here: Ask, which opens the chat with this artifact
    // as context. No item in this fixture admits an agent mode, so there is never a mode switch.
    const agentAsk = async (slug, prompt) => {
      await header.getByRole("button", { name: "Agent", exact: true }).click()
      const agent = page.getByRole("complementary", { name: "Agent" })
      await agent.getByRole("textbox", { name: "What should the agent do?" }).fill(prompt)
      assert.equal(await agent.getByRole("radiogroup", { name: "Agent mode" }).count(), 0, `${slug}: no agent mode without a backend`)
      assert.match(await agent.getByRole("link", { name: "Ask in chat" }).getAttribute("href"), new RegExp(`^/chat\\?.*page=${slug}.*prompt=`))
      return agent
    }
    const closeDrawer = async drawer => {
      await drawer.getByRole("button", { name: "Close panel" }).click()
      await drawer.waitFor({ state: "detached" }).catch(async () => assert.equal(await drawer.isVisible(), false))
    }
    // The status chip's popover: the lock and its actions live only here (#5132), never as a bar on the page.
    const statusPopover = page.getByRole("dialog", { name: "Status" })
    const openStatus = async () => { await chip.click(); await statusPopover.waitFor(); return statusPopover }
    const closeStatus = async () => { await page.keyboard.press("Escape"); await statusPopover.waitFor({ state: "detached" }) }
    const lockControls = page.getByRole("toolbar", { name: "Document editing" })

    // Every kind opens in place: nothing links out to /pages (owner, 8 Nov 2026). A typed Dashboard
    // whose own view waits behind NEXT_PUBLIC_ARTIFACT_DASHBOARDS opens in the generic project viewer;
    // a legacy deck shows its saved slides read-only while NEXT_PUBLIC_SLIDES_EDITOR is off.
    const KIND_VIEWS = [
      { name: "Launch notes", kind: "docs", slug: "launch-notes", status: "Live", shot: "07-docs" },
      { name: "Weekly competitor report", kind: "dashboard", slug: "weekly-competitor-report", status: "Stale · 1 panel", shot: "07-dashboard" },
      { name: "Sales pipeline", kind: "dashboard", slug: "sales-pipeline", viewer: true, shot: "07-dashboard-typed" },
      { name: "Field survey", kind: "app", slug: "field-survey", status: "Can't run here", viewer: true, shot: "07-app-interactive" },
      { name: "Q3 board deck", kind: "slides", slug: "q3-board-deck", shot: "07-slides" },
      { name: "Launch landing", kind: "design", slug: "launch-landing", status: "Live", shot: "07-design" },
      { name: "Template Crewship Design System", kind: "design", slug: "ref-design-system", status: "Saved · v1", library: true, shot: "07-design-library" },
      { name: "Partner brief", kind: "word", slug: "partner-brief", status: "Jana Nováková is editing", shot: "07-word" },
      { name: "Price model", kind: "sheet", slug: "price-model", status: "Free to edit", shot: "07-sheet" },
    ]
    for (const entry of KIND_VIEWS) {
      await openItem(entry.name, entry.kind, entry.status)
      assert.equal(await page.getByRole("link", { name: "Open full view" }).count(), 0, `${entry.name} opens in place`)
      assert.equal(await page.getByText(/Open full view|Open in Pages|Open page\b/).count(), 0, `${entry.name}: the frame never offers a way out to Pages`)
      assert.equal(await page.locator('a[href^="/pages"]').count(), 0, `${entry.name}: no link into /pages`)
      assert.equal(await viewer.count(), entry.viewer ? 1 : 0, `${entry.name}: a project viewer only for a project`)
      assert.equal(await lockControls.count(), 0, `${entry.name}: no lock bar outside the status chip`)
      await noEmptyPanels(entry.name)
      assert.doesNotMatch(await page.locator("body").textContent(), /\[object Object\]/, `${entry.name}: no stringified object in the page`)

      if (entry.kind === "docs") {
        // The live document, confirmed by the server; its outline and counts sit in the frame's slots.
        const doc = page.getByRole("region", { name: "Doc page" })
        // The frame names it and Info says who owns it: no second title or owner line above the document.
        assert.equal(await doc.getByText(/owned by/).count(), 0, "no owner line in the Docs body")
        await doc.getByRole("textbox", { name: "Document editor" }).getByText("The launch moves to Tuesday", { exact: false }).waitFor()
        await doc.getByRole("status").getByText("Saved · Confirmed by server").waitFor()
        // The counts sit in the viewport slot, which a phone hides (FRAME.md).
        if (!vp.mobile) await page.getByRole("group", { name: "Reading" }).waitFor()
        const status = await openStatus()
        await status.getByText("Everyone sees changes as they happen.").waitFor()
        await closeStatus()
      }
      if (entry.name === "Weekly competitor report") {
        // A legacy panel Page shows its own panels here (#4483), with the stale one marked.
        await page.getByRole("heading", { name: "Weekly competitor report", level: 1, exact: true }).waitFor()
        await page.getByText("Share of voice", { exact: true }).waitFor()
        // The frame is its only header: no second breadcrumb, no @id, no raw owner id.
        const body = page.locator("[data-artifact-stage]")
        assert.equal(await body.getByText("@page-weekly").count(), 0, "no raw page reference inside the frame")
        assert.equal(await body.getByText("fixture-human").count(), 0, "no raw person id inside the frame")
        assert.equal(await body.getByRole("button", { name: "All artifacts" }).count(), 0, "no second back link inside the frame")
      }
      if (entry.name === "Sales pipeline") {
        await page.getByTitle("Artifact preview").or(page.locator('section[aria-label="Artifact viewer"] iframe')).first().waitFor()
      }
      if (entry.name === "Q3 board deck") {
        // The saved deck, read-only: its slides as the canvas draws them, nothing to edit.
        const deck = page.getByRole("region", { name: "Saved deck" })
        await deck.getByText(/^Slide 1 of \d+$/).waitFor()
        assert.equal(await deck.getByRole("button").count(), 0, "the saved deck is read-only")
      }
      if (entry.kind === "app" && !entry.library) {
        // Interactive, but no isolated runtime origin here: it says so and loads nothing.
        await page.getByRole("group", { name: "Capabilities" }).getByLabel(/^User:/).waitFor()
        await page.getByRole("group", { name: "Capabilities" }).getByLabel(/^Data: .*responses/).waitFor()
        await stage.getByRole("alert").getByText("This app can't run here yet").waitFor()
        assert.equal(await stage.locator("iframe").count(), 0, "an app that cannot run loads no frame")
        assert.equal(await stage.getByText("Runs isolated").count(), 0, "an app that cannot run never says it runs isolated")
      }
      if (entry.library) {
        // The library itself in the frame: no second title row, viewer tabs or comment column (#5132).
        await page.getByRole("region", { name: "Colors" }).waitFor()
        assert.equal(await page.locator("[data-kind-mode]").getAttribute("data-kind-mode"), "library")
        assert.equal(await page.getByText("Comments for agent").count(), 0, "no legacy comment column")
        assert.equal(await page.getByRole("tablist", { name: "Artifact detail" }).count(), 0, "no legacy Preview/History/Comments tabs")
        await page.getByRole("toolbar", { name: "View" }).getByRole("button", { name: "Version: v1 · Latest" }).waitFor()
        await page.getByRole("group", { name: "Output" }).getByRole("link", { name: "Edit source" }).waitFor()
      }
      if (entry.kind === "design" && !entry.library) {
        await page.getByRole("region", { name: "Design editor" }).getByRole("img", { name: "Design canvas" }).waitFor()
        // Drawing tools are offered where the canvas can be changed (not on a phone, design-editor.tsx `canChange`).
        if (!vp.mobile) await page.getByRole("toolbar", { name: "Design tools" }).waitFor()
      }
      if (entry.kind === "word") {
        // Held by another person: the chip says who, its popover holds Request edit; no Edit anywhere.
        assert.equal(await page.getByRole("button", { name: "Edit", exact: true }).count(), 0)
        if (vp.mobile) {
          // A phone reads the saved version reflowed, never as an editor.
          const body = page.getByRole("region", { name: "Partner brief, saved version" })
          await body.getByText("Three partners, one launch date", { exact: false }).waitFor()
          assert.equal(await body.locator("[contenteditable=true]").count(), 0, "the preview is read-only")
        }
        await shot(entry.shot)
        const status = await openStatus()
        await status.getByText("You see a read-only preview until they release.").waitFor()
        await status.getByRole("toolbar", { name: "Document editing" }).getByText("Jana Nováková is editing").waitFor()
        assert.equal(await status.getByRole("button", { name: "Take over editing" }).count(), 0, "a held document is not taken over")
        await status.getByRole("button", { name: "Request edit" }).click()
        await status.getByRole("button", { name: "Request sent" }).waitFor()
        await status.getByText("Waiting for the handover.").waitFor()
        assert.deepEqual(await page.evaluate(() => window.__craftActions), [{ pageId: "page-brief", action: "request-handoff" }])
        await noSideways("word status")
        await shot("07-word-status")
        await closeStatus()
        const agent = await agentAsk(entry.slug, "What is still open?")
        assert.equal(await agent.getByRole("textbox", { name: "What should the agent do?" }).getAttribute("placeholder"), "Ask about this document…")
        await noSideways("agent drawer")
        await shot("07-word-agent")
        await closeDrawer(agent)
      }
      if (entry.kind === "sheet") {
        // Free: Take over editing from the popover makes this person the editor; Hand back frees it again.
        await shot(entry.shot)
        let status = await openStatus()
        await status.getByText("Nobody is editing. Take the lock to edit.").waitFor()
        await status.getByRole("button", { name: "Take over editing" }).click()
        await closeStatus()
        await page.getByRole("button", { name: "Status: You are editing", exact: true }).waitFor()
        status = await openStatus()
        await status.getByRole("button", { name: "Hand back" }).click()
        await closeStatus()
        await page.getByRole("button", { name: "Status: Free to edit", exact: true }).waitFor()
        assert.deepEqual((await page.evaluate(() => window.__craftActions)).slice(-2).map(a => a.action), ["acquire", "release"])
        await closeDrawer(await agentAsk(entry.slug, "Sum the Q3 column"))
      }
      if (entry.kind !== "word" && entry.kind !== "sheet") {
        await noSideways(entry.name)
        await shot(entry.shot)
        await closeDrawer(await agentAsk(entry.slug, "Summarise this"))
      }
      await noSideways(entry.name)
      await back()
    }

    // 4b. Sharing (mock of the agreed interface): Share with Comment, the Access tab says who and why;
    // then select two rows, Move with an impact preview, Confirm; Archive previews and deletes nothing.
    await openRow("Price model")
    // Every action lives in the title menu (on a phone, Share has no header button of its own).
    const menu = async () => {
      await page.getByRole("button", { name: vp.mobile ? "Artifact menu" : "Price model, artifact menu", exact: true }).click()
      return page.getByRole("menu")
    }
    if (vp.mobile) await (await menu()).getByRole("menuitem", { name: /^Share/ }).click()
    else await page.getByRole("button", { name: "Share", exact: true }).click()
    const share = page.getByRole("dialog", { name: /Share/ })
    await share.waitFor()
    assert.equal(await share.getByText(/expir|guest|public link/i).filter({ hasNotText: "No public links" }).count(), 0, "no expiry, guests or links")
    await share.getByLabel("Share with", { exact: true }).selectOption({ label: "Jana Nováková" })
    await share.getByLabel("Level", { exact: true }).selectOption({ label: "Comment" })
    await share.getByRole("button", { name: /^Share$/ }).click()
    await share.getByLabel("Level for Jana Nováková").waitFor()
    assert.equal(await share.getByLabel("Level for Jana Nováková").inputValue(), "comment")
    await noSideways("share dialog")
    await shot("08a-share")
    await page.keyboard.press("Escape")
    await share.waitFor({ state: "detached" })
    await (await menu()).getByRole("menuitem", { name: /^Info/ }).click()
    const info = page.getByRole("complementary", { name: "Info" })
    await info.getByText("Jana Nováková").waitFor()
    await noSideways("info drawer")
    await shot("08b-access")
    await info.getByRole("button", { name: "Close panel" }).click()
    await back()
    // A phone list shows its checkboxes only after Select.
    if (vp.mobile) await page.getByRole("button", { name: "Select", exact: true }).click()
    await list.getByRole("checkbox", { name: "Select Partner brief" }).check()
    await list.getByRole("checkbox", { name: "Select Price model" }).check()
    const bulk = page.getByRole("toolbar", { name: "Selection" })
    await bulk.getByText("2 selected").waitFor()
    await bulk.getByRole("button", { name: /^Archive/ }).click()
    const archivePreview = page.getByRole("dialog", { name: /Archive/ })
    await archivePreview.getByText(/not deleted/i).first().waitFor()
    // The dry run: the crew's brief is skipped with its reason, the owner's own sheet would be archived.
    await archivePreview.getByText("1 will be archived, 1 skipped").waitFor()
    await archivePreview.getByText("Only its owner can archive it").waitFor()
    await noSideways("bulk preview")
    await shot("08c-bulk-archive-preview")
    await archivePreview.getByRole("button", { name: "Cancel" }).click()
    await archivePreview.waitFor({ state: "detached" })
    assert.deepEqual(await listed(), ALL, "a cancelled preview changes nothing")
    await bulk.getByRole("button", { name: "Clear" }).click()

    // 5. New artifact from the bar's popover: one entry opens the existing dialog on its flow, then Cancel.
    await page.getByRole("button", { name: "New artifact", exact: true }).click()
    await page.getByRole("button", { name: "New App", exact: true }).click()
    const dialog = page.getByRole("dialog", { name: "New artifact" }).filter({ has: page.getByLabel("Name") })
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
