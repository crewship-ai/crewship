import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"

import { TypingDots } from "../messages/typing-indicator"

// While an agent works on its first reply the transcript showed one line of
// shimmering text under the last message. Harbor draws the agent's bubble
// with three dots in it instead, and keeps the name for assistive tech.

describe("TypingDots", () => {
  it("names who is working for assistive tech and draws three dots", () => {
    const { container } = render(<TypingDots name="Morgan" />)
    expect(screen.getByRole("status")).toHaveAccessibleName("Morgan is working")
    expect(container.querySelectorAll("[data-slot='typing-dot']")).toHaveLength(3)
  })
})
