import { act, cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { ChatTurn } from "@/hooks/use-chat"
import { ReplyAnnouncer } from "../reply-announcer"

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { cleanup(); vi.useRealTimers() })
const settle = () => act(() => { vi.advanceTimersByTime(100) })
const user: ChatTurn = { id: "u", role: "user", parts: [{ id: "p0", type: "text", content: "Hi", timestamp: new Date(0) }], isStreaming: false, timestamp: new Date(0) }
const reply = (over: Partial<ChatTurn> = {}): ChatTurn => ({ id: "a", role: "assistant", parts: [{ id: "p1", type: "text", content: "Hello there", timestamp: new Date(0) }], isStreaming: false, timestamp: new Date(0), ...over })
const region = () => screen.getByRole("status")

it("announces nothing while a reply streams, so tokens are never read one by one", () => {
  const { rerender } = render(<ReplyAnnouncer isStreaming={false} turns={[user]} agentName="Ada" />)
  rerender(<ReplyAnnouncer isStreaming turns={[user, reply({ isStreaming: true, parts: [{ id: "p1", type: "text", content: "Hel", timestamp: new Date(0) }] })]} agentName="Ada" />)
  rerender(<ReplyAnnouncer isStreaming turns={[user, reply({ isStreaming: true })]} agentName="Ada" />)
  expect(region()).toHaveTextContent("")
})
it.each([
  [undefined, "Ada replied."],
  ["completed", "Ada replied."],
  ["cancelled", "Reply stopped."],
  ["interrupted", "Reply interrupted."],
  ["failed", "Reply failed."],
] as const)("announces how a watched reply ended (%s)", (turnState, text) => {
  const { rerender } = render(<ReplyAnnouncer isStreaming turns={[user, reply({ isStreaming: true })]} agentName="Ada" />)
  rerender(<ReplyAnnouncer isStreaming={false} turns={[user, reply({ turnState })]} agentName="Ada" />)
  settle()
  expect(region()).toHaveTextContent(text)
})
it("does not announce history that loads already finished", () => {
  render(<ReplyAnnouncer isStreaming={false} turns={[user, reply()]} agentName="Ada" />)
  settle()
  expect(region()).toHaveTextContent("")
})
it("does not announce a reply from a previous conversation after switching", () => {
  const { rerender } = render(<ReplyAnnouncer sessionId="s1" isStreaming turns={[user, reply({ isStreaming: true })]} agentName="Ada" />)
  rerender(<ReplyAnnouncer sessionId="s2" isStreaming={false} turns={[]} agentName="Ada" />)
  rerender(<ReplyAnnouncer sessionId="s2" isStreaming={false} turns={[user, reply({ id: "old" })]} agentName="Ada" />)
  settle()
  expect(region()).toHaveTextContent("")
})
it("announces the second reply of a conversation again, not only the first", () => {
  const second = { ...user, id: "u2" }
  const { rerender } = render(<ReplyAnnouncer isStreaming turns={[user, reply({ isStreaming: true })]} agentName="Ada" />)
  rerender(<ReplyAnnouncer isStreaming={false} turns={[user, reply()]} agentName="Ada" />)
  settle()
  const first = region().firstElementChild
  rerender(<ReplyAnnouncer isStreaming turns={[user, reply(), second, reply({ id: "a2", isStreaming: true })]} agentName="Ada" />)
  rerender(<ReplyAnnouncer isStreaming={false} turns={[user, reply(), second, reply({ id: "a2" })]} agentName="Ada" />)
  expect(region()).toHaveTextContent("")
  settle()
  expect(region()).toHaveTextContent("Ada replied.")
  expect(region().firstElementChild).not.toBe(first)
})
it("does not credit an older reply when a run ends without a new one", () => {
  const second = { ...user, id: "u2" }
  const { rerender } = render(<ReplyAnnouncer isStreaming turns={[user, reply(), second]} agentName="Ada" />)
  rerender(<ReplyAnnouncer isStreaming={false} turns={[user, reply(), second]} agentName="Ada" />)
  settle()
  expect(region()).toHaveTextContent("")
})
it("announces a finished message that arrives without a stream, but not one's own", () => {
  const { rerender } = render(<ReplyAnnouncer isStreaming={false} turns={[user, reply()]} agentName="Ada" />)
  rerender(<ReplyAnnouncer isStreaming={false} turns={[user, reply(), { ...user, id: "t", authorUserId: "teammate" }]} agentName="Ada" />)
  settle()
  expect(region()).toHaveTextContent("New message.")
  rerender(<ReplyAnnouncer isStreaming={false} turns={[user, reply(), { ...user, id: "t", authorUserId: "teammate" }, { ...user, id: "mine" }]} agentName="Ada" />)
  settle()
  expect(region()).toHaveTextContent("New message.")
  rerender(<ReplyAnnouncer isStreaming={false} turns={[user, reply(), { ...user, id: "t", authorUserId: "teammate" }, { ...user, id: "mine" }, reply({ id: "proactive" })]} agentName="Ada" />)
  settle()
  expect(region()).toHaveTextContent("Ada replied.")
})
it("a history that loads into an empty chat is never announced, even a single turn", () => {
  const teammate = { ...user, id: "t", authorUserId: "teammate" }
  const { rerender } = render(<ReplyAnnouncer sessionId="s1" isStreaming={false} turns={[]} agentName="Ada" />)
  rerender(<ReplyAnnouncer sessionId="s1" isStreaming={false} turns={[reply()]} agentName="Ada" historyLoading />)
  rerender(<ReplyAnnouncer sessionId="s1" isStreaming={false} turns={[reply()]} agentName="Ada" />)
  settle()
  expect(region()).toHaveTextContent("")
  rerender(<ReplyAnnouncer sessionId="s2" isStreaming={false} turns={[]} agentName="Ada" />)
  rerender(<ReplyAnnouncer sessionId="s2" isStreaming={false} turns={[teammate]} agentName="Ada" />)
  settle()
  expect(region()).toHaveTextContent("")
  rerender(<ReplyAnnouncer sessionId="s2" isStreaming={false} turns={[teammate, { ...teammate, id: "t2" }]} agentName="Ada" />)
  settle()
  expect(region()).toHaveTextContent("New message.")
})
