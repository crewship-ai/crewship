import type { LLMProvider } from "@/lib/agent-personas"
import { CLI_ADAPTERS } from "@/lib/cli-adapters"

/** Curated model lists per LLM provider — what the dropdown offers when the
 *  user toggles between providers. The list is short on purpose; users with
 *  niche models pick "(custom)" and type a string.
 *
 *  Source of truth: CLI_ADAPTERS in @/lib/cli-adapters. We project per-CLI
 *  models[] into per-provider lists by union'ing the adapter values across
 *  all adapters whose provider matches. This keeps a single source of truth
 *  so the picker and the per-CLI models[] never drift apart. */
function modelsForProvider(provider: string): readonly string[] {
  const seen = new Set<string>()
  // Prefix-based recovery for the OpenCode multi-provider catalog. Without
  // this, single-provider dropdowns lose models that only exist in the
  // OPENCODE_MODELS list (e.g. openai/o3, openai/o3-pro, openai/gpt-5.5-pro
  // are not in OPENAI_CODEX_MODELS — Codex CLI doesn't accept reasoning
  // models — but they ARE valid OPENAI provider models for any non-Codex
  // surface). Strip the namespace and adopt as a native entry.
  const prefixByProvider: Record<string, string> = {
    ANTHROPIC: "anthropic/",
    OPENAI: "openai/",
    GOOGLE: "google/",
  }
  const prefix = prefixByProvider[provider]
  for (const adapter of Object.values(CLI_ADAPTERS)) {
    for (const m of adapter.models) {
      if (adapter.provider === provider && !m.value.includes("/")) {
        seen.add(m.value)
        continue
      }
      if (prefix && m.value.startsWith(prefix)) {
        // Only adopt the immediate "provider/model" form, not nested paths
        // like "openrouter/openai/gpt-5.5" which are router-specific routes.
        const rest = m.value.slice(prefix.length)
        if (!rest.includes("/")) {
          seen.add(rest)
        }
      }
    }
  }
  return Array.from(seen)
}

export const MODELS_BY_PROVIDER: Record<LLMProvider, readonly string[]> = {
  ANTHROPIC: modelsForProvider("ANTHROPIC"),
  OPENAI: modelsForProvider("OPENAI"),
  GOOGLE: modelsForProvider("GOOGLE"),
  CURSOR: modelsForProvider("CURSOR"),
  FACTORY: modelsForProvider("FACTORY"),
  OLLAMA: [
    // Ollama is served via OpenCode (provider/model paths). Listed here for
    // the provider chip; the actual API string in Crewship will be sent to
    // OpenCode prefixed as ollama/<model>. Versions current in
    // ollama.com/library as of 2026-05.
    "qwen2.5-coder:32b",
    "qwen3:32b",
    "qwen3.5:14b",
    "deepseek-r1:14b",
    "deepseek-r1:32b",
    "deepseek-coder:6.7b",
    "llama3.3:70b",
    "gpt-oss:20b",
    "gpt-oss:120b",
    "gemma3:12b",
    "phi4:14b",
    "phi3:mini",
    "mistral-nemo:12b",
    "codellama:13b",
    "starcoder2:15b",
    "mixtral:8x7b",
  ],
}

/** Default model for a provider (first entry in its list). Used when the
 *  user toggles provider — we auto-pick the newest model unless they had a
 *  custom one already selected. */
export function defaultModelForProvider(provider: LLMProvider): string {
  return MODELS_BY_PROVIDER[provider][0]
}

/** True if the given model string is one of the provider's curated entries.
 *  False ⇒ the user is in "custom" mode and we render a text input. */
export function isKnownModel(provider: LLMProvider, model: string): boolean {
  return MODELS_BY_PROVIDER[provider].includes(model)
}
