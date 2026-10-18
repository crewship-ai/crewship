import { test, expect } from "@playwright/test"
import type { ResourcePolicy } from "../lib/resource-sharing"

type Right = ResourcePolicy["rights"][number]
type Selection = { membership_id: string; revision: number; resource: { kind: "project" | "routine"; id: string }; preset_id: string; preset_version: number; level: string; reissue: Right[] }
const digest = "a".repeat(64)
const entries = ["project", "routine"].map(kind => ({ definition_hash: `${kind}-v1`, definition: {
  preset_id: `${kind}-levels`, version: 1, kind,
  managed_operations: kind === "project" ? ["list", "read", "contribute", "manage"] : ["view", "results", "run", "edit"],
  levels: kind === "project" ? [{ level: "view", operations: ["list", "read"] }, { level: "contribute", operations: ["list", "read", "contribute"] }, { level: "manage", operations: ["list", "read", "contribute", "manage"] }] : [{ level: "view", operations: ["view", "results"] }, { level: "run", operations: ["view", "results", "run"] }, { level: "edit", operations: ["view", "results", "run", "edit"] }],
} }))

// Production export with generic synthetic API fixtures. Actual authorization,
// CAS, retained rows and atomic rollback are covered by real Go router tests.
for (const width of [1280, 390]) test(`explicit preset consent and stale recovery at ${width}px`, async ({ page }, testInfo) => {
  await page.setViewportSize({ width, height: 950 })
  const requests: { path: string; body: unknown }[] = []
  const errors: string[] = []
  let reads = 0, applies = 0
  let normalized: Selection | null = null
  let afterRights: Right[] = []
  let policy: ResourcePolicy = { membership_id: "fixture-member", revision: 7, mode: "restricted", rights: [{ kind: "project", id: "fixture-project", operation: "delete_issues" }], grant_details: [{ kind: "project", id: "fixture-project", operation: "delete_issues", issuer_id: "original-owner", created_at: "2026-10-14T00:00:00Z", live: true }], preset_selections: [] }
  page.on("pageerror", error => errors.push(error.message))
  await page.addInitScript(() => localStorage.setItem("crewship.workspaceId:fixture-owner", "fixture-workspace"))
  await page.route("**/api/**", async route => {
    const request = route.request(), path = new URL(request.url()).pathname
    let body: unknown = [], status = 200
    if (path === "/api/auth/session") body = { user: { id: "fixture-owner", name: "Fixture owner", email: "owner@preset.invalid" }, expires: "2099-01-01T00:00:00Z" }
    else if (path === "/api/v1/workspaces") body = [{ id: "fixture-workspace", name: "Preset fixtures", slug: "presets", currentUserRole: "OWNER", currentUserAccessMode: "trusted" }]
    else if (path.endsWith("/members/capabilities")) body = [{ user_id: "fixture-guest", role: "MEMBER", capabilities: [] }]
    else if (path === "/api/v1/workspaces/fixture-workspace/members") body = [{ id: "fixture-member", role: "MEMBER", access_mode: "restricted", created_at: "2026-10-14T00:00:00Z", user: { id: "fixture-guest", full_name: "Fixture Guest", email: "guest@preset.invalid", avatar_url: null } }]
    else if (path.endsWith("/members/fixture-member/access")) { reads++; body = policy }
    else if (path.endsWith("/access/effective")) body = { membership_id: policy.membership_id, revision: policy.revision, account_restricted: true, explanation: "Current synthetic saved policy", decisions: [] }
    else if (path.endsWith("/access/presets")) body = { presets: entries }
    else if (path.endsWith("/presets/preview")) {
      normalized = request.postDataJSON() as Selection
      requests.push({ path, body: normalized })
      const entry = entries.find(item => item.definition.kind === normalized!.resource.kind)!
      const operations = entry.definition.levels.find(level => level.level === normalized!.level)!.operations
      const managed = (right: Right) => right.kind === normalized!.resource.kind && right.id === normalized!.resource.id && entry.definition.managed_operations.includes(right.operation)
      afterRights = [...policy.rights.filter(right => !managed(right)), ...operations.map(operation => ({ kind: normalized!.resource.kind, id: normalized!.resource.id, operation }))]
      body = { selection: normalized, definition_hash: entry.definition_hash, current_selection: null, before: policy.grant_details, changes: operations.map(operation => ({ right: { kind: normalized!.resource.kind, id: normalized!.resource.id, operation }, action: "add", issuer_id: "fixture-owner", live: true })), after_rights: afterRights, orthogonal_rights: policy.grant_details?.filter(right => !managed(right)), warnings: normalized.resource.kind === "project" ? [{ code: "RETAINED_DELETE_ISSUES", message: "The separate destructive right remains unchanged.", before_eligible: false, after_eligible: true }] : [{ code: "ROUTINE_RESULTS_HISTORY", message: "View includes eligible past and future regular outputs, possibly input material; private audience gates still apply." }], confirmation_digest: digest }
    } else if (path.endsWith("/presets/apply")) {
      const input = request.postDataJSON()
      requests.push({ path, body: input }); applies++
      expect(input).toEqual({ selection: normalized, confirmation_digest: digest })
      if (applies === 1) { status = 409; body = { error: "Resource snapshot changed" } }
      else {
        const selection = normalized!
        policy = { ...policy, revision: 9, rights: afterRights, grant_details: afterRights.map(right => policy.grant_details?.find(old => old.kind === right.kind && old.id === right.id && old.operation === right.operation) ?? { ...right, issuer_id: "fixture-owner", created_at: "2026-10-17T00:00:00Z", live: true }), preset_selections: [{ id: "explicit-choice", kind: selection.resource.kind, resource_id: selection.resource.id, preset_id: selection.preset_id, version: 1, level: selection.level, definition_hash: `${selection.resource.kind}-v1`, selected_by: "fixture-owner", selected_at: "2026-10-17T00:00:00Z", state: "selected" }] }
        body = policy
      }
    } else if (path === "/api/v1/projects") body = [{ id: "fixture-project", name: "Fixture project" }]
    else if (path.endsWith("/pipelines")) body = [{ id: "fixture-routine", name: "Fixture routine" }]
    else if (path === "/api/v1/me/preferences") body = {}
    else if (path === "/api/v1/ws-token") { status = 404; body = {} }
    await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })
  })
  await page.goto("/settings/members?member=fixture-guest")
  await page.getByRole("button", { name: "Edit resource access" }).click()
  const choose = async (kind: string, level: string) => {
    await page.getByLabel("Resource kind", { exact: true }).selectOption(kind)
    await page.getByLabel("Resource", { exact: true }).selectOption(`fixture-${kind}`)
    await expect(page.getByRole("combobox", { name: "Ordered preset" })).toBeEnabled()
    await page.getByRole("combobox", { name: "Ordered preset" }).selectOption(JSON.stringify([`${kind}-levels`, 1, level]))
  }
  await choose("project", "manage")
  await expect(page.getByText("Custom — no preset was explicitly selected")).toBeVisible()
  expect(requests).toHaveLength(0)
  await page.getByRole("button", { name: "Preview preset" }).click()
  await expect(page.getByText(/Eligibility false → true/)).toBeVisible()
  await expect(page.getByRole("button", { name: "Apply confirmed preset" })).toBeDisabled()
  await page.getByRole("checkbox", { name: "I reviewed these exact changes and consequences" }).check()
  await page.getByRole("button", { name: "Apply confirmed preset" }).click()
  await expect(page.getByRole("alert").filter({ hasText: "Access changed" })).toBeVisible()
  expect(applies).toBe(1); expect(reads).toBe(1)
  await page.getByRole("button", { name: "Reload current policy" }).click()
  await expect(page.getByLabel("Resource", { exact: true })).toHaveValue("")
  await choose("routine", "view")
  await page.getByRole("button", { name: "Preview preset" }).click()
  await expect(page.getByText(/eligible past and future regular outputs/)).toBeVisible()
  await page.getByRole("checkbox", { name: "I reviewed these exact changes and consequences" }).check()
  await page.getByRole("button", { name: "Apply confirmed preset" }).click()
  await expect(page.getByText("Preset applied", { exact: true })).toBeVisible()
  expect(applies).toBe(2); expect(requests).toHaveLength(4)
  expect(policy.rights).toContainEqual({ kind: "project", id: "fixture-project", operation: "delete_issues" })
  expect(policy.grant_details?.find(right => right.operation === "delete_issues")?.issuer_id).toBe("original-owner")
  await page.goto("/settings/members?member=fixture-guest")
  await page.getByRole("button", { name: "Edit resource access" }).click()
  await choose("routine", "view")
  await expect(page.getByText("Saved view v1 · selected — intent, not effective authority")).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
  expect(errors).toEqual([])
  await page.screenshot({ path: testInfo.outputPath("ordered-presets.png"), fullPage: true })
})
