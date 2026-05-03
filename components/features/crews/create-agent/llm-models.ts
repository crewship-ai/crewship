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
  for (const adapter of Object.values(CLI_ADAPTERS)) {
    if (adapter.provider !== provider) continue
    for (const m of adapter.models) {
      // Skip namespaced "provider/model" entries — those belong to the
      // OpenCode multi-provider catalog and would pollute single-provider
      // dropdowns (e.g. "openai/gpt-5.5" appearing under ANTHROPIC because
      // OpenCode's provider field is ANTHROPIC for sidecar credential mapping).
      if (m.value.includes("/")) continue
      seen.add(m.value)
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
