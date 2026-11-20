import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import type { ChatTurn } from "@/hooks/use-chat"

vi.mock("../chat-agent-context", () => ({
  useChatAgent: () => ({ id: "a1", slug: "jamie", name: "Jamie", avatarSeed: "jamie" }),
}))

import { StreamingIndicator } from "../streaming-indicator"

const at = new Date("2026-11-19T12:00:00Z")
const user: ChatTurn = { id: "u1", role: "user", parts: [{ id: "p", type: "text", content: "Hi", timestamp: at }], isStreaming: false, timestamp: at }
const statusOnly: ChatTurn = { id: "s", role: "assistant", parts: [{ id: "st", type: "status", content: "Starting agent…", timestamp: at }], isStreaming: true, timestamp: at }
const init: ChatTurn = { id: "i", role: "system", parts: [{ id: "x", type: "system_init", content: "init", timestamp: at }], isStreaming: false, timestamp: at }

afterEach(cleanup)

// One "agent is working" signal at a time (ANALYZA rev 2, B2): the agent's face
// with its ring. The indicator row exists only while no streaming assistant
// turn shows that face; there are no typing dots and no shimmer band.
describe("StreamingIndicator", () => {
  it("shows the face and one line between send and the first agent output", () => {
    render(<StreamingIndicator isStreaming stopping={false} turns={[user]} agentName="Jamie" />)
    const row = screen.getByRole("status")
    expect(row).toHaveTextContent("Jamie is working…")
    expect(row.querySelector('[data-testid="thinking-avatar"][data-active="true"]')).toBeTruthy()
    expect(row.querySelectorAll(".animate-bounce, [data-typing-dots]")).toHaveLength(0)
    expect(row.querySelector(".bg-clip-text")).toBeNull()
  })

  it("keeps the line after the start notice replaced the status turn", () => {
    render(<StreamingIndicator isStreaming stopping={false} turns={[user, init]} agentName="Jamie" />)
    expect(screen.getByRole("status")).toHaveTextContent("Jamie is working…")
  })

  it("steps aside while a streaming agent turn already shows the face", () => {
    const { container } = render(<StreamingIndicator isStreaming stopping={false} turns={[user, statusOnly]} agentName="Jamie" />)
    expect(container).toBeEmptyDOMElement()
  })

  it("says Stopping… after Stop, without the working ring", () => {
    render(<StreamingIndicator isStreaming stopping turns={[user, statusOnly]} agentName="Jamie" />)
    const row = screen.getByRole("status")
    expect(row).toHaveTextContent("Stopping…")
    expect(row.querySelector('[data-active="true"]')).toBeNull()
  })

  it("is gone when the run is not streaming", () => {
    const { container } = render(<StreamingIndicator isStreaming={false} stopping={false} turns={[user]} agentName="Jamie" />)
    expect(container).toBeEmptyDOMElement()
  })
})
