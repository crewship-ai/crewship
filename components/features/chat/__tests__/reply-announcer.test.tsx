import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"
import type { ChatTurn } from "@/hooks/use-chat"
import { ReplyAnnouncer } from "../reply-announcer"

afterEach(cleanup)
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
  expect(region()).toHaveTextContent(text)
})
it("does not announce history that loads already finished", () => {
  render(<ReplyAnnouncer isStreaming={false} turns={[user, reply()]} agentName="Ada" />)
  expect(region()).toHaveTextContent("")
})
it("does not announce a reply from a previous conversation after switching", () => {
  const { rerender } = render(<ReplyAnnouncer sessionId="s1" isStreaming turns={[user, reply({ isStreaming: true })]} agentName="Ada" />)
  rerender(<ReplyAnnouncer sessionId="s2" isStreaming={false} turns={[]} agentName="Ada" />)
  expect(region()).toHaveTextContent("")
})
