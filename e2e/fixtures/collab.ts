/**
 * Two-identity collaboration acceptance (#5144) — shared fixture.
 *
 * Runs only against a disposable server started by scripts/collab-throwaway.sh
 * (scripts/throwaway-server.sh + a Docs engine on the instance's own sockets,
 * seeded with `seed --offline-demo --with-users`). Every scenario creates its
 * own Docs/Design artifacts through the real CLI binary and signs in as
 * genuinely different accounts: the bootstrap owner, a MEMBER with an explicit
 * write grant and a VIEWER with a comment grant. Two windows of one account
 * would not prove anything about rights, so no scenario does that.
 */
import { execFileSync, spawn } from "node:child_process"
import { copyFileSync, mkdirSync, readFileSync } from "node:fs"
import path from "node:path"
import { throwawayRefusal } from "../throwaway-guard"
import { expect, type Browser, type BrowserContext, type Page, type TestInfo } from "@playwright/test"

export interface Identity { email: string; password: string; label: string }

// Fixture accounts minted by `crewship seed --offline-demo --with-users`; the
// passwords are the documented dev-seed values printed by that command.
export const OWNER: Identity = { email: process.env.COLLAB_OWNER_EMAIL ?? "demo@crewship.ai", password: process.env.COLLAB_OWNER_PASSWORD ?? "password123", label: "owner" }
export const EDITOR: Identity = { email: "member-baseline@crewship.local", password: "rbac-demo-1234", label: "editor" }
export const COMMENTER: Identity = { email: "viewer-baseline@crewship.local", password: "rbac-demo-1234", label: "commenter" }

const BIN = process.env.CREWSHIP_BIN ?? "./crewship"
const SERVER = process.env.PLAYWRIGHT_BASE_URL ?? ""
const EVIDENCE = process.env.COLLAB_EVIDENCE_DIR

/** Data dirs of the shared dev slots, from the host's environment registry when present. */
function sharedSlotDataDirs(): string[] {
  try {
    const envs = JSON.parse(readFileSync("/etc/crewship/environments.json", "utf8")) as Record<string, Record<string, unknown>>
    return Object.values(envs).flatMap(e => ["state_root", "data_dir", "dataDir"].map(k => e?.[k]).filter((v): v is string => typeof v === "string"))
  } catch { return [] }
}

/**
 * Never point this suite at a shared slot: it creates, shares, revokes and
 * restarts. The shared guard verifies the manifest, data dir, port and base
 * URL; the restart scenarios additionally need the manifest's own name.
 */
export function requireThrowaway() {
  const refusal = throwawayRefusal(process.env, SERVER, sharedSlotDataDirs())
  if (refusal) throw new Error(`collab suite runs only on a verified throwaway server (scripts/collab-throwaway.sh): ${refusal}`)
  const name = process.env.COLLAB_THROWAWAY_NAME
  if (name) {
    const manifest = JSON.parse(readFileSync(process.env.CREWSHIP_THROWAWAY_MANIFEST!, "utf8")) as { name?: string }
    if (manifest.name !== name) throw new Error("COLLAB_THROWAWAY_NAME is not the name of the verified throwaway instance")
  }
}

/** The owner's CLI, as configured by the throwaway server for this run. */
export function cli(args: string[], input?: string): string {
  return execFileSync(BIN, [...args, "--server", SERVER], { input, encoding: "utf8", env: { ...process.env, NO_COLOR: "1" }, stdio: ["pipe", "pipe", "pipe"] })
}

export function unique(prefix: string) {
  return `${prefix}-${Date.now().toString(36)}${Math.floor(Math.random() * 1e4).toString(36)}`
}

export interface Fixture { id: string; slug: string }

export function createDoc(slug: string, paragraphs: string[], name = slug): Fixture {
  const created = JSON.parse(cli(["page", "create", "--allow-empty", "--file", "-", "-f", "json"], `apiVersion: crewship/v1\nkind: Page\nmetadata:\n  name: ${JSON.stringify(name)}\n  slug: ${slug}\nspec:\n  panels: []\n`)) as { id: string }
  const content = paragraphs.map((text, i) => ({ type: "paragraph", attrs: { id: `p${i + 1}` }, content: [{ type: "text", text }] }))
  cli(["doc", "initialize", slug, "--file", "-"], JSON.stringify({ id: `init-${slug}`, schema: 4, page_id: created.id, body: { type: "doc", content } }))
  return { id: created.id, slug }
}

export interface DesignNode { id: string; type: string; name: string; x: number; y: number; width: number; height: number; fill: string; parentId?: string }

/**
 * Readers and commenters reach only the version the owner shared, so `share`
 * saves the live canvas as a version and pins it for them.
 */
export function createDesign(slug: string, nodes: DesignNode[], options: { share?: boolean } = {}): Fixture {
  const created = JSON.parse(cli(["page", "design", "create", slug, "--name", slug, "--test-item", "-f", "json"])) as { page_id: string }
  cli(["page", "design", "apply", slug, "--file", "-"], JSON.stringify({ operations: nodes.map(node => ({ op: "upsert", node })) }))
  if (options.share) {
    const saved = JSON.parse(cli(["page", "design", "snapshot", slug, "-f", "json"])) as { version: number }
    cli(["page", "design", "share", slug, "--version", String(saved.version)])
  }
  return { id: created.page_id, slug }
}

export function designNodes(slug: string): DesignNode[] {
  const view = JSON.parse(cli(["page", "design", "get", slug, "-f", "json"])) as { nodes: DesignNode[] }
  return view.nodes
}

export function readDoc(slug: string): string {
  return cli(["doc", "read", slug, "-f", "json"])
}

export const grant = (slug: string, who: Identity, level: "read" | "comment" | "write") => cli(["page", "grant", slug, "--user", who.email, "--level", level])
export const revoke = (slug: string, who: Identity) => cli(["page", "revoke", slug, "--user", who.email])

export interface Session { who: Identity; context: BrowserContext; page: Page }

/** A fresh browser context signed in through the real credentials endpoint. */
export async function signIn(browser: Browser, who: Identity, testInfo: TestInfo, options: { viewport?: { width: number; height: number } } = {}): Promise<Session> {
  const context = await browser.newContext({ baseURL: SERVER, viewport: options.viewport ?? { width: 1280, height: 860 }, recordVideo: { dir: testInfo.outputPath(`video-${who.label}`), size: options.viewport ?? { width: 1280, height: 860 } } })
  const csrf = await (await context.request.get("/api/auth/csrf")).json() as { csrfToken: string }
  const response = await context.request.post("/api/auth/callback/credentials", { data: { email: who.email, password: who.password, csrfToken: csrf.csrfToken, redirect: "false" } })
  expect(response.ok(), `sign-in for ${who.label}`).toBeTruthy()
  const session = await (await context.request.get("/api/auth/session")).json() as { user?: { email?: string } }
  expect(session.user?.email?.toLowerCase(), `${who.label} session`).toBe(who.email.toLowerCase())
  return { who, context, page: await context.newPage() }
}

/** Close every session and keep its video as evidence. */
export async function closeAll(sessions: Session[], testInfo: TestInfo) {
  for (const session of sessions) {
    const video = session.page.video()
    await session.context.close()
    if (!video) continue
    const file = await video.path()
    await testInfo.attach(`video-${session.who.label}`, { path: file, contentType: "video/webm" })
    if (EVIDENCE) {
      mkdirSync(EVIDENCE, { recursive: true })
      const name = `${testInfo.titlePath.slice(1).join(" ").replace(/[^a-z0-9]+/gi, "-").slice(0, 90)}-${session.who.label}.webm`
      copyFileSync(file, path.join(EVIDENCE, name))
    }
  }
}

export async function screenshot(page: Page, testInfo: TestInfo, name: string) {
  const file = testInfo.outputPath(`${name}.png`)
  await page.screenshot({ path: file })
  if (EVIDENCE) { mkdirSync(EVIDENCE, { recursive: true }); copyFileSync(file, path.join(EVIDENCE, `${name}.png`)) }
}

/** The frame's single status chip: data-status carries the key, never a free text. */
export const statusKey = (page: Page) => page.locator("button[data-status]").first().getAttribute("data-status")

/**
 * Restarts this throwaway instance (`throwaway-server.sh restart`), keeping its
 * data, and stays down for `downSeconds`. The returned promise is already
 * observed, so an assertion failing before it is awaited never leaves an
 * unhandled rejection; awaiting it still throws when the restart failed.
 */
export function restart(name: string, downSeconds = 15): Promise<void> {
  const child = spawn("scripts/throwaway-server.sh", ["restart", name, "--down-seconds", String(downSeconds)], { stdio: "inherit" })
  const done = new Promise<void>((resolve, reject) => {
    child.on("error", reject)
    child.on("exit", code => code === 0 ? resolve() : reject(new Error(`restart exited ${code}`)))
  })
  done.catch(() => {})
  return done
}
