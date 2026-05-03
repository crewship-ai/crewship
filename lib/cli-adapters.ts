import {
  AnthropicIcon,
  OpenAIIcon,
  GeminiIcon,
  OpenCodeIcon,
  CursorIcon,
} from "@/components/icons/provider-icons"
import type { ComponentType, SVGProps } from "react"

/** A selectable LLM model with display label and API value. */
export interface ModelOption {
  value: string
  label: string
}

/** Configuration for a CLI adapter (Claude Code, OpenCode, Codex CLI, Gemini CLI). */
export interface CLIAdapterConfig {
  label: string
  icon: ComponentType<SVGProps<SVGSVGElement>>
  provider: string
  envVar: string
  models: ModelOption[]
  defaultModel: string
  description: string
}

// Current canonical Anthropic model IDs as of 2026-04-29. Older 4.x and
// 3.x identifiers are kept further down so existing agents configured
// with them still resolve in the dropdown — they're flagged "legacy" in
// MODEL_DESCRIPTIONS at the UI layer.
const ANTHROPIC_MODELS: ModelOption[] = [
  { value: "claude-opus-4-7", label: "Claude Opus 4.7" },
  { value: "claude-sonnet-4-6", label: "Claude Sonnet 4.6" },
  { value: "claude-haiku-4-5-20251001", label: "Claude Haiku 4.5" },
  { value: "claude-opus-4-20250514", label: "Claude Opus 4" },
  { value: "claude-sonnet-4-20250514", label: "Claude Sonnet 4" },
  { value: "claude-3-5-sonnet-20241022", label: "Claude 3.5 Sonnet" },
  { value: "claude-3-5-haiku-20241022", label: "Claude 3.5 Haiku" },
]

const OPENAI_MODELS: ModelOption[] = [
  { value: "gpt-5", label: "GPT-5" },
  { value: "gpt-5-mini", label: "GPT-5 mini" },
  { value: "o3", label: "o3" },
  { value: "o3-mini", label: "o3-mini" },
  { value: "o4-mini", label: "o4-mini" },
  { value: "gpt-4o", label: "GPT-4o" },
  { value: "gpt-4o-mini", label: "GPT-4o mini" },
]

const GOOGLE_MODELS: ModelOption[] = [
  { value: "gemini-2.5-pro", label: "Gemini 2.5 Pro" },
  { value: "gemini-2.5-flash", label: "Gemini 2.5 Flash" },
  { value: "gemini-2.5-flash-lite", label: "Gemini 2.5 Flash Lite" },
  { value: "gemini-2.0-flash", label: "Gemini 2.0 Flash" },
]

// Cursor's headless agent multiplexes across providers; user picks the model
// in the Cursor account UI. The values here are the strings cursor-agent
// accepts via -m, mirroring what the IDE exposes as of 2026-05.
const CURSOR_MODELS: ModelOption[] = [
  { value: "claude-sonnet-4-6", label: "Claude Sonnet 4.6 (Cursor)" },
  { value: "gpt-5", label: "GPT-5 (Cursor)" },
  { value: "gpt-5-mini", label: "GPT-5 mini (Cursor)" },
  { value: "o3", label: "o3 (Cursor)" },
]

// Factory Droid multiplexes too — model strings here mirror what `droid exec
// --model` accepts as of the 2026-05 release.
const DROID_MODELS: ModelOption[] = [
  { value: "claude-sonnet-4-6", label: "Claude Sonnet 4.6 (Droid)" },
  { value: "claude-opus-4-7", label: "Claude Opus 4.7 (Droid)" },
  { value: "gpt-5", label: "GPT-5 (Droid)" },
  { value: "o3", label: "o3 (Droid)" },
]

/** Registry of all supported CLI adapters with their provider, models, and icon. */
export const CLI_ADAPTERS: Record<string, CLIAdapterConfig> = {
  CLAUDE_CODE: {
    label: "Claude Code",
    icon: AnthropicIcon,
    provider: "ANTHROPIC",
    envVar: "ANTHROPIC_API_KEY",
    models: ANTHROPIC_MODELS,
    defaultModel: "claude-sonnet-4-6",
    description: "Anthropic's coding agent",
  },
  OPENCODE: {
    label: "OpenCode",
    icon: OpenCodeIcon,
    provider: "ANTHROPIC",
    envVar: "ANTHROPIC_API_KEY",
    models: [...ANTHROPIC_MODELS, ...OPENAI_MODELS],
    defaultModel: "claude-sonnet-4-6",
    description: "Open-source multi-provider CLI",
  },
  CODEX_CLI: {
    label: "Codex CLI",
    icon: OpenAIIcon,
    provider: "OPENAI",
    envVar: "OPENAI_API_KEY",
    models: OPENAI_MODELS,
    defaultModel: "o4-mini",
    description: "OpenAI's coding agent",
  },
  GEMINI_CLI: {
    label: "Gemini CLI",
    icon: GeminiIcon,
    provider: "GOOGLE",
    envVar: "GOOGLE_API_KEY",
    models: GOOGLE_MODELS,
    defaultModel: "gemini-2.5-pro",
    description: "Google's coding agent",
  },
  CURSOR_CLI: {
    label: "Cursor CLI",
    icon: CursorIcon,
    provider: "CURSOR",
    envVar: "CURSOR_API_KEY",
    models: CURSOR_MODELS,
    defaultModel: "claude-sonnet-4-6",
    description: "Cursor's headless agent",
  },
  FACTORY_DROID: {
    label: "Factory Droid",
    icon: AnthropicIcon, // TODO: ship a dedicated FactoryIcon SVG
    provider: "FACTORY",
    envVar: "FACTORY_API_KEY",
    models: DROID_MODELS,
    defaultModel: "claude-sonnet-4-6",
    description: "Factory's autonomous coding agent",
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
    NONE: "--",
  }
  return labels[provider] ?? provider
}
