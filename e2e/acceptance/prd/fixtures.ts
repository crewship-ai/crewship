// The single import of a PRD scenario:
//   import { test, expect, prdRequirement } from "./fixtures"
// `page`, `context` and `request` are authenticated as the owner against the
// origin under test. `prd` carries mode gating, other roles, a disposable QA
// workspace and evidence files.
import { test as base, expect, type APIRequestContext, type Browser, type BrowserContext, type TestInfo } from "@playwright/test"
import * as fs from "node:fs"
import * as path from "node:path"
import { apiAs, createQaWorkspace, loginStorageState, deleteQaWorkspace, provisionMember, type ProvisionedMember, type QaWorkspace, type StorageState } from "./auth"
import { takeTrackedPages } from "./created"
import { outputPaths, readPrdEnv, type PrdAccount, type PrdEnv, type PrdMode, type PrdRole } from "./env"
import type { RunInfo } from "./global-setup"
import { PRD_EVIDENCE } from "./requirements"
import { slug } from "./report.mjs"

export { expect }
export { prdRequirement } from "./requirements"
export type { PrdAccount, PrdEnv, PrdMode, PrdRole, ProvisionedMember, QaWorkspace }

export class PrdScenario {
  private disposers: Array<() => Promise<unknown>> = []
  private workspace: QaWorkspace | null = null

  constructor(
    readonly env: PrdEnv,
    readonly run: RunInfo,
    private readonly testInfo: TestInfo,
    private readonly browser: Browser,
    private readonly owner: APIRequestContext,
  ) {}

  get mode(): PrdMode {
    return this.env.mode
  }
  get origin(): string {
    return this.env.origin
  }
  get appSha(): string {
    return this.run.appSha
  }

  /** Skips the test in quick mode (recorded as skipped with the reason). */
  requireFull(reason: string): void {
    this.testInfo.skip(this.env.mode !== "full", `full mode only: ${reason}`)
  }

  /**
   * Account of a configured role. Missing in quick mode → skip; missing in
   * full mode → fail, because an unconfigured run must not look green.
   */
  requireAccount(role: PrdRole): PrdAccount {
    const account = this.env.accounts[role]
    if (account) return account
    const vars = `PRD_${role.toUpperCase()}_EMAIL/PRD_${role.toUpperCase()}_PASSWORD`
    this.testInfo.skip(this.env.mode === "quick", `needs ${vars}`)
    throw new Error(`full mode needs ${vars}`)
  }

  /** Seeded agent for full mode (PRD_AGENT_SLUG, PRD_AGENT_WORKSPACE_ID). Skips in quick mode. */
  requireAgent(): { slug: string; workspaceId: string } {
    this.requireFull("needs a real agent")
    if (!this.env.agentSlug || !this.env.agentWorkspaceId) {
      throw new Error("full mode needs PRD_AGENT_SLUG and PRD_AGENT_WORKSPACE_ID for this scenario")
    }
    return { slug: this.env.agentSlug, workspaceId: this.env.agentWorkspaceId }
  }

  /** crewship CLI binary (PRD_CLI). Missing in quick mode → skip; in full mode → fail. */
  requireCli(): string {
    if (this.env.cli) return this.env.cli
    this.testInfo.skip(this.env.mode === "quick", "needs PRD_CLI")
    throw new Error("full mode needs PRD_CLI for this scenario")
  }

  /**
   * A NEW login for the role. Every context gets its own session: contexts that
   * share one login also share its refresh token, the editor rotates it, and the
   * next context to present the old token is treated as a replay, which revokes
   * the whole session for everybody holding it (internal/api/nextauth.go).
   */
  private async freshState(role: PrdRole): Promise<StorageState> {
    return loginStorageState(this.env.origin, this.requireAccount(role))
  }

  /** API context of a configured role (logged in by global setup); closed after the test. */
  async api(role: PrdRole): Promise<APIRequestContext> {
    if (role === "owner") return this.owner
    const ctx = await apiAs(this.env.origin, await this.freshState(role))
    this.disposers.push(() => ctx.dispose())
    return ctx
  }

  /** Browser context of a configured role, or of an explicit storage state; closed after the test. */
  async browserContext(who: PrdRole | StorageState): Promise<BrowserContext> {
    const storageState = typeof who === "string" ? await this.freshState(who) : who
    const ctx = await this.browser.newContext({ baseURL: this.env.origin, storageState, viewport: { width: 1440, height: 1000 } })
    this.disposers.push(() => ctx.close())
    return ctx
  }

  /**
   * The test's QA workspace: PRD_WORKSPACE_ID when set, otherwise a fresh
   * workspace created on first call and deleted after the test
   * (PRD_KEEP_WORKSPACES=1 keeps it for inspection).
   */
  async qaWorkspace(): Promise<QaWorkspace> {
    if (this.workspace) return this.workspace
    if (this.env.workspaceId) {
      const res = await this.owner.get(`/api/v1/workspaces/${encodeURIComponent(this.env.workspaceId)}`)
      expect(res.status(), "PRD_WORKSPACE_ID must be readable by the owner").toBe(200)
      const body = (await res.json()) as { id: string; slug: string }
      this.workspace = { id: body.id, slug: body.slug }
      return this.workspace
    }
    const ws = await createQaWorkspace(this.owner, this.testInfo.titlePath.slice(1, 2).join(" ") || this.testInfo.title)
    this.workspace = ws
    if (process.env.PRD_KEEP_WORKSPACES !== "1") {
      this.disposers.push(async () => {
        const status = await deleteQaWorkspace(this.owner, ws)
        if (status >= 300) console.warn(`[prd] QA workspace ${ws.id} not deleted (HTTP ${status})`)
      })
    }
    return ws
  }

  /** Provisions a new synthetic person in the QA workspace (credentials stay in memory). */
  async provision(role: "ADMIN" | "MANAGER" | "MEMBER" | "VIEWER", name?: string): Promise<ProvisionedMember> {
    return provisionMember(this.owner, this.env.origin, await this.qaWorkspace(), role, name)
  }

  /**
   * Writes an evidence file under <outputDir>/evidence/<scenario>/<test>/ and
   * attaches it; the first evidence file becomes the row's evidence path.
   * Objects are written as JSON. Never pass credentials or cookies.
   */
  async evidence(name: string, data: unknown): Promise<string> {
    const scenario = path.basename(this.testInfo.file).replace(/\.acceptance\.ts$/, "")
    const dir = path.join(outputPaths(this.env.outputDir).evidence, slug(scenario), slug(this.testInfo.title))
    fs.mkdirSync(dir, { recursive: true })
    const file = path.join(dir, path.basename(name))
    const body = typeof data === "string" || Buffer.isBuffer(data) ? data : JSON.stringify(data, null, 2) + "\n"
    fs.writeFileSync(file, body)
    // The annotation carries the stable path for the result row; the
    // attachment only makes the file visible in Playwright's own reports.
    this.testInfo.annotations.push({ type: PRD_EVIDENCE, description: path.relative(this.env.outputDir, file) })
    await this.testInfo.attach(path.basename(name), { path: file })
    return file
  }

  /** Internal: deletes the pages the test created (also after a failure), closes contexts, deletes the QA workspace. */
  async dispose(): Promise<void> {
    const pages = takeTrackedPages()
    if (pages.length > 0 && !this.env.workspaceId) {
      // A fresh login: the test's own session may be revoked or expired by now.
      // Pages inside a QA workspace go with the workspace, the others do not.
      const qaId = this.workspace?.id
      const own = pages.filter((p) => p.workspaceId !== qaId)
      if (own.length > 0) {
        let api: APIRequestContext | undefined
        try {
          api = await apiAs(this.env.origin, await this.freshState("owner"))
          for (const p of own) {
            const ref = /^[a-z0-9]{20,}$/i.test(p.ref) ? `@${p.ref}` : p.ref
            const res = await api.delete(`/api/v1/pages/${encodeURIComponent(ref)}?workspace_id=${encodeURIComponent(p.workspaceId)}`)
            if (res.status() >= 300 && res.status() !== 404) console.warn(`[prd] page ${p.ref} not deleted (HTTP ${res.status()})`)
          }
        } catch (err) {
          console.warn(`[prd] page cleanup failed: ${String(err)}`)
        } finally {
          await api?.dispose()
        }
      }
    }
    for (const d of this.disposers.reverse()) {
      try {
        await d()
      } catch (err) {
        console.warn(`[prd] cleanup failed: ${String(err)}`)
      }
    }
  }
}

function readRun(env: PrdEnv): RunInfo {
  return JSON.parse(fs.readFileSync(outputPaths(env.outputDir).run, "utf8")) as RunInfo
}

export const test = base.extend<{ prd: PrdScenario }, { prdEnv: PrdEnv; prdRun: RunInfo }>({
  // The owner of each test signs in again instead of replaying the cookies of
  // global setup: a login lives 15 minutes and a run lasts far longer, and a
  // shared refresh token gets the session revoked (see freshState above).
  storageState: async ({ prdEnv }, use) => {
    const owner = prdEnv.accounts.owner
    await use(owner ? await loginStorageState(prdEnv.origin, owner) : undefined)
  },
  prdEnv: [async ({}, use) => use(readPrdEnv()), { scope: "worker" }],
  prdRun: [async ({ prdEnv }, use) => use(readRun(prdEnv)), { scope: "worker" }],
  prd: async ({ prdEnv, prdRun, browser, request }, use, testInfo) => {
    const scenario = new PrdScenario(prdEnv, prdRun, testInfo, browser, request)
    try {
      await use(scenario)
    } finally {
      await scenario.dispose()
    }
  },
})
