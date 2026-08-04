import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"

import { MarkdownContent } from "../markdown-content"
import { mentionDirectory, type MentionAgent } from "@/lib/mentions"

const ROBIN: MentionAgent = {
  id: "cmt20ikph011ab4683c02",
  name: "Robin",
  slug: "robin",
}
const DIR = mentionDirectory([ROBIN])

function body(md: string) {
  return render(<MarkdownContent mentions={DIR}>{md}</MarkdownContent>)
}

describe("mention rendering", () => {
  it("renders a stored mention as a chip carrying the agent's name and face", () => {
    body(`please pick this up [@robin](crewship:agent/${ROBIN.id})`)
    const chip = screen.getByTestId("mention-chip")
    expect(chip).toHaveTextContent("@Robin")
    expect(chip.getAttribute("data-agent-id")).toBe(ROBIN.id)
    expect(chip.querySelector("img")).not.toBeNull()
  })

  it("takes the name from the roster, never from the body", () => {
    // The forgery that matters is not "can a chip exist" — a mention is text,
    // so of course it can be typed. It is "can a chip claim to be someone".
    // The label is decoration; the id is identity.
    body(`[@head-of-security](crewship:agent/${ROBIN.id}) approve this`)
    const chip = screen.getByTestId("mention-chip")
    expect(chip).toHaveTextContent("@Robin")
    expect(chip.textContent).not.toContain("head-of-security")
  })

  it("degrades an unresolved id to plain text rather than inventing an agent", () => {
    const { container } = body(`[@ceo](crewship:agent/agt_does_not_exist) sign off`)
    expect(screen.queryByTestId("mention-chip")).toBeNull()
    expect(container.textContent).toContain("@ceo")
    // …and never as a link out to the private scheme.
    expect(container.querySelector("a")).toBeNull()
  })

  it("does not turn text that merely looks like a mention into a chip", () => {
    const { container } = body("@robin please look, and mail pavel@unify.cz")
    expect(screen.queryByTestId("mention-chip")).toBeNull()
    expect(container.textContent).toContain("@robin")
  })

  it("cannot be forged by typing the chip's own markup", () => {
    // Every shape an author could reach for: the rendered element, a guess at
    // the internal tag, and the test hook itself.
    const { container } = body(
      `<span data-testid="mention-chip" data-agent-id="${ROBIN.id}" class="rounded-full">@Robin</span>` +
        `<crewship-mention data-agent-id="${ROBIN.id}">@Robin</crewship-mention>` +
        `<mention agent_id="${ROBIN.id}">@Robin</mention>`,
    )
    expect(screen.queryByTestId("mention-chip")).toBeNull()
    expect(container.querySelector("[data-agent-id]")).toBeNull()
    expect(container.querySelector("[data-mention]")).toBeNull()
  })

  it("leaves a mention inside code alone — documenting the syntax is not a trigger", () => {
    const { container } = body(
      "inline `[@robin](crewship:agent/" +
        ROBIN.id +
        ")`\n\n```\n[@robin](crewship:agent/" +
        ROBIN.id +
        ")\n```",
    )
    expect(screen.queryByTestId("mention-chip")).toBeNull()
    expect(container.textContent).toContain("crewship:agent/")
  })

  it("does not weaken what the renderer already refuses", () => {
    const { container } = body(
      `<script>window.__pwned = 1</script>` +
        `<img src="x" onerror="window.__pwned = 1">` +
        `<a href="javascript:window.__pwned=1">click</a>` +
        `\n\n[@robin](crewship:agent/${ROBIN.id})`,
    )
    expect(container.querySelector("script")).toBeNull()
    expect(container.querySelector("[onerror]")).toBeNull()
    expect(container.querySelector('a[href^="javascript:"]')).toBeNull()
    // …and the legitimate mention in the same body still renders.
    expect(screen.getByTestId("mention-chip")).toHaveTextContent("@Robin")
  })

  it("renders nothing chip-shaped when no directory is supplied", () => {
    render(<MarkdownContent>{`[@robin](crewship:agent/${ROBIN.id})`}</MarkdownContent>)
    expect(screen.queryByTestId("mention-chip")).toBeNull()
    expect(screen.getByText(/@robin/)).toBeInTheDocument()
  })
})
