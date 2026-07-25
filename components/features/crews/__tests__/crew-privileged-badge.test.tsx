import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import {
  CrewPrivilegedBadge,
  isCrewPrivileged,
} from "@/components/features/crews/crew-privileged-badge"

describe("isCrewPrivileged", () => {
  it("reads privileged out of the stored devcontainer_config", () => {
    expect(isCrewPrivileged(`{"image":"debian","privileged":true}`)).toBe(true)
  })

  it("treats an absent or false flag as not privileged", () => {
    expect(isCrewPrivileged(`{"image":"debian"}`)).toBe(false)
    expect(isCrewPrivileged(`{"image":"debian","privileged":false}`)).toBe(false)
  })

  it("fails safe on null / empty / malformed config", () => {
    // A config the parser rejects never reaches the container privileged
    // either, so reporting false here matches the runtime.
    expect(isCrewPrivileged(null)).toBe(false)
    expect(isCrewPrivileged("")).toBe(false)
    expect(isCrewPrivileged("{not json")).toBe(false)
  })
})

describe("<CrewPrivilegedBadge>", () => {
  it("warns when the crew runs privileged", () => {
    render(<CrewPrivilegedBadge devcontainerConfig={`{"image":"debian","privileged":true}`} />)
    expect(screen.getByText(/isolation reduced/i)).toBeInTheDocument()
  })

  it("renders nothing for a hardened crew", () => {
    const { container } = render(
      <CrewPrivilegedBadge devcontainerConfig={`{"image":"debian"}`} />,
    )
    expect(container).toBeEmptyDOMElement()
  })
})
