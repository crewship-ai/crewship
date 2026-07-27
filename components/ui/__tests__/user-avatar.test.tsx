import { describe, it, expect, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

import { UserAvatar } from "../user-avatar"

// Three surfaces list the same people — the top bar, the Settings roster and
// the capability grid — and each had its own initials-drawing code that
// ignored avatar_url entirely. Uploading a photo changed the top bar and
// nothing else, so the same person appeared twice on one screen with two
// different faces.

describe("UserAvatar", () => {
  beforeEach(() => cleanup())

  it("shows the photo when there is one", () => {
    render(<UserAvatar name="Ada Lovelace" email="ada@x.io" src="/api/v1/users/u1/avatar?v=1" />)
    const img = screen.getByRole("img", { name: "Ada Lovelace" })
    expect(img).toHaveAttribute("src", "/api/v1/users/u1/avatar?v=1")
  })

  it("falls back to initials when there is none", () => {
    render(<UserAvatar name="Ada Lovelace" email="ada@x.io" />)
    expect(screen.getByText("AL")).toBeTruthy()
    expect(screen.queryByRole("img")).toBeNull()
  })

  it("falls back to initials when the photo fails to load", () => {
    render(<UserAvatar name="Ada Lovelace" email="ada@x.io" src="/gone.png" />)
    fireEvent.error(screen.getByRole("img", { name: "Ada Lovelace" }))
    // A broken image icon in a list of people looks like a bug in the data.
    expect(screen.getByText("AL")).toBeTruthy()
  })

  it("uses the email when the person has no name yet", () => {
    // Provisioned accounts have no full_name until the person sets one.
    render(<UserAvatar name={null} email="newjoiner@x.io" />)
    expect(screen.getByText("NE")).toBeTruthy()
  })

  it("takes one initial from a single-word name", () => {
    render(<UserAvatar name="Prince" email="p@x.io" />)
    expect(screen.getByText("PR")).toBeTruthy()
  })

  it("labels the image with the person, not the file", () => {
    render(<UserAvatar name={null} email="ada@x.io" src="/a.png" />)
    // Screen-reader users scanning a member list need the person's identity.
    expect(screen.getByRole("img", { name: "ada@x.io" })).toBeTruthy()
  })
})

// The provisioning endpoint used to write full_name as "" rather than NULL,
// so rows already in the wild carry an empty string. `name ?? email` does
// not fall back on "" — every one of those members rendered with no name and
// no email, an anonymous coloured circle. Treat blank as absent.
describe("UserAvatar — a blank name is no name", () => {
  beforeEach(() => cleanup())

  it.each(["", "   "])("falls back to the email for name=%p", (blank) => {
    render(<UserAvatar name={blank} email="newjoiner@x.io" />)
    expect(screen.getByText("NE")).toBeTruthy()
  })

  it("labels the image with the email when the name is blank", () => {
    render(<UserAvatar name="  " email="ada@x.io" src="/a.png" />)
    expect(screen.getByRole("img", { name: "ada@x.io" })).toBeTruthy()
  })
})
