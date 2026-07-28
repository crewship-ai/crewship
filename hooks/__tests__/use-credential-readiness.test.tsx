// Tool readiness is the one number on /credentials that is about the
// CONTAINER rather than about the vault — "is `gh` actually installed for the
// crews that can use this token?". It is aggregated client-side from one call
// per crew, so the failure modes worth pinning are: a crew whose readiness
// call fails must not blank the whole column, and a response that arrives
// after the workspace changed must never be applied.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import { useCredentialReadiness } from "../use-credential-readiness"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

const crews = [
  { id: "crew1", name: "engineering" },
  { id: "crew2", name: "quality" },
]

beforeEach(() => {
  h.apiFetch.mockReset()
})

describe("aggregating readiness across crews", () => {
  it("names every crew and reports one gap per credential that lacks its CLI", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/crews?")) return ok(crews)
      if (url.includes("/crews/crew1/credential-readiness")) {
        return ok({
          crew_id: "crew1",
          crew_slug: "engineering",
          tools: ["git"],
          checked: 2,
          gaps: [
            {
              credential_id: "aws",
              credential_name: "AWS_MAIN",
              provider: "AWS",
              tool: "aws",
              feature: "ghcr.io/devcontainers/features/aws-cli:1",
              feature_id: "aws-cli",
            },
          ],
        })
      }
      return ok({ crew_id: "crew2", crew_slug: "quality", tools: ["git", "gh"], checked: 1, gaps: [] })
    })

    const { result } = renderHook(() => useCredentialReadiness("ws1"))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.crewNames).toEqual({ crew1: "engineering", crew2: "quality" })
    expect(Array.from(result.current.missingToolIds)).toEqual(["aws"])
    expect(result.current.gapsByCredential.get("aws")).toEqual([
      { crewId: "crew1", crewName: "engineering", tool: "aws", feature: "ghcr.io/devcontainers/features/aws-cli:1", featureId: "aws-cli" },
    ])
  })

  it("merges the same credential's gaps from several crews without duplicating the row", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/crews?")) return ok(crews)
      const crewId = url.includes("crew1") ? "crew1" : "crew2"
      return ok({
        crew_id: crewId,
        crew_slug: crewId,
        tools: [],
        checked: 1,
        gaps: [{ credential_id: "gh", credential_name: "GH_TOKEN", provider: "GITHUB", tool: "gh", feature: "f", feature_id: "github-cli" }],
      })
    })

    const { result } = renderHook(() => useCredentialReadiness("ws1"))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(Array.from(result.current.missingToolIds)).toEqual(["gh"])
    expect(result.current.gapsByCredential.get("gh")).toHaveLength(2)
  })
})

describe("degrading rather than blanking", () => {
  it("keeps the crews it could read when one crew's readiness call fails", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/crews?")) return ok(crews)
      if (url.includes("crew1")) throw new TypeError("fetch failed")
      return ok({
        crew_id: "crew2",
        crew_slug: "quality",
        tools: [],
        checked: 1,
        gaps: [{ credential_id: "gh", credential_name: "GH_TOKEN", provider: "GITHUB", tool: "gh", feature: "f", feature_id: "github-cli" }],
      })
    })

    const { result } = renderHook(() => useCredentialReadiness("ws1"))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.crewNames.crew1).toBe("engineering")
    expect(Array.from(result.current.missingToolIds)).toEqual(["gh"])
  })

  // A 500 or a shape we don't recognise must read as "we don't know", never as
  // "nothing is missing" — the latter is a green tick over a broken container.
  // crewsChecked is how the list tells those two apart.
  it("reports no gaps, no crash and nothing checked when the crew list itself fails", async () => {
    h.apiFetch.mockRejectedValue(new TypeError("offline"))
    const { result } = renderHook(() => useCredentialReadiness("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.crewNames).toEqual({})
    expect(result.current.missingToolIds.size).toBe(0)
    expect(result.current.crewsChecked).toBe(0)
  })

  it("counts only the crews that answered, so one failure is not read as a clean bill", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/crews?")) return ok(crews)
      if (url.includes("crew1")) return { ok: false, status: 500, json: async () => ({}) } as unknown as Response
      return ok({ crew_id: "crew2", tools: [], checked: 0, gaps: [] })
    })
    const { result } = renderHook(() => useCredentialReadiness("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.crewsChecked).toBe(1)
  })

  it("tolerates a readiness body without a gaps array", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/crews?")) return ok([crews[0]])
      return ok({ crew_id: "crew1" })
    })
    const { result } = renderHook(() => useCredentialReadiness("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.missingToolIds.size).toBe(0)
  })
})

describe("no workspace", () => {
  it("asks for nothing and reports an empty, settled state", async () => {
    const { result } = renderHook(() => useCredentialReadiness(null))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(h.apiFetch).not.toHaveBeenCalled()
  })
})

describe("stale-response guard", () => {
  it("discards a crew list that resolves after the workspace changed", async () => {
    let releaseA: (v: Response) => void = () => {}
    const pendingA = new Promise<Response>((r) => { releaseA = r })
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.includes("workspace_id=ws-a")) return pendingA
      if (url.startsWith("/api/v1/crews?")) return ok([{ id: "crewB", name: "beta" }])
      return ok({ crew_id: "crewB", tools: [], checked: 0, gaps: [] })
    })

    const { result, rerender } = renderHook(({ id }: { id: string }) => useCredentialReadiness(id), {
      initialProps: { id: "ws-a" },
    })
    rerender({ id: "ws-b" })

    await waitFor(() => expect(result.current.crewNames).toEqual({ crewB: "beta" }))
    releaseA(ok([{ id: "crewA", name: "alpha" }]))
    await new Promise((r) => setTimeout(r, 0))
    await new Promise((r) => setTimeout(r, 0))

    expect(result.current.crewNames).toEqual({ crewB: "beta" })
  })
})
