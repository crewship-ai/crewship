// Component browser regression, without touching a live workspace.
// Run after pnpm build: node e2e/issues-work-clarity.mjs
// Uses the real components and compiled application CSS. It does not prove
// authenticated routing, dispatch, or takeover; those have separate scenarios.
import assert from "node:assert/strict"
import { execFileSync } from "node:child_process"
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises"
import path from "node:path"
import { fileURLToPath } from "node:url"
import { chromium } from "@playwright/test"

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..")
const temp = await mkdtemp(path.join(root, ".issues-browser-"))
let browser
try {
  const entry = path.join(temp, "fixture.jsx")
  await writeFile(entry, `
    import React, { useState } from 'react';
    import { createRoot } from 'react-dom/client';
    import { IssueRunsCard } from '../components/features/issues/issue-runs-card';
    import { IssueWorkflowActions } from '../components/features/issues/issue-card-editors';
    const human = { id: 'issue-human', identifier: 'OPS-1', title: 'Prepare the report', status: 'TODO', assignee_type: 'user', assignee_id: 'u1', owner: { id: 'u1', name: 'Petra' } };
    const delegated = { ...human, id: 'issue-agent', delegate: { id: 'a1', name: 'Jordan' } };
    const base = { id: 'r1', run_id: 'run-1', agent_name: 'Jordan', status: 'COMPLETED', duration_ms: 1000 };
    function Fixture() {
      const [action, setAction] = useState('');
      return <main className="mx-auto flex max-w-5xl flex-col gap-4 p-4">
        <h1 className="text-lg font-semibold">Issue work clarity</h1>
        <section data-testid="human"><h2>Human-owned issue</h2><IssueWorkflowActions issue={human} onAction={setAction} /><IssueRunsCard issue={human} runs={[]} /></section>
        <section data-testid="delegated"><h2>Owner and agent delegate</h2><IssueWorkflowActions issue={delegated} onAction={setAction} /><p role="status">{action}</p></section>
        <section data-testid="results"><h2>Reported results</h2><IssueRunsCard issue={delegated} runs={[
          { ...base, outcome: 'FAILED', error_message: 'no outcome reported', task: '[MISSION]\\nInternal prompt scaffolding', result_summary: 'Partial findings. '.repeat(120) + 'Final evidence is preserved.', source: 'delegation' },
          { ...base, id: 'r2', outcome: 'NEEDS_HUMAN', result_summary: 'Which budget should I use?' },
          { ...base, id: 'r3', outcome: 'SUCCEEDED', result_summary: '' },
          { ...base, id: 'r4', outcome: '' },
          { ...base, id: 'r5', status: 'RUNNING', outcome: 'SUCCEEDED', result_summary: '' }
        ]} /></section>
      </main>;
    }
    createRoot(document.getElementById('root')).render(<Fixture />);
  `)
  const bundle = path.join(temp, "fixture.js")
  execFileSync("pnpm", ["exec", "esbuild", entry, "--bundle", "--platform=browser", "--format=iife", "--jsx=automatic", '--define:process.env={"NODE_ENV":"production"}', `--outfile=${bundle}`], { cwd: root, stdio: "pipe" })
  const js = await readFile(bundle, "utf8")
  const cssDir = path.join(root, ".next/static/chunks")
  const cssNames = (await readdir(cssDir)).filter((file) => file.endsWith(".css")).sort()
  assert.ok(cssNames.length, "Run pnpm build first to produce application CSS")
  const css = (await Promise.all(cssNames.map((file) => readFile(path.join(cssDir, file), "utf8")))).join("\n")
  browser = await chromium.launch({ headless: true })
  for (const width of [390, 820, 1440]) {
    const page = await browser.newPage({ viewport: { width, height: 1000 }, reducedMotion: "reduce" })
    page.setDefaultTimeout(15_000)
    const errors = []
    page.on("pageerror", (error) => {
      errors.push(error.message)
      console.error(error.message)
    })
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url())
      if (url.pathname === "/fixture.js") return route.fulfill({ contentType: "text/javascript", body: js })
      if (url.pathname === "/fixture.css") return route.fulfill({ contentType: "text/css", body: css })
      if (url.pathname === "/") return route.fulfill({ contentType: "text/html", body: '<!doctype html><html lang="en" class="dark"><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>Issues regression</title><link rel="stylesheet" href="/fixture.css"></head><body><div id="root"></div><script src="/fixture.js"></script></body></html>' })
      return route.fulfill({ status: 404, body: "Fixture has no backend" })
    })
    await page.goto("https://issues-fixture.test/")
    await page.getByText("Needs human input", { exact: true }).waitFor()
    assert.equal(await page.getByTestId("human").getByRole("button", { name: "Start work" }).count(), 0)
    assert.equal(await page.getByText("Done", { exact: true }).count(), 0)
    assert.equal(await page.getByText("Internal prompt scaffolding", { exact: false }).count(), 0)
    assert.equal(await page.getByText("Reported success", { exact: true }).count(), 1)
    assert.equal(await page.getByText("Running", { exact: true }).count(), 1)
    const start = page.getByTestId("delegated").getByRole("button", { name: "Start work" })
    await start.focus()
    await page.keyboard.press("Enter")
    assert.equal(await page.getByRole("status").innerText(), "start")
    const summary = page.getByText("Reported summary", { exact: true }).first()
    await summary.focus()
    await page.keyboard.press("Enter")
    assert.equal(await summary.locator("..").getAttribute("open"), "")
    assert.equal(await page.getByText(/Final evidence is preserved/).isVisible(), true)
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `horizontal overflow at ${width}px`)
    assert.deepEqual(errors, [], `browser errors at ${width}px`)
    await page.screenshot({ path: `/tmp/issues-work-clarity-${width}.png`, fullPage: true })
    console.log(`PASS ${width}px: human/delegate actions, outcomes, keyboard, complete summary, no overflow`)
    await page.close()
  }
} finally {
  await browser?.close()
  await rm(temp, { recursive: true, force: true })
}
