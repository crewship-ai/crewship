import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { apiFetch } from "@/lib/api-fetch"
import { searchToolsAcrossApps, TOOL_SEARCH_MIN } from "../tools-model"
import { ToolSearchResults } from "../tools-space"
import type { Tool } from "../composio/types"

const pr: Tool = { slug: "GITHUB_CREATE_PULL_REQUEST", name: "Create a pull request", description: "Opens a pull request", toolkit: { slug: "github" } }
const slackPost: Tool = { slug: "SLACK_SEND_MESSAGE", name: "Send message", description: "", toolkit: { slug: "slack" } }
const apps = [
  { slug: "github", accountId: "ca_gh" },
  { slug: "slack", accountId: "ca_sl" },
]

function tools(byToolkit: Record<string, Tool[] | number>) {
  vi.mocked(apiFetch).mockImplementation(async (input) => {
    const url = new URL(String(input), "http://localhost")
    const v = byToolkit[url.searchParams.get("toolkit") ?? ""] ?? []
    if (typeof v === "number") return new Response("{}", { status: v })
    return new Response(JSON.stringify({ enabled: true, total: v.length, tools: v }), { status: 200 })
  })
}

beforeEach(() => vi.mocked(apiFetch).mockReset())
afterEach(cleanup)

// INT-04 (N12): the Tools panel search also finds tools by name, across every
// connected app. The server lists tools per app only, so each app is asked.
describe("searchToolsAcrossApps", () => {
  it("asks every connected app once, with the query, and keeps the app's account", async () => {
    tools({ github: [pr], slack: [] })
    const r = await searchToolsAcrossApps("ws", apps, "pull request")
    expect(vi.mocked(apiFetch).mock.calls.map(([u]) => String(u))).toEqual([
      "/api/v1/integrations/composio/tools?workspace_id=ws&toolkit=github&search=pull+request&limit=5",
      "/api/v1/integrations/composio/tools?workspace_id=ws&toolkit=slack&search=pull+request&limit=5",
    ])
    expect(r.hits).toEqual([{ tool: pr, slug: "github", accountId: "ca_gh" }])
    expect(r.failed).toEqual([])
  })

  it("an app that fails is named, the others still answer", async () => {
    tools({ github: 502, slack: [slackPost] })
    const r = await searchToolsAcrossApps("ws", apps, "send")
    expect(r.hits.map((h) => h.tool.slug)).toEqual(["SLACK_SEND_MESSAGE"])
    expect(r.failed).toEqual(["github"])
  })

  it("a query shorter than the minimum asks nothing; one app is asked once even with two accounts", async () => {
    tools({ github: [pr] })
    expect((await searchToolsAcrossApps("ws", apps, "p".repeat(TOOL_SEARCH_MIN - 1))).hits).toEqual([])
    expect(apiFetch).not.toHaveBeenCalled()
    await searchToolsAcrossApps("ws", [apps[0], { slug: "github", accountId: "ca_gh2" }], "pull")
    expect(apiFetch).toHaveBeenCalledTimes(1)
  })
})

describe("ToolSearchResults", () => {
  it("search finds GITHUB_CREATE_PULL_REQUEST and opens GitHub › Tools", async () => {
    tools({ github: [pr], slack: [] })
    const onOpenTool = vi.fn()
    render(<ToolSearchResults workspaceId="ws" apps={apps} query="pull request" onOpenTool={onOpenTool} />)
    const hit = await screen.findByRole("button", { name: "Open GITHUB_CREATE_PULL_REQUEST in Github" })
    expect(screen.getByRole("region", { name: "Tools matching “pull request”" })).toBeInTheDocument()
    fireEvent.click(hit)
    expect(onOpenTool).toHaveBeenCalledWith("ca_gh", "GITHUB_CREATE_PULL_REQUEST")
  })

  it("says so when no app has a matching tool, and names apps that did not answer", async () => {
    tools({ github: 502, slack: [] })
    render(<ToolSearchResults workspaceId="ws" apps={apps} query="zzz" onOpenTool={vi.fn()} />)
    expect(await screen.findByText("No tool in a connected app matches “zzz”.")).toBeInTheDocument()
    expect(screen.getByText("Github did not answer.")).toBeInTheDocument()
  })

  it("draws nothing for a short query or without connected apps", async () => {
    const { container, rerender } = render(<ToolSearchResults workspaceId="ws" apps={apps} query="p" onOpenTool={vi.fn()} />)
    rerender(<ToolSearchResults workspaceId="ws" apps={[]} query="pull" onOpenTool={vi.fn()} />)
    await waitFor(() => expect(container).toBeEmptyDOMElement())
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
