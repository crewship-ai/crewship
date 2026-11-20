import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"

const motion = vi.hoisted(() => ({ reduced: false }))
vi.mock("motion/react", async (importOriginal) => ({
  ...(await importOriginal<typeof import("motion/react")>()),
  useReducedMotion: () => motion.reduced,
}))

import { Shimmer } from "../shimmer"

afterEach(() => { cleanup(); motion.reduced = false })

// The band used to be painted in the background colour, so as it passed it
// erased the letters it crossed; it now brightens them instead.
it("brightens the text under the band instead of erasing it", () => {
  render(<Shimmer>Jamie is thinking</Shimmer>)
  const el = screen.getByText("Jamie is thinking")
  expect(el.className).not.toContain("var(--color-background)")
  expect(el.className).toContain("var(--color-foreground)")
})

it("holds still under prefers-reduced-motion and stays readable", () => {
  motion.reduced = true
  render(<Shimmer className="text-label">Jamie is thinking</Shimmer>)
  const el = screen.getByText("Jamie is thinking")
  expect(el.className).not.toContain("bg-clip-text")
  expect(el.className).not.toContain("text-transparent")
  expect(el.className).toContain("text-label")
})
