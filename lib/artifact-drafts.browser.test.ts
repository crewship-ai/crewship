// @vitest-environment node
import { spawn, type ChildProcess } from "node:child_process"
import { existsSync, mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { buildSync } from "esbuild"
import { chromium } from "@playwright/test"
import { installLocalDraftKeyFixture, type DraftKeyFixtureStore } from "./local-draft-seal.test-support"
import { expect, it } from "vitest"

const executable = chromium.executablePath()

/** A real Chromium process on a persistent profile, so the test can SIGKILL it. */
async function launch(profile: string): Promise<{ child: ChildProcess; endpoint: string }> {
  const child = spawn(executable, ["--headless=new", "--no-sandbox", "--no-first-run", "--remote-debugging-port=0", `--user-data-dir=${profile}`, "about:blank"], { stdio: ["ignore", "ignore", "pipe"], detached: true })
  const endpoint = await new Promise<string>((resolve, reject) => {
    let output = ""
    const timer = setTimeout(() => reject(new Error(`Chromium did not start: ${output}`)), 15000)
    child.stderr!.on("data", chunk => {
      output += chunk
      const match = /DevTools listening on (ws:\/\/\S+)/.exec(output)
      if (match) { clearTimeout(timer); resolve(match[1]) }
    })
    child.once("exit", () => { clearTimeout(timer); reject(new Error(`Chromium exited: ${output}`)) })
  })
  return { child, endpoint }
}

/**
 * SIGKILL Chromium's whole process group and wait until it is gone. Killing
 * only the parent left its helper processes writing to the profile, so the
 * cleanup failed with ENOTEMPTY (#4772).
 */
async function kill(child: ChildProcess) {
  const group = -child.pid!
  try { process.kill(group, "SIGKILL") } catch { return }
  for (let i = 0; i < 100; i++) {
    try { process.kill(group, 0) } catch { return }
    await new Promise(resolve => setTimeout(resolve, 50))
  }
  throw new Error("Chromium's process group did not exit")
}

async function open(endpoint: string, envelopes: DraftKeyFixtureStore) {
  const browser = await chromium.connectOverCDP(endpoint)
  const context = browser.contexts()[0]
  await context.route("http://localhost/**", route => route.fulfill({ body: "<!doctype html><title>Artifact drafts</title>", contentType: "text/html" }))
  const page = await context.newPage()
  await installLocalDraftKeyFixture(page, { envelopes, human: () => "alice" })
  await page.goto("http://localhost/artifact-drafts")
  const code = buildSync({ entryPoints: [`${process.cwd()}/lib/artifact-drafts.ts`], bundle: true, write: false, format: "iife", globalName: "artifactDrafts", platform: "browser" }).outputFiles[0].text
  await page.addScriptTag({ content: code })
  return { browser, page }
}

it.skipIf(!existsSync(executable))("keeps an awaited draft when the whole browser is killed right after typing", async () => {
  const envelopes: DraftKeyFixtureStore = new Map()
  const profile = mkdtempSync(join(tmpdir(), "artifact-drafts-"))
  try {
    const first = await launch(profile)
    const { page } = await open(first.endpoint, envelopes)
    await page.evaluate(async () => {
      const api = (window as unknown as { artifactDrafts: typeof import("./artifact-drafts") }).artifactDrafts
      await api.writeArtifactDraft(api.artifactDraftKey("artifact-source", "alice", "w", "demo")!, "Typed just before the crash")
    })
    // No grace period: the measured localStorage loss happened within 2.5 s.
    await kill(first.child)

    const second = await launch(profile)
    try {
      const { browser, page: reopened } = await open(second.endpoint, envelopes)
      const result = await reopened.evaluate(async () => {
        const api = (window as unknown as { artifactDrafts: typeof import("./artifact-drafts") }).artifactDrafts
        return {
          owner: await api.readArtifactDraft(api.artifactDraftKey("artifact-source", "alice", "w", "demo")!),
          other: await api.readArtifactDraft(api.artifactDraftKey("artifact-source", "bob", "w", "demo")!),
        }
      })
      expect(result).toEqual({ owner: "Typed just before the crash", other: "" })
      const afterExpiry = await reopened.evaluate(async () => {
        const api = (window as unknown as { artifactDrafts: typeof import("./artifact-drafts") }).artifactDrafts
        await api.suspendOtherArtifactDrafts(null)
        return api.readArtifactDraft(api.artifactDraftKey("artifact-source", "alice", "w", "demo")!)
      })
      expect(afterExpiry).toBe("")
      await browser.close()
    } finally { await kill(second.child) }
  } finally { rmSync(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }) }
}, 60000)
