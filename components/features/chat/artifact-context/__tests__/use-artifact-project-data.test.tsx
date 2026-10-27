import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { createArtifactMockServer } from "../projects/mock-server"
import { useArtifactProjectMutations, useArtifactProjectsAvailable, useArtifactProjectSnapshot } from "../projects/use-artifact-project-data"

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs(); vi.restoreAllMocks() })
const wrap = ({ children }: { children: ReactNode }) => <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>
const files = [{ path: "index.html", content: "<h1 id=t>Hi</h1>", media_type: "text/html" }]

function serve(options: Partial<Parameters<typeof createArtifactMockServer>[0]> = {}) {
  const mock = createArtifactMockServer({ slug: "deck", files, ...options })
  const spy = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => mock.handle(String(input), init) ?? new Response("404", { status: 404 }))
  return { mock, spy }
}

it("availability is false while loading and when the server says off", async () => {
  serve({ enabled: false })
  const { result } = renderHook(() => useArtifactProjectsAvailable("w"), { wrapper: wrap })
  expect(result.current).toBe(false)
  await new Promise(resolve => setTimeout(resolve, 20))
  expect(result.current).toBe(false)
})

it("availability turns true only after the server confirms", async () => {
  serve()
  const { result } = renderHook(() => useArtifactProjectsAvailable("w"), { wrapper: wrap })
  await waitFor(() => expect(result.current).toBe(true))
})

it("with the flag off availability is never requested", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "false")
  const { spy } = serve()
  const off = renderHook(() => useArtifactProjectsAvailable("w"), { wrapper: wrap })
  await new Promise(resolve => setTimeout(resolve, 20))
  expect(off.result.current).toBe(false)
  expect(spy).not.toHaveBeenCalled()
})

it("a successful restore refreshes the snapshot; a conflict does not", async () => {
  const { mock } = serve()
  const { result } = renderHook(() => ({ snap: useArtifactProjectSnapshot("w", "deck"), actions: useArtifactProjectMutations("w", "deck") }), { wrapper: wrap })
  await waitFor(() => expect(result.current.snap.data?.latest_version).toBe(1))

  mock.agentPublish(files, "agent")
  const before = mock.state.requests.length
  await act(async () => { await result.current.actions.restore.mutateAsync({ base_version: 1, version: 1 }).catch(() => undefined) })
  await waitFor(() => expect(result.current.actions.restore.error?.code).toBe("artifact_version_conflict"))
  expect(mock.state.requests.slice(before)).toEqual(["POST /api/v1/pages/deck/artifact/restore"])
  expect(result.current.snap.data?.latest_version).toBe(1)

  await act(async () => { await result.current.actions.restore.mutateAsync({ base_version: 2, version: 1 }) })
  await waitFor(() => expect(result.current.snap.data?.latest_version).toBe(3))
})

it("a snapshot failure surfaces the typed error without retrying", async () => {
  const { mock } = serve()
  const { result } = renderHook(() => useArtifactProjectSnapshot("w", "other"), { wrapper: wrap })
  await waitFor(() => expect(result.current.error?.code).toBe("not_found"))
  expect(mock.state.requests).toEqual(["GET /api/v1/pages/other/artifact"])
})
