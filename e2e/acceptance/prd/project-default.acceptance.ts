// PRD §7 milestone B, "project-default" (#4994, #4995): a brand-new workspace
// has Artifact Projects as a normal default. Unlike the other project rows this
// scenario never calls enableProjects(): nothing turns a flag on. In the fresh
// QA workspace it creates a project, publishes a new version, gets a 409 on a
// stale base, shares v2 and restores v1 as a new version, all through the API,
// and opens the links the API returned in the browser. A provisioned reader
// sees only the shared version and cannot write. The returned links are
// Artifacts addresses that keep the workspace and the shared version, and an
// old /pages/<slug>?artifact=1&version=N link still opens that version. Last,
// a workspace manager's explicit off hides Projects again.
import { loginStorageState } from "./auth"
import { trackPage } from "./created"
import { expect, prdRequirement, test } from "./fixtures"
import { HERO, grantRead, heroOf, pinWorkspace, preview, projectSlug, publish, snapshot, threeScreens, wsQuery } from "./project/project-api"

const V1 = "Welcome to the PRD default cafe"
const V2 = "Welcome to the PRD default cafe, open 8-18"

interface Link { path: string; workspace: string | null; version: string | null; legacy: boolean }

function link(raw: unknown): Link {
  expect(typeof raw, "the response carries a url").toBe("string")
  const u = new URL(String(raw), "http://origin.invalid")
  return { path: u.pathname, workspace: u.searchParams.get("workspace_id"), version: u.searchParams.get("version"), legacy: u.searchParams.has("artifact") }
}

test.describe("project default", () => {
  test(
    "a new workspace has Projects without any flag change, and its links keep the workspace and the shared version",
    {
      annotation: [
        prdRequirement("B.project.default-on", "A new workspace has Projects with no flag change: create, version, conflict, share, restore and a read-only reader; a workspace manager's explicit off hides them"),
        prdRequirement("B.project.artifacts-links", "Create, share and restore return Artifacts links that keep the workspace and the exact shared version; old Pages project links open the same version"),
      ],
    },
    async ({ prd, request, page, context }) => {
      test.setTimeout(Math.max(test.info().timeout, 10 * 60_000))
      const ws = await prd.qaWorkspace()
      const q = wsQuery(ws.id)

      // Nobody chose anything for this workspace: no override, and Projects are on.
      const flags = await request.get(`/api/v1/feature-flags?${q}`)
      expect(flags.status(), await flags.text()).toBe(200)
      const flag = ((await flags.json()) as Array<{ key: string; enabled: boolean; override_enabled?: boolean }>).find((f) => f.key === "artifact_projects")
      expect(flag, "the artifact_projects flag exists").toBeTruthy()
      expect(flag?.override_enabled, "the fresh workspace has no override").toBeUndefined()
      const availability = async () => {
        const res = await request.get(`/api/v1/pages/artifacts/availability?${q}`)
        expect(res.status(), await res.text()).toBe(200)
        return ((await res.json()) as { enabled?: boolean }).enabled === true
      }
      expect(await availability(), "Projects are available in a new workspace by default").toBe(true)

      // v1, through the API, without enabling anything.
      const slug = projectSlug("prd-default")
      const name = `PRD project ${slug.slice(-10)}`
      const created = await request.post(`/api/v1/pages/artifacts?${q}`, { data: { slug, name, entrypoint: "index.html", files: threeScreens(V1), message: "Three linked screens" } })
      expect(created.status(), await created.text()).toBe(201)
      trackPage(ws.id, slug)
      const receipt = (await created.json()) as { page_id: string; version: number; url: string }
      const id = receipt.page_id
      const artifactPath = `/artifacts/${encodeURIComponent(id)}`
      const createLink = link(receipt.url)
      expect(createLink, "create returns the Artifacts address in this workspace").toEqual({ path: artifactPath, workspace: ws.id, version: null, legacy: false })

      // The UI is there too: the returned link opens the project.
      await pinWorkspace(context, ws.id)
      await page.goto(receipt.url)
      await expect(preview(page).locator(HERO)).toHaveText(V1, { timeout: 60_000 })
      await page.screenshot({ path: test.info().outputPath("owner-created.png"), fullPage: true })

      // v2, then a stale save from the same base is a conflict and changes nothing.
      const v2 = await publish(request, ws.id, slug, 1, threeScreens(V2), "v2")
      expect(v2.status(), await v2.text()).toBe(200)
      const publishLink = link(((await v2.json()) as { url: string }).url)
      const stale = await publish(request, ws.id, slug, 1, threeScreens("lost edit"), "stale")
      const staleBody = (await stale.json().catch(() => ({}))) as { code?: string }
      expect(stale.status(), "a save from a stale base is refused").toBe(409)
      expect((await snapshot(request, ws.id, slug)).latest_version, "the conflict made no version").toBe(2)

      // Share exactly v2.
      const shared = await request.post(`/api/v1/pages/${encodeURIComponent(slug)}/artifact/share?${q}`, { data: { base_version: 2, version: 2 } })
      expect(shared.status(), await shared.text()).toBe(200)
      const shareBody = (await shared.json()) as { shared_version: number; url: string }
      expect(shareBody.shared_version).toBe(2)
      const shareLink = link(shareBody.url)
      expect(shareLink, "share returns the Artifacts address pinned to the shared version").toEqual({ path: artifactPath, workspace: ws.id, version: "2", legacy: false })

      // Restore v1: a new version v3, nothing deleted, the share stays on v2.
      const restored = await request.post(`/api/v1/pages/${encodeURIComponent(slug)}/artifact/restore?${q}`, { data: { base_version: 2, version: 1 } })
      expect(restored.status(), await restored.text()).toBe(200)
      const restoreBody = (await restored.json()) as { version: number; url: string }
      expect(restoreBody.version).toBe(3)
      const restoreLink = link(restoreBody.url)
      const owner = await snapshot(request, ws.id, slug)
      expect({ latest: owner.latest_version, shared: owner.shared_version, hero: heroOf(owner) }).toEqual({ latest: 3, shared: 2, hero: V1 })
      expect(owner.versions.find((v) => v.version === 3)?.restored_from, "v3 is restored from v1").toBe(1)
      expect(owner.versions.map((v) => v.version).sort()).toEqual([1, 2, 3])
      for (const [what, l] of Object.entries({ publish: publishLink, restore: restoreLink })) {
        expect(l, `${what} returns the Artifacts address in this workspace`).toEqual({ path: artifactPath, workspace: ws.id, version: null, legacy: false })
      }

      // The owner's shared link opens v2, not Latest (which is v3 = V1).
      await page.goto(shareBody.url)
      await expect(preview(page).locator(HERO)).toHaveText(V2, { timeout: 60_000 })
      // An old Pages project link moves to the same page, workspace and version.
      await page.goto(`/pages/${encodeURIComponent(slug)}?artifact=1&version=2&${q}`)
      await page.waitForURL((u) => u.pathname === artifactPath && u.searchParams.get("version") === "2" && u.searchParams.get("workspace_id") === ws.id, { timeout: 60_000 })
      await expect(preview(page).locator(HERO)).toHaveText(V2, { timeout: 60_000 })
      const legacyLanding = page.url()

      // A reader: sees the shared version only, cannot write.
      const reader = await prd.provision("VIEWER", "PRD reader")
      await grantRead(request, ws.id, slug, reader.userId)
      const readerCtx = await prd.browserContext(await loginStorageState(prd.origin, reader.account))
      await pinWorkspace(readerCtx, ws.id)
      const readerPage = await readerCtx.newPage()
      await readerPage.goto(shareBody.url)
      await expect(preview(readerPage).locator(HERO)).toHaveText(V2, { timeout: 60_000 })
      for (const control of ["Select", "Restore as new version", "Send to agent", /^Share v\d+$/]) {
        await expect(readerPage.getByRole("button", { name: control, exact: typeof control === "string" })).toHaveCount(0)
      }
      await readerPage.screenshot({ path: test.info().outputPath("reader-shared-link.png"), fullPage: true })
      const api = readerCtx.request
      const read = await snapshot(api, ws.id, slug)
      expect(read.versions.map((v) => v.version), "the reader lists only the shared version").toEqual([2])
      expect(read.can_write ?? false).toBe(false)
      expect(heroOf(read)).toBe(V2)
      const base = `/api/v1/pages/${encodeURIComponent(slug)}`
      const attempts = {
        readV1: (await api.get(`${base}/artifact?${q}&version=1`)).status(),
        readV3: (await api.get(`${base}/artifact?${q}&version=3`)).status(),
        publish: (await api.post(`${base}/artifact/versions?${q}`, { data: { base_version: 3, entrypoint: "index.html", files: threeScreens("reader write"), message: "reader" } })).status(),
        restore: (await api.post(`${base}/artifact/restore?${q}`, { data: { base_version: 3, version: 2 } })).status(),
        share: (await api.post(`${base}/artifact/share?${q}`, { data: { base_version: 3, version: 3 } })).status(),
      }
      for (const [what, status] of Object.entries(attempts)) expect([403, 404], `reader ${what} → ${status}`).toContain(status)
      const after = await snapshot(request, ws.id, slug)
      expect({ latest: after.latest_version, shared: after.shared_version }, "the reader changed nothing").toEqual({ latest: 3, shared: 2 })

      // A workspace manager's explicit off hides Projects in this workspace.
      const off = await request.put(`/api/v1/feature-flags/artifact_projects/override?${q}`, { data: { enabled: false } })
      expect(off.status(), await off.text()).toBeLessThan(300)
      expect(await availability(), "an explicit workspace off wins over the default").toBe(false)
      const hidden = {
        read: (await request.get(`${base}/artifact?${q}`)).status(),
        create: (await request.post(`/api/v1/pages/artifacts?${q}`, { data: { slug: projectSlug("prd-default-off"), name: "PRD off", entrypoint: "index.html", files: threeScreens("off"), message: "off" } })).status(),
      }
      expect(hidden, "Projects are hidden while the workspace has them off").toEqual({ read: 404, create: 404 })
      await page.goto(`${artifactPath}?${q}`)
      await expect(page.getByRole("heading", { name })).toBeVisible({ timeout: 60_000 })
      await expect(preview(page).locator(HERO)).toHaveCount(0)
      await page.screenshot({ path: test.info().outputPath("workspace-off.png"), fullPage: true })

      await prd.evidence("result.json", {
        mode: prd.mode, appSha: prd.appSha, workspace: ws.id, slug, pageId: id,
        defaultFlag: { instanceEnabled: flag?.enabled ?? null, override: flag?.override_enabled ?? null },
        links: { create: createLink, publish: publishLink, share: shareLink, restore: restoreLink, legacyLanding },
        conflict: { status: stale.status(), code: staleBody.code ?? null },
        owner: { latest: owner.latest_version, shared: owner.shared_version, restoredFrom: owner.versions.find((v) => v.version === 3)?.restored_from ?? null },
        reader: { versions: read.versions.map((v) => v.version), canWrite: read.can_write ?? false, hero: heroOf(read), attempts },
        workspaceOff: hidden,
      })
    },
  )
})
