import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { GeneralSection } from "../general-section"

// The Identity card is the reference implementation of the shared Save
// affordance (useDirtyForm + <SaveFooter/>). These tests pin the contract the
// rest of settings copies from: nothing typed = nothing on screen, one write
// per Save, and a failed write never eats what someone typed.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const onUpdated = vi.fn()

function renderSection(preferredLanguage: string | null = null) {
  return render(
    <GeneralSection
      workspaceId="ws1"
      orgName="Acme Robotics"
      orgSlug="acme"
      preferredLanguage={preferredLanguage}
      agentCount={3}
      crewCount={2}
      memberCount={4}
      role="OWNER"
      onUpdated={onUpdated}
      onDelete={vi.fn()}
    />,
  )
}

const nameInput = () => screen.getByLabelText(/workspace name/i)
const saveButton = () => screen.getByRole("button", { name: /^save$/i })
const patchCalls = () =>
  apiFetch.mock.calls.filter((c) => c[1]?.method === "PATCH")

describe("GeneralSection — Identity save affordance", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    apiFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ name: "Acme Robotics", slug: "acme", preferred_language: null }),
    })
  })

  it("shows no Save affordance while nothing has been edited", () => {
    renderSection()
    expect(screen.queryByRole("button", { name: /^save$/i })).toBeNull()
    expect(screen.queryByText(/unsaved changes/i)).toBeNull()
  })

  it("surfaces the footer as soon as the name is edited", () => {
    renderSection()
    fireEvent.change(nameInput(), { target: { value: "Acme Robotics Ltd" } })
    expect(screen.getByText(/unsaved changes/i)).toBeInTheDocument()
    expect(saveButton()).toBeEnabled()
  })

  it("Cancel throws the draft away and restores the saved name", () => {
    renderSection()
    fireEvent.change(nameInput(), { target: { value: "Typo Inc" } })
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }))

    expect(nameInput()).toHaveValue("Acme Robotics")
    // Footer collapses with the draft — there is nothing left to commit.
    expect(screen.queryByRole("button", { name: /^save$/i })).toBeNull()
    expect(patchCalls()).toHaveLength(0)
  })

  it("Save issues exactly one PATCH carrying the edited fields", async () => {
    apiFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ name: "Acme Robotics Ltd", slug: "acme-ltd", preferred_language: null }),
    })
    renderSection()
    fireEvent.change(nameInput(), { target: { value: "Acme Robotics Ltd" } })
    fireEvent.change(screen.getByLabelText(/^slug$/i), { target: { value: "acme-ltd" } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(patchCalls()).toHaveLength(1))
    const [url, init] = patchCalls()[0]
    expect(url).toBe("/api/v1/workspaces/ws1?workspace_id=ws1")
    expect(JSON.parse(init.body)).toMatchObject({ name: "Acme Robotics Ltd", slug: "acme-ltd" })
    await waitFor(() => expect(onUpdated).toHaveBeenCalled())
  })

  it("keeps the typed value and surfaces the reason when the PATCH is rejected", async () => {
    apiFetch.mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ error: "slug already taken" }),
    })
    renderSection()
    fireEvent.change(screen.getByLabelText(/^slug$/i), { target: { value: "taken" } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(screen.getByText(/slug already taken/i)).toBeInTheDocument())
    // Retyping a rejected edit from scratch is the worst outcome available.
    expect(screen.getByLabelText(/^slug$/i)).toHaveValue("taken")
    expect(saveButton()).toBeEnabled()
  })

  it("folds the agent language into the same footer instead of writing on pick", async () => {
    renderSection()
    fireEvent.click(screen.getByRole("button", { name: /select language/i }))
    fireEvent.click(await screen.findByText("Czech"))

    // Picking used to PATCH on the spot — a typed-in value now waits for Save
    // like the name and slug it shares a card with.
    expect(patchCalls()).toHaveLength(0)
    expect(screen.getByText(/unsaved changes/i)).toBeInTheDocument()

    fireEvent.click(saveButton())
    await waitFor(() => expect(patchCalls()).toHaveLength(1))
    expect(JSON.parse(patchCalls()[0][1].body)).toMatchObject({ preferred_language: "Czech" })
  })
})
