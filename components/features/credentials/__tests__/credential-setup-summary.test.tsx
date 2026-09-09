import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"
import { CredentialSetupSummary } from "../credential-setup-summary"

describe("credential settings summary", () => {
  it("shows the current scope, protection and explicit expiry", () => {
    render(<CredentialSetupSummary scope="CREW" crewCount={2} tier={3} expiresAt="2027-02-01" />)
    expect(screen.getByText("2 selected crews")).toBeInTheDocument()
    expect(screen.getByText("Keeper L3")).toBeInTheDocument()
    expect(screen.getByText("2027-02-01")).toBeInTheDocument()
  })
  it("does not invent provider expiry or a tier when omitted", () => {
    render(<CredentialSetupSummary scope="WORKSPACE" crewCount={0} />)
    expect(screen.getByText("Workspace")).toBeInTheDocument()
    expect(screen.queryByText("Expiry")).not.toBeInTheDocument()
    expect(screen.queryByText("Protection")).not.toBeInTheDocument()
  })
  it("distinguishes no expiry configured from an omitted expiry", () => {
    render(<CredentialSetupSummary scope="WORKSPACE" crewCount={0} tier={2} expiresAt="" />)
    expect(screen.getByText("No expiry set")).toBeInTheDocument()
  })
})
