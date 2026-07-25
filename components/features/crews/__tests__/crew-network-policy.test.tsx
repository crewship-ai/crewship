import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
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

// #1377 gap 3 — allow_private_endpoints round-trips end-to-end (migration v135
// → resolver → agentrun) but had no UI affordance: reaching an on-prem Ollama /
// LAN model needed the CLI, and nothing in the UI explained why a private
// endpoint was blocked.
describe("<CrewNetworkPolicy> — private endpoints toggle", () => {
  it("renders the toggle OFF and explains the SSRF fence", () => {
    render(
      <CrewNetworkPolicy
        networkMode="free"
        allowedDomains={[]}
        allowPrivateEndpoints={false}
        canEdit
        canEditPrivateEndpoints
        onSave={vi.fn()}
      />,
    )
    const toggle = screen.getByRole("switch", { name: /private endpoints/i })
    expect(toggle).toHaveAttribute("aria-checked", "false")
    // The instance ceiling is the #1 "why is it still blocked?" surprise.
    expect(screen.getByText(/CREWSHIP_ALLOW_PRIVATE_ENDPOINTS/)).toBeInTheDocument()
  })

  it("saves allow_private_endpoints=true when flipped on", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(
      <CrewNetworkPolicy
        networkMode="restricted"
        allowedDomains={["github.com"]}
        allowPrivateEndpoints={false}
        canEdit
        canEditPrivateEndpoints
        onSave={onSave}
      />,
    )
    fireEvent.click(screen.getByRole("switch", { name: /private endpoints/i }))
    fireEvent.click(await screen.findByRole("button", { name: /save network policy/i }))
    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith("restricted", ["github.com"], true),
    )
  })

  it("is read-only for non-admins but still shows the current posture", () => {
    render(
      <CrewNetworkPolicy
        networkMode="free"
        allowedDomains={[]}
        allowPrivateEndpoints
        canEdit
        canEditPrivateEndpoints={false}
        onSave={vi.fn()}
      />,
    )
    const toggle = screen.getByRole("switch", { name: /private endpoints/i })
    expect(toggle).toHaveAttribute("aria-checked", "true")
    expect(toggle).toBeDisabled()
    expect(screen.getByText(/requires an admin/i)).toBeInTheDocument()
  })

  it("keeps the toggle hidden when the crew record predates the field", () => {
    render(
      <CrewNetworkPolicy networkMode="free" allowedDomains={[]} canEdit onSave={vi.fn()} />,
    )
    // Undefined (not false) means "backend didn't tell us" — don't render a
    // control that would PATCH a value the caller never loaded.
    expect(screen.queryByRole("switch", { name: /private endpoints/i })).toBeNull()
  })
})
