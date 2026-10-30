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
const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString()
const row = (over: Record<string, unknown>) => ({ owner: "user/fixture-human", reach: ["owner"], panel_count: 0, panel_states: { fresh: 0, stale: 0, failed: 0, never_produced: 0 }, created_at: ago(9000), ...over })
const LIST = [
  row({ id: "page-site", slug: "pricing-site", name: "Pricing site", artifact_kind: "project", artifact_shared_version: 1, updated_at: ago(12) }),
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
  if (path === "/api/v1/pages/%40page-brief" && method === "GET") return json({ id: "page-brief", slug: "partner-brief", name: "Partner brief", has_document: true })
  if (path === "/api/v1/pages/%40page-brief/document" && method === "GET") return json({ page_id: "page-brief", schema: 3, rev: 2, content: {
    type: "doc", content: [{ type: "paragraph", attrs: { id: "p1" }, content: [{ type: "text", text: "Three partners, one launch date." }] }] } })
  const response = /^\/api\/v1\/pages\/pricing-site\/(?:artifact|craft)\b/.test(path) || path === "/api/v1/pages/artifacts/availability" ? project.handle(url, init) : undefined
  if (response) return response
  window.__unmocked.push(`${method} ${url}`)
  return new Response("not mocked", { status: 404 })
}

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

/** The app router's choice between the two route files, by the address bar. */
function Routes() {
  const id = useParams<{ id?: string }>()?.id
  return <div className="min-h-dvh bg-background text-foreground">{id ? <ArtifactDetailRoute /> : <ArtifactsPage />}</div>
}

createRoot(document.getElementById("root")!).render(<QueryClientProvider client={queryClient}><Routes /></QueryClientProvider>)
