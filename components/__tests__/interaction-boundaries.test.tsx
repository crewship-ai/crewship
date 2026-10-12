import { afterEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { TypingDots } from "@/components/features/chat/messages/typing-indicator"
import { ReactionsRow } from "@/components/features/chat/reactions/reactions-row"
import { ConversationLink } from "@/components/features/conversations/conversation-link"
import { ProviderFilterSection } from "@/components/features/credentials/provider-filter-section"
import { RunTagChips } from "@/components/features/routines/routine-tag-chips"
import { NeedsReconciliationNotice } from "@/components/features/work/work-state-pill"
import { Action, Actions } from "@/components/ai-elements/actions"
import { Suggestion, Suggestions } from "@/components/ai-elements/suggestion"
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card"

afterEach(() => { cleanup(); vi.restoreAllMocks() })

it.each([undefined, null, "", "  "])("keeps an accessible working status without an agent name (%s)", (name) => {
  render(<TypingDots name={name} />)
  expect(screen.getByRole("status")).toHaveAccessibleName("Agent is working")
})

it("does not render controls for an empty reaction inventory", () => {
  const onToggle = vi.fn()
  const { container } = render(<ReactionsRow reactions={{}} onToggle={onToggle} />)
  expect(container).toBeEmptyDOMElement()
  expect(onToggle).not.toHaveBeenCalled()
})

it.each([undefined, "", "https://[invalid"])("keeps absent or malformed conversation destinations inert (%s)", (href) => {
  render(<ConversationLink href={href}>Referenced item</ConversationLink>)
  expect(screen.getByText("Referenced item")).toBeVisible()
  expect(screen.queryByRole("link")).not.toBeInTheDocument()
  expect(screen.queryByRole("button")).not.toBeInTheDocument()
})

it("cancels external navigation without opening a window", () => {
  const open = vi.spyOn(window, "open").mockReturnValue(null)
  render(<ConversationLink href="mailto:team@example.test">Email team</ConversationLink>)
  fireEvent.click(screen.getByRole("button", { name: "Email team" }))
  expect(screen.getByRole("alertdialog")).toHaveTextContent("mailto:team@example.test")
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
  expect(open).not.toHaveBeenCalled()
  expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument()
})

it("filters unavailable providers and lets the reader select and clear the provider filter", () => {
  const onSelect = vi.fn()
  const providers = [{ value: "anthropic", label: "Anthropic", count: 2 }, { value: "openai", label: "OpenAI", count: 0 }]
  const { rerender } = render(<ProviderFilterSection providers={providers} selected={[]} onSelect={onSelect} />)
  expect(screen.queryByText("OpenAI")).not.toBeInTheDocument()
  expect(screen.getByLabelText("2 connected accounts")).toBeVisible()
  fireEvent.click(screen.getByText("Anthropic"))
  expect(onSelect).toHaveBeenLastCalledWith("anthropic")
  rerender(<ProviderFilterSection providers={providers} selected={["anthropic"]} onSelect={onSelect} active={false} />)
  fireEvent.click(screen.getByText("All providers"))
  expect(onSelect).toHaveBeenLastCalledWith("")
})

it("preserves the run tag layout supplied by its parent", () => {
  const { container } = render(<RunTagChips tags={["nightly"]} className="mt-4" />)
  expect(screen.getByTitle("Run tag: nightly")).toHaveTextContent("nightly")
  expect(container.firstElementChild).toHaveClass("mt-4")
})

it.each([undefined, null, ""])("keeps unresolved work visible when no runtime locator is known (%s)", (runtimeLocator) => {
  render(<NeedsReconciliationNotice state="needs_reconciliation" runtimeLocator={runtimeLocator} />)
  expect(screen.getByRole("status")).toHaveTextContent("this is not a result")
  expect(screen.getByRole("status")).toHaveTextContent("may still be alive")
  expect(screen.getByRole("status")).not.toHaveTextContent("Last known runtime:")
})

it("uses a tooltip as an action's accessible name and respects disabled actions", () => {
  const onClick = vi.fn()
  render(<Actions><Action tooltip="Copy response" onClick={onClick}>C</Action><Action label="Delete response" disabled onClick={onClick}>D</Action></Actions>)
  fireEvent.click(screen.getByRole("button", { name: "C Copy response" }))
  expect(onClick).toHaveBeenCalledTimes(1)
  fireEvent.click(screen.getByRole("button", { name: "D Delete response" }))
  expect(onClick).toHaveBeenCalledTimes(1)
})

it("keeps a suggestion safe without a callback and passes the underlying prompt for custom labels", () => {
  const onClick = vi.fn()
  const { rerender } = render(<Suggestions><Suggestion suggestion="Explain this result" /></Suggestions>)
  fireEvent.click(screen.getByRole("button", { name: "Explain this result" }))
  rerender(<Suggestions><Suggestion suggestion="Explain this result" onClick={onClick}>Explain</Suggestion></Suggestions>)
  fireEvent.click(screen.getByRole("button", { name: "Explain" }))
  expect(onClick).toHaveBeenCalledWith("Explain this result")
})

it("keeps a card's action interactive alongside its title, content and footer", () => {
  const onClick = vi.fn()
  render(<Card><CardHeader><CardTitle>Stored backup</CardTitle><CardDescription>Ready to inspect</CardDescription><CardAction className="self-center"><button onClick={onClick}>Inspect</button></CardAction></CardHeader><CardContent>Archive details</CardContent><CardFooter>Created today</CardFooter></Card>)
  expect(screen.getByText("Stored backup")).toBeVisible()
  expect(screen.getByText("Ready to inspect")).toBeVisible()
  expect(screen.getByText("Archive details")).toBeVisible()
  expect(screen.getByText("Created today")).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Inspect" }))
  expect(onClick).toHaveBeenCalledOnce()
})
