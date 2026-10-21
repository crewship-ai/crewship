import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"

vi.mock("next/link", () => ({ default: ({ children, ...p }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a {...p}>{children}</a> }))

import { RoutinesMovedNote } from "../agent-settings-parts"

afterEach(cleanup)

describe("RoutinesMovedNote", () => {
  it("says what moved and where it went", () => {
    render(<RoutinesMovedNote />)
    expect(screen.getByText("Schedules and webhooks moved to Routines")).toBeInTheDocument()
    expect(screen.getByText(/A routine runs this agent on a schedule, a webhook or an issue, with a test run and history\./)).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Routines" })).toHaveAttribute("href", "/routines")
  })
})
