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

// #1648 — the card rendered the CONFIGURED egress mode as though it were the
// effective one. On a container provider with no egress proxy that produced a
// "Restricted" badge and the sentence "All other traffic is blocked" over a
// crew whose traffic was not being blocked at all. The operator's intent is
// still stored and still shown; what changed is that the card no longer
// reports it as being in force when the server says it is not.
describe("<CrewNetworkPolicy> — configured vs enforced", () => {
  const REASON =
    "egress is enforced by the in-container crewship-sidecar proxy, whose binary this provider does not mount"

  it("marks a restricted crew the provider cannot fence, and gives the reason", () => {
    render(
      <CrewNetworkPolicy
        networkMode="restricted"
        enforced={false}
        unenforcedReason={REASON}
        allowedDomains={["github.com"]}
        canEdit
        onSave={vi.fn()}
      />,
    )
    expect(screen.getByText(/not enforced/i)).toBeInTheDocument()
    expect(screen.getByRole("alert")).toHaveTextContent(/crewship-sidecar/)
    // The claim that made the card a liar must be gone.
    expect(screen.queryByText(/All other traffic is blocked/i)).toBeNull()
  })

  it("says nothing extra when the provider does enforce the mode", () => {
    render(
      <CrewNetworkPolicy
        networkMode="restricted"
        enforced={true}
        allowedDomains={[]}
        canEdit
        onSave={vi.fn()}
      />,
    )
    expect(screen.queryByRole("alert")).toBeNull()
    expect(screen.queryByText(/not enforced/i)).toBeNull()
    expect(screen.getByText(/All other traffic is blocked/i)).toBeInTheDocument()
  })

  it("renders unchanged against a server that does not report enforcement", () => {
    render(
      <CrewNetworkPolicy
        networkMode="restricted"
        allowedDomains={[]}
        canEdit
        onSave={vi.fn()}
      />,
    )
    expect(screen.queryByRole("alert")).toBeNull()
    expect(screen.getByText(/All other traffic is blocked/i)).toBeInTheDocument()
  })

  it("does not mark a free crew — there is nothing to enforce", () => {
    render(
      <CrewNetworkPolicy
        networkMode="free"
        enforced={false}
        unenforcedReason={REASON}
        allowedDomains={[]}
        canEdit
        onSave={vi.fn()}
      />,
    )
    expect(screen.queryByRole("alert")).toBeNull()
    expect(screen.queryByText(/not enforced/i)).toBeNull()
  })
})
