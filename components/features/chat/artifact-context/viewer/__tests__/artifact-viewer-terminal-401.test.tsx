// Real AuthProvider, apiFetch and React Query: a 200, then a 401 whose token refresh also answers 401.
// The sign-in redirect waits for the local-draft cleanup, so the viewer must already have withdrawn the project.
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"

const purge = vi.hoisted(() => vi.fn())
vi.mock("@/lib/document-queue", () => ({ purgeOtherDocumentDrafts: purge }))
vi.mock("@/lib/chat-e2-recovery", () => ({ purgeOtherE2Recovery: vi.fn() }))
vi.mock("../../projects/artifact-viewer-context", () => ({
  useArtifactViewer: () => ({ workspaceId: "review-w", target: { slug: "review-private" }, close: vi.fn() }),
}))

import { AuthProvider, useAuth } from "@/hooks/use-auth"
import { AUTH_EVENT, _resetRefreshInflightForTesting } from "@/lib/api-fetch"
import { artifactProjectKeys } from "../../projects/use-artifact-project-data"
import { ArtifactViewerPanel } from "../artifact-viewer-panel"

class Channel {
  onmessage = null
  postMessage() {}
  close() {}
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  _resetRefreshInflightForTesting()
})

function SignedInViewer() {
  const auth = useAuth()
  return auth.session ? <ArtifactViewerPanel /> : <div>Waiting for session</div>
}

it("a terminal 401 after a failed refresh hides the private preview before the asynchronous auth cleanup finishes", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
  vi.stubGlobal("BroadcastChannel", Channel)
  _resetRefreshInflightForTesting()
  // The real AuthProvider deliberately awaits this IndexedDB purge before
  // location.replace. Keep it pending to inspect the pre-navigation boundary.
  purge.mockImplementation((id: string | null) => id === null ? new Promise(() => {}) : Promise.resolve())
  const redirect = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  const expired = vi.fn()
  window.addEventListener(AUTH_EVENT, expired)
  let refused = false
  const calls: string[] = []
  const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } })
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(String(input), window.location.href).pathname
    calls.push(path)
    if (path === "/api/auth/session") return json(200, { user: { id: "review-u", name: "Review" }, expires: "2099-01-01" })
    if (path === "/api/auth/token/refresh") return json(401, { error: "session_expired" })
    if (path === "/api/v1/pages/review-private/artifact") {
      if (refused) return json(401, { error: "session_expired" })
      return json(200, { page_id: "review-p", name: "Private review project", can_write: true, latest_version: 1, shared_version: 0,
        manifest: { version: 1, entrypoint: "index.html", files: [{ path: "index.html", content: "<h1 id='artifact-private'>Private body</h1>", media_type: "text/html" }] }, versions: [], comments: [] })
    }
    throw new Error(`unexpected review request ${path}`)
  }))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(<QueryClientProvider client={client}><AuthProvider><SignedInViewer /></AuthProvider></QueryClientProvider>)
  await screen.findByRole("heading", { name: "Private review project" })
  expect(screen.getByTitle(/Preview of/)).toBeInTheDocument()
  refused = true
  await act(async () => { await client.invalidateQueries({ queryKey: artifactProjectKeys.snapshots("review-w", "review-u", "review-private") }) })
  await waitFor(() => expect(client.getQueryState(artifactProjectKeys.snapshot("review-w", "review-u", "review-private"))?.status).toBe("error"))
  expect(calls.filter(path => path === "/api/auth/token/refresh")).toHaveLength(1)
  expect(expired).toHaveBeenCalledOnce()
  expect(purge).toHaveBeenCalledWith(null)
  expect(redirect).not.toHaveBeenCalled()
  window.removeEventListener(AUTH_EVENT, expired)
  expect(screen.queryByRole("heading", { name: "Private review project" })).not.toBeInTheDocument()
  expect(screen.queryByTitle(/Preview of/)).not.toBeInTheDocument()
})
