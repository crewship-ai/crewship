import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"
import { RecentSessionsCard, RecentRunsCard, PeersCard } from "../agent-canvas-cards"
import type { ChatRow, RunRow } from "../agent-canvas-tabs/types"

afterEach(cleanup)
const time = "2026-10-03T00:00:00Z"
const chat: ChatRow = { id: "session /&?", title: "Planning", message_count: 1, status: "ACTIVE", started_at: time, ended_at: null, created_at: time }
const run: RunRow = { id: "run", status: "SUCCESS", trigger_type: "MANUAL", started_at: time, finished_at: "2026-10-03T00:00:02Z", error_message: null, created_at: time }

it("distinguishes loading and empty sessions while encoding the agent destination", () => {
  const { rerender } = render(<RecentSessionsCard agentSlug="writer /?" chats={null} />)
  expect(screen.getByText("Loading…")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Open chat →" })).toHaveAttribute("href", "/chat/writer%20%2F%3F")
  rerender(<RecentSessionsCard agentSlug="writer /?" chats={[]} />)
  expect(screen.getByText("No sessions yet.")).toBeInTheDocument()
  expect(screen.queryByText("Loading…")).not.toBeInTheDocument()
})

it("caps recent sessions at five and preserves encoded selection and untitled fallback", () => {
  const chats = [chat, ...Array.from({ length: 5 }, (_, i) => ({ ...chat, id: `session-${i}`, title: i === 0 ? null : `Session ${i}`, status: "CLOSED", message_count: i }))]
  render(<RecentSessionsCard agentSlug="writer" chats={chats} />)
  expect(screen.getAllByRole("link")).toHaveLength(6)
  expect(screen.getByRole("link", { name: /Planning/ })).toHaveAttribute("href", "/chat/writer?session=session%20%2F%26%3F")
  expect(screen.getByText("Untitled session")).toBeInTheDocument()
  expect(screen.queryByText("Session 4")).not.toBeInTheDocument()
  expect(screen.getByRole("link", { name: /Untitled session/ })).toHaveTextContent("0 messages")
  expect(screen.getByRole("link", { name: /Planning/ })).toHaveTextContent("1 message")
})

it("distinguishes run loading and empty state and encodes the full agent filter", () => {
  const { rerender } = render(<RecentRunsCard agentId="id /&" runs={null} />)
  expect(screen.getByText("Loading…")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "View all →" })).toHaveAttribute("href", "/runs?agent_id=id%20%2F%26")
  rerender(<RecentRunsCard agentId="id" runs={[]} />)
  expect(screen.getByText("No runs yet.")).toBeInTheDocument()
})

it("renders bounded run history with failure detail and duration only when both timestamps exist", () => {
  const { container } = render(<RecentRunsCard agentId="agent" runs={[
    run,
    { ...run, id: "failed", status: "FAILED", trigger_type: "CRON", error_message: "Permission refused", started_at: null },
    { ...run, id: "running", status: "RUNNING", trigger_type: "WEBHOOK", finished_at: null },
    { ...run, id: "pending", status: "PENDING", trigger_type: "QUEUE", started_at: null, finished_at: null },
    { ...run, id: "cancelled", status: "CANCELLED", trigger_type: "SCHEDULE" },
    { ...run, id: "omitted", trigger_type: "SHOULD_NOT_SHOW" },
  ]} />)
  expect(screen.getByText("manual")).toBeInTheDocument()
  expect(screen.getByText("cron — Permission refused")).toBeInTheDocument()
  expect(screen.queryByText("should_not_show")).not.toBeInTheDocument()
  expect(screen.getAllByText(/· 2s ·/)).toHaveLength(2)
  expect(container.querySelector(".animate-pulse")).not.toBeNull()
})

it("caps peer previews and handles missing names, timestamps and message identifiers", () => {
  render(<PeersCard messages={[
    { id: "a", from_agent_name: "Reviewer", preview: "Looks good", created_at: time },
    {},
    { from_agent_name: "", preview: "" },
    { id: "d", from_agent_name: "Writer", created_at: time },
    { id: "e", from_agent_name: "Omitted peer" },
  ]} />)
  expect(screen.getByText("Reviewer")).toBeInTheDocument()
  expect(screen.getByText("· Looks good")).toBeInTheDocument()
  expect(screen.getByText("Unknown")).toBeInTheDocument()
  expect(screen.getAllByText("?")).toHaveLength(2)
  expect(screen.queryByText("Omitted peer")).not.toBeInTheDocument()
})
