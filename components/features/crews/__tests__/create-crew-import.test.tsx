// =============================================================================
// Importing a crew manifest into the wizard.
//
// `crewship apply -f crew.yaml` has been able to create a crew from YAML for a
// while and the browser had no way in. This file guards the way in, and in
// particular the part that is easy to get wrong: the wizard's submit path
// creates a crew and nothing inside it, so an imported document's agents,
// credentials and services are LEFT BEHIND. That has to be said on screen
// before the import is applied, not discovered afterwards.
// =============================================================================

import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CreateCrewDialog } from "@/components/features/crews/create-crew-dialog"

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() },
}))

const MANIFEST = `apiVersion: crewship/v1
kind: Crew
metadata:
  name: Data Engineering
  slug: data-eng
  description: Pipelines and warehousing.
  icon: database
spec:
  devcontainer:
    image: mcr.microsoft.com/devcontainers/python:3.12
    features:
      "ghcr.io/devcontainers/features/aws-cli:1": {}
    memory_mb: 8192
  mise:
    tools:
      python: "3.12"
  agents:
    - name: Ada
      slug: ada
    - name: Grace
      slug: grace
  credentials:
    - env: ANTHROPIC_API_KEY
`

function setupFetch() {
  vi.stubGlobal("fetch", vi.fn(async (url: string | URL) => {
    const u = typeof url === "string" ? url : url.toString()
    if (u.includes("/crew-templates")) {
      return { ok: true, status: 200, json: async () => [] } as Response
    }
    if (u.includes("/catalog")) {
      return { ok: true, status: 200, json: async () => ({ features: [], runtimes: [] }) } as Response
    }
    return { ok: true, status: 200, json: async () => ({}), text: async () => "" } as Response
  }))
}

/**
 * Walk to Step 2 and open the import panel.
 *
 * Step 1 gates Continue on a valid name + slug, so something has to be typed
 * to get here at all. That is useful rather than incidental: it means every
 * test below starts with a form that already has content, and an import that
 * failed to overwrite it would show up.
 */
async function openImport(typedName = "Placeholder") {
  render(<CreateCrewDialog workspaceId="ws_test" open onOpenChange={vi.fn()} onCreated={vi.fn()} />)
  fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: typedName } })
  fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
  fireEvent.click(await screen.findByRole("button", { name: /^Import YAML/ }))
  return screen.getByLabelText(/or paste it/i) as HTMLTextAreaElement
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

describe("<CreateCrewDialog> — importing a manifest", () => {
  it("opens as a panel, so the step strip stops claiming a step number", async () => {
    setupFetch()
    await openImport()

    // Same rule the base-image and icon panels follow: a panel is not a step,
    // and "step 2 of 4" over a file picker is a lie about where you are.
    expect(screen.getByText(/Import — new crew/)).toBeInTheDocument()
    expect(screen.queryByText(/step 2 of 4/)).toBeNull()
  })

  it("says what it will fill in before anything is applied", async () => {
    setupFetch()
    const textarea = await openImport()
    fireEvent.change(textarea, { target: { value: MANIFEST } })

    expect(await screen.findByText("Data Engineering")).toBeInTheDocument()
    expect(screen.getByText("data-eng")).toBeInTheDocument()
    expect(screen.getByText("mcr.microsoft.com/devcontainers/python:3.12")).toBeInTheDocument()
    expect(screen.getByText("1 feature")).toBeInTheDocument()
    expect(screen.getByText("python 3.12")).toBeInTheDocument()
  })

  it("names what it is leaving behind, and points at the tool that would not", async () => {
    setupFetch()
    const textarea = await openImport()
    fireEvent.change(textarea, { target: { value: MANIFEST } })

    const warning = await screen.findByText(/also declares/i)
    expect(warning).toHaveTextContent(/2 agents/)
    expect(warning).toHaveTextContent(/Ada, Grace/)
    expect(warning).toHaveTextContent(/1 credential/)
    expect(warning).toHaveTextContent(/crewship apply -f/)
  })

  it("fills the form and lands on Identity, where the rewritten fields are", async () => {
    setupFetch()
    const textarea = await openImport()
    fireEvent.change(textarea, { target: { value: MANIFEST } })

    fireEvent.click(await screen.findByRole("button", { name: /Fill the form from this file/ }))

    // Step 1, not step 3: the import rewrote name/slug/icon, and those are on
    // Identity. Landing anywhere else walks the user past the one thing they
    // most need to check. The name it replaces is the one openImport typed.
    await waitFor(() => {
      expect(screen.getByPlaceholderText("Engineering")).toHaveValue("Data Engineering")
    })
    expect(screen.getByPlaceholderText("engineering")).toHaveValue("data-eng")
    expect(screen.getByText(/step 1 of 4/)).toBeInTheDocument()
  })

  it("refuses a file that is not a crew manifest, by name", async () => {
    setupFetch()
    const textarea = await openImport()
    fireEvent.change(textarea, { target: { value: "kind: Project\nmetadata:\n  name: X" } })

    expect(await screen.findByRole("alert")).toHaveTextContent(/`kind: Project`/)
    expect(screen.queryByRole("button", { name: /Fill the form/ })).toBeNull()
  })

  it("leaves the wizard untouched when the panel is backed out of", async () => {
    setupFetch()
    const textarea = await openImport("Typed By Hand")
    fireEvent.change(textarea, { target: { value: MANIFEST } })
    // Parsed and previewed, but not applied.
    await screen.findByText("Data Engineering")

    // Inside a panel the surface offers two ways back — the header arrow and
    // the footer's Cancel, which is relabelled "Back". Both are named Back;
    // the header's is first in the DOM. Either must discard the preview.
    const back = () => screen.getAllByRole("button", { name: /^Back$/ })[0]
    fireEvent.click(back())
    await waitFor(() => expect(screen.getByRole("button", { name: /^Import YAML/ })).toBeInTheDocument())

    // Step 1 still holds what was typed, not what was previewed.
    fireEvent.click(back())
    await waitFor(() => {
      expect(screen.getByPlaceholderText("Engineering")).toHaveValue("Typed By Hand")
    })
  })
})
