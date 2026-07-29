import type { ComponentProps } from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { GeneralSection } from "../general-section"

// The Identity card is the reference implementation of the shared Save
// affordance (useDirtyForm + <SaveFooter/>). These tests pin the contract the
// rest of settings copies from: nothing typed = nothing on screen, one write
// per Save, and a failed write never eats what someone typed.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

// The privileged-credentials override has its own test file and its own fetch;
// here it only has to prove it landed on this section.
vi.mock("../privileged-credentials-card", () => ({
  PrivilegedCredentialsCard: () => <div data-testid="privileged-credentials-card" />,
}))

const onUpdated = vi.fn()

type SectionProps = ComponentProps<typeof GeneralSection>

function renderSection(overrides: Partial<SectionProps> = {}) {
  const props: SectionProps = {
    workspaceId: "ws1",
    orgName: "Acme Robotics",
    orgSlug: "acme",
    preferredLanguage: null,
    agentCount: 3,
    crewCount: 2,
    memberCount: 4,
    role: "OWNER",
    onUpdated,
    onDelete: vi.fn(),
    ...overrides,
  }
  return render(<GeneralSection {...props} />)
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

// `PATCH /api/v1/workspaces/{id}` is roleManage — ADMIN and up. The card used
// to hand everyone an editable form, so a MEMBER could type a new name, press
// Save and collect a 403. Read-only below the tier: the values stay legible
// (you should be able to see which workspace you are in), the controls don't.
describe("GeneralSection — role gating of the Identity card", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    apiFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ name: "Acme Robotics Ltd", slug: "acme", preferred_language: null }),
    })
    onUpdated.mockReset()
  })

  it("keeps the identity fields editable and saveable for an ADMIN", async () => {
    renderSection({ role: "ADMIN" })
    fireEvent.change(nameInput(), { target: { value: "Acme Robotics Ltd" } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(patchCalls()).toHaveLength(1))
    expect(JSON.parse(patchCalls()[0][1].body)).toMatchObject({ name: "Acme Robotics Ltd" })
  })

  it("renders the identity card as plain text for a MEMBER — no inputs, no Save", () => {
    renderSection({ role: "MEMBER" })

    expect(screen.queryByLabelText(/workspace name/i)).toBeNull()
    expect(screen.queryByLabelText(/^slug$/i)).toBeNull()
    // Not "disabled inputs" — a greyed-out box still invites the attempt.
    expect(screen.queryByRole("textbox")).toBeNull()
    expect(screen.queryByRole("button", { name: /^save$/i })).toBeNull()
    expect(screen.queryByText(/unsaved changes/i)).toBeNull()
  })

  it("still shows a MEMBER which workspace they are in", () => {
    renderSection({ role: "MEMBER", preferredLanguage: "Czech" })

    expect(screen.getByText("Acme Robotics")).toBeInTheDocument()
    expect(screen.getByText("acme")).toBeInTheDocument()
    expect(screen.getByText(/Czech/)).toBeInTheDocument()
    // Usage counts are read-only for everyone and must survive the gate.
    expect(screen.getByText("Agents")).toBeInTheDocument()
  })

  it("tells a MEMBER why the fields are not editable, quietly", () => {
    renderSection({ role: "MEMBER" })
    expect(screen.getByText(/only workspace admins can change/i)).toBeInTheDocument()
    // No alert/banner treatment: this is a normal state, not an error.
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("does not let a MEMBER reach the language picker", () => {
    renderSection({ role: "MEMBER" })
    expect(screen.queryByRole("button", { name: /select language/i })).toBeNull()
    expect(screen.queryByRole("combobox")).toBeNull()
  })

  it("hides the Danger zone from an ADMIN — delete stays OWNER-only", () => {
    renderSection({ role: "ADMIN" })
    expect(screen.queryByText(/danger zone/i)).toBeNull()
    expect(screen.queryByRole("button", { name: /delete workspace/i })).toBeNull()
  })

  it("hides the Danger zone from a MEMBER", () => {
    renderSection({ role: "MEMBER" })
    expect(screen.queryByText(/danger zone/i)).toBeNull()
  })

  // It used to sit under "Crews & Containers", which read as per-crew config
  // it is not: it is a workspace-wide, fail-closed isolation boundary. General
  // is where workspace-wide switches live, so this is where an owner looks.
  it("carries the workspace-wide privileged-credentials override", () => {
    renderSection({ role: "OWNER" })
    expect(screen.getByTestId("privileged-credentials-card")).toBeInTheDocument()
  })

  it("shows the override to a MEMBER too — read-only, but not a secret", () => {
    // Same rule as the identity card: a role that may read the state keeps
    // seeing it; the card gates its own switch on the caller's abilities.
    renderSection({ role: "MEMBER" })
    expect(screen.getByTestId("privileged-credentials-card")).toBeInTheDocument()
  })
})
