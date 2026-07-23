import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { CrewNetworkPolicy } from "@/components/features/crews/crew-network-policy"
import { PACKAGE_REGISTRY_DOMAINS } from "@/components/features/crews/registry-presets"

describe("<CrewNetworkPolicy> — #1377 egress ergonomics", () => {
  it("mentions wildcard subdomain support in restricted mode", () => {
    render(
      <CrewNetworkPolicy
        networkMode="restricted"
        allowedDomains={[]}
        canEdit
        onSave={vi.fn()}
      />,
    )
    // The single biggest trap is that exact-match hides subdomain support —
    // the panel must advertise the "*.example.com" rule.
    expect(screen.getByText(/\*\.github\.com/)).toBeInTheDocument()
  })

  it("one-click 'Allow package registries' appends the curated preset", () => {
    render(
      <CrewNetworkPolicy
        networkMode="restricted"
        allowedDomains={["github.com"]}
        canEdit
        onSave={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: /package registries/i }))

    const textarea = screen.getByLabelText(/Extra Allowed Domains/i) as HTMLTextAreaElement
    const value = textarea.value.toLowerCase()
    // Existing entry preserved …
    expect(value).toContain("github.com")
    // … and every registry host folded in.
    for (const host of PACKAGE_REGISTRY_DOMAINS) {
      expect(value).toContain(host)
    }
  })

  it("does not offer the preset button to read-only viewers", () => {
    render(
      <CrewNetworkPolicy
        networkMode="restricted"
        allowedDomains={[]}
        canEdit={false}
        onSave={vi.fn()}
      />,
    )
    expect(screen.queryByRole("button", { name: /package registries/i })).toBeNull()
  })
})
