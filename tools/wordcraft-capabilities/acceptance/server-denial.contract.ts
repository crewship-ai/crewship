// @vitest-environment node
import { createHash, randomUUID } from "node:crypto"
import { spawnSync } from "node:child_process"
import { expect, it } from "vitest"
import catalog from "../../../schemas/wordcraft/capabilities.json"

// Intentionally requires an already-running, disposable, explicitly selected
// fixture. Missing integration is a failure, never an accepted denial or skip.
const required = (name: string) => {
  const value = process.env[name]
  if (!value) throw new Error(`Required acceptance input missing: ${name}`)
  return value
}
let base: URL, page: string, binary: string, token: string, mcpUrl: URL, tool: string
let requestId = 0, mcpSession = "", mcpVersion = ""
const timeout = () => AbortSignal.timeout(15_000)
const hash = (value: unknown) => createHash("sha256").update(JSON.stringify(value)).digest("hex")
const pageRoute = () => new URL(`/api/v1/pages/${encodeURIComponent(page)}/mooring`, base)
const auth = () => ({ Authorization: `Bearer ${token}`, "Content-Type": "application/json" })
interface ReadState { app_id: string; state: { rev: number; generation: number; snapshot: unknown } }
async function readState(): Promise<ReadState> {
  const response = await fetch(pageRoute(), { headers: auth(), signal: timeout(), redirect: "error" })
  expect(response.status, "fixture must authorize the control read").toBe(200)
  const state = await response.json() as ReadState
  expect(/wordcraft/i.test(state.app_id), "fixture must be a WordCraft document").toBe(true)
  expect(Number.isSafeInteger(state.state?.rev), "control must expose a confirmed revision").toBe(true)
  expect(Number.isSafeInteger(state.state?.generation), "control must expose a session generation").toBe(true)
  expect(state.state).toHaveProperty("snapshot")
  return state
}
function cli(operation: string, packet?: object) {
  return spawnSync(binary, ["--server", base.origin, "--format", "json", "mooring", operation, page, ...(packet ? ["--file", "-"] : [])], {
    input: packet ? JSON.stringify(packet) : undefined, encoding: "utf8", timeout: 15_000, maxBuffer: 4 * 1024 * 1024,
    env: { ...process.env, CREWSHIP_SERVER: base.origin, CREWSHIP_TOKEN: token, CREWSHIP_PROFILE: "", CREWSHIP_NO_SLUG_CACHE: "1" },
  })
}
async function rpc(method: string, params: object) {
  const id = ++requestId
  const response = await fetch(mcpUrl, { method: "POST", headers: { ...auth(), Accept: "application/json, text/event-stream", ...(mcpSession ? { "Mcp-Session-Id": mcpSession } : {}), ...(mcpVersion ? { "MCP-Protocol-Version": mcpVersion } : {}) }, body: JSON.stringify({ jsonrpc: "2.0", id, method, params }), signal: timeout(), redirect: "error" })
  expect(response.status, "MCP must accept a valid authenticated JSON-RPC call").toBe(200)
  mcpSession = response.headers.get("Mcp-Session-Id") ?? mcpSession
  const text = await response.text()
  const body = response.headers.get("content-type")?.includes("text/event-stream")
    ? text.split("\n").filter(line => line.startsWith("data:")).map(line => JSON.parse(line.slice(5).trim())).find(frame => frame.id === id)
    : JSON.parse(text)
  expect(body?.jsonrpc).toBe("2.0")
  expect(body?.id).toBe(id)
  expect(Boolean(body?.error), "RPC routing or protocol errors are not command denial").toBe(false)
  return body.result
}

async function setup() {
  base = new URL(required("WORDCRAFT_CE_BASE_URL"))
  page = required("WORDCRAFT_CE_PAGE")
  binary = required("WORDCRAFT_CE_CLI")
  token = required("WORDCRAFT_CE_TOKEN")
  mcpUrl = new URL(required("WORDCRAFT_CE_MCP_URL"))
  tool = required("WORDCRAFT_CE_MCP_TOOL")
  // No implicit server and no token forwarding to another authority.
  expect(base.pathname).toBe("/")
  expect(base.username + base.password + base.search + base.hash).toBe("")
  expect(mcpUrl.origin, "use the same fixture authority for CLI and MCP").toBe(base.origin)
  await readState()
  const control = cli("read")
  expect(Boolean(control.error), "CLI control read must execute successfully").toBe(false)
  expect(control.status, "CLI control read must reach the selected server").toBe(0)
  const cliState = JSON.parse(control.stdout) as ReadState
  expect(hash(cliState.state)).toBe(hash((await readState()).state))
  mcpSession = ""; mcpVersion = ""
  const initialization = await rpc("initialize", { protocolVersion: "2025-03-26", capabilities: {}, clientInfo: { name: "wordcraft-denial-acceptance", version: "1" } })
  expect(typeof initialization?.protocolVersion).toBe("string")
  mcpVersion = initialization.protocolVersion
  const notification = await fetch(mcpUrl, { method: "POST", headers: { ...auth(), ...(mcpSession ? { "Mcp-Session-Id": mcpSession } : {}), ...(mcpVersion ? { "MCP-Protocol-Version": mcpVersion } : {}) }, body: JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }), signal: timeout(), redirect: "error" })
  expect([200, 202, 204].includes(notification.status)).toBe(true)
  const tools = await rpc("tools/list", {})
  expect(tools.tools?.some((entry: { name: string }) => entry.name === tool), "required command tool must be registered").toBe(true)
}

for (const { command, parameters, code } of [
  { command: "format.color", parameters: { color: "FF0000" }, code: "command_not_qualified" },
  { command: "file.open", parameters: { path: "forbidden-acceptance.docx" }, code: "command_forbidden" },
]) {
  it(`server refuses the same ${command} through CLI and MCP without changing confirmed state`, async () => {
    await setup()
    expect(catalog.commands.find(entry => entry.id === command)?.qualification).toBe("unqualified")
    const before = await readState()
    const packet = { mutation_id: `denied-${randomUUID()}`, base_rev: before.state.rev, generation: before.state.generation, command, arguments: parameters }
    const denied = cli("command", packet)
    expect(Boolean(denied.error), "spawn/timeouts are not server rejection").toBe(false)
    expect(denied.status, "CLI must expose a non-successful server outcome").not.toBe(0)
    const diagnostic = denied.stdout + denied.stderr
    // Both markers are required: parse, network, auth, unknown-route and generic
    // permission errors cannot satisfy this command-catalog acceptance test.
    expect(/\b403\b/.test(diagnostic), "CLI must preserve the server denial status").toBe(true)
    expect(diagnostic.includes(code), "CLI must preserve the command-catalog denial code").toBe(true)
    expect(hash((await readState()).state)).toBe(hash(before.state))
    const outcome = await rpc("tools/call", { name: tool, arguments: { page, request: { ...packet, mutation_id: `denied-${randomUUID()}` } } })
    expect(outcome.isError, "MCP must expose a tool-level command rejection").toBe(true)
    expect(outcome.structuredContent).toMatchObject({ status: 403, code, command })
    expect(hash((await readState()).state)).toBe(hash(before.state))
  }, 60_000)
}
