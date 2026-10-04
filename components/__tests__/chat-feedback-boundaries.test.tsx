import { afterEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react"
import { ReconnectBanner } from "@/components/features/chat/messages/reconnect-banner"
import { FollowUps } from "@/components/features/chat/suggestions/follow-ups"
import { ScopeFailure, httpError, scopeErrorMessage, useRetry } from "@/components/features/chat/scope-fetch"
import { mergeDomains } from "@/components/features/crews/registry-presets"
import { preferenceCategory, preferenceLabel } from "@/components/features/crews/preference-categories"
import { SubSpanIcon, subSpanVisual } from "@/components/features/activity/sub-span-visual"
import type { AskForm } from "@/components/features/chat/asks/types"
import type { SubSpanKind } from "@/lib/trace/types"

vi.mock("@/lib/telemetry", async (original) => ({ ...await original<typeof import("@/lib/telemetry")>(), emitChatEvent: vi.fn(), emitChatEventOnce: vi.fn() }))
afterEach(() => { cleanup(); vi.restoreAllMocks() })

it.each([
  ["connecting", undefined, "Reconnecting…"],
  ["disconnected", 0, "Disconnected"],
  ["connecting", 1, "Reconnecting… · 1 message queued"],
  ["disconnected", 3, "Disconnected · 3 messages queued"],
] as const)("explains %s and its queued messages", (status, queuedCount, message) => {
  render(<ReconnectBanner status={status} queuedCount={queuedCount} />)
  expect(screen.getByRole("status")).toHaveTextContent(message)
  expect(screen.getByRole("status")).toHaveAttribute("aria-live", "polite")
})
it.each(["connected", "idle"])("keeps the connection banner hidden for %s", (status) => {
  render(<ReconnectBanner status={status} queuedCount={2} />)
  expect(screen.queryByRole("status")).not.toBeInTheDocument()
})

const form: AskForm = { id: "feedback", label: "Give feedback", template: "Feedback: {{text}}", fields: [{ name: "text", label: "Feedback", type: "text" }] }
it("does not advertise follow-up forms without a handler or an available form", () => {
  const { rerender } = render(<FollowUps prompts={[]} forms={[form]} onPick={vi.fn()} show />)
  expect(screen.queryByRole("button")).not.toBeInTheDocument()
  rerender(<FollowUps prompts={[]} onPickForm={vi.fn()} onPick={vi.fn()} show />)
  expect(screen.queryByRole("button")).not.toBeInTheDocument()
})
it("keeps hidden follow-ups inert", () => {
  render(<FollowUps prompts={["Explain"]} forms={[form]} onPickForm={vi.fn()} onPick={vi.fn()} show={false} />)
  expect(screen.queryByRole("button")).not.toBeInTheDocument()
})
it("separates opening a questionnaire from sending a follow-up question", () => {
  const question = vi.fn()
  const questionnaire = vi.fn()
  render(<FollowUps prompts={["Explain"]} forms={[form]} onPickForm={questionnaire} onPick={question} show />)
  fireEvent.click(screen.getByRole("button", { name: /Give feedback.*opens a form/i }))
  expect(questionnaire).toHaveBeenCalledWith(form)
  expect(question).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: "Explain" }))
  expect(question).toHaveBeenCalledWith("Explain")
})
it("keeps pending follow-up controls disabled until the caller enables them", () => {
  const onPick = vi.fn()
  const { rerender } = render(<FollowUps prompts={["Explain"]} onPick={onPick} show disabled />)
  fireEvent.click(screen.getByRole("button", { name: "Explain" }))
  expect(onPick).not.toHaveBeenCalled()
  rerender(<FollowUps prompts={["Explain"]} onPick={onPick} show />)
  fireEvent.click(screen.getByRole("button", { name: "Explain" }))
  expect(onPick).toHaveBeenCalledOnce()
})

it("keeps failed scopes explicit and offers retry only when it can do something", () => {
  const retry = vi.fn()
  const { rerender } = render(<ScopeFailure label="Conversations unavailable" detail={scopeErrorMessage(httpError(503))} />)
  expect(screen.getByRole("alert")).toHaveTextContent("Conversations unavailableHTTP 503")
  expect(screen.queryByRole("button")).not.toBeInTheDocument()
  rerender(<ScopeFailure label="Conversations unavailable" detail={scopeErrorMessage("Connection lost")} onRetry={retry} />)
  fireEvent.click(screen.getByRole("button", { name: "Retry" }))
  expect(retry).toHaveBeenCalledOnce()
  expect(screen.getByRole("alert")).toHaveTextContent("Connection lost")
})
it("gives every explicit retry a new request revision without replacing its callback", () => {
  const { result } = renderHook(() => useRetry())
  const retry = result.current.retry
  expect(result.current.nonce).toBe(0)
  act(() => { retry(); retry() })
  expect(result.current.nonce).toBe(2)
  expect(result.current.retry).toBe(retry)
})
it("normalizes registry entries without adding blanks or altering caller arrays", () => {
  const base = [" Example.test ", "", "REGISTRY.NPMJS.ORG"]
  const extra = [" registry.npmjs.org ", "  ", "pypi.org", "PYPI.ORG"]
  expect(mergeDomains(base, extra)).toEqual(["example.test", "registry.npmjs.org", "pypi.org"])
  expect(base).toEqual([" Example.test ", "", "REGISTRY.NPMJS.ORG"])
  expect(extra).toHaveLength(4)
})
it.each([
  ["work", "work", "Work"], ["work.", "work", "Work."], ["communication.reply_style", "communication", "Reply style"],
  ["jazyk", "language", "Jazyk"], ["unknown.custom_note", "other", "Unknown.custom note"], ["design_preferences", "appearance", "Design preferences"],
])("keeps the preference %s visible with a useful category and label", (key, category, label) => {
  expect(preferenceCategory(key).id).toBe(category)
  expect(preferenceLabel(key)).toBe(label)
})
it("renders a future server span kind with the generic tool visual", () => {
  // Wire values can come from a newer server than the bundled client.
  const future = "future-server-kind" as SubSpanKind
  expect(subSpanVisual(future)).toBe(subSpanVisual("tool"))
  const { container } = render(<SubSpanIcon kind={future} className="h-5 w-5" />)
  expect(container.querySelector("svg")).toHaveClass("h-5", "w-5", "text-purple")
})
