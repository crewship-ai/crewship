import type { LLMProvider } from "@/lib/agent-personas"

/** Curated model lists per LLM provider — what the dropdown offers when the
 *  user toggles between providers. The list is short on purpose; users with
 *  niche models pick "(custom)" and type a string.
 *
 *  Source of truth: each provider's published model catalog as of 2026-05.
 *  Keep ordered newest → oldest so the freshest is the implicit default. */
export const MODELS_BY_PROVIDER: Record<LLMProvider, readonly string[]> = {
  ANTHROPIC: [
    "claude-opus-4-7",
    "claude-sonnet-4-6",
    "claude-sonnet-4-5",
    "claude-haiku-4-5",
  ],
  OPENAI: [
    "gpt-5",
    "gpt-5-mini",
    "gpt-4o",
    "gpt-4o-mini",
  ],
  GOOGLE: [
    "gemini-2.5-pro",
    "gemini-2.5-flash",
    "gemini-2.5-flash-lite",
    "gemini-1.5-pro",
  ],
  OLLAMA: [
    // Whatever the user has pulled locally. List the most-common starters;
    // the actual catalog is whatever `ollama list` returns. Defaulting to a
    // popular open-weight is friendlier than starting empty.
    "llama3.3",
    "qwen2.5-coder:32b",
    "deepseek-r1:14b",
    "phi3:mini",
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
