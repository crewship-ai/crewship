/**
 * Reads the agent's artifact tool calls out of chat tool parts. The activity
 * names what the agent did and which version came out; the card loads the
 * project itself, so file contents from the input are never copied here.
 */

export type ArtifactToolKind = "create" | "publish" | "restore"

export interface ArtifactToolActivity {
  tool: ArtifactToolKind
  state: "running" | "done" | "failed"
  slug?: string
  pageId?: string
  name?: string
  version?: number
  baseVersion?: number
  restoreTarget?: number
  error?: string
}

// Bare name, or MCP-prefixed `mcp__<server>__artifact_x` as the runtime reports tools from an MCP server.
const TOOL = /^(?:mcp__[A-Za-z0-9_.-]*[A-Za-z0-9.-]__)?artifact_(create|publish|restore)$/
const MAX_ERROR = 200

const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)
const text = (value: unknown): value is string => typeof value === "string" && value.trim().length > 0
const int = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0

function toolKind(name: string | undefined | null): ArtifactToolKind | null {
  const match = typeof name === "string" ? TOOL.exec(name) : null
  return match ? match[1] as ArtifactToolKind : null
}

export function isArtifactToolName(name: string | undefined | null): boolean {
  return toolKind(name) !== null
}

function json(value: string): unknown {
  try { return JSON.parse(value) } catch { return undefined }
}

/** Unwraps `[{type:"text",text}]` and `{content:[…], isError}` down to the text the tool returned. */
function unwrap(raw: string): { text: string; isError: boolean } {
  let current = raw
  let isError = false
  for (let depth = 0; depth < 3; depth++) {
    const parsed = json(current)
    const content = Array.isArray(parsed) ? parsed : object(parsed) && Array.isArray(parsed.content) ? parsed.content : null
    if (!content) break
    if (object(parsed) && parsed.isError === true) isError = true
    const parts = content.filter(part => object(part) && part.type === "text" && typeof part.text === "string").map(part => (part as { text: string }).text)
    if (parts.length === 0) break
    current = parts.join("\n")
  }
  return { text: current, isError }
}

const short = (value: string) => {
  const line = value.replace(/\s+/g, " ").trim()
  return line.length > MAX_ERROR ? `${line.slice(0, MAX_ERROR - 1)}…` : line
}

export function artifactToolActivity(call: { toolName: string; input?: unknown }, result?: { content: string; isError?: boolean } | null): ArtifactToolActivity | null {
  const tool = toolKind(call.toolName)
  if (!tool) return null
  const input = typeof call.input === "string" ? json(call.input) : call.input
  const activity: ArtifactToolActivity = { tool, state: "running" }
  if (object(input)) {
    if (text(input.slug)) activity.slug = input.slug
    if (tool === "create" && text(input.name)) activity.name = input.name
    if (tool !== "create" && int(input.base_version)) activity.baseVersion = input.base_version
    if (tool === "restore" && int(input.version)) activity.restoreTarget = input.version
  }
  if (!result) return activity

  const { text: body, isError } = unwrap(typeof result.content === "string" ? result.content : "")
  const parsed = json(body)
  const failed = (message: string) => ({ ...activity, state: "failed" as const, error: short(message) || "The tool call failed." })
  if (result.isError || isError) return failed(object(parsed) && text(parsed.error) ? parsed.error : body)
  if (!object(parsed)) return failed(body || "The tool returned no result.")
  if (text(parsed.error)) return failed(parsed.error)

  const done: ArtifactToolActivity = { ...activity, state: "done" }
  if (text(parsed.slug)) done.slug = parsed.slug
  if (text(parsed.page_id)) done.pageId = parsed.page_id
  if (int(parsed.version)) done.version = parsed.version
  return done
}
