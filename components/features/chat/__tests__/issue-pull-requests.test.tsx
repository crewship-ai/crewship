import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ChatIssuePullRequests } from "../issue-pull-requests"
import type { IssueCodeLink } from "@/lib/types/mission"

const fetcher = vi.fn()
let userId: string | null = "viewer"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => fetcher(...args) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "workspace" }) }))
vi.mock("@/hooks/use-auth", () => ({ useSession: () => ({ data: { user: { id: userId } } }) }))
const link: IssueCodeLink = {
  id: "link", mission_id: "issue", workspace_id: "workspace", provider: "GITHUB", host: "github.com", owner: "acme", repo: "repo", number: 7, kind: "PULL_REQUEST", url: "https://github.com/acme/repo/pull/7",
  title: "Real stored PR", state: "OPEN", author: "author", source_branch: "feature", target_branch: "main", remote_created_at: null, remote_updated_at: null, remote_merged_at: null, remote_closed_at: null, credential_id: null,
  last_synced_at: "2026-10-14T12:00:00Z", last_sync_error: null, created_at: "2026-10-14T12:00:00Z", updated_at: "2026-10-14T12:00:00Z",
}
const client = () => new QueryClient({ defaultOptions: { queries: { retry: false } } })
beforeEach(() => { userId = "viewer"; fetcher.mockReset() })
afterEach(cleanup)

describe("chat read-only PR metadata", () => {
  it("loads only the existing authorized endpoint and labels CI unknown", async () => {
    fetcher.mockResolvedValue(new Response(JSON.stringify([link])))
    render(<QueryClientProvider client={client()}><ChatIssuePullRequests crewId="crew" identifier="ENG-1" revision={1} /></QueryClientProvider>)
    expect(await screen.findByText("Real stored PR")).toBeInTheDocument()
    expect(screen.getByText(/CI unknown/)).toHaveTextContent("State last synchronized")
    expect(screen.getByText("Stored data · live CI unavailable")).toBeInTheDocument()
    expect(fetcher).toHaveBeenCalledWith("/api/v1/crews/crew/issues/ENG-1/code-links?workspace_id=workspace", expect.objectContaining({ cache: "no-store", signal: expect.any(AbortSignal) }))
    expect(screen.getByRole("link", { name: /Real stored PR/ })).toHaveAttribute("href", link.url)
    expect(screen.queryByRole("button", { name: /attach|merge|refresh/i })).not.toBeInTheDocument()
  })
  it("distinguishes failed synchronization, unknown remote state and unsafe URLs", async () => {
    fetcher.mockResolvedValue(new Response(JSON.stringify([{ ...link, state: null, title: "<img onerror=alert(1)>", url: "javascript:alert(1)", last_synced_at: null, last_sync_error: "provider unavailable" }])))
    render(<QueryClientProvider client={client()}><ChatIssuePullRequests crewId="crew" identifier="ENG-1" revision={1} /></QueryClientProvider>)
    expect(await screen.findByText("<img onerror=alert(1)>")).toBeInTheDocument()
    expect(screen.getByText(/Last synchronization failed; stored state may be outdated/)).toBeInTheDocument()
    expect(screen.queryByRole("link")).not.toBeInTheDocument()
    expect(document.querySelector("img")).toBeNull()
  })
  it("hides previous metadata after access denial or an identity change", async () => {
    fetcher.mockResolvedValueOnce(new Response(JSON.stringify([link]))).mockResolvedValue(new Response("", { status: 403 }))
    const queryClient = client()
    const view = render(<QueryClientProvider client={queryClient}><ChatIssuePullRequests crewId="crew" identifier="ENG-1" revision={1} /></QueryClientProvider>)
    await screen.findByText("Real stored PR")
    await queryClient.refetchQueries()
    await waitFor(() => expect(screen.queryByText("Real stored PR")).not.toBeInTheDocument())
    expect(screen.getByRole("alert")).toHaveTextContent("Pull requests unavailable")
    userId = "another-viewer"
    view.rerender(<QueryClientProvider client={queryClient}><ChatIssuePullRequests crewId="crew" identifier="ENG-1" revision={1} /></QueryClientProvider>)
    expect(screen.queryByText("Real stored PR")).not.toBeInTheDocument()
    await screen.findByRole("alert")
  })
  it("offers retry after an upstream failure and handles an empty link list", async () => {
    fetcher.mockResolvedValueOnce(new Response("", { status: 500 })).mockResolvedValue(new Response("[]"))
    render(<QueryClientProvider client={client()}><ChatIssuePullRequests crewId="crew" identifier="ENG-1" revision={1} /></QueryClientProvider>)
    fireEvent.click(await screen.findByRole("button", { name: "Retry pull requests" }))
    expect(await screen.findByText(/No pull request attached/)).toBeInTheDocument()
    expect(screen.queryByText(/CI unknown/)).not.toBeInTheDocument()
    expect(screen.getByText("Stored data · live CI unavailable")).toBeInTheDocument()
  })
})
