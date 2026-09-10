import { render, screen } from "@testing-library/react"
import { expect, it } from "vitest"
import { RoutinePublicationReview } from "../routine-publication-review"

it("does not present an unavailable published definition as an empty recipe", () => {
  render(
    <RoutinePublicationReview
      draft={{ steps: [{ id: "a" }] }}
      existing
      name="Report"
      validated={false}
    />,
  )
  expect(screen.getByText(/published definition is unavailable/)).toBeInTheDocument()
  expect(screen.queryByText(/Added ·/)).not.toBeInTheDocument()
})

it("explains changes and publication effects without requiring the JSON comparison", () => {
  render(
    <RoutinePublicationReview
      draft={{ steps: [{ id: "a", name: "Deliver" }] }}
      published={{ steps: [] }}
      existing
      name="Report"
      validated
    />,
  )
  expect(screen.getByText("Added · 1")).toBeInTheDocument()
  expect(screen.getByText("Deliver")).toBeInTheDocument()
  expect(
    screen.getByText(/Already accepted runs and pinned plans keep their version/),
  ).toBeInTheDocument()
})
