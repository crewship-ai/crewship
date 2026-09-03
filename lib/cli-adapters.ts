import {
  AnthropicIcon,
  ClaudeIcon,
  OpenAIIcon,
  GeminiIcon,
  OpenCodeIcon,
  CursorIcon,
  FactoryIcon,
} from "@/components/icons/provider-icons"
import type { ComponentType, SVGProps } from "react"
import { adapterDefaultModel, adapterModels, catalogModelLabel } from "@/lib/model-catalog"

/** A selectable LLM model with display label and API value. */
export interface ModelOption {
  value: string
  label: string
  /** Optional category for grouping in the picker UI. */
  category?: "frontier" | "reasoning" | "fast" | "cheap" | "legacy" | "local"
}

/**
 * Maturity of a CLI adapter in the current release.
 *
 * - `production`: parity tested against the upstream CLI, full feature
 *   surface (streaming, tool use, MCP if applicable). Safe default.
 * - `experimental`: scaffolded and mechanically working but lacks one or
 *   more of: parity tests, fixture-based stream parsing, MCP integration,
 *   prompt-injection scrubbers. Show a warning before users opt in.
 *
 * The badge surfaces in the onboarding picker, the agent creation form,
 * and anywhere else the adapter is selectable so beta testers are not
 * surprised by silent feature gaps. Update `cli-adapters.spec.ts` (when
 * present) before flipping experimental → production.
 */
export type CLIAdapterStatus = "production" | "experimental"

/** Configuration for a CLI adapter (Claude Code, OpenCode, Codex, Gemini, Cursor, Factory). */
export interface CLIAdapterConfig {
  label: string
  icon: ComponentType<SVGProps<SVGSVGElement>>
  provider: string
  envVar: string
  models: ModelOption[]
  defaultModel: string
  description: string
  status: CLIAdapterStatus
  /**
   * Optional short caveat shown next to the experimental badge in the
   * picker. Use for known limitations the user MUST see at selection
   * time (e.g. "MCP not supported in headless mode"). Leave undefined
   * for production adapters or experimental ones with no specific
   * runtime caveat beyond "not yet parity-tested".
   */
  caveat?: string
}

// ===== MODEL LISTS =====
//
// SOURCE OF TRUTH: config/models.json, read through lib/model-catalog.ts and,
// on the server, through internal/llm.CuratedModels. This file used to carry
// six hand-typed lists that drifted from the Go side (which still offered
// gpt-4o while this offered GPT-5.5) and from each other; now an adapter's
// list is whatever the catalog says it accepts, and adding a model is one
// edit to one file that both the binary and the bundle pick up.
//
// The onboarding picker offers the FULL Claude Code list. It was trimmed to
// Sonnet 5 alone for a while, on the argument that only Sonnet had been run
// end to end — which left a customer who pays for Opus unable to choose it
// without leaving the wizard. The catalog's `default` (Sonnet 5) is what the
// picker preselects; the rest are there to be chosen.
const modelsFor = (adapterKey: string): ModelOption[] =>
  adapterModels(adapterKey).map((m) => ({ value: m.id, label: m.label, category: m.category }))

/**
 * True when the model routes to the operator's local OpenAI-compatible
 * endpoint (Ollama et al.) and therefore needs no provider API key.
 * Mirrors localModelPrefix / localEndpointModel in
 * internal/orchestrator/exec_env.go — keep in sync.
 *
 * This asks which ENDPOINT the model goes to, not whether the run is
 * proxy-routed through the sidecar. Those became two different questions when a
 * provider became a credential: an authenticated endpoint is now reached
 * through the sidecar, but it is the same endpoint and still needs no key from
 * the user. Routing depends on which credentials a crew actually holds, which
 * only the server can answer, so there is deliberately no client-side mirror of
 * it — see resolveRoutedProvider.
 */
export function isLocalModel(model?: string): boolean {
  return typeof model === "string" && model.startsWith("ollama/")
}

/**
 * Registry of all supported CLI adapters with their provider, models, and icon.
 *
 * Status reflects live-validation coverage: only Claude Code has been
 * parity-tested against real production runs. The other five ship complete
 * command builders + stream parsers (fixture-tested in CI) but await live
 * smoke validation; Cursor's MCP path is additionally broken upstream in
 * headless mode. Update the `status` and `caveat` here when each adapter's
 * gaps are closed.
 */
export const CLI_ADAPTERS: Record<string, CLIAdapterConfig> = {
  CLAUDE_CODE: {
    label: "Claude Code",
    // The Claude starburst, not the Anthropic "A": it is the mark people
    // know from the Claude app and from Claude Code's own splash screen.
    icon: ClaudeIcon,
    provider: "ANTHROPIC",
    envVar: "ANTHROPIC_API_KEY",
    models: modelsFor("CLAUDE_CODE"),
    defaultModel: adapterDefaultModel("CLAUDE_CODE"),
    description: "Anthropic's coding agent",
    status: "production",
  },
  OPENCODE: {
    label: "OpenCode",
    icon: OpenCodeIcon,
    provider: "ANTHROPIC",
    envVar: "ANTHROPIC_API_KEY",
    models: modelsFor("OPENCODE"),
    defaultModel: adapterDefaultModel("OPENCODE"),
    description: "Open-source multi-provider CLI",
    status: "experimental",
    caveat:
      "Cost + model reporting wired; awaiting live smoke validation. Local (ollama/…) models need CREWSHIP_LOCAL_MODEL_BASE_URL on the server and no API key.",
  },
  CODEX_CLI: {
    label: "Codex CLI",
    icon: OpenAIIcon,
    provider: "OPENAI",
    envVar: "OPENAI_API_KEY",
    models: modelsFor("CODEX_CLI"),
    defaultModel: adapterDefaultModel("CODEX_CLI"),
    description: "OpenAI's coding agent",
    status: "experimental",
    caveat: "Not yet parity-tested against live runs.",
  },
  GEMINI_CLI: {
    label: "Gemini CLI",
    icon: GeminiIcon,
    provider: "GOOGLE",
    envVar: "GOOGLE_API_KEY",
    models: modelsFor("GEMINI_CLI"),
    defaultModel: adapterDefaultModel("GEMINI_CLI"),
    description: "Google's coding agent",
    status: "experimental",
  },
  CURSOR_CLI: {
    label: "Cursor CLI",
    icon: CursorIcon,
    provider: "CURSOR",
    envVar: "CURSOR_API_KEY",
    models: modelsFor("CURSOR_CLI"),
    defaultModel: adapterDefaultModel("CURSOR_CLI"),
    description: "Cursor's headless agent",
    status: "experimental",
    caveat: "MCP tools are not invoked in Cursor's headless mode (upstream limitation).",
  },
  FACTORY_DROID: {
    label: "Factory Droid",
    icon: FactoryIcon,
    provider: "FACTORY",
    envVar: "FACTORY_API_KEY",
    models: modelsFor("FACTORY_DROID"),
    defaultModel: adapterDefaultModel("FACTORY_DROID"),
    description: "Factory's autonomous coding agent",
    status: "experimental",
  },
}

/** All CLI adapter keys (e.g. "CLAUDE_CODE", "OPENCODE"). */
export const CLI_ADAPTER_KEYS = Object.keys(CLI_ADAPTERS)

/** Look up CLI adapter configuration by key. Returns undefined for unknown adapters. */
export function getAdapterConfig(key: string): CLIAdapterConfig | undefined {
  return CLI_ADAPTERS[key]
}

/** Return the list of available LLM models for a given CLI adapter key. */
export function getModelsForAdapter(key: string): ModelOption[] {
  return CLI_ADAPTERS[key]?.models ?? []
}

/** Convert a provider key (e.g. "ANTHROPIC") to a human-readable label (e.g. "Anthropic"). */
export function getProviderLabel(provider: string): string {
  const labels: Record<string, string> = {
    ANTHROPIC: "Anthropic",
    OPENAI: "OpenAI",
    GOOGLE: "Google",
    CURSOR: "Cursor",
    FACTORY: "Factory",
    OLLAMA: "Ollama",
    NONE: "--",
  }
  return labels[provider] ?? provider
}

/**
 * Look up the friendly label for a model API string by scanning every
 * adapter's model list. Returns the input unchanged when the model is unknown
 * (custom user-typed model). Used everywhere agent metadata is rendered to
 * avoid showing raw API IDs like "claude-sonnet-4-6" instead of
 * "Claude Sonnet 4.6".
 *
 * Tries each adapter in turn so a model registered under multiple adapters
 * (e.g. claude-sonnet-4-6 in Claude/Cursor/Droid) returns the first match,
 * which is fine because labels for the same model are equivalent up to a
 * suffix annotation like "(Cursor)".
 */
export function getModelLabel(value: string): string {
  if (!value) return ""
  // Provider rows and aliases come first: claude-sonnet-4-6 is also an
  // adapter row under Cursor with a "(Cursor)" suffix, and an existing Claude
  // Code workspace on that model briefly read "Claude Sonnet 4.6 (Cursor)".
  return catalogModelLabel(value) ?? value
}

/**
 * Get the icon component for a provider. Falls back to AnthropicIcon for
 * unknown providers (matches PROVIDER_ICONS map default).
 */
export function getProviderIcon(provider: string): ComponentType<SVGProps<SVGSVGElement>> {
  return CLI_ADAPTERS[Object.keys(CLI_ADAPTERS).find((k) => CLI_ADAPTERS[k].provider === provider) ?? ""]?.icon ?? AnthropicIcon
}
