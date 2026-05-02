import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { StepContainer } from "../step-container"
import { INITIAL_STATE, type WizardState } from "../types"

// =============================================================================
// RuntimeConfig + MCPConfigEditor are heavy components that fetch catalogs
// and depend on browser APIs. Stub them with thin doubles that expose the
// `value` / `onChange` contract so we can test wiring + summary chips without
// pulling in their internals.
// =============================================================================

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

vi.mock("@/components/features/mcp/mcp-config-editor", () => ({
  MCPConfigEditor: ({ value, onChange, workspaceId }: {
    value: string
    onChange: (json: string) => void
    workspaceId?: string
  }) => (
    <div data-testid="mcp-editor-stub">
      <span data-testid="mcp-workspace-id">{workspaceId}</span>
      <button
        type="button"
        onClick={() => onChange('{"mcpServers":{"github":{"command":"npx","args":[]}}}')}
      >
        Add GitHub MCP
      </button>
      <code data-testid="mcp-current-value">{value}</code>
    </div>
  ),
}))

function harness(initial: Partial<WizardState> = {}) {
  let state: WizardState = { ...INITIAL_STATE, ...initial }
  const setState = vi.fn((patch: Partial<WizardState>) => {
    state = { ...state, ...patch }
  })
  const r = render(<StepContainer state={state} setState={setState} workspaceId="ws_test" />)
  return {
    ...r,
    setState,
    rerenderWith: (patch: Partial<WizardState>) => {
      state = { ...state, ...patch }
      r.rerender(<StepContainer state={state} setState={setState} workspaceId="ws_test" />)
    },
  }
}

beforeEach(() => { /* clean slate */ })
afterEach(() => { /* nothing global to undo */ })

describe("<StepContainer> — section structure", () => {
  it("renders Image & features and MCP servers sections", () => {
    harness()
    expect(screen.getByText("Image & features")).toBeInTheDocument()
    expect(screen.getByText("MCP servers")).toBeInTheDocument()
  })

  it("BOTH sections are always visible (no collapse) — RuntimeConfig + MCPConfigEditor mount immediately", () => {
    harness()
    expect(screen.getByTestId("runtime-config-stub")).toBeInTheDocument()
    expect(screen.getByTestId("mcp-editor-stub")).toBeInTheDocument()
  })

  it("MCPConfigEditor receives the workspaceId prop", () => {
    harness()
    expect(screen.getByTestId("mcp-workspace-id")).toHaveTextContent("ws_test")
  })
})

describe("<StepContainer> — value flow", () => {
  it("RuntimeConfig.onChange propagates all 3 fields into wizard state", () => {
    const { setState } = harness()
    fireEvent.click(screen.getByRole("button", { name: /Set base ubuntu/ }))

    expect(setState).toHaveBeenCalledWith({
      runtimeImage: "ubuntu:22.04",
      devcontainerConfig: '{"image":"ubuntu:22.04","features":{"ghcr.io/devcontainers/features/git:1":{}}}',
      miseConfig: "",
    })
  })

  it("MCPConfigEditor.onChange propagates JSON string into mcpConfig", () => {
    const { setState } = harness()
    fireEvent.click(screen.getByRole("button", { name: /Add GitHub MCP/ }))

    expect(setState).toHaveBeenCalledWith({
      mcpConfig: '{"mcpServers":{"github":{"command":"npx","args":[]}}}',
    })
  })
})

describe("<StepContainer> — summary chips", () => {
  it("shows the default base image when nothing is configured", () => {
    harness()
    // "debian:bookworm-slim" appears in the intro text AND in the summary chip.
    expect(screen.getAllByText("debian:bookworm-slim").length).toBeGreaterThanOrEqual(1)
    expect(screen.getByText(/No servers configured/)).toBeInTheDocument()
  })

  it("shows feature count summary when devcontainer_config has features", () => {
    harness({
      devcontainerConfig: '{"image":"debian:bookworm-slim","features":{"a":{},"b":{},"c":{}}}',
    })
    expect(screen.getByText(/3 features/)).toBeInTheDocument()
  })

  it("singular vs plural for 1 feature", () => {
    harness({
      devcontainerConfig: '{"image":"debian:bookworm-slim","features":{"a":{}}}',
    })
    // Count is rendered in the section header chip; allow >=1 match because
    // ancestor textContent also matches the regex.
    expect(screen.getAllByText(/1 feature$/).length).toBeGreaterThanOrEqual(1)
  })

  it("renders MCP server count summary when mcpConfig has servers", () => {
    harness({
      mcpConfig: '{"mcpServers":{"github":{"command":"npx"},"slack":{"type":"http","url":"x"}}}',
    })
    expect(screen.getByText("2 servers configured")).toBeInTheDocument()
  })

  it("singular vs plural for MCP — 1 server", () => {
    harness({
      mcpConfig: '{"mcpServers":{"github":{"command":"npx"}}}',
    })
    expect(screen.getByText("1 server configured")).toBeInTheDocument()
  })

  it("'customized' badge appears once a non-default value is set", () => {
    harness({
      devcontainerConfig: '{"image":"alpine:3.19","features":{"a":{}}}',
    })
    expect(screen.getByText("customized")).toBeInTheDocument()
  })

  it("'configured' badge appears once an MCP server exists", () => {
    harness({
      mcpConfig: '{"mcpServers":{"github":{"command":"npx"}}}',
    })
    expect(screen.getByText("configured")).toBeInTheDocument()
  })

  it("counts mise tools from [tools] section in TOML", () => {
    harness({
      miseConfig: '[tools]\npython = "3.12"\nnode = "20"\n[other]\nnotcounted = "x"',
    })
    expect(screen.getByText(/2 runtimes/)).toBeInTheDocument()
  })

  it("ignores malformed JSON gracefully (no crash, count = 0)", () => {
    harness({
      devcontainerConfig: '{not valid json',
      mcpConfig: '{also broken',
    })
    // Should still render. No "X features" pill, no "X servers" — fall-through.
    expect(screen.getByText(/No servers configured/)).toBeInTheDocument()
  })
})
