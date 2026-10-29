// Browser route fixture: the real /artifacts and /artifacts/[id] route code over the real
// Artifacts library, with next/navigation backed by the browser's own history, so the address
// bar, Back and reload behave as in the app. Pages, the project and the Word read are in-memory
// mocks; no live backend. Driven by e2e/artifacts-open-inside.browser.mjs.
import React from "react"
import { createRoot } from "react-dom/client"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { useParams } from "next/navigation"
import ArtifactsPage from "../app/(dashboard)/artifacts/page"
import { ArtifactDetailRoute } from "../app/(dashboard)/artifacts/[id]/page-client"
import { createArtifactMockServer } from "../components/features/chat/artifact-context/projects/mock-server"
import type { ArtifactFile } from "../components/features/chat/artifact-context/projects/types"

const html = (path: string, body: string): ArtifactFile => ({ path, media_type: "text/html", content: `<!doctype html><html><head><meta charset="utf-8"></head><body>${body}</body></html>` })
const project = createArtifactMockServer({ slug: "pricing-site", name: "Pricing site", entrypoint: "plans.html", files: [html("plans.html", '<h1 id="headline">Simple pricing</h1>')], message: "Plans" })
// The native Word's lock is free, so its lock bar offers Edit.
const brief = createArtifactMockServer({ slug: "@page-brief", name: "Partner brief", entrypoint: "brief.html", files: [html("brief.html", "<p>Brief</p>")], craft: { enabled: true } })
const cell = (v: string | number) => ({ value: typeof v === "number" ? { t: "Number", v } : { t: "Text", v }, formula: null, style: 0 })
const WORKBOOK = { schema_version: 1, rev: 1, generation: 1, snapshot: { active_sheet: 0, sheets: [{ name: "Prices", cells: { bands: { "0": { rows: {
  "0": { "0": cell("Plan"), "1": cell("Price") }, "1": { "0": cell("Team"), "1": cell(49) },
} } }, len: 2 } }] } }
const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString()
const row = (over: Record<string, unknown>) => ({ owner: "user/fixture-human", reach: ["owner"], panel_count: 0, panel_states: { fresh: 0, stale: 0, failed: 0, never_produced: 0 }, created_at: ago(9000), ...over })
const LIST = [
  row({ id: "page-site", slug: "pricing-site", name: "Pricing site", artifact_kind: "project", artifact_shared_version: 1, updated_at: ago(12) }),
  row({ id: "page-weekly", slug: "weekly-report", name: "Weekly report", panel_count: 1, panel_states: { fresh: 1, stale: 0, failed: 0, never_produced: 0 }, updated_at: ago(40) }),
  row({ id: "page-model", slug: "price-model", name: "Price model", artifact_kind: "sheet", has_workbook: true, artifact_shared_version: 1, updated_at: ago(60) }),
  row({ id: "page-wire", slug: "checkout-wireframe", name: "Checkout wireframe", artifact_kind: "design", artifact_shared_version: 1, updated_at: ago(70) }),
  row({ id: "page-brief", slug: "partner-brief", name: "Partner brief", artifact_kind: "word", has_document: true, artifact_shared_version: 1, updated_at: ago(95) }),
]

declare global { interface Window { __unmocked: string[] } }
window.__unmocked = []
const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } })
window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url
  const path = new URL(url, location.href).pathname
  const method = (init?.method ?? "GET").toUpperCase()
  if (path === "/api/v1/pages" && method === "GET") return json(LIST)
  if (path === "/api/v1/page-folders" && method === "GET") return json({ folders: [] })
  if (path === "/api/v1/workspaces/ws-fixture/members" && method === "GET") return json([])
  if (path === "/api/v1/me/preferences" && method === "GET") return json({})
  if (path.startsWith("/api/v1/me/preferences/") && method === "PUT") return json({ ok: true })
  if (path === "/api/v1/documents/status" && method === "GET") return json({ enabled: true })
  // A panel Page, read by its immutable ID the way the detail asks for it.
  if (path === "/api/v1/pages/%40page-weekly" && method === "GET") return json({ id: "page-weekly", slug: "weekly-report", name: "Weekly report", owner: "user/fixture-human",
    panels: [{ id: "status", schema: "status.v1", title: "Service status", sla_seconds: 30, state: "fresh" }] })
  // The Sheet: the workbook editor is off here, so the surface shows the saved workbook read-only.
  if (path === "/api/v1/feature-flags" && method === "GET") return json([])
  if (path === "/api/v1/pages/%40page-model/workbook" && method === "GET") return json(WORKBOOK)
  if (path === "/api/v1/pages/%40page-brief" && method === "GET") return json({ id: "page-brief", slug: "partner-brief", name: "Partner brief", has_document: true })
  if (path === "/api/v1/pages/%40page-brief/document" && method === "GET") return json({ page_id: "page-brief", schema: 3, rev: 2, content: {
    type: "doc", content: [{ type: "paragraph", attrs: { id: "p1" }, content: [{ type: "text", text: "Three partners, one launch date." }] }] } })
  if (path.startsWith("/api/v1/pages/%40page-brief/craft/")) { const answer = brief.handle(url, init); if (answer) return answer }
  const response = /^\/api\/v1\/pages\/pricing-site\/(?:artifact|craft)\b/.test(path) || path === "/api/v1/pages/artifacts/availability" ? project.handle(url, init) : undefined
  if (response) return response
  window.__unmocked.push(`${method} ${url}`)
  return new Response("not mocked", { status: 404 })
}

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

/** The app router's choice between the route files, by the address bar; the static /artifacts/edit wins over [id]. */
function Routes() {
  const id = useParams<{ id?: string }>()?.id
  // The editor itself (the unchanged Craft editor page) is not part of this fixture; only its address is.
  if (id === "edit") return <p>Editor route</p>
  return <div className="min-h-dvh bg-background text-foreground">{id ? <ArtifactDetailRoute /> : <ArtifactsPage />}</div>
}

createRoot(document.getElementById("root")!).render(<QueryClientProvider client={queryClient}><Routes /></QueryClientProvider>)
