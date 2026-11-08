// Isolated production-component browser test of the Artifacts library (Harbor 2 v87): Home with
// Needs attention, kind chips, grid/table and the New popover; the panel's spaces, search operators
// and a facet (a bottom sheet on a phone); every kind's detail inside the ArtifactFrame (title menu,
// status chip, Agent and Info drawer); a project's comment, new version and restore; a native Word
// held by another person; and the New artifact dialog. Every Pages, artifact and Craft request goes to in-memory mocks;
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
    define: { "process.env": JSON.stringify({ NODE_ENV: "test", NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS: "true", NEXT_PUBLIC_CHAT_CRAFT_LEASE: "true", NEXT_PUBLIC_ARTIFACTS_LIBRARY: "true", NEXT_PUBLIC_ARTIFACTS_SHARING_MOCK: "true" }) },
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
    const HOME = ["Open Pricing site", "Open Partner brief", "Open Price model", "Open Sales pipeline", "Open Weekly competitor report", "Open Q3 board deck", "Open Q3 support summary"]
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
    const ALL = ["Open Pricing site", "Open Partner brief", "Open Price model", "Open Sales pipeline", "Open Weekly competitor report", "Open Q3 board deck", "Open Q3 support summary"]
    assert.deepEqual(await listed(), ALL)
    await page.getByRole("toolbar", { name: "Display" }).getByText("7 artifacts").waitFor()
    // A legacy Page with panels is an App in the UI's words, not a Dashboard and never "Page".
    assert.match(await list.getByRole("button", { name: "Open Weekly competitor report" }).innerText(), /\bApp\b/)
    assert.doesNotMatch(await list.getByRole("button", { name: "Open Weekly competitor report" }).innerText(), /\bPage\b/)
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
    if (!vp.mobile) {
      // 2b. The peek: the list stays with its filter; J/K move the peek, Escape closes it, Enter opens.
      await list.getByRole("button", { name: "Open Pricing site" }).click()
      await peek.getByRole("heading", { name: "Pricing site", exact: true }).waitFor()
      assert.deepEqual(await listed(), ["Open Pricing site", "Open Q3 support summary"], "the list keeps its filter under the peek")
      assert.equal(await list.getByRole("button", { name: "Open Pricing site" }).getAttribute("aria-current"), "true")
      await page.keyboard.press("j")
      await peek.getByRole("heading", { name: "Q3 support summary", exact: true }).waitFor()
      await noSideways("peek")
      await shot("02b-peek")
      await page.keyboard.press("k")
      await peek.getByRole("heading", { name: "Pricing site", exact: true }).waitFor()
      await page.keyboard.press("Escape")
      await peek.waitFor({ state: "detached" })
      assert.deepEqual(await listed(), ["Open Pricing site", "Open Q3 support summary"])
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
    await page.getByRole("toolbar", { name: "Display" }).getByText("7 artifacts").waitFor()
    assert.deepEqual(await listed(), ALL)

    // 3 + 4. Open every kind inside the ArtifactFrame. The project opens in its library viewer:
    // comment, a new version in History, restore. The others keep their full-view / Open page link.
    // The frame header's way back (the crumb, or a phone's back button); a body may carry its own.
    const back = async () => { await page.getByRole("group", { name: "Artifact" }).getByRole("button", { name: "All artifacts", exact: true }).first().click(); await list.waitFor() }
    const frame = page.locator("[data-artifact-frame]")
    const openItem = async (name, kind) => {
      await openRow(name)
      // The frame names it once, in the header's title menu, and says which UI kind it is.
      await page.getByRole("button", { name: `${name}, artifact menu` }).waitFor()
      assert.equal(await frame.getAttribute("data-artifact-frame"), kind, `${name} is a ${kind}`)
      assert.ok(await page.getByRole("heading", { name, exact: true }).count() <= 1, `${name} is titled at most once in the body`)
      // The chip shows only what a real signal says: the stale report does, an item without one shows none.
      if (name === "Weekly competitor report") await page.getByRole("button", { name: "Status: Stale · 1 panel" }).waitFor()
      else if (name !== "Partner brief") assert.equal(await page.getByRole("button", { name: /^Status: / }).count(), 0, `${name}: no invented status`)
    }
    // A project is an App in the frame (#5132): view, output, viewport and tools slots, its queue and history in the drawer.
    const stage = page.locator("[data-app-stage]")
    const viewer = page.locator('section[aria-label="Artifact viewer"], [data-app-stage], [data-dashboard-stage]')
    const header = page.getByRole("group", { name: "Artifact" })
    const openDrawer = async name => {
      if (vp.mobile && name === "History") {
        await header.getByRole("button", { name: "Artifact menu" }).click()
        await page.getByRole("menuitem", { name: "Version history" }).click()
      } else await header.getByRole("button", { name, exact: true }).click()
    }

    await openItem("Pricing site", "app")
    await stage.waitFor()
    await page.getByRole("group", { name: "Capabilities" }).getByText("Static · no capabilities").waitFor()
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

    const linked = [
      { name: "Sales pipeline", kind: "dashboard", link: "Open full view", href: "/pages/%40page-pipeline?workspace_id=ws-fixture&artifact=1" },
      // A legacy Page shows its own content in the frame (#4483): no way out to Pages.
      { name: "Weekly competitor report", kind: "app", link: null },
      { name: "Partner brief", kind: "word", link: "Open full view", href: "/pages/partner-brief?workspace_id=ws-fixture" },
      { name: "Price model", kind: "sheet", link: "Open full view", href: "/pages/price-model?workspace_id=ws-fixture" },
      { name: "Q3 board deck", kind: "slides", link: "Open full view", href: "/pages/q3-board-deck?workspace_id=ws-fixture" },
    ]
    for (const entry of linked) {
      await openItem(entry.name, entry.kind)
      if (entry.link) {
        const link = page.getByRole("link", { name: entry.link })
        await link.waitFor()
        assert.equal(await link.getAttribute("href"), entry.href, `${entry.name} links to its full view`)
      } else assert.equal(await page.getByRole("link", { name: /Open full view|Open page|Open in Pages/ }).count(), 0, `${entry.name} opens in place`)
      assert.equal(await viewer.count(), 0, `${entry.name} has no project viewer`)
      await noEmptyPanels(entry.name)
      assert.equal(await page.getByText(/Open in Pages/).count(), 0, `${entry.name}: the frame never offers Open in Pages`)
      if (entry.kind === "word") {
        // Native Word held by another person: the lock and Request edit, no Edit.
        await page.getByText("Editing: Jana Nováková").waitFor()
        assert.equal(await page.getByRole("button", { name: "Edit", exact: true }).count(), 0)
        await page.getByRole("button", { name: "Request edit" }).click()
        await page.getByText("Request sent to Jana Nováková · they can hand over or decline").waitFor()
        // The saved version reads in place, never as an editor.
        const body = page.getByRole("region", { name: "Partner brief, saved version" })
        await body.getByText("Three partners, one launch date").waitFor()
        assert.equal(await body.locator("[contenteditable=true]").count(), 0, "the preview is read-only")
        // Agent: no agent path here but chat, so Ask only — no mode switch — opening the chat with the artifact as context.
        await page.getByRole("button", { name: "Agent", exact: true }).click()
        const agent = page.getByRole("complementary", { name: "Agent" })
        await agent.getByRole("textbox", { name: "What should the agent do?" }).fill("What is still open?")
        assert.equal(await agent.getByRole("radiogroup", { name: "Agent mode" }).count(), 0)
        assert.match(await agent.getByRole("link", { name: "Ask in chat" }).getAttribute("href"), /^\/chat\?.*page=partner-brief.*prompt=What/)
        await noSideways("agent drawer")
        await shot("07-word-agent")
        await agent.getByRole("button", { name: "Close panel" }).click()
        await agent.waitFor({ state: "detached" }).catch(async () => assert.equal(await agent.isVisible(), false))
      }
      await noSideways(entry.name)
      await shot(`07-${entry.kind}`)
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
