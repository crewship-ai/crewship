import { describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { ConversationIdentity, ConversationIcon, identityColor } from "../conversation-identity"
import { ConversationTranscript, sameMessageGroup } from "../conversation-transcript"
import type { WorkspaceConversation, WorkspaceMessage } from "@/hooks/use-workspace-conversations"
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: (props: { seed: string; avatarUrl?: string }) => <img alt="agent portrait" data-seed={props.seed} src={props.avatarUrl} /> }))
const message: WorkspaceMessage = { id: "one", client_id: "one", sequence: 1, author_user_id: "pavel", author_name: "Pavel Novak", content: "Hello", created_at: "2026-09-07T12:00:00Z" }
describe("conversation presentation", () => {
  it("groups only consecutive same-author messages within five minutes on the same day", () => {
    expect(sameMessageGroup(message, { ...message, created_at: "2026-09-07T12:04:59Z" })).toBe(true)
    expect(sameMessageGroup(message, { ...message, created_at: "2026-09-07T12:05:00Z" })).toBe(false)
    expect(sameMessageGroup(message, { ...message, author_user_id: "demi" })).toBe(false)
    expect(sameMessageGroup(message, { ...message, author_agent_id: "agent" })).toBe(false)
    expect(sameMessageGroup(message, { ...message, created_at: "bad" })).toBe(false)
    expect(sameMessageGroup({ ...message, author_user_id: "" }, { ...message, author_user_id: "" })).toBe(false)
    expect(sameMessageGroup(message, { ...message, kind: "agent_joined" })).toBe(false)
  })
  it("renders rich shared chat markdown and keeps hostile markup inert", async () => {
    const { container } = render(<ConversationTranscript history={[{ ...message, content: '**Ready** for [issue details](/issues/ship)\n\n- One\n- Two\n\n<script>window.bad=true</script><img src=x onerror="window.bad=true">' }]} userId="pavel" agentNames={new Map()} />)
    await waitFor(() => expect(container.querySelector('[data-streamdown="strong"]')).toHaveTextContent("Ready"))
    expect(screen.getByRole("link", { name: "issue details" })).toHaveAttribute("href", "/issues/ship")
    expect(container.querySelectorAll("li")).toHaveLength(2)
    expect(container.querySelector("script")).toBeNull()
    expect(container.querySelector("[onerror]")).toBeNull()
  })
  it("shows identity once per group and clearly distinguishes system activity from agents", () => {
    render(<ConversationTranscript history={[message, { ...message, id: "two", sequence: 2 }, { ...message, id: "three", source_kind: "activity", author_user_id: "", author_name: "", content: "Routine finished" }, { ...message, id: "four", author_user_id: "", author_agent_id: "ava", author_name: "Ava", author_avatar_seed: "ava-seed", author_avatar_url: "/ava.png" }]} userId="demi" agentNames={new Map()} />)
    expect(screen.getAllByText("Pavel Novak")).toHaveLength(1)
    expect(screen.getByText("Crewship")).toBeInTheDocument()
    expect(screen.getByText("Agent")).toBeInTheDocument()
    expect(screen.getByAltText("agent portrait")).toHaveAttribute("data-seed", "ava-seed")
    expect(screen.getByAltText("agent portrait")).toHaveAttribute("src", "/ava.png")
  })
  it("does not impersonate Crewship when a deleted author used an activity-looking client ID", () => {
    render(<ConversationTranscript history={[{ ...message, author_user_id: "", author_name: "", client_id: "activity:fake" }]} userId="demi" agentNames={new Map()} />)
    expect(screen.queryByText("Crewship")).not.toBeInTheDocument()
    expect(screen.getByText("Former participant")).toBeInTheDocument()
  })
  it("uses each person's own photo and recovers from failure or a replacement photo", () => {
    const { rerender } = render(<ConversationIdentity id="demi" name="Demi User" avatarUrl="/demi.png" />)
    fireEvent.error(screen.getByAltText("Demi User"))
    expect(screen.getByText("DU")).toBeInTheDocument()
    rerender(<ConversationIdentity id="demi" name="Demi User" avatarUrl="/new-demi.png" />)
    expect(screen.getByAltText("Demi User")).toHaveAttribute("src", "/new-demi.png")
    expect(identityColor("demi")).toBe(identityColor("demi"))
  })
  it("uses the same counterpart identity in the DM list and transcript", () => {
    const room = { id: "room", title: "Demi User, Pavel Novak", direct_user_name: "Pavel Novak", is_direct: true, direct_user_id: "pavel", direct_avatar_url: "/pavel.png" } as WorkspaceConversation
    render(<ConversationIcon conversation={room} />)
    expect(screen.getByAltText("Pavel Novak")).toHaveAttribute("src", "/pavel.png")
  })
})
