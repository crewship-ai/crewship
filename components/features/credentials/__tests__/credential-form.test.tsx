// Tests for CredentialForm env-var-name validation — the credential
// name doubles as the ENV variable agents read, so newly typed names
// must match ^[A-Z_][A-Z0-9_]*$. Legacy (pre-existing) invalid names
// warn but stay submittable so old credentials don't become
// uneditable.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CredentialForm } from "../credential-form"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => h.apiFetch(...args),
}))

beforeEach(() => {
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => [] })
})

function renderForm(props: Partial<React.ComponentProps<typeof CredentialForm>> = {}) {
  const onSubmit = vi.fn().mockResolvedValue(null)
  render(
    <CredentialForm
      workspaceId="ws1"
      mode="create"
      hideValue
      onSubmit={onSubmit}
      onCancel={() => {}}
      {...props}
    />,
  )
  return { onSubmit }
}

const nameInput = () => screen.getByLabelText("Name") as HTMLInputElement
const submit = () => fireEvent.click(screen.getByRole("button", { name: /save/i }))

describe("create mode", () => {
  it("accepts a valid env var name", async () => {
    const { onSubmit } = renderForm()
    fireEvent.change(nameInput(), { target: { value: "STRIPE_API_KEY" } })
    submit()
    await waitFor(() => expect(onSubmit).toHaveBeenCalled())
    expect(onSubmit.mock.calls[0][0].name).toBe("STRIPE_API_KEY")
  })

  it("shows the inline error with a normalised suggestion after blur", () => {
    renderForm()
    fireEvent.change(nameInput(), { target: { value: "stripe api-key" } })
    // No premature nagging while the field is still focused…
    expect(screen.queryByText(/must be a valid env var name/i)).not.toBeInTheDocument()
    fireEvent.blur(nameInput())
    expect(screen.getByText(/must be a valid env var name/i)).toBeInTheDocument()

    // …and the one-click fix applies the normalised name.
    fireEvent.click(screen.getByRole("button", { name: "Use STRIPE_API_KEY" }))
    expect(nameInput().value).toBe("STRIPE_API_KEY")
    expect(screen.queryByText(/must be a valid env var name/i)).not.toBeInTheDocument()
  })

  it("blocks submit on an invalid name", async () => {
    const { onSubmit } = renderForm()
    fireEvent.change(nameInput(), { target: { value: "stripe key" } })
    submit()
    await screen.findAllByText(/must be a valid env var name/i)
    expect(onSubmit).not.toHaveBeenCalled()
  })
})

describe("edit mode with a legacy invalid name", () => {
  it("warns but still submits when the legacy name is left unchanged", async () => {
    const { onSubmit } = renderForm({
      mode: "edit",
      initial: { name: "my legacy key" },
    })
    // Warning is visible immediately (amber, non-blocking).
    expect(screen.getByText(/isn't a valid env var name/i)).toBeInTheDocument()
    submit()
    await waitFor(() => expect(onSubmit).toHaveBeenCalled())
    expect(onSubmit.mock.calls[0][0].name).toBe("my legacy key")
  })

  it("blocks submit when the name is changed to a different invalid value", async () => {
    const { onSubmit } = renderForm({
      mode: "edit",
      initial: { name: "my legacy key" },
    })
    fireEvent.change(nameInput(), { target: { value: "another bad name" } })
    submit()
    await screen.findAllByText(/must be a valid env var name/i)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("accepts changing a legacy name to a valid one", async () => {
    const { onSubmit } = renderForm({
      mode: "edit",
      initial: { name: "my legacy key" },
    })
    fireEvent.change(nameInput(), { target: { value: "MY_LEGACY_KEY" } })
    submit()
    await waitFor(() => expect(onSubmit).toHaveBeenCalled())
    expect(onSubmit.mock.calls[0][0].name).toBe("MY_LEGACY_KEY")
  })
})

// The "Test value" button was gated on BrandEntry.cli — the five brands
// Crewship drives inside agent containers. That is not the set of brands the
// server can probe: GITHUB, GITLAB and VERCEL have real upstream probes in
// credentials_test_endpoint.go and none is cli:true, so the button was hidden
// for three providers that would have answered.
//
// The flag is gone; the server now says so via
// GET /credentials/default-env-var?provider=…&type=… → { testable }.
describe("test-value button gating", () => {
  function mockTestable(testable: boolean) {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (typeof url === "string" && url.includes("default-env-var")) {
        return { ok: true, status: 200, json: async () => ({ env_var: "GH_TOKEN", testable }) }
      }
      return { ok: true, status: 200, json: async () => [] }
    })
  }

  it("offers Test for a provider the server can probe", async () => {
    mockTestable(true)
    renderForm({ hideValue: false, onTest: vi.fn().mockResolvedValue({ valid: true }) })
    fireEvent.change(nameInput(), { target: { value: "GH_TOKEN" } })
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "ghp_abc123" } })
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /test value/i })).toBeInTheDocument(),
    )
  })

  it("hides Test for a provider with no probe, so it can't render a placebo", async () => {
    mockTestable(false)
    renderForm({ hideValue: false, onTest: vi.fn().mockResolvedValue({ valid: true }) })
    fireEvent.change(nameInput(), { target: { value: "NOTION_TOKEN" } })
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "secret_abc123" } })
    await waitFor(() => expect(nameInput().value).toBe("NOTION_TOKEN"))
    expect(screen.queryByRole("button", { name: /test value/i })).not.toBeInTheDocument()
  })

  // provider === "NONE" is the initial/default state (no brand detected yet).
  // The effect must bail out before ever calling the server — if this
  // regressed, every fresh form would fire an avoidable request on mount.
  it("never calls the testable probe while no provider is detected", async () => {
    renderForm({ hideValue: false, onTest: vi.fn() })
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "some-random-value" } })
    // Give any (incorrect) effect a chance to fire before asserting it didn't.
    await new Promise((r) => setTimeout(r, 0))
    expect(h.apiFetch).not.toHaveBeenCalledWith(expect.stringContaining("default-env-var"))
    expect(screen.queryByRole("button", { name: /test value/i })).not.toBeInTheDocument()
  })

  // A non-ok HTTP response (server up, but erroring) must be treated the
  // same as "can't confirm testability" — hide the button rather than risk
  // showing a Test action the server can't actually service.
  it("hides Test when the default-env-var probe responds non-ok", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (typeof url === "string" && url.includes("default-env-var")) {
        return { ok: false, status: 500, json: async () => ({}) }
      }
      return { ok: true, status: 200, json: async () => [] }
    })
    renderForm({ hideValue: false, onTest: vi.fn() })
    fireEvent.change(nameInput(), { target: { value: "STRIPE_API_KEY" } })
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "sk_test_abc123" } })
    // Brand *was* detected (proves we're on the real STRIPE path, not the
    // NONE short-circuit above) — the icon renders regardless of testable.
    await waitFor(() => expect(screen.getByTitle(/detected: stripe/i)).toBeInTheDocument())
    expect(screen.queryByRole("button", { name: /test value/i })).not.toBeInTheDocument()
  })

  // A network-level failure (rejected fetch, not just a bad status) must be
  // swallowed the same way — an uncaught rejection here would crash the form.
  it("hides Test when the default-env-var probe's fetch rejects outright", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (typeof url === "string" && url.includes("default-env-var")) {
        throw new Error("network down")
      }
      return { ok: true, status: 200, json: async () => [] }
    })
    renderForm({ hideValue: false, onTest: vi.fn() })
    fireEvent.change(nameInput(), { target: { value: "STRIPE_API_KEY" } })
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "sk_test_abc123" } })
    await waitFor(() => expect(screen.getByTitle(/detected: stripe/i)).toBeInTheDocument())
    expect(screen.queryByRole("button", { name: /test value/i })).not.toBeInTheDocument()
  })

  // The effect's cleanup sets a local `cancelled` flag when the provider
  // changes again before the in-flight request resolves. If that guard
  // regressed, a slow/stale response could land after a newer one and flip
  // the button's visibility back to a wrong, out-of-date answer.
  it("ignores a stale probe response once the provider has changed again", async () => {
    const pending: Array<(v: unknown) => void> = []
    h.apiFetch.mockImplementation((url: string) => {
      if (typeof url === "string" && url.includes("default-env-var")) {
        return new Promise((resolve) => pending.push(resolve))
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderForm({ hideValue: false, onTest: vi.fn() })
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "some-secret-value" } })

    fireEvent.change(nameInput(), { target: { value: "STRIPE_API_KEY" } })
    await waitFor(() => expect(pending.length).toBe(1))

    fireEvent.change(nameInput(), { target: { value: "GH_TOKEN" } })
    await waitFor(() => expect(pending.length).toBe(2))

    // Resolve the *current* (GH_TOKEN) request first: testable.
    pending[1]({ ok: true, status: 200, json: async () => ({ testable: true }) })
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /test value/i })).toBeInTheDocument(),
    )

    // Now resolve the *stale* (STRIPE) request with the opposite answer.
    // Because its effect was already cleaned up, this must be a no-op.
    pending[0]({ ok: true, status: 200, json: async () => ({ testable: false }) })
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.getByRole("button", { name: /test value/i })).toBeInTheDocument()
  })
})

// Paste-first autodetection: dropping a recognisable secret shape into an
// empty Value field on a fresh (create-mode) form should pre-fill the Name
// and brand for the user, mirroring Doppler/1Password. If this regressed,
// pasting an Anthropic key would leave the user to type the whole env var
// name and pick the brand by hand.
describe("brand auto-detection from a pasted value", () => {
  it("fills name + brand from a recognised value prefix", async () => {
    renderForm({ hideValue: false })
    fireEvent.change(screen.getByLabelText("Value"), {
      target: { value: "sk-ant-abcdefghijklmnop" },
    })
    expect(nameInput().value).toBe("ANTHROPIC_API_KEY")
    expect(
      screen.getByRole("button", { name: /provider: anthropic/i }),
    ).toBeInTheDocument()
  })

  it("does not autofill once the user has already typed a name", async () => {
    renderForm({ hideValue: false })
    fireEvent.change(nameInput(), { target: { value: "MY_OWN_NAME" } })
    fireEvent.change(screen.getByLabelText("Value"), {
      target: { value: "sk-ant-abcdefghijklmnop" },
    })
    // The paste-first convenience only applies while the name is still
    // blank — respecting a name the user already committed to.
    expect(nameInput().value).toBe("MY_OWN_NAME")
  })

  it("does not autofill in edit mode", async () => {
    renderForm({ mode: "edit", hideValue: false, initial: { name: "" } })
    // Edit mode appends "(leave empty to keep existing)" to the Value
    // label, so match loosely rather than the exact create-mode string.
    fireEvent.change(screen.getByLabelText(/^value/i), {
      target: { value: "sk-ant-abcdefghijklmnop" },
    })
    expect(nameInput().value).toBe("")
  })
})

// The BrandPicker lets a user override auto-detection outright. Once they
// have, further name edits must not silently reassign the brand out from
// under them — that "latch" is what makes manual override durable enough
// to trust.
describe("manual brand override via BrandPicker", () => {
  it("latches the manually chosen brand against further name-driven detection", async () => {
    renderForm()
    fireEvent.click(screen.getByRole("button", { name: /provider: generic secret/i }))
    fireEvent.change(screen.getByPlaceholderText("Search brands…"), {
      target: { value: "notion" },
    })
    fireEvent.click(screen.getByTitle("Notion"))
    expect(
      screen.getByRole("button", { name: /provider: notion/i }),
    ).toBeInTheDocument()

    // Now type a name that would normally auto-detect a *different* brand.
    fireEvent.change(nameInput(), { target: { value: "GITHUB_TOKEN" } })
    expect(
      screen.getByRole("button", { name: /provider: notion/i }),
    ).toBeInTheDocument()
  })
})

// Value requiredness differs by mode: creating a credential with no value
// makes no sense (nothing to store), but editing must allow leaving it
// blank to mean "keep the existing value" — the placeholder literally says
// so. Conflating the two would either block harmless edits or let people
// create empty secrets.
describe("value requiredness by mode", () => {
  it("blocks create submit when the value is empty", async () => {
    const { onSubmit } = renderForm({ hideValue: false })
    fireEvent.change(nameInput(), { target: { value: "STRIPE_API_KEY" } })
    submit()
    await screen.findByText(/value is required/i)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("allows an empty value on edit (keeps the existing secret)", async () => {
    const { onSubmit } = renderForm({
      mode: "edit",
      hideValue: false,
      initial: { name: "STRIPE_API_KEY", value: "" },
    })
    submit()
    await waitFor(() => expect(onSubmit).toHaveBeenCalled())
    expect(onSubmit.mock.calls[0][0].value).toBe("")
  })
})

// Submit failure paths: the caller can either resolve with an error string
// (validated server-side rejection) or reject outright (network blip). Both
// must surface *some* message rather than fail silently and leave the user
// staring at a form that looks like it did nothing.
describe("submit failure handling", () => {
  it("renders the server's error string and stays on the form", async () => {
    const onSubmit = vi.fn().mockResolvedValue("That name is already in use")
    renderForm({ onSubmit })
    fireEvent.change(nameInput(), { target: { value: "STRIPE_API_KEY" } })
    submit()
    expect(await screen.findByText("That name is already in use")).toBeInTheDocument()
  })

  it("shows a generic network error when onSubmit rejects", async () => {
    const onSubmit = vi.fn().mockRejectedValue(new Error("boom"))
    renderForm({ onSubmit })
    fireEvent.change(nameInput(), { target: { value: "STRIPE_API_KEY" } })
    submit()
    expect(await screen.findByText(/network error/i)).toBeInTheDocument()
  })
})

// Tags are the primary post-grouping organisation tool, so their input
// quirks (comma/Enter to commit, backspace-to-pop, dedupe, the 8-tag cap)
// are all user-visible behaviour, not incidental.
describe("tags", () => {
  // The draft input's placeholder is cleared once a tag exists (so the
  // remaining space isn't wasted on hint text), which makes it unsafe to
  // re-query by placeholder after the first tag lands — grab the node once
  // per test and keep using that same reference.
  const getTagDraft = () => screen.getByPlaceholderText(/prod, billing, internal/i)

  it("adds a tag on Enter and clears the draft", () => {
    renderForm()
    const draft = getTagDraft()
    fireEvent.change(draft, { target: { value: "prod" } })
    fireEvent.keyDown(draft, { key: "Enter" })
    expect(screen.getByText("prod")).toBeInTheDocument()
    expect(draft).toHaveValue("")
  })

  it("adds a tag on comma", () => {
    renderForm()
    const draft = getTagDraft()
    fireEvent.change(draft, { target: { value: "billing" } })
    fireEvent.keyDown(draft, { key: "," })
    expect(screen.getByText("billing")).toBeInTheDocument()
  })

  it("commits a pending draft on blur", () => {
    renderForm()
    const draft = getTagDraft()
    fireEvent.change(draft, { target: { value: "internal" } })
    fireEvent.blur(draft)
    expect(screen.getByText("internal")).toBeInTheDocument()
  })

  it("does not add a duplicate or blank tag", () => {
    renderForm()
    const draft = getTagDraft()
    fireEvent.change(draft, { target: { value: "prod" } })
    fireEvent.keyDown(draft, { key: "Enter" })
    fireEvent.change(draft, { target: { value: "prod" } })
    fireEvent.keyDown(draft, { key: "Enter" })
    fireEvent.keyDown(draft, { key: "Enter" }) // blank draft, no-op
    expect(screen.getAllByText("prod")).toHaveLength(1)
  })

  it("caps tags at 8", () => {
    renderForm()
    const draft = getTagDraft()
    for (let i = 0; i < 9; i++) {
      fireEvent.change(draft, { target: { value: `tag${i}` } })
      fireEvent.keyDown(draft, { key: "Enter" })
    }
    expect(screen.queryByText("tag8")).not.toBeInTheDocument()
    for (let i = 0; i < 8; i++) {
      expect(screen.getByText(`tag${i}`)).toBeInTheDocument()
    }
  })

  it("removes the last tag on backspace when the draft is empty", () => {
    renderForm()
    const draft = getTagDraft()
    fireEvent.change(draft, { target: { value: "prod" } })
    fireEvent.keyDown(draft, { key: "Enter" })
    fireEvent.keyDown(draft, { key: "Backspace" })
    expect(screen.queryByText("prod")).not.toBeInTheDocument()
  })

  it("removes a tag via its remove button", () => {
    renderForm()
    const draft = getTagDraft()
    fireEvent.change(draft, { target: { value: "prod" } })
    fireEvent.keyDown(draft, { key: "Enter" })
    fireEvent.click(screen.getByRole("button", { name: /remove tag prod/i }))
    expect(screen.queryByText("prod")).not.toBeInTheDocument()
  })
})

// The Advanced section is collapsed by default to keep the primary form
// short; "More options" must disappear once it's open (no dead-end toggle)
// and description/expiry edits must actually reach the submitted payload.
describe("advanced section", () => {
  it("toggles open and hides the More options affordance", () => {
    renderForm()
    expect(screen.queryByLabelText("Description")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /more options/i }))
    expect(screen.getByLabelText("Description")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /more options/i })).not.toBeInTheDocument()
  })

  it("includes a typed description and expiry in the submitted payload", async () => {
    const { onSubmit } = renderForm()
    fireEvent.change(nameInput(), { target: { value: "STRIPE_API_KEY" } })
    fireEvent.click(screen.getByRole("button", { name: /more options/i }))
    fireEvent.change(screen.getByLabelText("Description"), {
      target: { value: "  used for billing sync  " },
    })
    fireEvent.change(screen.getByLabelText("Expires on"), { target: { value: "2027-01-01" } })
    submit()
    await waitFor(() => expect(onSubmit).toHaveBeenCalled())
    expect(onSubmit.mock.calls[0][0].description).toBe("used for billing sync")
    expect(onSubmit.mock.calls[0][0].expiresAt).toBe("2027-01-01")
  })
})

// Crew scoping: picking "Specific crews only" without actually selecting a
// crew is a dead-end the user could easily fall into, so it's blocked with
// an actionable message. Removing a crew badge and switching back to
// workspace scope both need to actually clear crewIds, not just hide the UI.
describe("crew scope", () => {
  function openScopeSelect() {
    const trigger = screen.getByLabelText("Visible to")
    fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerId: 1 })
    fireEvent.pointerUp(trigger, { button: 0, pointerId: 1 })
    fireEvent.click(trigger)
  }

  beforeEach(() => {
    Element.prototype.hasPointerCapture = vi.fn(() => false)
    Element.prototype.scrollIntoView = vi.fn()
  })

  it("blocks submit when CREW scope has no crews selected", async () => {
    const { onSubmit } = renderForm()
    fireEvent.change(nameInput(), { target: { value: "STRIPE_API_KEY" } })
    fireEvent.click(screen.getByRole("button", { name: /more options/i }))
    openScopeSelect()
    fireEvent.click(await screen.findByRole("option", { name: /specific crews only/i }))
    submit()
    await screen.findByText(/pick at least one crew/i)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("renders selected crew badges and removes one on click", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (typeof url === "string" && url.includes("/api/v1/crews")) {
        return {
          ok: true,
          status: 200,
          json: async () => [
            { id: "c1", name: "Alpha" },
            { id: "c2", name: "Bravo" },
          ],
        }
      }
      return { ok: true, status: 200, json: async () => [] }
    })
    renderForm({
      initial: { name: "STRIPE_API_KEY", scope: "CREW", crewIds: ["c1", "c2"] },
    })
    fireEvent.click(screen.getByRole("button", { name: /more options/i }))
    await waitFor(() => expect(screen.getByText("Alpha")).toBeInTheDocument())
    expect(screen.getByText("Bravo")).toBeInTheDocument()

    fireEvent.click(screen.getByText("Alpha"))
    expect(screen.queryByText("Alpha")).not.toBeInTheDocument()
    expect(screen.getByText("Bravo")).toBeInTheDocument()
  })

  it("clears crewIds when switching scope back to Workspace", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (typeof url === "string" && url.includes("/api/v1/crews")) {
        return {
          ok: true,
          status: 200,
          json: async () => [{ id: "c1", name: "Alpha" }],
        }
      }
      return { ok: true, status: 200, json: async () => [] }
    })
    renderForm({
      initial: { name: "STRIPE_API_KEY", scope: "CREW", crewIds: ["c1"] },
    })
    fireEvent.click(screen.getByRole("button", { name: /more options/i }))
    await waitFor(() => expect(screen.getByText("Alpha")).toBeInTheDocument())

    openScopeSelect()
    fireEvent.click(await screen.findByRole("option", { name: /whole workspace/i }))

    // Scope flipping back to CREW again should show no badges left — the
    // WORKSPACE transition really cleared crewIds, it didn't just hide them.
    openScopeSelect()
    fireEvent.click(await screen.findByRole("option", { name: /specific crews only/i }))
    expect(screen.queryByText("Alpha")).not.toBeInTheDocument()
    expect(screen.getByText(/select crews/i)).toBeInTheDocument()
  })

  it("shows a loading state while the crew list is being fetched", async () => {
    // Deliberately never resolve: this test only cares about the
    // in-flight state. Resolving to an empty array here would trigger the
    // effect's re-fetch loop documented below (crews.length stays 0
    // forever), which would make this test flaky rather than prove
    // anything about the loading indicator.
    h.apiFetch.mockImplementation((url: string) => {
      if (typeof url === "string" && url.includes("/api/v1/crews")) {
        return new Promise(() => {})
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderForm({ initial: { name: "STRIPE_API_KEY", scope: "CREW" } })
    fireEvent.click(screen.getByRole("button", { name: /more options/i }))
    expect(await screen.findByText(/loading crews/i)).toBeInTheDocument()
  })

  // The workspace crews endpoint is trusted to return an array, but a
  // malformed/non-array body must fall back to an empty list rather than
  // crash `.map`/`.filter` downstream.
  //
  // NOTE (production bug, not fixed here per task constraints): because
  // the fetch effect's deps include `crews.length` and `crewsLoading`,
  // getting back an empty/malformed list means crews.length never leaves
  // 0 and the effect re-fires forever — the workspace's crews endpoint
  // gets hammered continuously for as long as this panel stays mounted
  // with CREW scope and zero real crews. We only assert the single
  // request that proves the non-array body didn't crash the render; we
  // do not wait for the UI to "settle" because it never does.
  it("does not crash when the server returns a non-array crews body", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (typeof url === "string" && url.includes("/api/v1/crews")) {
        return { ok: true, status: 200, json: async () => ({ not: "an array" }) }
      }
      return { ok: true, status: 200, json: async () => [] }
    })
    renderForm({ initial: { name: "STRIPE_API_KEY", scope: "CREW" } })
    fireEvent.click(screen.getByRole("button", { name: /more options/i }))
    await waitFor(() =>
      expect(h.apiFetch).toHaveBeenCalledWith(expect.stringContaining("/api/v1/crews")),
    )
  })

  // Same re-fetch-loop caveat as above — only asserting the catch path ran
  // without throwing, not that the UI reaches a stable state.
  it("does not crash when the crews fetch rejects", async () => {
    h.apiFetch.mockImplementation((url: string) => {
      if (typeof url === "string" && url.includes("/api/v1/crews")) {
        return Promise.reject(new Error("network down"))
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => [] })
    })
    renderForm({ initial: { name: "STRIPE_API_KEY", scope: "CREW" } })
    fireEvent.click(screen.getByRole("button", { name: /more options/i }))
    await waitFor(() =>
      expect(h.apiFetch).toHaveBeenCalledWith(expect.stringContaining("/api/v1/crews")),
    )
  })
})

// An unsalvageable typed name (nothing left after stripping invalid
// characters) has no suggestion to offer — the UI must fall back to the
// plain instruction instead of rendering a broken/empty "Use " button.
describe("name with no salvageable suggestion", () => {
  it("shows the plain instruction with no fix-it button (create mode)", () => {
    renderForm()
    fireEvent.change(nameInput(), { target: { value: "!!!" } })
    fireEvent.blur(nameInput())
    expect(screen.getByText(/must be a valid env var name/i)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /^use /i })).not.toBeInTheDocument()
  })

  it("shows the plain legacy warning with no rename link (edit mode)", () => {
    renderForm({ mode: "edit", initial: { name: "!!!" } })
    expect(screen.getByText(/isn't a valid env var name/i)).toBeInTheDocument()
    expect(screen.getByText(/or rename it\./i)).toBeInTheDocument()
  })

  // The legacy warning's inline rename link is a real one-click fix, not
  // just a suggestion string — clicking it must actually apply the name.
  it("clicking the legacy warning's rename link applies the suggested name", () => {
    renderForm({ mode: "edit", initial: { name: "my legacy key" } })
    fireEvent.click(screen.getByRole("button", { name: "MY_LEGACY_KEY" }))
    expect(nameInput().value).toBe("MY_LEGACY_KEY")
  })
})

// Submitting a completely blank form is the most basic guard: it must
// short-circuit before any of the env-var-name / value / scope checks run.
// Note: the Name input also carries the native HTML `required` attribute,
// so a real click on the Save button never reaches our handler at all —
// the browser's own constraint validation blocks it first. We dispatch
// the form's submit event directly to exercise our own defensive check
// underneath that native guard (defense in depth: JS still validates in
// case `required` is ever bypassed, e.g. programmatic submission).
describe("blank name on submit", () => {
  it("blocks submit with 'Name is required' and calls nothing", async () => {
    const { onSubmit } = renderForm()
    const form = document.querySelector("form") as HTMLFormElement
    fireEvent.submit(form)
    await screen.findByText(/name is required/i)
    expect(onSubmit).not.toHaveBeenCalled()
  })
})

// The value show/hide toggle is the only way to visually confirm what you
// pasted before saving — if it regressed, users would submit typos blind.
describe("value visibility toggle", () => {
  it("toggles the value input between password and text", () => {
    renderForm({ hideValue: false })
    const valueInput = screen.getByLabelText("Value") as HTMLInputElement
    expect(valueInput.type).toBe("password")
    fireEvent.click(screen.getByRole("button", { name: /show value/i }))
    expect(valueInput.type).toBe("text")
    fireEvent.click(screen.getByRole("button", { name: /hide value/i }))
    expect(valueInput.type).toBe("password")
  })
})

// The "Test value" action is a real network probe, not just a gating flag —
// clicking it must call onTest with the current form values and render
// both a pass and a fail outcome so the user knows the secret actually works.
describe("clicking Test value", () => {
  function mockTestable() {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (typeof url === "string" && url.includes("default-env-var")) {
        return { ok: true, status: 200, json: async () => ({ testable: true }) }
      }
      return { ok: true, status: 200, json: async () => [] }
    })
  }

  it("shows Valid when onTest resolves valid: true", async () => {
    mockTestable()
    const onTest = vi.fn().mockResolvedValue({ valid: true })
    renderForm({ hideValue: false, onTest })
    fireEvent.change(nameInput(), { target: { value: "GH_TOKEN" } })
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "ghp_abc123" } })
    fireEvent.click(await screen.findByRole("button", { name: /test value/i }))
    expect(await screen.findByText("Valid")).toBeInTheDocument()
    expect(onTest).toHaveBeenCalledWith(expect.objectContaining({ name: "GH_TOKEN" }))
  })

  it("shows the server's error message when onTest resolves valid: false", async () => {
    mockTestable()
    const onTest = vi.fn().mockResolvedValue({ valid: false, error: "Token expired" })
    renderForm({ hideValue: false, onTest })
    fireEvent.change(nameInput(), { target: { value: "GH_TOKEN" } })
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "ghp_abc123" } })
    fireEvent.click(await screen.findByRole("button", { name: /test value/i }))
    expect(await screen.findByText("Token expired")).toBeInTheDocument()
  })
})

// The "Advanced" toggle button (distinct from the "More options" shortcut
// in the footer) is the primary way to collapse the section back down.
describe("Advanced toggle button", () => {
  it("opens and closes the advanced section", () => {
    renderForm()
    fireEvent.click(screen.getByRole("button", { name: /^advanced/i }))
    expect(screen.getByLabelText("Description")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /^advanced/i }))
    expect(screen.queryByLabelText("Description")).not.toBeInTheDocument()
  })
})

// knownTags drives the tag autocomplete <datalist>; already-applied tags
// must be filtered out of the suggestion list so the user isn't prompted
// to re-add a tag they already have.
describe("known tag suggestions", () => {
  it("filters out already-applied tags from the datalist", () => {
    renderForm({ knownTags: ["prod", "staging", "billing"] })
    const datalist = document.getElementById("cred-tag-suggestions") as HTMLDataListElement
    expect(datalist.querySelectorAll("option")).toHaveLength(3)

    const draft = screen.getByPlaceholderText(/prod, billing, internal/i)
    fireEvent.change(draft, { target: { value: "prod" } })
    fireEvent.keyDown(draft, { key: "Enter" })

    expect(datalist.querySelectorAll("option")).toHaveLength(2)
    expect(
      Array.from(datalist.querySelectorAll("option")).map((o) => (o as HTMLOptionElement).value),
    ).toEqual(["staging", "billing"])
  })
})

// Toggling a crew from the picker list is the actual multi-select
// interaction, distinct from removing one via its badge — both paths must
// mutate crewIds correctly (add when unselected, remove when selected).
describe("crew picker list selection", () => {
  beforeEach(() => {
    Element.prototype.hasPointerCapture = vi.fn(() => false)
    Element.prototype.scrollIntoView = vi.fn()
  })

  it("selects a crew from the list and toggles it back off", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (typeof url === "string" && url.includes("/api/v1/crews")) {
        return {
          ok: true,
          status: 200,
          json: async () => [
            { id: "c1", name: "Alpha" },
            { id: "c2", name: "Bravo" },
          ],
        }
      }
      return { ok: true, status: 200, json: async () => [] }
    })
    renderForm({ initial: { name: "STRIPE_API_KEY", scope: "CREW" } })
    fireEvent.click(screen.getByRole("button", { name: /more options/i }))
    // role="combobox" doesn't support "name from contents" per the ARIA
    // accessible-name spec, and this trigger has no <label for> / aria-label
    // pairing it to "Crews" — so it must be located by its visible text,
    // not a role+name query.
    fireEvent.click(await screen.findByText(/select crews/i))
    const option = await screen.findByRole("option", { name: "Alpha" })
    fireEvent.click(option)

    // cmdk's own `aria-selected` tracks keyboard-highlight state, not our
    // checkmark — so assert on what the user actually sees change: the
    // trigger's summary label and the badge list below it.
    expect(await screen.findByText(/1 crew selected/i)).toBeInTheDocument()

    fireEvent.click(option)
    expect(await screen.findByText(/select crews…/i)).toBeInTheDocument()
  })
})

// The crews effect guards on `crews.length === 0 && !crewsLoading` and lists
// both in its deps. A workspace with no crews never leaves length 0, so when
// the request settles and `finally` clears crewsLoading, the guard is satisfied
// again and the effect fires a second time. It converges rather than looping —
// measured, not inferred — but the extra request is pure waste, and it lands on
// exactly the workspaces least able to absorb it: brand-new ones, mid-onboarding,
// against a 120/min limiter.
describe("crews fetch", () => {
  async function settle() {
    for (let i = 0; i < 40; i++) await new Promise((r) => setTimeout(r, 5))
  }

  it("requests the crew list once when the workspace has no crews", async () => {
    h.apiFetch.mockImplementation(async (url: unknown) =>
      String(url).includes("/crews")
        ? { ok: true, status: 200, json: async () => [] }
        : { ok: true, status: 200, json: async () => ({ env_var: "", testable: false }) },
    )
    renderForm({ initial: { scope: "CREW" } })
    await settle()
    expect(h.apiFetch.mock.calls.filter((c) => String(c[0]).includes("/crews"))).toHaveLength(1)
  })

  it("does not retry on failure either — a down endpoint must not become a poll", async () => {
    h.apiFetch.mockImplementation(async (url: unknown) => {
      if (String(url).includes("/crews")) throw new Error("network down")
      return { ok: true, status: 200, json: async () => ({ env_var: "", testable: false }) }
    })
    renderForm({ initial: { scope: "CREW" } })
    await settle()
    expect(h.apiFetch.mock.calls.filter((c) => String(c[0]).includes("/crews"))).toHaveLength(1)
  })
})

// The brand IS the icon — it is what the rail, the dashboard and the
// credential's own page draw for this credential. The picker sat here
// unlabelled, opposite the Name label, which made the one control that changes
// a credential's face read as a read-only badge.
describe("the icon picker", () => {
  it("is labelled, so it reads as something you can change", () => {
    renderForm()
    expect(screen.getByText("Icon")).toBeInTheDocument()
  })
})
