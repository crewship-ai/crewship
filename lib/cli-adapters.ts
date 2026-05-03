import {
  AnthropicIcon,
  OpenAIIcon,
  GeminiIcon,
  OpenCodeIcon,
  CursorIcon,
  FactoryIcon,
} from "@/components/icons/provider-icons"
import type { ComponentType, SVGProps } from "react"

/** A selectable LLM model with display label and API value. */
export interface ModelOption {
  value: string
  label: string
  /** Optional category for grouping in the picker UI. */
  category?: "frontier" | "reasoning" | "fast" | "cheap" | "legacy"
}

/** Configuration for a CLI adapter (Claude Code, OpenCode, Codex, Gemini, Cursor, Factory). */
export interface CLIAdapterConfig {
  label: string
  icon: ComponentType<SVGProps<SVGSVGElement>>
  provider: string
  envVar: string
  models: ModelOption[]
  defaultModel: string
  description: string
}

// ===== ANTHROPIC =====
// Source: https://platform.claude.com/docs/en/about-claude/models/overview
// claude-opus-4-7 / claude-sonnet-4-6 are valid API strings (alias = canonical
// for these versions). Haiku 4.5 has alias `claude-haiku-4-5` resolving to
// the dated `claude-haiku-4-5-20251001`. Anything claude-3-* / claude-*-4-2025*
// is deprecated (retiring 2026-06-15) and removed from the picker.
const ANTHROPIC_MODELS: ModelOption[] = [
  { value: "claude-opus-4-7", label: "Claude Opus 4.7", category: "frontier" },
  { value: "claude-sonnet-4-6", label: "Claude Sonnet 4.6", category: "frontier" },
  { value: "claude-haiku-4-5-20251001", label: "Claude Haiku 4.5", category: "fast" },
  { value: "claude-opus-4-6", label: "Claude Opus 4.6", category: "legacy" },
  { value: "claude-sonnet-4-5-20250929", label: "Claude Sonnet 4.5", category: "legacy" },
  { value: "claude-opus-4-5-20251101", label: "Claude Opus 4.5", category: "legacy" },
  { value: "claude-opus-4-1-20250805", label: "Claude Opus 4.1", category: "legacy" },
]

// ===== OPENAI — Codex CLI subset =====
// Source: https://developers.openai.com/codex/models
// Codex CLI accepts ONLY the GPT-5.x coding-tuned family — NOT the o-series
// (despite o3/o4-mini being valid for the general API). Pre-fix list mixed
// these; CLI silently rejects unknown models.
const OPENAI_CODEX_MODELS: ModelOption[] = [
  { value: "gpt-5.5", label: "GPT-5.5", category: "frontier" },
  { value: "gpt-5.4", label: "GPT-5.4", category: "frontier" },
  { value: "gpt-5.4-mini", label: "GPT-5.4 mini", category: "fast" },
  { value: "gpt-5.3-codex", label: "GPT-5.3 Codex", category: "frontier" },
  { value: "gpt-5.3-codex-spark", label: "GPT-5.3 Codex Spark", category: "reasoning" },
  { value: "gpt-5.2", label: "GPT-5.2", category: "legacy" },
]

// ===== OPENAI — General API (used by OpenCode, Cursor multiplexer) =====
// Source: https://developers.openai.com/api/docs/models/all
// Wider lineup than Codex CLI accepts.
const OPENAI_MODELS: ModelOption[] = [
  { value: "gpt-5.5", label: "GPT-5.5", category: "frontier" },
  { value: "gpt-5.5-pro", label: "GPT-5.5 Pro", category: "reasoning" },
  { value: "gpt-5.4", label: "GPT-5.4", category: "frontier" },
  { value: "gpt-5.4-pro", label: "GPT-5.4 Pro", category: "reasoning" },
  { value: "gpt-5.4-mini", label: "GPT-5.4 mini", category: "fast" },
  { value: "gpt-5.4-nano", label: "GPT-5.4 nano", category: "cheap" },
  { value: "gpt-5", label: "GPT-5", category: "legacy" },
  { value: "gpt-5-mini", label: "GPT-5 mini", category: "fast" },
  { value: "gpt-5-nano", label: "GPT-5 nano", category: "cheap" },
  { value: "gpt-5.3-codex", label: "GPT-5.3 Codex", category: "frontier" },
  { value: "o3", label: "o3", category: "reasoning" },
  { value: "o3-pro", label: "o3 Pro", category: "reasoning" },
]

// ===== GOOGLE GEMINI =====
// Source: https://ai.google.dev/gemini-api/docs/models
// 3.x family is preview; 2.5 is GA stable. gemini-2.0-flash + 1.5-pro removed
// from current model index — out.
const GOOGLE_MODELS: ModelOption[] = [
  { value: "gemini-3.1-pro-preview", label: "Gemini 3.1 Pro (Preview)", category: "frontier" },
  { value: "gemini-3-flash-preview", label: "Gemini 3 Flash (Preview)", category: "fast" },
  { value: "gemini-3.1-flash-lite-preview", label: "Gemini 3.1 Flash Lite (Preview)", category: "cheap" },
  { value: "gemini-2.5-pro", label: "Gemini 2.5 Pro", category: "frontier" },
  { value: "gemini-2.5-flash", label: "Gemini 2.5 Flash", category: "fast" },
  { value: "gemini-2.5-flash-lite", label: "Gemini 2.5 Flash Lite", category: "cheap" },
]

// ===== CURSOR (cursor-agent -m) =====
// Source: cursor.com/docs/cli/reference/parameters
// Cursor multiplexes — accepts underlying provider IDs + their in-house
// Composer model. cursor-agent --list-models shows the live per-account list.
const CURSOR_MODELS: ModelOption[] = [
  { value: "composer", label: "Cursor Composer", category: "frontier" },
  { value: "claude-opus-4-7", label: "Claude Opus 4.7 (Cursor)", category: "frontier" },
  { value: "claude-sonnet-4-6", label: "Claude Sonnet 4.6 (Cursor)", category: "frontier" },
  { value: "claude-haiku-4-5", label: "Claude Haiku 4.5 (Cursor)", category: "fast" },
  { value: "gpt-5.5", label: "GPT-5.5 (Cursor)", category: "frontier" },
  { value: "gpt-5.4", label: "GPT-5.4 (Cursor)", category: "frontier" },
  { value: "gpt-5.3-codex", label: "GPT-5.3 Codex (Cursor)", category: "frontier" },
  { value: "gemini-3.1-pro-preview", label: "Gemini 3.1 Pro (Cursor)", category: "frontier" },
  { value: "gemini-2.5-pro", label: "Gemini 2.5 Pro (Cursor)", category: "frontier" },
  { value: "grok-4.1-fast", label: "Grok 4.1 Fast (Cursor)", category: "fast" },
]

// ===== FACTORY DROID (droid exec --model) =====
// Source: https://docs.factory.ai/cli/droid-exec/overview + /models
// Bare IDs (no provider prefix). -fast variants are premium-tier multiplier.
// Custom format: custom:Display-Name-Index.
const DROID_MODELS: ModelOption[] = [
  { value: "claude-opus-4-7", label: "Claude Opus 4.7 (Droid)", category: "frontier" },
  { value: "claude-opus-4-6", label: "Claude Opus 4.6 (Droid)", category: "frontier" },
  { value: "claude-opus-4-6-fast", label: "Claude Opus 4.6 Fast (Droid)", category: "frontier" },
  { value: "claude-sonnet-4-6", label: "Claude Sonnet 4.6 (Droid)", category: "frontier" },
  { value: "claude-haiku-4-5-20251001", label: "Claude Haiku 4.5 (Droid)", category: "fast" },
  { value: "gpt-5.5", label: "GPT-5.5 (Droid)", category: "frontier" },
  { value: "gpt-5.5-fast", label: "GPT-5.5 Fast (Droid)", category: "frontier" },
  { value: "gpt-5.5-pro", label: "GPT-5.5 Pro (Droid)", category: "reasoning" },
  { value: "gpt-5.4", label: "GPT-5.4 (Droid)", category: "frontier" },
  { value: "gpt-5.4-fast", label: "GPT-5.4 Fast (Droid)", category: "frontier" },
  { value: "gpt-5.4-mini", label: "GPT-5.4 mini (Droid)", category: "fast" },
  { value: "gpt-5.3-codex", label: "GPT-5.3 Codex (Droid)", category: "frontier" },
  { value: "gpt-5.3-codex-fast", label: "GPT-5.3 Codex Fast (Droid)", category: "frontier" },
  { value: "gemini-3.1-pro-preview", label: "Gemini 3.1 Pro (Droid)", category: "frontier" },
  { value: "gemini-3-flash-preview", label: "Gemini 3 Flash (Droid)", category: "fast" },
  { value: "glm-5.1", label: "GLM 5.1 (Droid)", category: "frontier" },
  { value: "kimi-k2.6", label: "Kimi K2.6 (Droid)", category: "frontier" },
  { value: "minimax-m2.7", label: "MiniMax M2.7 (Droid)", category: "cheap" },
]

// ===== OPENCODE — curated provider/model list =====
// Source: opencode.ai/docs/providers + models.dev
// OpenCode accepts "provider/model" strings across 75+ providers. Curated
// list of the most-deployed combinations.
const OPENCODE_MODELS: ModelOption[] = [
  { value: "anthropic/claude-opus-4-7", label: "Anthropic / Claude Opus 4.7", category: "frontier" },
  { value: "anthropic/claude-sonnet-4-6", label: "Anthropic / Claude Sonnet 4.6", category: "frontier" },
  { value: "anthropic/claude-haiku-4-5", label: "Anthropic / Claude Haiku 4.5", category: "fast" },
  { value: "openai/gpt-5.5", label: "OpenAI / GPT-5.5", category: "frontier" },
  { value: "openai/gpt-5.4", label: "OpenAI / GPT-5.4", category: "frontier" },
  { value: "openai/gpt-5.4-mini", label: "OpenAI / GPT-5.4 mini", category: "fast" },
  { value: "openai/gpt-5.4-nano", label: "OpenAI / GPT-5.4 nano", category: "cheap" },
  { value: "openai/gpt-5.3-codex", label: "OpenAI / GPT-5.3 Codex", category: "frontier" },
  { value: "openai/o3", label: "OpenAI / o3", category: "reasoning" },
  { value: "openai/o3-pro", label: "OpenAI / o3 Pro", category: "reasoning" },
  { value: "google/gemini-3.1-pro-preview", label: "Google / Gemini 3.1 Pro", category: "frontier" },
  { value: "google/gemini-3-flash-preview", label: "Google / Gemini 3 Flash", category: "fast" },
  { value: "google/gemini-2.5-pro", label: "Google / Gemini 2.5 Pro", category: "frontier" },
  { value: "google/gemini-2.5-flash", label: "Google / Gemini 2.5 Flash", category: "fast" },
  { value: "xai/grok-4.1-fast", label: "xAI / Grok 4.1 Fast", category: "fast" },
  { value: "xai/grok-4", label: "xAI / Grok 4", category: "frontier" },
  { value: "deepseek/deepseek-v4-flash", label: "DeepSeek / V4 Flash", category: "fast" },
  { value: "deepseek/deepseek-v3.2", label: "DeepSeek / V3.2", category: "frontier" },
  { value: "deepseek/deepseek-r1", label: "DeepSeek / R1", category: "reasoning" },
  { value: "groq/llama-3.3-70b-versatile", label: "Groq / Llama 3.3 70B", category: "fast" },
  { value: "groq/qwen-2.5-coder-32b", label: "Groq / Qwen 2.5 Coder 32B", category: "fast" },
  { value: "moonshotai/kimi-k2.6", label: "Moonshot / Kimi K2.6", category: "frontier" },
  { value: "zai/glm-5.1", label: "Z.ai / GLM 5.1", category: "frontier" },
  { value: "minimax/minimax-m2.7", label: "MiniMax / M2.7", category: "cheap" },
  { value: "openrouter/anthropic/claude-sonnet-4-6", label: "OpenRouter / Claude Sonnet 4.6", category: "frontier" },
  { value: "openrouter/openai/gpt-5.5", label: "OpenRouter / GPT-5.5", category: "frontier" },
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
    models: OPENCODE_MODELS,
    defaultModel: "anthropic/claude-sonnet-4-6",
    description: "Open-source multi-provider CLI",
  },
  CODEX_CLI: {
    label: "Codex CLI",
    icon: OpenAIIcon,
    provider: "OPENAI",
    envVar: "OPENAI_API_KEY",
    models: OPENAI_CODEX_MODELS,
    defaultModel: "gpt-5.5",
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
    defaultModel: "composer",
    description: "Cursor's headless agent",
  },
  FACTORY_DROID: {
    label: "Factory Droid",
    icon: FactoryIcon,
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
  for (const adapter of Object.values(CLI_ADAPTERS)) {
    const found = adapter.models.find((m) => m.value === value)
    if (found) return found.label
  }
  return value
}

/**
 * Get the icon component for a provider. Falls back to AnthropicIcon for
 * unknown providers (matches PROVIDER_ICONS map default).
 */
export function getProviderIcon(provider: string): ComponentType<SVGProps<SVGSVGElement>> {
  return CLI_ADAPTERS[Object.keys(CLI_ADAPTERS).find((k) => CLI_ADAPTERS[k].provider === provider) ?? ""]?.icon ?? AnthropicIcon
}
