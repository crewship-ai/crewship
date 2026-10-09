import { afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, render, screen } from "@testing-library/react"

// #2876 — looking at an agent is a read. Rendering its avatar used to queue a
// PUT /api/v1/agents/{id}/avatar for any agent without a stored render, for
// whoever happened to be looking: refused writes for VIEWER, MEMBER and
// restricted members, audit noise for everyone else.
//
// Nothing here is mocked between the surface and the network: the real
// AgentAvatar, the real persistence module and the real generator run, and
// only apiFetch is a recorder. The agents carry no stored render and use the
// resident default style, which is exactly the shape the old backfill fired
// on.

const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  accessMode: "trusted" as string,
  requests: [] as string[],
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (url: string, init?: RequestInit) => {
    h.requests.push(`${init?.method ?? "GET"} ${url}`)
    return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } })
  }),
}))

vi.mock("@/hooks/use-workspace", () => ({
  useCurrentWorkspaceId: () => "ws-1",
  useWorkspace: () => ({
    workspaceId: "ws-1",
    role: h.role,
    currentUserAccessMode: h.accessMode,
    capabilities: null,
    loading: false,
  }),
}))

import { AgentAvatar } from "../agent-avatar"
import { RoutineAgentLink } from "@/components/features/routines/routine-agent-link"

// Fresh ids per case: the old backfill remembered agents it had already
// tried, so reusing ids would let a later role pass on an earlier one's
// memory rather than on its own behaviour.
function agentsFor(tag: string) {
  return ["alice", "bob", "carol"].map((slug) => ({
    id: `${tag}-${slug}`, slug, name: slug, avatar_url: null,
  }))
}

const VIEWERS = [
  { label: "OWNER", role: "OWNER", accessMode: "trusted" },
  { label: "MEMBER", role: "MEMBER", accessMode: "trusted" },
  { label: "VIEWER", role: "VIEWER", accessMode: "trusted" },
  { label: "restricted member", role: "MEMBER", accessMode: "restricted" },
]

async function settle() {
  // Long enough for a mount effect, its async body and a fetch to resolve.
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 20))
  })
}

afterEach(() => {
  cleanup()
  h.requests.length = 0
})

describe.each(VIEWERS)("viewing agents as $label", ({ label, role, accessMode }) => {
  it("an agent link (routines, agent page header) sends no request", async () => {
    h.role = role
    h.accessMode = accessMode
    const agents = agentsFor(`${label}-link`)
    render(
      <>
        {agents.map((agent) => (
          <RoutineAgentLink key={agent.id} slug={agent.slug} agent={agent} />
        ))}
      </>,
    )
    expect(screen.getAllByRole("link")).toHaveLength(agents.length)
    await settle()
    expect(h.requests).toEqual([])
  })

  it("a roster of avatars without stored renders sends no request", async () => {
    h.role = role
    h.accessMode = accessMode
    const agents = agentsFor(`${label}-roster`)
    render(
      <>
        {agents.map((agent) => (
          <AgentAvatar key={agent.id} seed={agent.slug} avatarUrl={agent.avatar_url} alt={agent.name} />
        ))}
      </>,
    )
    expect(screen.getAllByRole("img")).toHaveLength(agents.length)
    await settle()
    expect(h.requests).toEqual([])
  })
})
