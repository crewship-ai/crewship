import { afterEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { ConversationLink, isConversationAppLink } from "../conversation-link"

afterEach(() => vi.restoreAllMocks())
describe("conversation links", () => {
  it("keeps workspace-scoped app links as normal same-tab navigation", () => {
    render(<ConversationLink href="/issues/COP-1?workspace_id=ws#activity">Open issue</ConversationLink>)
    const link = screen.getByRole("link", { name: "Open issue" })
    expect(link).toHaveAttribute("href", "/issues/COP-1?workspace_id=ws#activity")
    expect(link).not.toHaveAttribute("target")
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument()
  })
  it.each(["https://example.com/issue", "//example.com/issue", "/\\example.com/issue"])("requires confirmation before opening %s", (href) => {
    const opened = vi.spyOn(window, "open").mockImplementation(() => null)
    render(<ConversationLink href={href}>External issue</ConversationLink>)
    fireEvent.click(screen.getByRole("button", { name: "External issue" }))
    expect(screen.getByRole("alertdialog")).toHaveTextContent(href)
    expect(opened).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Open link", exact: true }))
    expect(opened).toHaveBeenCalledWith(href, "_blank", "noopener,noreferrer")
  })
  it.each(["javascript:alert(1)", "data:text/html,bad", "file:///tmp/file"])("never enables untrusted scheme %s", (href) => {
    render(<ConversationLink href={href}>Unsafe</ConversationLink>)
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
    expect(screen.queryByRole("link")).not.toBeInTheDocument()
  })
  it("does not trust URL parser ambiguities or absolute destinations", () => {
    for (const href of ["//evil.test", "/\\evil.test", "/\n/evil.test", "/\t/evil.test", " https://evil.test", "https://crewship.example/issues/x"]) expect(isConversationAppLink(href)).toBe(false)
  })
})
