// Harness smoke: proves the runner reaches the origin, logs in and identifies
// the deployed build. Read-only in both modes; safe against any deployment.
import { expect, prdRequirement, test } from "./fixtures"

test.describe("harness smoke", () => {
  test(
    "origin is healthy without a session",
    { annotation: prdRequirement("harness.reach", "The runner reaches the origin under test") },
    async ({ prd, playwright }) => {
      const anon = await playwright.request.newContext({ baseURL: prd.origin })
      try {
        const res = await anon.get("/api/health")
        expect(res.status()).toBe(200)
        const body = (await res.json()) as { status?: string }
        expect(body.status).toBe("ok")
        await prd.evidence("health.json", { origin: prd.origin, status: res.status(), body: { status: body.status } })
      } finally {
        await anon.dispose()
      }
    },
  )

  test(
    "owner session identifies the deployed build",
    { annotation: prdRequirement("harness.identity", "Every result row names the exact deployed app SHA") },
    async ({ prd, request, page }) => {
      const version = await request.get("/api/v1/system/version")
      expect(version.status()).toBe(200)
      const v = (await version.json()) as { commit?: string; current?: string }
      expect(v.commit, "the server must report its build commit").toBeTruthy()
      expect(prd.appSha).toBe(v.commit)

      const workspaces = await request.get("/api/v1/workspaces")
      expect(workspaces.status()).toBe(200)

      // The same session works in the browser, not only over the API.
      await page.goto("/")
      await expect(page).not.toHaveURL(/\/login(\b|\/|\?)/)

      await prd.evidence("build.json", { mode: prd.mode, origin: prd.origin, commit: v.commit, version: v.current ?? null })
    },
  )

  test(
    "full mode has the inputs agent scenarios need",
    { annotation: prdRequirement("harness.full-inputs", "Full mode is configured with a seeded agent") },
    async ({ prd, request }) => {
      const agent = prd.requireAgent()
      const res = await request.get(`/api/v1/agents?workspace_id=${encodeURIComponent(agent.workspaceId)}`)
      expect(res.status()).toBe(200)
      const body = (await res.json()) as unknown
      const list = (Array.isArray(body) ? body : ((body as { agents?: unknown[] }).agents ?? [])) as Array<{ slug?: string }>
      expect(list.map((a) => a.slug), `agent ${agent.slug} must exist in PRD_AGENT_WORKSPACE_ID`).toContain(agent.slug)
      await prd.evidence("agent.json", { agentSlug: agent.slug, found: true })
    },
  )
})
