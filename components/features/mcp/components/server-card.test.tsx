import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { ServerCard, type ServerCardProps } from "./server-card"
import type { ServerEntry } from "../types"

const entry = (patch: Partial<ServerEntry> = {}): ServerEntry => ({ _key: 7, name: "local", transport: "stdio", command: "npx", args: "--help", url: "", env: [], headers: [], ...patch })
function props(patch: Partial<ServerCardProps> = {}): ServerCardProps {
  return { entry: entry(), index: 3, readOnly: false, credentials: [], credLoading: false, hasCredentialSupport: false, onFetchCredentials: vi.fn(), onAddCredential: vi.fn(), onUpdate: vi.fn(), onRemove: vi.fn(), onAddEnvVar: vi.fn(), onUpdateEnvVar: vi.fn(), onRemoveEnvVar: vi.fn(), onAddHeader: vi.fn(), onUpdateHeader: vi.fn(), onRemoveHeader: vi.fn(), ...patch }
}
afterEach(cleanup)

it("edits and removes a local server using its list index", () => {
  const p = props(); render(<ServerCard {...p} />)
  fireEvent.change(screen.getByLabelText("Server name"), { target: { value: "renamed" } })
  fireEvent.change(screen.getByLabelText("Command"), { target: { value: "uvx" } })
  fireEvent.change(screen.getByLabelText("Arguments"), { target: { value: "server --readonly" } })
  expect(p.onUpdate).toHaveBeenNthCalledWith(1, 3, { name: "renamed" })
  expect(p.onUpdate).toHaveBeenNthCalledWith(2, 3, { command: "uvx" })
  expect(p.onUpdate).toHaveBeenNthCalledWith(3, 3, { args: "server --readonly" })
  fireEvent.click(screen.getByRole("button", { name: "Remove server local" }))
  expect(p.onRemove).toHaveBeenCalledExactlyOnceWith(3)
  expect(screen.queryByLabelText("URL")).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Advanced (0 env)" }))
  expect(screen.getByText(/Use.*syntax to reference credentials/)).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Add Variable" }))
  expect(p.onAddEnvVar).toHaveBeenCalledExactlyOnceWith(3)
})

it("changes transport through the selector", async () => {
  const p = props(); render(<ServerCard {...p} />)
  fireEvent.click(screen.getByRole("combobox"))
  fireEvent.click(await screen.findByRole("option", { name: "HTTP (remote)" }))
  expect(p.onUpdate).toHaveBeenCalledExactlyOnceWith(3, { transport: "http" })
})

it("edits HTTP address, headers and environment rows independently", () => {
  const p = props({ entry: entry({ transport: "http", url: "https://example.test/mcp", headers: [{ key: "Authorization", value: "Bearer fixture" }], env: [{ key: "MODE", value: "safe" }] }) })
  render(<ServerCard {...p} />)
  expect(screen.queryByLabelText("Command")).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText("URL"), { target: { value: "https://next.test/mcp" } })
  expect(p.onUpdate).toHaveBeenCalledWith(3, { url: "https://next.test/mcp" })
  fireEvent.change(screen.getByLabelText("Header 1 name"), { target: { value: "X-Mode" } })
  fireEvent.change(screen.getByLabelText("Header 1 value"), { target: { value: "read" } })
  expect(p.onUpdateHeader).toHaveBeenNthCalledWith(1, 3, 0, "key", "X-Mode")
  expect(p.onUpdateHeader).toHaveBeenNthCalledWith(2, 3, 0, "value", "read")
  fireEvent.change(screen.getByLabelText("Environment variable 1 name"), { target: { value: "NEW_MODE" } })
  fireEvent.change(screen.getByLabelText("Environment variable 1 value"), { target: { value: "strict" } })
  expect(p.onUpdateEnvVar).toHaveBeenNthCalledWith(1, 3, 0, "key", "NEW_MODE")
  expect(p.onUpdateEnvVar).toHaveBeenNthCalledWith(2, 3, 0, "value", "strict")
  fireEvent.click(screen.getByRole("button", { name: "Delete header Authorization" }))
  fireEvent.click(screen.getByRole("button", { name: "Delete env var MODE" }))
  fireEvent.click(screen.getByRole("button", { name: "Add Header" }))
  expect(p.onRemoveHeader).toHaveBeenCalledExactlyOnceWith(3, 0)
  expect(p.onRemoveEnvVar).toHaveBeenCalledExactlyOnceWith(3, 0)
  expect(p.onAddHeader).toHaveBeenCalledExactlyOnceWith(3)
})

it.each(["", "named"])("keeps read-only %s server fields disabled and hides mutation controls", (name) => {
  const p = props({ readOnly: true, hasCredentialSupport: true, workspaceId: "w", entry: entry({ name, transport: "http", headers: [{ key: "X-Mode", value: "read" }], env: [{ key: "MODE", value: "safe" }] }) })
  render(<ServerCard {...p} />)
  expect(screen.getByText(name || "(unnamed)")).toBeInTheDocument()
  for (const field of screen.getAllByRole("textbox")) expect(field).toBeDisabled()
  expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
  expect(screen.queryByRole("button", { name: /Remove server|Delete|Add Header|Add Variable/ })).not.toBeInTheDocument()
  expect(p.onUpdate).not.toHaveBeenCalled()
})

it("labels unnamed editable server and rows for removal", () => {
  const p = props({ entry: entry({ name: "", transport: "http", env: [{ key: "", value: "" }], headers: [{ key: "", value: "" }] }) })
  render(<ServerCard {...p} />)
  expect(screen.getByRole("button", { name: "Remove server (unnamed)" })).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Delete header (unnamed)" })).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Delete env var (unnamed)" })).toBeInTheDocument()
})

it("opens advanced settings for headers even without environment variables", () => {
  const p = props({ entry: entry({ transport: "http", headers: [{ key: "X-Mode", value: "read" }] }) })
  render(<ServerCard {...p} />)
  expect(screen.getByLabelText("Header 1 value")).toBeVisible()
})

it("falls back to a plain field when credential support has no selected workspace", () => {
  const p = props({ hasCredentialSupport: true, entry: entry({ env: [{ key: "MODE", value: "safe" }] }) })
  render(<ServerCard {...p} />)
  expect(screen.getByLabelText("Environment variable 1 value")).toHaveValue("safe")
  expect(screen.queryByRole("button", { name: "Select credential..." })).not.toBeInTheDocument()
})

it("uses the credential picker to update the selected environment row", async () => {
  const p = props({ hasCredentialSupport: true, workspaceId: "w", credentials: [{ id: "key", name: "github-token", type: "SECRET", status: "ACTIVE" }], entry: entry({ env: [{ key: "GITHUB_TOKEN", value: "" }] }) })
  render(<ServerCard {...p} />)
  fireEvent.click(screen.getByRole("button", { name: "Select credential..." }))
  expect(p.onFetchCredentials).toHaveBeenCalledOnce()
  fireEvent.click(await screen.findByRole("button", { name: /github-token/ }))
  expect(p.onUpdateEnvVar).toHaveBeenCalledExactlyOnceWith(3, 0, "value", "${GITHUB_TOKEN}")
})
