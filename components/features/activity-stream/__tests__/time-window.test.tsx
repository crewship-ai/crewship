import { afterEach, expect, it } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"

import { LaneAxis, TIME_WINDOW_MS } from "../time-window"

afterEach(cleanup)

const NOW = Date.parse("2026-10-08T15:30:00Z")

it("marks every day of the week on the 7 d axis, none skipped (#3017)", () => {
  render(<LaneAxis from={NOW - TIME_WINDOW_MS["7d"]} to={NOW} win="7d" />)
  const labels = screen.getAllByTestId("axis-label").map((l) => l.textContent)
  expect(labels).toHaveLength(8)
  expect(labels.at(-1)).toBe("now")
  expect(new Set(labels.slice(0, 7)).size).toBe(7)
})

it("marks the 24 h axis every four hours", () => {
  render(<LaneAxis from={NOW - TIME_WINDOW_MS["24h"]} to={NOW} win="24h" />)
  expect(screen.getAllByTestId("axis-label")).toHaveLength(7)
})
