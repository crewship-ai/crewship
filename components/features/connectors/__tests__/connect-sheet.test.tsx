// Tests for ConnectorConnectSheet — orchestrates SchemaForm + verify
// + install API calls per auth_mode. The component shape is the
// glue between catalog click and finished workspace integration.
//
// TDD STUB — component throws until implemented.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { ConnectorConnectSheet } from "../connect-sheet"
import type { ConnectorManifest } from "../types"

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

// Use GitHub as the PAT exemplar — it has a real, shipping MCP server
// at @modelcontextprotocol/server-github. Avoids referencing a fake
// package name in a test fixture.
const patManifest: ConnectorManifest = {
  id: "github",
  name: "GitHub",
  description: "Code repos & issues",
  category: "dev_tools",
  brand: { logo: "github", color: "#181717" },
  auth_mode: "pat",
  fields: [
    {
      key: "pat",
      label: "Personal Access Token",
      type: "password",
      required: true,
      help: "Create at github.com/settings/personal-access-tokens.",
    },
  ],
  mcp: {
    transport: "stdio",
    command: "npx",
    args: ["-y", "@modelcontextprotocol/server-github"],
  },
}

const mcpOAuthManifest: ConnectorManifest = {
  id: "linear",
  name: "Linear",
  description: "Issue tracking",
  category: "dev_tools",
  brand: { logo: "linear", color: "#5E6AD2" },
  auth_mode: "mcp_oauth",
  mcp: {
    transport: "streamable-http",
    endpoint: "https://mcp.linear.app/mcp",
  },
  docs: { setup_md: "Click connect — Linear handles the rest." },
}

const byoManifest: ConnectorManifest = {
  id: "slack",
  name: "Slack",
  description: "Send messages",
  category: "communication",
  brand: { logo: "slack", color: "#4A154B" },
  auth_mode: "byo_oauth",
  fields: [
    { key: "client_id", label: "Slack OAuth Client ID", type: "text", required: true },
    { key: "client_secret", label: "Slack OAuth Client Secret", type: "password", required: true },
  ],
  oauth: {
    authorization_url: "https://slack.com/oauth/v2/authorize",
    token_url: "https://slack.com/api/oauth.v2.access",
    scopes: ["chat:write"],
    pkce: true,
  },
  mcp: {
    transport: "streamable-http",
    endpoint: "https://example.invalid/slack",
  },
  docs: {
    setup_md: "## Connect Slack\n\n1. Open https://api.slack.com/apps\n2. Add `${instance_url}/oauth/callback` as redirect URL.\n3. Copy client_id and secret here.",
  },
}

describe("ConnectorConnectSheet — PAT flow", () => {
  let originalFetch: typeof fetch
  beforeEach(() => {
    originalFetch = global.fetch
  })
  afterEach(() => {
    global.fetch = originalFetch
    // Tests later in this describe block stub window.open via
    // vi.stubGlobal — without unstubbing, the popup mock leaks into
    // unrelated tests in the same suite and silently passes/fails the
    // wrong assertions. Always unstub.
    vi.unstubAllGlobals()
  })

  it("renders schema form for the manifest's fields", () => {
    render(
      <ConnectorConnectSheet
        manifest={patManifest}
        open
        onOpenChange={vi.fn()}
        workspaceId="ws-1"
        onInstalled={vi.fn()}
      />,
    )
    expect(screen.getByLabelText("Personal Access Token")).toBeDefined()
  })

  it("hides the form for mcp_oauth (no fields needed)", () => {
    render(
      <ConnectorConnectSheet
        manifest={mcpOAuthManifest}
        open
        onOpenChange={vi.fn()}
        workspaceId="ws-1"
        onInstalled={vi.fn()}
      />,
    )
    // No password / text inputs expected; only a Connect button.
    const inputs = document.querySelectorAll("input[type=password], input[type=text]")
    expect(inputs.length).toBe(0)
  })

  it("renders setup_md markdown for byo_oauth manifests", () => {
    render(
      <ConnectorConnectSheet
        manifest={byoManifest}
        open
        onOpenChange={vi.fn()}
        workspaceId="ws-1"
        onInstalled={vi.fn()}
      />,
    )
    expect(screen.getByText(/Open https:\/\/api\.slack\.com\/apps/i)).toBeDefined()
  })

  it("submits PAT form → calls install endpoint and onInstalled", async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url.includes("/install")) {
        return { ok: true, status: 201, json: async () => ({ integration_id: "int_123" }) } as unknown as Response
      }
      // verify is best-effort and may be called pre-install
      return { ok: true, status: 200, json: async () => ({ ok: true }) } as unknown as Response
    })
    global.fetch = fetchMock as unknown as typeof fetch

    const onInstalled = vi.fn()
    render(
      <ConnectorConnectSheet
        manifest={patManifest}
        open
        onOpenChange={vi.fn()}
        workspaceId="ws-1"
        onInstalled={onInstalled}
      />,
    )
    fireEvent.change(screen.getByLabelText("Personal Access Token"), { target: { value: "sk-real" } })
    fireEvent.click(screen.getByRole("button", { name: /connect/i }))

    await waitFor(() => {
      // Discriminated InstallResult: PAT path completes synchronously
      // → status=installed + integrationId.
      expect(onInstalled).toHaveBeenCalledWith({ status: "installed", integrationId: "int_123" })
    })
    // The install endpoint must have been called with the workspace_id.
    const installCall = fetchMock.mock.calls.find(([u]) => String(u).includes("/install"))
    expect(installCall).toBeDefined()
    expect(String(installCall![0])).toContain("workspace_id=ws-1")
  })

  it("BYO OAuth submit → opens oauth_url in new window", async () => {
    const popup = vi.fn()
    vi.stubGlobal("open", popup)

    const fetchMock = vi.fn(async () => ({
      ok: true,
      status: 201,
      json: async () => ({
        integration_id: "int_456",
        next_step: "oauth",
        oauth_url: "https://slack.com/oauth/v2/authorize?client_id=abc&state=xyz",
      }),
    } as unknown as Response))
    global.fetch = fetchMock as unknown as typeof fetch

    render(
      <ConnectorConnectSheet
        manifest={byoManifest}
        open
        onOpenChange={vi.fn()}
        workspaceId="ws-1"
        onInstalled={vi.fn()}
      />,
    )
    fireEvent.change(screen.getByLabelText("Slack OAuth Client ID"), { target: { value: "abc" } })
    fireEvent.change(screen.getByLabelText("Slack OAuth Client Secret"), { target: { value: "def" } })
    fireEvent.click(screen.getByRole("button", { name: /connect/i }))

    await waitFor(() => {
      expect(popup).toHaveBeenCalled()
    })
    // First call to window.open should target the oauth_url.
    const firstUrl = String(popup.mock.calls[0]?.[0] ?? "")
    expect(firstUrl).toContain("slack.com/oauth/v2/authorize")
  })

  it("surfaces install error message", async () => {
    const fetchMock = vi.fn(async () => ({
      ok: false,
      status: 400,
      json: async () => ({ error: "missing field: api_key" }),
    } as unknown as Response))
    global.fetch = fetchMock as unknown as typeof fetch

    const { toast } = await import("sonner")
    render(
      <ConnectorConnectSheet
        manifest={patManifest}
        open
        onOpenChange={vi.fn()}
        workspaceId="ws-1"
        onInstalled={vi.fn()}
      />,
    )
    fireEvent.change(screen.getByLabelText("Personal Access Token"), { target: { value: "sk-real" } })
    fireEvent.click(screen.getByRole("button", { name: /connect/i }))

    await waitFor(() => {
      expect((toast as { error: ReturnType<typeof vi.fn> }).error).toHaveBeenCalled()
    })
  })

  it("renders nothing when manifest is null (closed sheet)", () => {
    const { container } = render(
      <ConnectorConnectSheet
        manifest={null}
        open={false}
        onOpenChange={vi.fn()}
        workspaceId="ws-1"
        onInstalled={vi.fn()}
      />,
    )
    expect(container.querySelector("input")).toBeNull()
  })
})
