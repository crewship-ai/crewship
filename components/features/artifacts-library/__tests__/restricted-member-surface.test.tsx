import React from "react"
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { serverAllowsRestricted } from "@/lib/__tests__/restricted-route-oracle"

// #5132: a restricted member's only Pages surface moves to /artifacts with
// EXACTLY the same rights. The real routes and the real catalog render here;
// every API request is recorded and judged by the server's own allowlist.
//
// BEFORE is what the member had at ccf155788, where every /pages address
// rendered the Page actions catalog: one catalog read, and one option per
// declared action ("<artifact> — <action>"). After the move they must see the
// same items at /artifacts, a subset at one artifact's address, the access
// gate (with no title) at anything the catalog does not grant, and never one
// request more.
const BEFORE = {
  requests: ["GET /api/v1/workspaces/ws1/restricted-pages"],
  items: ["Brief — Publish", "Brief — Archive", "Launch plan — Notify"],
}
const CATALOG = [
  { id: "page-brief", slug: "brief", name: "Brief", publication: 3, actions: [
    { intent_hash: "i1", panel_id: "p", id: "publish", label: "Publish", inputs: [] },
    { intent_hash: "i2", panel_id: "p", id: "archive", label: "Archive", inputs: [] },
  ] },
  { id: "page-launch", slug: "launch", name: "Launch plan", actions: [{ intent_hash: "i3", panel_id: "q", id: "notify", label: "Notify", inputs: [] }] },
  // Granted but with nothing to run: the old catalog listed no option for it, so nothing names it now either.
  { id: "page-silent", slug: "silent", name: "Silent draft", actions: [] },
]

const requests: string[] = []
const nav = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: nav.replace, push: nav.push, prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => window.location.pathname,
  useParams: () => ({}),
}))
vi.mock("@/lib/api-fetch", () => ({
  broadcastSessionExpired: vi.fn(),
  apiFetch: vi.fn(async (input: string, init?: RequestInit) => {
    requests.push(`${(init?.method ?? "GET").toUpperCase()} ${input}`)
    if (input === "/api/v1/workspaces/ws1/restricted-pages") return new Response(JSON.stringify(CATALOG), { status: 200 })
    return new Response(JSON.stringify({ error: "Resource not found" }), { status: 404 })
  }),
}))
const ROW = { id: "ws1", name: "Alpha", slug: "alpha", currentUserRole: "MEMBER", currentUserAccessMode: "restricted", restricted_session: true, restricted_surfaces: ["chat", "pages", "account_security"] }
vi.mock("@/hooks/use-workspace", async importOriginal => ({
  ...await importOriginal<typeof import("@/hooks/use-workspace")>(),
  useWorkspace: () => ({ workspaceId: "ws1", workspace: ROW, workspaces: [ROW], loading: false, error: false, setWorkspaceId: vi.fn() }),
}))
vi.mock("@/hooks/use-auth", async importOriginal => ({
  ...await importOriginal<typeof import("@/hooks/use-auth")>(),
  useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "u-r", email: "rita@example.test" } } }),
  useSession: () => ({ status: "authenticated", data: { user: { id: "u-r", email: "rita@example.test" } } }),
}))
// Anything a trusted session would mount instead says so, and fails the test.
vi.mock("@/components/features/artifacts-library/artifacts-library", () => ({ ArtifactsLibrary: () => <p>LIBRARY MOUNTED</p> }))
vi.mock("@/components/features/pages/pages-layout", () => ({ PagesLayout: () => <p>PAGES LAYOUT MOUNTED</p> }))
vi.mock("@/components/features/pages/artifact-project", () => ({ ArtifactProject: () => <p>PROJECT MOUNTED</p> }))

const readLoss = vi.hoisted(() => vi.fn(async () => false))
vi.mock("@/lib/mooring/native-read-loss", () => ({ verifyNativeReadLoss: readLoss }))
// The editor host itself must never mount for a restricted member.
vi.mock("@/components/features/pages/native-craft-editor-host", () => ({ NativeCraftEditorHost: () => <p>EDITOR HOST MOUNTED</p> }))
vi.mock("@/components/features/pages/native-craft-preview", () => ({ NativeCraftPreview: () => <p>EDITOR PREVIEW MOUNTED</p> }))

import PagesPage from "@/app/(dashboard)/pages/page"
import CraftPage from "@/app/(dashboard)/pages/craft/editor/page"
import PageDetailRoute from "@/app/(dashboard)/pages/[slug]/page"
import ArtifactsPage from "@/app/(dashboard)/artifacts/page"
import ArtifactRoute from "@/app/(dashboard)/artifacts/[id]/page"

function routeOf(pathname: string) {
  if (pathname === "/pages") return <PagesPage />
  if (pathname === "/pages/craft/editor") return <CraftPage />
  if (pathname.startsWith("/pages/")) return <PageDetailRoute />
  if (pathname === "/artifacts") return <ArtifactsPage />
  if (pathname.startsWith("/artifacts/")) return <ArtifactRoute />
  throw new Error(`no route for ${pathname}`)
}

/** Opens an address like the browser would, following client redirects; resolves with where it landed. */
async function visit(url: string, hops = 0): Promise<string> {
  cleanup()
  nav.replace.mockReset()
  window.history.replaceState(null, "", url)
  render(routeOf(window.location.pathname))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 20)) })
  const target = nav.replace.mock.calls.at(-1)?.[0] as string | undefined
  if (target) {
    if (hops > 2) throw new Error(`redirect loop at ${url}`)
    return visit(target, hops + 1)
  }
  return url
}

const noTrustedView = () => expect(document.body.textContent).not.toMatch(/MOUNTED/)
const noPageWord = () => expect(document.body.textContent ?? "").not.toMatch(/\bPages?\b/)
const forbidden = () => requests.filter(r => { const at = r.indexOf(" "); return !serverAllowsRestricted(r.slice(0, at), r.slice(at + 1)) })
const unique = () => [...new Set(requests)].sort()

/** Every "<artifact> — <action>" the member can reach from the catalog home, opening each artifact. */
async function reachableItems(): Promise<string[]> {
  const home = await screen.findByRole("region", { name: /Shared with you/ })
  const cards = within(home).getAllByRole("link").map(link => ({ name: link.querySelector("b")?.textContent ?? "", href: link.getAttribute("href")! }))
  const items: string[] = []
  for (const card of cards) {
    await visit(card.href)
    const select = await screen.findByRole("combobox", { name: "Action" })
    for (const option of within(select).getAllByRole("option")) if (option.getAttribute("value")) items.push(`${card.name} — ${option.textContent}`)
    noTrustedView()
  }
  return items.sort()
}

async function expectGate(...secrets: string[]) {
  expect(await screen.findByText("This artifact isn’t part of your access")).toBeTruthy()
  expect(screen.getByRole("link", { name: "Go to Artifacts" }).getAttribute("href")).toBe("/artifacts?workspace_id=ws1")
  for (const secret of [...secrets, ...CATALOG.map(entry => entry.name)]) expect(document.body.textContent).not.toContain(secret)
  noTrustedView()
}

beforeEach(() => { requests.length = 0; nav.push.mockReset(); readLoss.mockClear() })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

describe.each(["true", "false"])("a restricted member with NEXT_PUBLIC_ARTIFACTS_LIBRARY=%s", library => {
  beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", library); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN", "true") })

  it("sees at /artifacts exactly the catalog items and requests they had at /pages", async () => {
    expect(await visit("/artifacts?workspace_id=ws1")).toBe("/artifacts?workspace_id=ws1")
    noPageWord()
    expect(await reachableItems()).toEqual([...BEFORE.items].sort())
    expect(unique()).toEqual(BEFORE.requests)
    expect(forbidden()).toEqual([])
    expect(screen.queryByText("Silent draft")).toBeNull()
  })

  it.each([
    ["/pages?workspace_id=ws1", "/artifacts?workspace_id=ws1"],
    ["/pages?ref=page-brief&workspace_id=ws1", "/artifacts/page-brief?workspace_id=ws1"],
    ["/pages/brief?workspace_id=ws1", "/artifacts?workspace_id=ws1&item=brief"],
    ["/pages/brief?artifact=1&version=2&workspace_id=ws1", "/artifacts?workspace_id=ws1&item=brief"],
    ["/pages/brief?mode=edit&workspace_id=ws1", "/artifacts?workspace_id=ws1&item=brief"],
    ["/pages/%40page-brief?workspace_id=ws1", "/artifacts/page-brief?workspace_id=ws1"],
    ["/pages/craft/editor?page_id=page-brief&workspace_id=ws1", "/artifacts/page-brief?workspace_id=ws1"],
  ])("following the old link %s lands on %s", async (link, landing) => {
    expect(await visit(link)).toBe(landing)
    noTrustedView()
    expect(forbidden()).toEqual([])
    expect(unique().every(request => BEFORE.requests.includes(request))).toBe(true)
  })

  it("an old /pages/<slug> link opens that artifact's actions, a subset of before", async () => {
    await visit("/pages/brief?workspace_id=ws1")
    const select = await screen.findByRole("combobox", { name: "Action" })
    const items = within(select).getAllByRole("option").filter(option => option.getAttribute("value")).map(option => `Brief — ${option.textContent}`)
    expect(items).toEqual(["Brief — Publish", "Brief — Archive"])
    expect(items.every(item => BEFORE.items.includes(item))).toBe(true)
    expect(screen.queryByText("Launch plan")).toBeNull()
    noPageWord()
    expect(unique()).toEqual(BEFORE.requests)
  })

  it.each([
    ["an old link to an artifact they lack", "/pages/secret-plan?workspace_id=ws1", "secret-plan"],
    ["a slug address they lack", "/artifacts?item=secret-plan&workspace_id=ws1", "secret-plan"],
    ["a granted artifact with nothing to run", "/artifacts?item=silent&workspace_id=ws1", "silent"],
  ])("%s shows the access gate without a title", async (_name, link, secret) => {
    await visit(link)
    await expectGate(secret)
    expect(unique()).toEqual(BEFORE.requests)
    expect(forbidden()).toEqual([])
  })

  // #5135: a granted artifact's own address opens exactly what ?item=<slug> opens.
  it.each([
    ["its direct /artifacts/<id>", "/artifacts/page-brief?workspace_id=ws1"],
    ["a pinned /artifacts/<id>", "/artifacts/page-brief?workspace_id=ws1&version=2"],
    ["an old /pages/@<id> link", "/pages/%40page-brief?workspace_id=ws1"],
    ["an old native editor link", "/pages/craft/editor?page_id=page-brief&workspace_id=ws1"],
  ])("a granted artifact at %s opens the same actions as its slug link", async (_name, link) => {
    await visit(link)
    const select = await screen.findByRole("combobox", { name: "Action" })
    const byId = within(select).getAllByRole("option").filter(option => option.getAttribute("value")).map(option => `Brief — ${option.textContent}`)
    expect(screen.getByRole("heading", { name: "Brief" })).toBeTruthy()
    const idView = document.body.innerHTML
    expect(screen.queryByText("This artifact isn’t part of your access")).toBeNull()
    expect(screen.queryByText("Launch plan")).toBeNull()
    noTrustedView()
    noPageWord()
    expect(unique()).toEqual(BEFORE.requests)
    expect(forbidden()).toEqual([])
    await visit("/artifacts?item=brief&workspace_id=ws1")
    const bySlug = within(await screen.findByRole("combobox", { name: "Action" })).getAllByRole("option").filter(option => option.getAttribute("value")).map(option => `Brief — ${option.textContent}`)
    expect(byId).toEqual(bySlug)
    expect(idView).toBe(document.body.innerHTML)
  })

  // No existence oracle (PRD §0.6): an ID outside the catalog, whether never
  // granted, nonexistent or deleted, sends the same ID-free catalog read and
  // renders the same gate, byte for byte.
  it("non-granted, nonexistent, deleted and action-less IDs send identical requests and render an identical gate", async () => {
    const seen: { requests: string[]; html: string }[] = []
    for (const id of ["page-secret", "page-never-existed", "page-deleted", "page-silent"]) {
      requests.length = 0
      await visit(`/artifacts/${id}?workspace_id=ws1`)
      await expectGate(id)
      expect(requests).toEqual(BEFORE.requests)
      expect(requests.some(request => request.includes(id))).toBe(false)
      expect(forbidden()).toEqual([])
      seen.push({ requests: [...requests], html: document.body.innerHTML })
    }
    for (const view of seen) expect(view).toEqual(seen[0])
  })

  it.each([
    ["a pinned /artifacts/<id>", "/artifacts/page-secret?workspace_id=ws1&version=2"],
    ["an old /pages/@<id> link", "/pages/%40page-secret?workspace_id=ws1"],
  ])("%s outside the access shows the gate after only the ID-free catalog read", async (_name, link) => {
    await visit(link)
    await expectGate("page-secret")
    await waitFor(() => expect(unique()).toEqual(BEFORE.requests))
    expect(requests.some(request => request.includes("page-secret"))).toBe(false)
  })

  it("an old native editor link runs only the read-loss proof, then shows the gate", async () => {
    expect(await visit("/pages/craft/editor?page_id=page-secret&workspace_id=ws1")).toBe("/artifacts/page-secret?workspace_id=ws1")
    expect(readLoss).toHaveBeenCalledWith({ human: "u-r", workspace: "ws1", page: "page-secret" })
    await expectGate("page-secret")
    expect(unique()).toEqual(BEFORE.requests)
  })
})
