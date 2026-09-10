import React from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderHook, waitFor } from "@testing-library/react"

import type { ReviewSnapshotWire } from "@/lib/pages/editor-contract"

// The realtime context is a provider this hook does not need in a unit test;
// `useRealtimeEventSafe` is already a no-op without one, but stubbing it keeps
// the socket-free guarantee explicit rather than incidental.
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: () => undefined }))

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch }))

import { candidateDefinitionForComparison, hiddenPanelIds, liveDefinitionFromPage, normalizeSla, publishConflictOf, usePageReview } from "@/hooks/use-page-review"

function json(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: async () => body, headers: { get: () => null } } as unknown as Response
}

function newQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
}

function wrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  }
}

const snapshot: ReviewSnapshotWire = {
  issued_at: "2026-09-10T12:03:00Z",
  candidate: {
    revision: 7,
    git_commit: "c7",
    source_digest: "sha256:cand",
    created_at: "2026-09-10T12:03:00Z",
    actor: { kind: "agent", id: "ag_demo" },
    build: { id: "build-7", state: "ready", artifact_digest: "sha256:art" },
  },
  baseline: {
    publication_version: 3,
    published: true,
    definition_digest: "sha256:live-def",
    source_revision: 4,
    git_commit: "c4",
    source_available: true,
    source_unavailable_reason: null,
  },
  routines: [
    { routine: "ops-restart", published_digest: "sha256:old", current_digest: "sha256:new", state: "changed" },
    { routine: "ops-collect", published_digest: null, current_digest: null, state: "unknown" },
  ],
  capabilities: { may_edit_spec: true, may_publish: true },
  blockers: [],
  initial_publication: false,
}

function route(overrides: { snapshot?: ReviewSnapshotWire; draftRevision?: number } = {}) {
  apiFetch.mockImplementation(async (url: string) => {
    if (url.includes("/project/review")) return json(overrides.snapshot ?? snapshot)
    if (url.includes("/project/history/")) return json({ git_commit: "c4", revision: 4, digest: "sha256:base", definition: { kind: "Page" }, project: { files: [{ path: "src/App.tsx", content: "old" }] } })
    if (url.includes("/project?")) return json({ git_commit: "c7", revision: overrides.draftRevision ?? 7, digest: "sha256:cand", definition: { kind: "Page" }, project: { files: [{ path: "src/App.tsx", content: "new" }] } })
    throw new Error(`unexpected request ${url}`)
  })
}

beforeEach(() => {
  apiFetch.mockReset()
})

describe("usePageReview", () => {
  it("reads the authorized snapshot and both source sides from the endpoints that exist", async () => {
    route()
    const qc = newQueryClient()
    const { result } = renderHook(() => usePageReview("ws1", "operations lab", true), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.baseline.data).toBeTruthy())

    const urls = apiFetch.mock.calls.map(call => call[0] as string)
    expect(urls.some(u => u.startsWith("/api/v1/pages/operations%20lab/project/review?workspace_id=ws1"))).toBe(true)
    expect(urls.some(u => u.startsWith("/api/v1/pages/operations%20lab/project?workspace_id=ws1"))).toBe(true)
    expect(urls.some(u => u.startsWith("/api/v1/pages/operations%20lab/project/history/4?workspace_id=ws1"))).toBe(true)
    // The query must be cancellable: every read is handed the query's signal.
    expect(apiFetch.mock.calls.every(call => "signal" in (call[1] ?? {}))).toBe(true)
    expect(result.current.baselineUnavailable).toBeNull()
    expect(result.current.candidateMoved).toBe(false)
  })

  it("never fetches, and never substitutes an empty project for, a baseline the server says is gone", async () => {
    const gone: ReviewSnapshotWire = {
      ...snapshot,
      baseline: { ...snapshot.baseline, source_available: false, source_unavailable_reason: "The retained source for publication 3 was pruned on 2026-08-01." },
    }
    route({ snapshot: gone })
    const qc = newQueryClient()
    const { result } = renderHook(() => usePageReview("ws1", "ops", true), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.baselineUnavailable).toBe("The retained source for publication 3 was pruned on 2026-08-01."))

    expect(apiFetch.mock.calls.some(call => (call[0] as string).includes("/project/history/"))).toBe(false)
    expect(result.current.baseline.data).toBeUndefined()
  })

  it("reports a draft that moved out from under the review rather than publishing it", async () => {
    route({ draftRevision: 9 })
    const qc = newQueryClient()
    const { result } = renderHook(() => usePageReview("ws1", "ops", true), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.candidateMoved).toBe(true))
  })

  it("fences the publish with the digests the snapshot showed", async () => {
    route()
    const qc = newQueryClient()
    const { result } = renderHook(() => usePageReview("ws1", "ops", true), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.snapshot.data).toBeTruthy())

    apiFetch.mockImplementation(async (url: string) => {
      if (url.includes("/project/publish")) return json({ version: 4, build_id: "build-7", source_revision: 7 })
      return json(snapshot)
    })
    result.current.publish.mutate()
    await waitFor(() => expect(result.current.publish.isSuccess).toBe(true))

    const call = apiFetch.mock.calls.find(c => (c[0] as string).includes("/project/publish"))!
    const body = JSON.parse((call[1] as { body: string }).body)
    expect(body.expected_definition_digest).toBe("sha256:live-def")
    // Only routines whose current hash the snapshot actually carried: the
    // `unknown` one is not fenced against an invented empty value.
    expect(body.expected_routine_digests).toEqual({ "ops-restart": "sha256:new" })
    expect(body.expected_publication).toBe(3)
    expect(body.expected_revision).toBe(7)
    expect(body.build_id).toBe("build-7")
    expect(body.reviewed_code).toBe(true)
  })

  it("turns a 409 into a typed conflict naming the routines to re-review", async () => {
    route()
    const qc = newQueryClient()
    const { result } = renderHook(() => usePageReview("ws1", "ops", true), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.snapshot.data).toBeTruthy())

    apiFetch.mockImplementation(async (url: string) => {
      if (url.includes("/project/publish")) return json({ error: "a routine changed", conflict: "routines", routines: ["ops-restart"] }, 409)
      return json(snapshot)
    })
    result.current.publish.mutate()
    await waitFor(() => expect(result.current.conflict).toBeTruthy())

    expect(result.current.conflict).toEqual({ error: "a routine changed", conflict: "routines", routines: ["ops-restart"] })
    expect(publishConflictOf(result.current.publish.error)?.conflict).toBe("routines")
  })

  it("stays idle while disabled", async () => {
    route()
    const qc = newQueryClient()
    renderHook(() => usePageReview("ws1", "ops", false), { wrapper: wrapper(qc) })
    await Promise.resolve()
    expect(apiFetch).not.toHaveBeenCalled()
  })
})

describe("shaping the live Page for the comparator", () => {
  const page = {
    name: "Ops",
    slug: "ops",
    description: "d",
    panels: [
      { id: "services", schema: "status.v1", owner: "crew/ops", producer: "routine/nightly", sla_seconds: 300, span: 6, title: "Services" },
      { panel_id: "memory", span: 6, sealed: true, owner_crew_name: "Platform" },
    ],
  } as never

  it("maps the panel wire onto the document's key names so nothing reads as rewritten", () => {
    expect(liveDefinitionFromPage(page)).toEqual({
      apiVersion: "crewship.ai/v1",
      kind: "Page",
      metadata: { name: "Ops", slug: "ops", description: "d" },
      spec: { panels: [{ id: "services", schema: "status.v1", owner: "crew/ops", producer: "routine/nightly", sla: "300s", span: 6, title: "Services" }] },
    })
  })

  it("has no definition to compare when there is no live Page", () => {
    expect(liveDefinitionFromPage(null)).toBeNull()
  })

  it("names the panels it cannot read instead of reporting them as removed", () => {
    expect(hiddenPanelIds(page)).toEqual(["memory"])
    expect(hiddenPanelIds(null)).toEqual([])
  })

  it("drops the same hidden panels from the candidate, so they are not reported as added either", () => {
    const candidate = { apiVersion: "crewship.ai/v1", kind: "Page", spec: { panels: [{ id: "services", sla: "5m" }, { id: "memory", sla: "5m" }] } }
    expect(candidateDefinitionForComparison(candidate, ["memory"])).toEqual({
      apiVersion: "crewship.ai/v1",
      kind: "Page",
      spec: { panels: [{ id: "services", sla: "300s" }] },
    })
  })

  it("spells one SLA the same way on both sides, and leaves what it cannot parse alone", () => {
    expect(normalizeSla(300)).toBe("300s")
    expect(normalizeSla("5m")).toBe("300s")
    expect(normalizeSla("1h30m")).toBe("5400s")
    expect(normalizeSla("soon")).toBe("soon")
    expect(normalizeSla(undefined)).toBeUndefined()
  })
})
