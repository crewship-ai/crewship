import {
  AnthropicIcon,
  OpenAIIcon,
  GeminiIcon,
  OpenCodeIcon,
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

const ANTHROPIC_MODELS: ModelOption[] = [
  { value: "claude-sonnet-4-20250514", label: "Claude Sonnet 4" },
  { value: "claude-opus-4-20250514", label: "Claude Opus 4" },
  { value: "claude-haiku-4-5-20251001", label: "Claude Haiku 4.5" },
]

const OPENAI_MODELS: ModelOption[] = [
  { value: "o3", label: "o3" },
  { value: "o4-mini", label: "o4-mini" },
  { value: "gpt-4o", label: "GPT-4o" },
]

const GOOGLE_MODELS: ModelOption[] = [
  { value: "gemini-2.5-pro", label: "Gemini 2.5 Pro" },
  { value: "gemini-2.5-flash", label: "Gemini 2.5 Flash" },
]

/** Registry of all supported CLI adapters with their provider, models, and icon. */
export const CLI_ADAPTERS: Record<string, CLIAdapterConfig> = {
  CLAUDE_CODE: {
    label: "Claude Code",
    icon: AnthropicIcon,
    provider: "ANTHROPIC",
    envVar: "ANTHROPIC_API_KEY",
    models: ANTHROPIC_MODELS,
    defaultModel: "claude-sonnet-4-20250514",
    description: "Anthropic's coding agent",
  },
  OPENCODE: {
    label: "OpenCode",
    icon: OpenCodeIcon,
    provider: "ANTHROPIC",
    envVar: "ANTHROPIC_API_KEY",
    models: [...ANTHROPIC_MODELS, ...OPENAI_MODELS],
    defaultModel: "claude-sonnet-4-20250514",
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
    NONE: "--",
  }
  return labels[provider] ?? provider
}
