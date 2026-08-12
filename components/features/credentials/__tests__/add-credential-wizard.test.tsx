// The add-credential flow. Three things are worth a test here and the rest is
// layout:
//
//  1. The SHAPE drives the form. This is the whole KISS bet from §0 — six item
//     types instead of a brand catalog — and it is only true if picking a type
//     actually changes which boxes appear.
//  2. The brand is a HINT, never a gate. §0 item 5 is explicit; a regression
//     that made detection required would block every unrecognised secret,
//     which is most of them.
//  3. The write order and RBAC. The credential row, its custom fields and its
//     binding are three separate endpoints with three different role tiers,
//     and a failure in the second or third must never be reported as "nothing
//     was saved" — the secret is in the vault by then.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { AddCredentialWizard } from "../add-credential-wizard"

const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  capabilities: [] as string[],
  apiFetch: vi.fn(),
}))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
vi.mock("@/hooks/use-abilities", async () => {
  const { defineAbilitiesFor } = await import("@/lib/permissions/abilities")
  const { hasCapability } = await import("@/lib/capabilities")
  return {
    useAbilities: () => ({
      abilities: defineAbilitiesFor(h.role as never),
      role: h.role,
      capabilities: h.capabilities,
      hasCapability: (cap: never) => hasCapability(h.capabilities, cap),
      loading: false,
    }),
  }
})

function ok(body: unknown, status = 200) {
  return { ok: true, status, json: async () => body } as unknown as Response
}
function fail(status: number, body: unknown = {}) {
  return { ok: false, status, json: async () => body } as unknown as Response
}

function renderWizard(overrides: { onSuccess?: () => void; onCancel?: () => void } = {}) {
  const onSuccess = overrides.onSuccess ?? vi.fn()
  const onCancel = overrides.onCancel ?? vi.fn()
  render(
    <AddCredentialWizard workspaceId="ws1" onSuccess={onSuccess} onCancel={onCancel} />,
  )
  return { onSuccess, onCancel }
}

/** Walk to step 2 with the given shape selected. */
function pickShape(label: RegExp) {
  fireEvent.click(screen.getByRole("button", { name: label }))
  fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
}

function bodyOf(call: unknown[]): Record<string, unknown> {
  return JSON.parse(String((call[1] as { body?: string })?.body ?? "{}"))
}

beforeEach(() => {
  h.role = "OWNER"
  h.capabilities = []
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue(ok({ id: "cred_new" }, 201))
})

describe("step 1 — the shape decides the form, not the brand", () => {
  it("offers exactly the six item types the PRD scoped", () => {
    renderWizard()
    for (const label of ["Token", "Login", "Key pair", "SSH key", "File", "Certificate"]) {
      expect(screen.getByRole("button", { name: new RegExp(label, "i") })).toBeInTheDocument()
    }
  })

  it("asks a Token for one secret and nothing else", () => {
    renderWizard()
    pickShape(/^token/i)
    expect(screen.getByLabelText(/^token$/i)).toBeInTheDocument()
    expect(screen.queryByLabelText(/^username$/i)).not.toBeInTheDocument()
  })

  it("asks a Login for a username and a password", () => {
    renderWizard()
    pickShape(/^login/i)
    expect(screen.getByLabelText(/^username$/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/^password$/i)).toBeInTheDocument()
  })

  it("asks a Key pair for three parts and says which one stays readable", () => {
    renderWizard()
    pickShape(/^key pair/i)
    expect(screen.getByLabelText(/secret access key/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/access key id/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/region \(optional\)/i)).toBeInTheDocument()
    expect(screen.getByText(/stored in the clear so it stays searchable/i)).toBeInTheDocument()
  })

  it("warns that a File has to become a real file inside the container", () => {
    renderWizard()
    pickShape(/^file/i)
    expect(screen.getByText(/written to tmpfs for the run and removed afterwards/i)).toBeInTheDocument()
  })
})

// The step bar used to be three static pills. It is the only thing on screen
// that says where you are in a flow you cannot see the end of, so it now
// carries the state in the accessibility tree rather than in a border colour,
// and it is the way back to a step you already finished.
describe("the step bar", () => {
  it("announces the step you are on rather than only tinting it", () => {
    renderWizard()
    expect(screen.getByRole("button", { name: /shape/i })).toHaveAttribute("aria-current", "step")
  })

  it("walks back to a finished step but will not skip ahead to an unfinished one", () => {
    renderWizard()
    pickShape(/^login/i)
    expect(screen.getByRole("button", { name: /values/i })).toHaveAttribute("aria-current", "step")
    // Step 3 needs a password and a name first; offering it would be a link
    // to a form that cannot be submitted.
    expect(screen.getByRole("button", { name: /delivery/i })).toBeDisabled()

    fireEvent.click(screen.getByRole("button", { name: /shape/i }))
    expect(screen.getByRole("button", { name: /^login/i })).toBeInTheDocument()
  })

  it("keeps three steps on one line at 390px by muting the labels you are not on", () => {
    renderWizard()
    // Visible to a screen reader either way — the accessible name of each step
    // button is unchanged — but only the current label takes horizontal space.
    expect(screen.getByText("Values").className).toContain("max-sm:sr-only")
    expect(screen.getByText("Shape").className).not.toContain("max-sm:sr-only")
  })
})

// 390×844. The dialog is the whole screen there, so the three things that
// decide whether it is usable are: the tiles reflow, the body scrolls without
// taking the actions with it, and the actions are big enough to hit.
describe("layout on a phone", () => {
  it("reflows the six shapes two-up, and three-up once there is room", () => {
    renderWizard()
    const grid = screen.getByTestId("shape-grid")
    expect(grid.className).toContain("grid-cols-2")
    expect(grid.className).toContain("sm:grid-cols-3")
  })

  it("docks the actions in a footer the scrolling body cannot carry off-screen", () => {
    renderWizard()
    const body = screen.getByTestId("wizard-body")
    const footer = screen.getByTestId("wizard-footer")
    expect(body.className).toContain("overflow-y-auto")
    expect(body.contains(footer)).toBe(false)
    expect(within(footer).getByRole("button", { name: /^continue$/i })).toBeInTheDocument()
    expect(within(footer).getByRole("button", { name: /^cancel$/i })).toBeInTheDocument()
  })

  it("gives the footer buttons a thumb-sized target and the width to share", () => {
    renderWizard()
    const cont = screen.getByRole("button", { name: /^continue$/i })
    expect(cont.className).toContain("max-sm:h-11")
    expect(cont.className).toContain("max-sm:flex-1")
  })

  it("keeps every field at 16px, so the first tap does not zoom the dialog", () => {
    renderWizard()
    pickShape(/^ssh key/i)
    // iOS Safari zooms whenever a focused field is under 16px. A plain field
    // inherits the ui kit's text-base and is fine; one that opts down to 12px
    // mono has to opt back up below sm, and both were 12–14px flat before.
    expect(screen.getByLabelText(/private key/i).className).toContain("max-sm:text-base")
    expect(screen.getByLabelText(/^passphrase/i).className).not.toMatch(/(^|\s)text-(xs|sm)(\s|$)/)
  })

  it("keeps the shape tiles tall enough to hit and readable enough to tell apart", () => {
    renderWizard()
    const tile = screen.getByRole("button", { name: /^certificate/i })
    expect(tile.className).toContain("min-h-20")
    // The blurb is the thing that distinguishes "Key pair" from "Token" for
    // anyone who has not read the PRD; it is not decoration to be dropped.
    expect(within(tile).getByText("PEM chain for mTLS")).toBeInTheDocument()
  })
})

// The user's own words about step 1: "I don't want to pick any group at the
// start — I just want to give an icon, which brand it is, and that's it." The
// icon lives on the first step now, next to the shape, and it is still a hint:
// nothing about it gates the flow.
describe("the brand icon is offered up front", () => {
  it("sits on the first step without becoming a required choice", () => {
    renderWizard()
    expect(screen.getByRole("button", { name: /provider: generic secret/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^continue$/i })).not.toBeDisabled()
  })

  it("carries an icon picked on the first step through to the saved credential", async () => {
    const { onSuccess } = renderWizard()
    fireEvent.click(screen.getByRole("button", { name: /provider: generic secret/i }))
    fireEvent.change(screen.getByPlaceholderText("Search brands…"), { target: { value: "notion" } })
    fireEvent.click(screen.getByTitle("Notion"))
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))

    // Deliberately unrecognisable, so the provider on the wire can only have
    // come from the choice made on step 1.
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "opaque-value-1" } })
    fireEvent.change(screen.getByLabelText(/name \(which account\)/i), { target: { value: "INTERNAL" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.click(screen.getByRole("button", { name: /save secret/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const createCall = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials?"))!
    expect(bodyOf(createCall)).toMatchObject({ provider: "NOTION" })
  })
})

describe("brand detection is a hint", () => {
  it("recognises a pasted GitHub PAT and suggests a variable name", () => {
    renderWizard()
    pickShape(/^token/i)
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "github_pat_11ABCDE" } })
    expect(screen.getByText(/looks like github/i)).toBeInTheDocument()
    expect(screen.getByText("GH_TOKEN")).toBeInTheDocument()
  })

  // The gate test. An unrecognised secret is the common case and must not slow
  // anybody down.
  it("lets an unrecognised value through with no brand and no complaint", () => {
    renderWizard()
    pickShape(/^token/i)
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "zzz-some-internal-thing" } })
    expect(screen.queryByText(/looks like/i)).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(/name \(which account\)/i), { target: { value: "internal-thing" } })
    expect(screen.getByRole("button", { name: /^continue$/i })).not.toBeDisabled()
  })

  it("also detects the brand from the name when the value shape says nothing", () => {
    renderWizard()
    pickShape(/^token/i)
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "opaque-value" } })
    fireEvent.change(screen.getByLabelText(/name \(which account\)/i), { target: { value: "gitlab-ci" } })
    expect(screen.getByText(/looks like gitlab/i)).toBeInTheDocument()
  })
})

describe("secrets are masked by default", () => {
  it("renders the primary secret input masked until the user asks to see it", () => {
    renderWizard()
    pickShape(/^token/i)
    const input = screen.getByLabelText(/^token$/i) as HTMLInputElement
    expect(input.type).toBe("password")
    fireEvent.click(screen.getByRole("button", { name: /^show token$/i }))
    expect((screen.getByLabelText(/^token$/i) as HTMLInputElement).type).toBe("text")
  })
})

describe("step 2 → 3 gating", () => {
  it("will not continue without the required parts", () => {
    renderWizard()
    pickShape(/^key pair/i)
    fireEvent.change(screen.getByLabelText(/secret access key/i), { target: { value: "s3cret" } })
    fireEvent.change(screen.getByLabelText(/name \(which account\)/i), { target: { value: "aws-prod" } })
    // access_key_id is required and still empty.
    expect(screen.getByRole("button", { name: /^continue$/i })).toBeDisabled()
    // …and names the part that is holding it up, rather than a dead button.
    expect(screen.getByText(/is still empty/i)).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText(/access key id/i), { target: { value: "AKIA1" } })
    expect(screen.getByRole("button", { name: /^continue$/i })).not.toBeDisabled()
  })

  it("will not continue without a name", () => {
    renderWizard()
    pickShape(/^token/i)
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "abc123" } })
    expect(screen.getByRole("button", { name: /^continue$/i })).toBeDisabled()
  })
})

/** Fill a token credential and land on step 3. */
function toScopeStep(name = "github-acme", value = "github_pat_11ABCDE") {
  pickShape(/^token/i)
  fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value } })
  fireEvent.change(screen.getByLabelText(/name \(which account\)/i), { target: { value: name } })
  fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
}

describe("step 3 — scope and slot", () => {
  it("prefills the slot from the detected brand but lets the user overwrite it", async () => {
    const { onSuccess } = renderWizard()
    toScopeStep()
    const slot = screen.getByLabelText(/slot/i) as HTMLInputElement
    expect(slot.value).toBe("GH_TOKEN")

    fireEvent.change(slot, { target: { value: "GH_TOKEN_READONLY" } })
    fireEvent.click(screen.getByRole("button", { name: /save secret/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const bindingCall = h.apiFetch.mock.calls.find(([url]) => String(url).includes("/credentials/bindings"))!
    expect(bodyOf(bindingCall)).toMatchObject({
      credential_id: "cred_new",
      scope: "WORKSPACE",
      crew_id: "",
      slot: "GH_TOKEN_READONLY",
    })
  })

  // POST /credentials/bindings is roleManage. A MANAGER may create the
  // credential but not claim a slot, so offering the box would be a form
  // field whose submit 403s.
  it("hides the slot from a MANAGER and says what happens instead", () => {
    h.role = "MANAGER"
    renderWizard()
    toScopeStep()
    expect(screen.queryByLabelText(/slot/i)).not.toBeInTheDocument()
    expect(screen.getByText(/delivered under its own name/i)).toBeInTheDocument()
  })

  it("never posts a binding for a role that cannot create one", async () => {
    h.role = "MANAGER"
    const { onSuccess } = renderWizard()
    toScopeStep()
    fireEvent.click(screen.getByRole("button", { name: /save secret/i }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    expect(h.apiFetch.mock.calls.some(([url]) => String(url).includes("/credentials/bindings"))).toBe(false)
  })

  // Without a binding the delivery layer falls back to the credential's own
  // name, so a name that is not a legal env var silently reaches nothing.
  it("warns when there is no slot and the name is not a legal env var", () => {
    renderWizard()
    toScopeStep("github acme", "opaque")
    fireEvent.change(screen.getByLabelText(/slot/i), { target: { value: "" } })
    expect(screen.getByText(/not a valid environment-variable name/i)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /use github_acme/i }))
    expect(screen.queryByText(/not a valid environment-variable name/i)).not.toBeInTheDocument()
  })

  it("refuses to save a crew-scoped credential with no crew picked", async () => {
    renderWizard()
    toScopeStep()
    fireEvent.click(screen.getByRole("button", { name: /selected crews/i }))
    fireEvent.click(screen.getByRole("button", { name: /save secret/i }))
    expect(await screen.findByText(/pick at least one crew/i)).toBeInTheDocument()
    expect(h.apiFetch.mock.calls.some(([, init]) => (init as { method?: string })?.method === "POST"
      && String(h.apiFetch.mock.calls[0][0]).includes("/api/v1/credentials?"))).toBe(false)
  })
})

describe("saving", () => {
  it("posts the credential with the type its shape implies, then its extra fields", async () => {
    const { onSuccess } = renderWizard()
    pickShape(/^key pair/i)
    fireEvent.change(screen.getByLabelText(/secret access key/i), { target: { value: "s3cret" } })
    fireEvent.change(screen.getByLabelText(/access key id/i), { target: { value: "AKIA1" } })
    fireEvent.change(screen.getByLabelText(/region/i), { target: { value: "eu-central-1" } })
    fireEvent.change(screen.getByLabelText(/name \(which account\)/i), { target: { value: "aws-prod" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.change(screen.getByLabelText(/slot/i), { target: { value: "AWS_SECRET_ACCESS_KEY" } })
    fireEvent.click(screen.getByRole("button", { name: /save secret/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())

    const createCall = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials?"))!
    expect(bodyOf(createCall)).toMatchObject({
      name: "aws-prod",
      value: "s3cret",
      type: "GENERIC_SECRET",
      scope: "WORKSPACE",
    })

    const fieldCalls = h.apiFetch.mock.calls.filter(([url]) => String(url).includes("/fields"))
    expect(fieldCalls.map(bodyOf)).toEqual([
      { key: "access_key_id", value: "AKIA1", is_secret: false, ordinal: 0 },
      { key: "region", value: "eu-central-1", is_secret: false, ordinal: 1 },
    ])
  })

  it("puts a Login's username on the credential row, not in a custom field", async () => {
    const { onSuccess } = renderWizard()
    pickShape(/^login/i)
    fireEvent.change(screen.getByLabelText(/^password$/i), { target: { value: "hunter2" } })
    fireEvent.change(screen.getByLabelText(/^username$/i), { target: { value: "svc-account" } })
    fireEvent.change(screen.getByLabelText(/name \(which account\)/i), { target: { value: "db-login" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.click(screen.getByRole("button", { name: /save secret/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const createCall = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials?"))!
    expect(bodyOf(createCall)).toMatchObject({ type: "USERPASS", username: "svc-account" })
    expect(h.apiFetch.mock.calls.some(([url]) => String(url).includes("/fields"))).toBe(false)
  })

  it("sends user-added custom fields with the secrecy the user chose", async () => {
    const { onSuccess } = renderWizard()
    pickShape(/^token/i)
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "abc123" } })
    fireEvent.click(screen.getByRole("button", { name: /add a field/i }))
    fireEvent.change(screen.getByLabelText(/custom field 1 key/i), { target: { value: "tenant_id" } })
    fireEvent.change(screen.getByLabelText(/custom field 1 value/i), { target: { value: "acme" } })
    fireEvent.click(screen.getByRole("button", { name: /custom field 1 is secret/i }))
    fireEvent.change(screen.getByLabelText(/name \(which account\)/i), { target: { value: "THING" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.click(screen.getByRole("button", { name: /save secret/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const fieldCalls = h.apiFetch.mock.calls.filter(([url]) => String(url).includes("/fields"))
    expect(fieldCalls.map(bodyOf)).toEqual([
      { key: "tenant_id", value: "acme", is_secret: false, ordinal: 0 },
    ])
  })

  // Tags drive the sidebar's Tag facet. Dropping them from the create path
  // (the old flat form had them) would leave a filter nobody can populate
  // without a second visit to the edit dialog.
  it("carries tags typed on the create path", async () => {
    const { onSuccess } = renderWizard()
    pickShape(/^token/i)
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "abc123" } })
    const tagInput = screen.getByLabelText(/tags \(optional\)/i)
    fireEvent.change(tagInput, { target: { value: "Prod" } })
    fireEvent.keyDown(tagInput, { key: "Enter" })
    fireEvent.change(screen.getByLabelText(/name \(which account\)/i), { target: { value: "THING" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.click(screen.getByRole("button", { name: /save secret/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const createCall = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials?"))!
    expect(bodyOf(createCall)).toMatchObject({ tags: ["prod"] })
  })

  it("surfaces the server's rejection and writes nothing further", async () => {
    h.apiFetch.mockResolvedValue(fail(409, { error: "Credential with this name already exists" }))
    const { onSuccess } = renderWizard()
    toScopeStep()
    fireEvent.click(screen.getByRole("button", { name: /save secret/i }))

    expect(await screen.findByText(/credential with this name already exists/i)).toBeInTheDocument()
    expect(onSuccess).not.toHaveBeenCalled()
    expect(h.apiFetch.mock.calls.some(([url]) => String(url).includes("/fields"))).toBe(false)
    expect(h.apiFetch.mock.calls.some(([url]) => String(url).includes("/bindings"))).toBe(false)
  })

  // The credential row exists by then. Reporting "save failed" would send the
  // user to create it again, which 409s on the unique name.
  it("reports a failed slot claim as a partial save, not as a failure", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (String(url).includes("/bindings")) {
        return fail(409, { error: "slot GH_TOKEN is already bound in this scope — delete the existing binding first" })
      }
      return ok({ id: "cred_new" }, 201)
    })
    const { onSuccess } = renderWizard()
    toScopeStep()
    fireEvent.click(screen.getByRole("button", { name: /save secret/i }))

    expect(await screen.findByText(/already bound in this scope/i)).toBeInTheDocument()
    expect(screen.getByText(/but some parts did not land/i)).toBeInTheDocument()
    expect(onSuccess).toHaveBeenCalled()
  })

  it("reports a network failure without claiming anything about the vault's contents", async () => {
    h.apiFetch.mockRejectedValue(new TypeError("offline"))
    const { onSuccess } = renderWizard()
    toScopeStep()
    fireEvent.click(screen.getByRole("button", { name: /save secret/i }))
    expect(await screen.findByText(/network error while saving/i)).toBeInTheDocument()
    expect(onSuccess).not.toHaveBeenCalled()
  })
})
