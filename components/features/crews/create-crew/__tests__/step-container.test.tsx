import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { StepContainer } from "../step-container"
import { INITIAL_STATE, type WizardState } from "../types"

// This surface fetches on mount and the fetch is not what this file asserts.
// vitest.setup.ts fails a test that opens a socket, so pin it to an empty
// payload here rather than let it reach the network — the leak used to be a
// silent ECONNREFUSED while the suite still reported green.
beforeEach(() => {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify({ data: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    ),
  )
})

// RuntimeConfig fetches a 1308-row catalog and depends on browser APIs. A thin
// double keeps its `value` / `onChange` contract so the wiring is testable
// without its internals.
vi.mock("../../runtime-config", () => ({
  RuntimeConfig: ({ value, onChange }: {
    value: { runtimeImage: string; devcontainerConfig: string; miseConfig: string }
    onChange: (v: { runtimeImage: string; devcontainerConfig: string; miseConfig: string }) => void
  }) => (
    <div data-testid="runtime-config-stub">
      <button
        type="button"
        onClick={() => onChange({
          runtimeImage: "ubuntu:22.04",
          devcontainerConfig: '{"image":"ubuntu:22.04","features":{"ghcr.io/devcontainers/features/git:1":{}}}',
          miseConfig: "",
        })}
      >
        Set base ubuntu + 1 feature
      </button>
      <code data-testid="runtime-current-image">{value.runtimeImage}</code>
    </div>
  ),
}))

vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ role: "OWNER" }),
}))

function renderStep(overrides: Partial<WizardState> = {}) {
  const setState = vi.fn()
  const onPickImage = vi.fn()
  const state: WizardState = { ...INITIAL_STATE, ...overrides }
  const utils = render(<StepContainer state={state} setState={setState} onPickImage={onPickImage} />)
  return { setState, onPickImage, state, ...utils }
}

/** Size lives behind a disclosure; open it before asserting on its controls. */
function openSize() {
  fireEvent.click(screen.getByRole("button", { name: /^Size/ }))
}

describe("<StepContainer> — what the step is made of", () => {
  it("puts image, network and size on one step", async () => {
    renderStep()
    // The image and tooling sections are RuntimeConfig's own now — it draws
    // them under layout="sections" instead of this step wrapping the whole
    // component in one section with a tab strip inside it. RuntimeConfig is
    // stubbed here, so the stub standing in for those sections is the
    // assertion; runtime-config's own tests cover what it renders.
    //
    // Awaited, because RuntimeConfig is a `dynamic()` import: the step used to
    // draw the section title itself and so had something synchronous to
    // assert on, and now it does not.
    expect(await screen.findByTestId("runtime-config-stub")).toBeInTheDocument()
    expect(screen.getByText("Network")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^Size/ })).toBeInTheDocument()
  })

  it("mounts RuntimeConfig immediately rather than behind a disclosure", () => {
    renderStep()
    expect(screen.getByTestId("runtime-config-stub")).toBeInTheDocument()
  })

  // Tools reach agents through Composio and the integrations surface. A
  // crew-level MCP editor in the create path was a second way to say the same
  // thing, and the one nobody used.
  it("does not offer an MCP editor", () => {
    renderStep({ mcpConfig: '{"mcpServers":{"github":{"command":"npx"}}}' })
    expect(screen.queryByTestId("mcp-editor-stub")).toBeNull()
    expect(screen.queryByText(/MCP/i)).toBeNull()
  })

  it("passes RuntimeConfig's changes up as a state patch", () => {
    const { setState } = renderStep()
    fireEvent.click(screen.getByRole("button", { name: /Set base ubuntu/ }))
    expect(setState).toHaveBeenCalledWith(expect.objectContaining({ runtimeImage: "ubuntu:22.04" }))
    // And it does not smuggle mcpConfig back in.
    expect(setState.mock.calls[0][0]).not.toHaveProperty("mcpConfig")
  })
})

describe("<StepContainer> — network", () => {
  // Open is the wizard's proposal: an allowlist that is still maturing fails
  // as a silent timeout deep inside a run, which is the worst failure shape a
  // platform has. The mechanism stays one switch away.
  it("starts open, and says so", () => {
    renderStep()
    expect(screen.getByText("Open egress")).toBeInTheDocument()
    expect(screen.queryByText(/Allowed hosts/)).toBeNull()
  })

  // "The container reaches any host" undersold it and made Open egress read
  // like a middle setting. free mode is the maximum: the sidecar's dial guard
  // is `p.allowPrivate || p.freeMode`, so it also permits RFC1918 + loopback.
  // Saying so is what makes it clear there is no third, wilder mode missing.
  it("admits that open includes the private network, and what it still does not", () => {
    renderStep()
    expect(screen.getByText(/private network and localhost/i)).toBeInTheDocument()
    expect(screen.getByText(/metadata stays blocked/i)).toBeInTheDocument()
  })

  it("switching on the allowlist patches networkMode and reveals the editor", () => {
    const { setState, rerender } = renderStep()
    fireEvent.click(screen.getByRole("switch", { name: /allowlist/i }))
    expect(setState).toHaveBeenCalledWith({ networkMode: "restricted" })

    rerender(<StepContainer state={{ ...INITIAL_STATE, networkMode: "restricted" }} setState={setState} />)
    expect(screen.getByText(/Allowed hosts/)).toBeInTheDocument()
  })

  it("switching back to open clears the domains rather than hiding them", () => {
    const { setState } = renderStep({ networkMode: "restricted", allowedDomains: ["github.com"] })
    fireEvent.click(screen.getByRole("switch", { name: /allowlist/i }))
    expect(setState).toHaveBeenCalledWith({ networkMode: "free", allowedDomains: [] })
  })

  it("warns that an empty allowlist locks everything", () => {
    renderStep({ networkMode: "restricted", allowedDomains: [] })
    expect(screen.getByText(/locks all egress/i)).toBeInTheDocument()
  })

  it("does not warn once a host is listed", () => {
    renderStep({ networkMode: "restricted", allowedDomains: ["github.com"] })
    expect(screen.queryByText(/locks all egress/i)).toBeNull()
  })

  it("advertises wildcard subdomain support", () => {
    renderStep({ networkMode: "restricted" })
    expect(screen.getByText(/\*\.github\.com/)).toBeInTheDocument()
  })
})

describe("<StepContainer> — size", () => {
  // An administrator's question. It used to open a step of its own, which is
  // why the wizard's third screen asked a new user to have an opinion about
  // fractional cores before it asked what the crew was for.
  it("is folded away, with the current values in the summary", () => {
    renderStep()
    expect(screen.getByRole("button", { name: /^Size/ })).toHaveAttribute("aria-expanded", "false")
    expect(screen.getByText(/2 cores · 4 GB · no auto-stop/)).toBeInTheDocument()
  })

  it("summarises a TTL when one is set", () => {
    renderStep({ ttlHours: 4 })
    expect(screen.getByText(/stops after 4 h/)).toBeInTheDocument()
  })

  it("patches memoryMB from a preset chip", () => {
    const { setState } = renderStep()
    openSize()
    fireEvent.click(screen.getByRole("button", { name: "1 GB" }))
    expect(setState).toHaveBeenCalledWith({ memoryMB: 1024 })
  })

  it("patches cpus from a preset chip", () => {
    const { setState } = renderStep()
    openSize()
    fireEvent.click(screen.getByRole("button", { name: "4" }))
    expect(setState).toHaveBeenCalledWith({ cpus: 4 })
  })

  it("Never patches ttlHours to null", () => {
    const { setState } = renderStep({ ttlHours: 4 })
    openSize()
    fireEvent.click(screen.getByRole("button", { name: "Never" }))
    expect(setState).toHaveBeenCalledWith({ ttlHours: null })
  })

  it("shows the CLI flag each control maps to", () => {
    renderStep()
    openSize()
    expect(screen.getByText("--memory-mb 4096")).toBeInTheDocument()
    expect(screen.getByText("--cpus 2")).toBeInTheDocument()
    expect(screen.getByText("--ttl 0")).toBeInTheDocument()
  })

  it("warns when the memory will not hold a second agent", () => {
    renderStep({ memoryMB: 1024 })
    openSize()
    expect(screen.getByText(/cannot hold a second agent/i)).toBeInTheDocument()
  })

  it("Custom… opens a numeric input and commits a valid value", () => {
    const { setState } = renderStep()
    openSize()
    const [customBtn] = screen.getAllByRole("button", { name: "Custom…" })
    fireEvent.click(customBtn)
    const input = screen.getByLabelText(/Custom MB value/i)
    fireEvent.change(input, { target: { value: "8192" } })
    fireEvent.blur(input)
    expect(setState).toHaveBeenCalledWith({ memoryMB: 8192 })
  })

  it("refuses an out-of-range custom value and keeps the field open to say why", () => {
    const { setState } = renderStep()
    openSize()
    const [customBtn] = screen.getAllByRole("button", { name: "Custom…" })
    fireEvent.click(customBtn)
    const input = screen.getByLabelText(/Custom MB value/i)
    fireEvent.change(input, { target: { value: "999999" } })
    fireEvent.blur(input)
    expect(setState).not.toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent(/Enter \d+-\d+ MB/)
  })
})

// ── The base image is a row, not a catalogue ─────────────────────────────
//
// Nine radio rows and a search box used to sit inline on this step, which
// also carries tooling, network and sizing — a step made of lists.
// docs/prd/create-surface-parity.md §6.3 shows one row saying what the crew
// runs on, with the catalogue behind it as
// a panel the surface swaps to.
describe("<StepContainer> — base image", () => {
  it("says what the crew will run, defaults included", () => {
    renderStep()
    const row = screen.getByRole("button", { name: /Change/ })
    // The wizard sends nothing for the image unless asked, and the server's
    // default is what it will get; the row says so rather than looking unset.
    expect(row).toHaveTextContent("debian:bookworm-slim")
  })

  // DEFAULT_BASE_IMAGE is not one of BASE_IMAGES' full registry paths, so a
  // plain catalogue lookup misses it and the row called the shipped default
  // "Custom image" — the one label that means "an operator typed this".
  // isCustomBaseImage() has always excluded it; the row now agrees.
  it("does not call the shipped default a custom image", () => {
    renderStep()
    const row = screen.getByRole("button", { name: /Change/ })
    expect(row).not.toHaveTextContent(/Custom image/)
    expect(row).toHaveTextContent(/default/i)
  })

  it("does call a registry reference a custom image", () => {
    renderStep({ runtimeImage: "ghcr.io/acme/thing:v1" })
    expect(screen.getByRole("button", { name: /Change/ })).toHaveTextContent(/Custom image/)
  })

  it("shows a picked image by its catalogue name", () => {
    renderStep({ runtimeImage: "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm" })
    expect(screen.getByRole("button", { name: /Change/ })).toHaveTextContent(/Node 22/)
  })

  it("calls up to the wizard rather than opening a catalogue in place", () => {
    const { onPickImage } = renderStep()
    fireEvent.click(screen.getByRole("button", { name: /Change/ }))
    expect(onPickImage).toHaveBeenCalledTimes(1)
  })
})
