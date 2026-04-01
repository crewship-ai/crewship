/**
 * Example MCP template — copy this file to add a new provider.
 *
 * 1. Copy this file and rename it to your-provider.ts
 * 2. Fill in the MCPTemplate fields
 * 3. Import and add it to the array in ../registry.ts
 * 4. Add the icon mapping in ../registry.ts TEMPLATE_ICONS
 *
 * That's it — the template will appear in the "Add MCP Server" popover.
 */
import type { MCPTemplate } from "../../types"

export const example: MCPTemplate = {
  // Unique identifier (used as the server name in config JSON)
  name: "example",
  // Human-readable label shown in the UI
  label: "Example Service",
  // lucide-react icon name (must be mapped in registry.ts TEMPLATE_ICONS)
  icon: "plug",
  // "stdio" for local process, "streamable-http" for remote endpoint
  transport: "stdio",
  // For stdio: the command to run
  command: "npx",
  // For stdio: space-separated arguments
  args: "-y @example/mcp-server",
  // For http: the endpoint URL (omit for stdio)
  // url: "https://mcp.example.com/sse",
  // Suggested env var name (auto-creates an env row in the UI)
  envHint: "EXAMPLE_API_KEY",
  // For http: suggested header (auto-creates a header row)
  // headerHint: "Authorization: Bearer ${EXAMPLE_TOKEN}",
  // OAuth provider key matching backend OAuthProviders map (omit for API key auth)
  // oauthProvider: "example",
}
