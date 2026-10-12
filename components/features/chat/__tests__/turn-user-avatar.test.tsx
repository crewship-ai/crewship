import { beforeEach, expect, it, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import { ChatTurnUserAvatar } from "../messages/turn-user-avatar"

const state = vi.hoisted(() => ({
  session: null as null | { user: { name?: string; email?: string; avatar_url?: string } },
}))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: state.session }) }))
beforeEach(() => { state.session = null })

it("keeps the transcript gutter when no authentication provider is available", () => {
  const { container } = render(<ChatTurnUserAvatar />)
  expect(container.firstElementChild).toHaveAttribute("aria-hidden", "true")
  expect(screen.queryByRole("img")).not.toBeInTheDocument()
})

it("uses a teammate's initials rather than the signed-in user's portrait", () => {
  state.session = { user: { name: "Current Reader", email: "reader@example.net", avatar_url: "/reader.png" } }
  render(<ChatTurnUserAvatar authorName="Morgan Review" />)
  expect(screen.getByText("MR")).toBeVisible()
  expect(screen.queryByRole("img")).not.toBeInTheDocument()
  expect(screen.queryByText("CR")).not.toBeInTheDocument()
})

it("shows the reader's saved portrait on their own turn", () => {
  state.session = { user: { name: "Current Reader", email: "reader@example.net", avatar_url: "/reader.png" } }
  render(<ChatTurnUserAvatar />)
  expect(screen.getByRole("img", { name: "Current Reader" })).toHaveAttribute("src", "/reader.png")
})

it("falls back to the account email when the name and photo are absent", () => {
  state.session = { user: { email: "reader@example.net", avatar_url: "" } }
  render(<ChatTurnUserAvatar authorName={null} />)
  expect(screen.getByText("RE")).toBeVisible()
})

it("does not take down the turn when a partial session has no email", () => {
  state.session = { user: { name: "Morgan Review" } }
  render(<ChatTurnUserAvatar />)
  expect(screen.getByText("MR")).toBeVisible()
  expect(screen.queryByRole("img")).not.toBeInTheDocument()
})
