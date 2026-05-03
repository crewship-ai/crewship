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

/** Default model for a provider. Used when the user toggles provider — we
 *  auto-pick a sensible model unless they had a custom one already selected.
 *
 *  Resolution order:
 *    1. CLI_ADAPTERS[*].defaultModel for the adapter whose `provider` matches
 *       AND whose default is actually present in MODELS_BY_PROVIDER[provider].
 *       Both checks matter: the picker's "custom mode" toggle keys off
 *       isKnownModel(), so returning a default that isn't in the curated
 *       list would flip new agents into custom mode on first render.
 *    2. First entry of MODELS_BY_PROVIDER[provider] as a last-resort
 *       fallback (currently the only path for OLLAMA, which has no
 *       dedicated adapter — it's served via OpenCode prefixes).
 *
 *  Adapter iteration order is forced to sorted-keys so two adapters that
 *  both claim the same provider (today: CLAUDE_CODE + OPENCODE both have
 *  provider="ANTHROPIC") resolve deterministically. Without the sort,
 *  Object.values returns insertion order which is engine-defined for
 *  string keys — fine in V8 today but not guaranteed by spec, so a
 *  silent default-shift bug is exactly the kind of thing that surfaces
 *  during a future bundler upgrade and not in CI. */
export function defaultModelForProvider(provider: LLMProvider): string {
  const known = MODELS_BY_PROVIDER[provider]
  const knownSet = new Set<string>(known)
  for (const key of Object.keys(CLI_ADAPTERS).sort()) {
    const adapter = CLI_ADAPTERS[key]
    if (adapter.provider === provider && adapter.defaultModel && knownSet.has(adapter.defaultModel)) {
      return adapter.defaultModel
    }
  }
  return known[0]
}

/** True if the given model string is one of the provider's curated entries.
 *  False ⇒ the user is in "custom" mode and we render a text input. */
export function isKnownModel(provider: LLMProvider, model: string): boolean {
  return MODELS_BY_PROVIDER[provider].includes(model)
}
