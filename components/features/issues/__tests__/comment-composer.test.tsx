import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

import { CommentComposer } from "../comment-composer"
import type { MentionAgent } from "@/lib/mentions"

const AGENTS: MentionAgent[] = [
  { id: "a_robin", name: "Robin", slug: "robin", role_title: "Backend" },
  { id: "a_ada", name: "Ada", slug: "ada", role_title: "Data" },
  { id: "a_rosa", name: "Rosa", slug: "rosa" },
]

function setup(onSubmit = vi.fn(async () => true)) {
  render(<CommentComposer agents={AGENTS} onSubmit={onSubmit} />)
  const box = screen.getByRole("combobox") as HTMLTextAreaElement
  return { box, onSubmit }
}

/** fireEvent.change does not move the caret; the composer reads it from here. */
function type(box: HTMLTextAreaElement, value: string) {
  fireEvent.change(box, { target: { value } })
  box.selectionStart = value.length
  box.selectionEnd = value.length
  fireEvent.keyUp(box, { key: "r" })
}

describe("CommentComposer", () => {
  it("posts the body it was given on Cmd+Enter", async () => {
    const { box, onSubmit } = setup()
    type(box, "looks good to me")
    fireEvent.keyDown(box, { key: "Enter", metaKey: true })
    expect(onSubmit).toHaveBeenCalledWith("looks good to me")
  })

  it("posts on Ctrl+Enter too, and clears once the post lands", async () => {
    const onSubmit = vi.fn(async () => true)
    const { box } = setup(onSubmit)
    type(box, "shipping")
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true })
    expect(onSubmit).toHaveBeenCalledWith("shipping")
    await vi.waitFor(() => expect(box.value).toBe(""))
  })

  it("keeps the draft when the post fails", async () => {
    const onSubmit = vi.fn(async () => false)
    const { box } = setup(onSubmit)
    type(box, "this will 500")
    fireEvent.keyDown(box, { key: "Enter", metaKey: true })
    await vi.waitFor(() => expect(onSubmit).toHaveBeenCalled())
    expect(box.value).toBe("this will 500")
  })

  it("a bare Enter is a newline, not a post", () => {
    const { box, onSubmit } = setup()
    type(box, "line one")
    fireEvent.keyDown(box, { key: "Enter" })
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("refuses to post an empty or whitespace-only body", () => {
    const { box, onSubmit } = setup()
    type(box, "   ")
    expect(screen.getByRole("button", { name: /comment/i })).toBeDisabled()
    fireEvent.keyDown(box, { key: "Enter", metaKey: true })
    expect(onSubmit).not.toHaveBeenCalled()
  })

  describe("@ autocomplete", () => {
    it("opens on @ and filters as you type", () => {
      const { box } = setup()
      type(box, "hey @")
      expect(screen.getByRole("listbox")).toBeInTheDocument()
      expect(screen.getAllByRole("option")).toHaveLength(3)

      type(box, "hey @ro")
      expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
        expect.stringContaining("Robin"),
        expect.stringContaining("Rosa"),
      ])
    })

    it("does not open inside a word — an email is not a mention", () => {
      const { box } = setup()
      // "ro" matches two agents, so if this opened it would open with results
      // — the assertion has to fail for the right reason.
      type(box, "mail pavel@ro")
      expect(screen.queryByRole("listbox")).toBeNull()
    })

    it("announces itself as a combobox and tracks the active option", () => {
      const { box } = setup()
      expect(box).toHaveAttribute("aria-expanded", "false")
      type(box, "hey @ro")
      expect(box).toHaveAttribute("aria-expanded", "true")

      const [first, second] = screen.getAllByRole("option")
      expect(box.getAttribute("aria-activedescendant")).toBe(first.id)
      fireEvent.keyDown(box, { key: "ArrowDown" })
      expect(box.getAttribute("aria-activedescendant")).toBe(second.id)
      fireEvent.keyDown(box, { key: "ArrowUp" })
      expect(box.getAttribute("aria-activedescendant")).toBe(first.id)
    })

    it("wraps around at both ends", () => {
      const { box } = setup()
      type(box, "hey @ro")
      const [first, second] = screen.getAllByRole("option")
      fireEvent.keyDown(box, { key: "ArrowUp" })
      expect(box.getAttribute("aria-activedescendant")).toBe(second.id)
      fireEvent.keyDown(box, { key: "ArrowDown" })
      expect(box.getAttribute("aria-activedescendant")).toBe(first.id)
    })

    it("Enter inserts the token, not the name", () => {
      const { box } = setup()
      type(box, "hey @ro")
      fireEvent.keyDown(box, { key: "ArrowDown" })
      fireEvent.keyDown(box, { key: "Enter" })
      expect(box.value).toBe("hey [@rosa](crewship:agent/a_rosa) ")
      expect(screen.queryByRole("listbox")).toBeNull()
    })

    it("clicking an option inserts the same token", () => {
      const { box } = setup()
      type(box, "hey @ro")
      fireEvent.click(screen.getByText("Robin"))
      expect(box.value).toBe("hey [@robin](crewship:agent/a_robin) ")
    })

    it("Escape closes the picker and leaves the text alone", () => {
      const { box } = setup()
      type(box, "hey @ro")
      fireEvent.keyDown(box, { key: "Escape" })
      expect(screen.queryByRole("listbox")).toBeNull()
      expect(box.value).toBe("hey @ro")
      expect(box).toHaveAttribute("aria-expanded", "false")
    })

    it("Enter posts normally once the picker is closed", () => {
      const { box, onSubmit } = setup()
      type(box, "hey @ro")
      fireEvent.keyDown(box, { key: "Escape" })
      fireEvent.keyDown(box, { key: "Enter", metaKey: true })
      expect(onSubmit).toHaveBeenCalledWith("hey @ro")
    })

    it("stays shut when there is nobody to mention", () => {
      render(<CommentComposer agents={[]} onSubmit={vi.fn()} />)
      const box = screen.getAllByRole("combobox")[0] as HTMLTextAreaElement
      type(box, "hey @")
      expect(screen.queryByRole("listbox")).toBeNull()
    })

    it("never hides an interactive element behind opacity-0", () => {
      const { container, ...rest } = render(
        <CommentComposer agents={AGENTS} onSubmit={vi.fn()} />,
      )
      void rest
      for (const el of container.querySelectorAll("button, [role=option], textarea")) {
        expect(el.className).not.toMatch(/(^|[\s:])opacity-0(\s|$)/)
      }
    })
  })
})
