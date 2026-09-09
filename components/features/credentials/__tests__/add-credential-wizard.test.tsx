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
import { LOGIN_PROVIDERS } from "@/lib/credentials/login-providers"
import { providerConnectionGuide } from "@/lib/credentials/provider-connection-guides"

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
  it("offers six secret types, with provider onboarding in its own flow", () => {
    renderWizard()
    expect(screen.queryByRole("button", { name: /^provider login/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("group", { name: /choose.*provider/i })).not.toBeInTheDocument()
    for (const label of ["Token", "Login", "Key pair", "SSH key", "File", "Certificate"]) {
      // Anchored: "Login" must not also match the "Provider login" tile.
      expect(screen.getByRole("button", { name: new RegExp("^" + label, "i") })).toBeInTheDocument()
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
    expect(screen.getByRole("button", { name: /type/i })).toHaveAttribute("aria-current", "step")
  })

  it("walks back to a finished step but will not skip ahead to an unfinished one", () => {
    renderWizard()
    pickShape(/^login/i)
    expect(screen.getByRole("button", { name: /details/i })).toHaveAttribute("aria-current", "step")
    // Step 3 needs a password and a name first; offering it would be a link
    // to a form that cannot be submitted.
    expect(screen.getByRole("button", { name: /use/i })).toBeDisabled()

    fireEvent.click(screen.getByRole("button", { name: /type/i }))
    expect(screen.getByRole("button", { name: /^login/i })).toBeInTheDocument()
  })

  // The three steps still have to fit at 390px, and CreateSurfaceSteps solves
  // that differently from the step bar this wizard used to own: instead of
  // muting the labels you are not on (sr-only below sm), the chip row is
  // hidden outright on a phone and replaced by the current label plus a
  // progress bar. The accessible names of the three step buttons are unchanged
  // either way, which is what the two tests above pin.
  it("collapses to one label and a progress bar at 390px", () => {
    renderWizard()
    const bar = screen.getByRole("progressbar")
    expect(bar).toHaveAttribute("aria-valuenow", "1")
    expect(bar).toHaveAttribute("aria-valuemax", "3")
    // The chip row is what a pointer device gets; it does not take width on a
    // phone, and it is not what the phone reads.
    const chips = screen.getByRole("button", { name: /type/i }).parentElement!
    expect(chips.className).toContain("max-sm:hidden")
  })
})

// 390×844. The dialog is the whole screen there, so the three things that
// decide whether it is usable are: the tiles reflow, the body scrolls without
// taking the actions with it, and the actions are big enough to hit.
describe("layout on a phone", () => {
  it("reflows the shapes two-up, and three-up once there is room", () => {
    renderWizard()
    const grid = screen.getByTestId("shape-grid")
    expect(grid.className).toContain("grid-cols-2")
    expect(grid.className).toContain("sm:grid-cols-3")
  })

  it("docks the actions in a footer the scrolling body cannot carry off-screen", () => {
    renderWizard()
    pickShape(/^token/i)
    const body = screen.getByTestId("wizard-body")
    const footer = screen.getByTestId("wizard-footer")
    expect(body.className).toContain("overflow-y-auto")
    expect(body.contains(footer)).toBe(false)
    expect(within(footer).getByRole("button", { name: /^continue$/i })).toBeInTheDocument()
    expect(within(footer).getByRole("button", { name: /^cancel$/i })).toBeInTheDocument()
  })

  // h-12, not h-11. This project sets `--spacing: 0.23rem` (globals.css), so
  // the whole scale is 92% of what its name suggests and `h-11` is 40.5px —
  // 8% short of the 44px every platform HIG asks for, and short in a way
  // nobody notices because 40px still looks fine. The shell's footer is the
  // one place that number is now decided; this surface used to be 40.5px.
  it("gives the footer buttons a thumb-sized target and the width to share", () => {
    renderWizard()
    pickShape(/^token/i)
    const cont = screen.getByRole("button", { name: /^continue$/i })
    expect(cont.className).toContain("max-sm:h-12")
    expect(cont.className).toContain("max-sm:flex-[2]")
    expect(screen.getByRole("button", { name: /^cancel$/i }).className).toContain("max-sm:flex-1")
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
describe("the brand icon belongs beside the name", () => {
  it("starts without a selected type or an unnecessary Continue", () => {
    renderWizard()
    expect(screen.queryByRole("button", {name: /provider: generic secret/i})).not.toBeInTheDocument()
    expect(screen.queryByRole("button", {name: /^continue$/i})).not.toBeInTheDocument()
    expect(screen.getByRole("button", {name: /^Token/})).toHaveAttribute("aria-pressed", "false")
    pickShape(/^token/i)
    expect(screen.getByRole("button", {name: /provider: generic secret/i})).toBeInTheDocument()
  })
})

describe("provider login (#2428)", () => {
  function renderWizard() {
    const onSuccess = vi.fn()
    render(<AddCredentialWizard workspaceId="ws1" initial={{ itemType: "PROVIDER_LOGIN" }} onSuccess={onSuccess} onCancel={() => {}} />)
    return { onSuccess }
  }

  function pickShape(_label: RegExp) {
    // Provider selection advances directly; retained for older scenarios.
  }

  it.each(LOGIN_PROVIDERS)("prepares $key with its own key instructions and account name", (p) => {
    renderWizard()
    fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${p.label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`) }))
    if (p.key === "OPENAI") fireEvent.click(screen.getByRole("button", { name: /import from codex cli/i }))
    if (p.key === "OPENAI" || p.key === "ANTHROPIC") fireEvent.click(screen.getByRole("button", { name: /^api key/i }))
    expect(screen.queryByLabelText(/^provider$/i)).not.toBeInTheDocument()
    expect(screen.getByLabelText(/^name$/i)).toHaveValue(p.label)
    expect(screen.getByRole("link", { name: /get api key/i })).toHaveAttribute("href", providerConnectionGuide(p.key)!.url)
    expect(screen.getByText(providerConnectionGuide(p.key)!.instruction)).toBeInTheDocument()
    expect(screen.getByLabelText(/^API key$/i)).toHaveValue("")
  })

  it("keeps a draft when returning to the same provider, but clears the key for another", () => {
    renderWizard()
    fireEvent.click(screen.getByRole("button", { name: /^Grok \/ xAI/ }))
    fireEvent.change(screen.getByLabelText(/^API key$/), { target: { value: "fixture-key" } })
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "My production account" } })
    fireEvent.click(screen.getByRole("button", { name: /^back$/i }))
    fireEvent.click(screen.getByRole("button", { name: /^Grok \/ xAI/ }))
    expect(screen.getByLabelText(/^API key$/)).toHaveValue("fixture-key")
    fireEvent.click(screen.getByRole("button", { name: /^back$/i }))
    fireEvent.click(screen.getByRole("button", { name: /^Groq / }))
    fireEvent.click(screen.getByRole("button", { name: /change and clear value/i }))
    expect(screen.getByLabelText(/^API key$/)).toHaveValue("")
    expect(screen.getByLabelText(/^name$/i)).toHaveValue("My production account")
  })

  it("starts directly with a provider, without using a decorative brand picker", () => {
    renderWizard()
    fireEvent.click(screen.getByRole("button", { name: /^ChatGPT \/ OpenAI/i }))
    expect(screen.queryByLabelText(/^provider$/i)).not.toBeInTheDocument()
    expect(screen.getByText("Connect ChatGPT / OpenAI")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /sign in with a code/i })).toHaveAttribute("aria-pressed", "true")
    expect(screen.queryByRole("button", { name: /provider: openai/i })).not.toBeInTheDocument()
  })

  it("gives Gemini its own file import and clears the token when switching to Grok", () => {
    renderWizard()
    fireEvent.click(screen.getByRole("button", { name: /^Gemini \/ Google/i }))
    expect(screen.getByLabelText(/^API key$/i)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /^Import Gemini login/i }))
    fireEvent.change(screen.getByLabelText(/Gemini login/), { target: { value: "old-google-token" } })
    fireEvent.click(screen.getByRole("button", { name: /^back$/i }))
    fireEvent.click(screen.getByRole("button", { name: /^Grok \/ xAI/i }))
    fireEvent.click(screen.getByRole("button", { name: /change and clear value/i }))
    expect(screen.getByLabelText(/^API key$/i)).toHaveValue("")
    expect(screen.queryByRole("button", { name: /^Subscription/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Sign in with a code/i })).not.toBeInTheDocument()
    expect(screen.getByText(/Uses Grok through OpenCode/)).toBeInTheDocument()
  })

  it("saves a Grok account with the xAI provider and API-key mode", async () => {
    const { onSuccess } = renderWizard()
    fireEvent.click(screen.getByRole("button", { name: /^Grok \/ xAI/i }))
    fireEvent.change(screen.getByLabelText(/^API key$/i), { target: { value: "fixture-xai-key" } })
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "Grok account" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    expect(screen.queryByRole("group", { name: "How closely Keeper guards it" })).not.toBeInTheDocument()
    expect(screen.getByText("How will you use it?")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /save provider|finish setup/i }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const createCall = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials?"))!
    expect(bodyOf(createCall)).toMatchObject({ provider: "XAI", type: "PROVIDER_LOGIN", mode: "api_key", value: "fixture-xai-key" })
  })

  function pickOpenAI() {
    fireEvent.click(screen.getByRole("button", { name: /^ChatGPT \/ OpenAI/i }))
    fireEvent.click(screen.getByRole("button", { name: /import from codex cli/i }))
  }

  it("will not continue without a provider — the server routes by it", () => {
    renderWizard()
    pickShape(/provider login/i)
    expect(screen.queryByRole("button", { name: /^continue$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /save secret|save & assign/i })).not.toBeInTheDocument()
  })

  it("stores a ChatGPT login as PROVIDER_LOGIN · OPENAI · subscription and asks for the whole auth.json", async () => {
    const { onSuccess } = renderWizard()
    pickShape(/provider login/i)
    pickOpenAI()
    const box = screen.getByLabelText(/codex login \(auth\.json\)/i)
    expect(box.tagName).toBe("TEXTAREA")
    expect(screen.getByText(/paste the contents of ~\/\.codex\/auth\.json/i)).toBeInTheDocument()
    fireEvent.change(box, { target: { value: '{"tokens":{"id_token":"i","access_token":"a","refresh_token":"r","account_id":"x"}}' } })
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "ChatGPT Plus · jana" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.click(screen.getByRole("button", { name: /save provider|finish setup/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const createCall = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials?"))!
    // Contract §10.2: the type is PROVIDER_LOGIN, the mode is its own field,
    // the value is exactly what was pasted — the server splits it into parts.
    expect(bodyOf(createCall)).toMatchObject({ type: "PROVIDER_LOGIN", provider: "OPENAI", mode: "subscription" })
    expect(bodyOf(createCall).value).toContain('"refresh_token":"r"')
  })

  it("stores the same seat as PROVIDER_LOGIN · api_key when the operator picks a metered key", async () => {
    const { onSuccess } = renderWizard()
    pickShape(/provider login/i)
    pickOpenAI()
    fireEvent.click(screen.getByRole("button", { name: /^api key/i }))
    fireEvent.change(screen.getByLabelText(/^api key$/i), { target: { value: "sk-proj-abc" } })
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "OpenAI API" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.click(screen.getByRole("button", { name: /save provider|finish setup/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const createCall = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials?"))!
    expect(bodyOf(createCall)).toMatchObject({ type: "PROVIDER_LOGIN", provider: "OPENAI", mode: "api_key" })
  })

  it("offers a sign-in choice only where a device flow exists — OpenAI subscription, not Anthropic, not a key", () => {
    renderWizard()
    pickShape(/provider login/i)
    pickOpenAI()
    expect(screen.getByRole("button", { name: /import from codex cli/i })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByRole("button", { name: /sign in with a code/i })).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: /^api key/i }))
    expect(screen.getByRole("button", { name: /sign in with a code/i })).toHaveAttribute("aria-pressed", "false")
  })

  it("keeps the setup-token paste for Anthropic — there is no device flow to offer", () => {
    renderWizard()
    pickShape(/provider login/i)
    fireEvent.click(screen.getByRole("button", { name: /^Claude \/ Anthropic/i }))
    expect(screen.getByLabelText(/^setup token$/i)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /sign in with a code/i })).not.toBeInTheDocument()
  })

  it.each(["WORKSPACE", "CREW"])("device sign-in persists %s access without a second create", async (scope) => {
    // Starting the flow answers with a code; the first poll says pending, the
    // second says the login exists. The wizard then has a credential id and
    // must not POST a second row — only name it and bind it.
    h.apiFetch.mockImplementation(async (url: unknown, init?: { method?: string }) => {
      const u = String(url)
      if (u.startsWith("/api/v1/provider-logins/device?") && init?.method === "POST") {
        return ok({ device_id: "dev_1", user_code: "ABCD-EFGH", verification_url: "https://auth.openai.com/device", expires_at: new Date(Date.now() + 600_000).toISOString(), interval_s: 1 })
      }
      if (u.startsWith("/api/v1/provider-logins/device/dev_1")) {
        polls.count += 1
        return ok(polls.count < 2 ? { status: "pending" } : { status: "complete", credential_id: "cred_dev" })
      }
      if (u.startsWith("/api/v1/credentials/cred_dev?") && init?.method === "PATCH") return ok({ id: "cred_dev" })
      if (u.startsWith("/api/v1/crews?")) return ok([{ id: "crew-one", name: "Engineering" }])
      if (u.startsWith("/api/v1/credentials/bindings")) return ok({ id: "b1" }, 201)
      if (u.startsWith("/api/v1/workspaces/")) return ok([])
      return ok({ id: "cred_new" }, 201)
    })
    const polls = { count: 0 }
    const onSuccess = vi.fn()
    render(<AddCredentialWizard workspaceId="ws1" initial={{ itemType: "PROVIDER_LOGIN" }} onSuccess={onSuccess} onCancel={() => {}} devicePollMs={5} />)
    pickShape(/provider login/i)
    pickOpenAI()
    fireEvent.click(screen.getByRole("button", { name: /sign in with a code/i }))

    // The code, large and copyable, and the page to open.
    expect(await screen.findByTestId("device-user-code")).toHaveTextContent("ABCD-EFGH")
    expect(screen.getByRole("link", { name: /open auth\.openai\.com/i })).toHaveAttribute("href", "https://auth.openai.com/device")
    // No paste box while a code is live — the value never comes through here.
    expect(screen.queryByLabelText(/codex login \(auth\.json\)/i)).not.toBeInTheDocument()
    // The step is held until the sign-in lands.
    expect(screen.getByRole("button", { name: /^continue$/i })).toBeDisabled()

    await screen.findByText("Signed in. Continue to choose access for this account.")
    const starts = () => h.apiFetch.mock.calls.filter(([url, init]) => String(url).startsWith("/api/v1/provider-logins/device?") && init?.method === "POST").length
    const startsBeforeBack = starts()
    expect(screen.getByRole("button", { name: /^back$/i })).toBeDisabled()
    expect(starts()).toBe(startsBeforeBack)
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "ChatGPT Plus · jana" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    if (scope === "CREW") {
      fireEvent.click(screen.getByRole("button", { name: /assign now/i }))
      fireEvent.click(screen.getByRole("button", { name: /selected crews/i }))
      fireEvent.click(screen.getByRole("combobox"))
      fireEvent.click(await screen.findByRole("option", { name: "Engineering" }))
      fireEvent.keyDown(screen.getByPlaceholderText("Search crews…"), { key: "Escape" })
    }
    fireEvent.click(screen.getByRole("button", { name: /save provider|finish setup/i }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())

    const creates = h.apiFetch.mock.calls.filter(([url, init]) => String(url).startsWith("/api/v1/credentials?") && (init as { method?: string })?.method === "POST")
    expect(creates).toHaveLength(0)
    const patch = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials/cred_dev?"))!
    expect(bodyOf(patch)).toMatchObject({ name: "ChatGPT Plus · jana", scope: "WORKSPACE" })
    const bind = h.apiFetch.mock.calls.find(([url, init]) => String(url).startsWith("/api/v1/credentials/bindings") && init?.method === "POST")!
    if (scope === "CREW") {
      expect(bodyOf(patch).crew_ids).toBeUndefined()
      expect(bodyOf(bind)).toMatchObject({ credential_id: "cred_dev", scope: "CREW", crew_id: "crew-one" })
    } else {
      expect(bind).toBeUndefined()
    }
  })

  it("names the owner: the members list, defaulting to the person signed in", async () => {
    h.apiFetch.mockImplementation(async (url: unknown) => {
      if (String(url).startsWith("/api/v1/workspaces/ws1/members")) {
        return ok([
          { id: "m1", role: "OWNER", user: { id: "u_pavel", email: "pavel@unify.cz", full_name: null } },
          { id: "m2", role: "MEMBER", user: { id: "u_jana", email: "jana@unify.cz", full_name: null } },
        ])
      }
      return ok({ id: "cred_new" }, 201)
    })
    const { onSuccess } = renderWizard()
    pickShape(/provider login/i)
    pickOpenAI()
    // A text box naming the signed-in person until the members arrive, then
    // a select over them.
    await waitFor(() => expect(screen.getByLabelText(/^owner$/i).tagName).toBe("SELECT"))
    fireEvent.change(screen.getByLabelText(/^owner$/i), { target: { value: "u_jana" } })
    fireEvent.change(screen.getByLabelText(/codex login \(auth\.json\)/i), { target: { value: '{"tokens":{"access_token":"fixture-access","id_token":"fixture-id","account_id":"fixture-account"}}' } })
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "ChatGPT Plus · jana" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.click(screen.getByRole("button", { name: /save provider|finish setup/i }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const createCall = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials?"))!
    expect(bodyOf(createCall)).toMatchObject({ owner_user_id: "u_jana" })
  })

  it("Re-login opens on the sign-in step with the seat's provider and mode chosen", () => {
    render(
      <AddCredentialWizard
        workspaceId="ws1"
        onSuccess={() => {}}
        onCancel={() => {}}
        initial={{ itemType: "PROVIDER_LOGIN", provider: "OPENAI", loginMode: "subscription", signIn: "device", step: "values", name: "ChatGPT Plus · jana" }}
      />,
    )
    expect(screen.getByRole("button", { name: /connect/i })).toHaveAttribute("aria-current", "step")
    expect(screen.getByRole("button", { name: /sign in with a code/i })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByLabelText(/^name$/i)).toHaveValue("ChatGPT Plus · jana")
    expect(screen.getByTestId("device-sign-in")).toBeInTheDocument()
    expect(screen.queryByLabelText(/^owner$/i)).not.toBeInTheDocument()
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
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "internal-thing" } })
    expect(screen.getByRole("button", { name: /^continue$/i })).not.toBeDisabled()
  })

  it("also detects the brand from the name when the value shape says nothing", () => {
    renderWizard()
    pickShape(/^token/i)
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "opaque-value" } })
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "gitlab-ci" } })
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
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "aws-prod" } })
    // access_key_id is required and still empty.
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    expect(screen.getByRole("button", { name: /details/i })).toHaveAttribute("aria-current", "step")
    // …and names the part that is holding it up, rather than a dead button.
    expect(screen.getByText(/is still empty/i)).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText(/access key id/i), { target: { value: "AKIA1" } })
    expect(screen.getByRole("button", { name: /^continue$/i })).not.toBeDisabled()
  })

  it("will not continue without a name", () => {
    renderWizard()
    pickShape(/^token/i)
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "abc123" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    expect(screen.getByRole("button", { name: /details/i })).toHaveAttribute("aria-current", "step")
  })
})

/** Fill a token credential and land on step 3. */
function toScopeStep(name = "github-acme", value = "github_pat_11ABCDE") {
  pickShape(/^token/i)
  fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value } })
  fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: name } })
  fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
}

describe("step 3 — scope and slot", () => {
  it("prefills the slot from the detected brand but lets the user overwrite it", async () => {
    const { onSuccess } = renderWizard()
    toScopeStep()
    fireEvent.click(screen.getByRole("button", {name: /assign now/i}))
    fireEvent.click(screen.getByRole("button", {name: /all agents/i}))
    const slot = screen.getByLabelText(/variable name/i) as HTMLInputElement
    expect(slot.value).toBe("GH_TOKEN")

    fireEvent.change(slot, { target: { value: "GH_TOKEN_READONLY" } })
    fireEvent.click(screen.getByRole("button", { name: /save secret|save & assign/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const bindingCall = h.apiFetch.mock.calls.find(([url, init]) => String(url).includes("/credentials/bindings") && init?.method === "POST")!
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
    expect(screen.queryByLabelText(/variable name/i)).not.toBeInTheDocument()
    expect(screen.queryByRole("button", {name: /assign now/i})).not.toBeInTheDocument()
  })

  it("never posts a binding for a role that cannot create one", async () => {
    h.role = "MANAGER"
    const { onSuccess } = renderWizard()
    toScopeStep()
    fireEvent.click(screen.getByRole("button", { name: /save secret|save & assign/i }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    expect(h.apiFetch.mock.calls.some(([url]) => String(url).includes("/credentials/bindings"))).toBe(false)
  })

  // Without a binding the delivery layer falls back to the credential's own
  // name, so a name that is not a legal env var silently reaches nothing.
  it("saves a human display name without assigning it as a variable", async () => {
    const {onSuccess} = renderWizard()
    toScopeStep("github acme", "opaque")
    fireEvent.click(screen.getByRole("button", {name: /save secret|save & assign/i}))
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    expect(h.apiFetch.mock.calls.some(([url]) => String(url).includes("/bindings"))).toBe(false)
  })

  it("refuses to save a crew-scoped credential with no crew picked", async () => {
    renderWizard()
    toScopeStep()
    fireEvent.click(screen.getByRole("button", {name: /assign now/i}))
    fireEvent.click(screen.getByRole("button", { name: /selected crews/i }))
    fireEvent.click(screen.getByRole("button", { name: /save secret|save & assign/i }))
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
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "aws-prod" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))

    fireEvent.click(screen.getByRole("button", { name: /save secret|save & assign/i }))

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
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "db-login" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.click(screen.getByRole("button", { name: /save secret|save & assign/i }))

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
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "THING" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.click(screen.getByRole("button", { name: /save secret|save & assign/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const fieldCalls = h.apiFetch.mock.calls.filter(([url]) => String(url).includes("/fields"))
    expect(fieldCalls.map(bodyOf)).toEqual([
      { key: "tenant_id", value: "acme", is_secret: false, ordinal: 0 },
    ])
  })

  // The create request already accepts `token_expires_at`
  // (internal/api/credentials_mutate.go createCredentialRequest.TokenExpires)
  // and writes it straight into the column the "Expiring" KPI and the 30-day
  // warning read — the wizard just never offered a control for it.
  it("sends the expiry date as an ISO string when the user sets one", async () => {
    const { onSuccess } = renderWizard()
    pickShape(/^token/i)
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "abc123" } })
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "expiring-thing" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.change(screen.getByLabelText(/expires on/i), { target: { value: "2027-01-15" } })
    fireEvent.click(screen.getByRole("button", { name: /save secret|save & assign/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const createCall = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials?"))!
    expect(bodyOf(createCall)).toMatchObject({ token_expires_at: "2027-01-15T00:00:00.000Z" })
  })

  it("omits the expiry entirely when the user leaves it blank", async () => {
    const { onSuccess } = renderWizard()
    pickShape(/^token/i)
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "abc123" } })
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "no-expiry-thing" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.click(screen.getByRole("button", { name: /save secret|save & assign/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const createCall = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials?"))!
    expect(bodyOf(createCall)).not.toHaveProperty("token_expires_at")
  })

  // Tags drive the sidebar's Tag facet. Dropping them from the create path
  // (the old flat form had them) would leave a filter nobody can populate
  // without a second visit to the edit dialog.
  it("carries tags typed on the create path", async () => {
    const { onSuccess } = renderWizard()
    pickShape(/^token/i)
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "abc123" } })
    // "(optional)" moved out of the label and into the kit's hint line
    // under the control, where every other optional field says it.
    const tagInput = screen.getByLabelText(/^tags$/i)
    fireEvent.change(tagInput, { target: { value: "Prod" } })
    fireEvent.keyDown(tagInput, { key: "Enter" })
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "THING" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.click(screen.getByRole("button", { name: /save secret|save & assign/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const createCall = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials?"))!
    expect(bodyOf(createCall)).toMatchObject({ tags: ["prod"] })
  })

  it("surfaces the server's rejection and writes nothing further", async () => {
    h.apiFetch.mockResolvedValue(fail(409, { error: "Credential with this name already exists" }))
    const { onSuccess } = renderWizard()
    toScopeStep()
    fireEvent.click(screen.getByRole("button", { name: /save secret|save & assign/i }))

    expect(await screen.findByText(/credential with this name already exists/i)).toBeInTheDocument()
    expect(onSuccess).not.toHaveBeenCalled()
    expect(h.apiFetch.mock.calls.some(([url]) => String(url).includes("/fields"))).toBe(false)
    expect(h.apiFetch.mock.calls.some(([url]) => String(url).includes("/bindings"))).toBe(false)
  })

  // The credential row exists by then. Reporting "save failed" would send the
  // user to create it again, which 409s on the unique name.
  it("reports a failed slot claim as a partial save, not as a failure", async () => {
    h.apiFetch.mockImplementation(async (url: string, init?: {method?: string}) => {
      if (String(url).includes("/bindings") && init?.method === "POST") {
        return fail(409, { error: "slot GH_TOKEN is already bound in this scope — delete the existing binding first" })
      }
      return ok({ id: "cred_new" }, 201)
    })
    const { onSuccess } = renderWizard()
    toScopeStep()
    fireEvent.click(screen.getByRole("button", {name: /assign now/i}))
    fireEvent.click(screen.getByRole("button", {name: /all agents/i}))
    fireEvent.click(screen.getByRole("button", { name: /save secret|save & assign/i }))

    expect(await screen.findByText(/already bound in this scope/i)).toBeInTheDocument()
    expect(screen.getByText(/but some parts did not land/i)).toBeInTheDocument()
    expect(onSuccess).not.toHaveBeenCalled()
  })

  it("reports a network failure without claiming anything about the vault's contents", async () => {
    h.apiFetch.mockRejectedValue(new TypeError("offline"))
    const { onSuccess } = renderWizard()
    toScopeStep()
    fireEvent.click(screen.getByRole("button", { name: /save secret|save & assign/i }))
    expect(await screen.findByText(/credential may have been saved/i)).toBeInTheDocument()
    expect(onSuccess).not.toHaveBeenCalled()
  })
})
