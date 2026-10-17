/**
 * "Paste the value, or pick the type" (Harbor 2, Add secret step 1).
 *
 * Recognises a pasted value by its prefix — in the browser only; nothing is
 * sent anywhere until the person saves. The answer fills the service, a name
 * and the variable agents will see; a model provider's key is pointed at
 * "Add AI provider" instead, because agents pay with it rather than read it.
 */

import { defaultEnvVarName } from "@/lib/credential-provider"
import { detectBrandFromValue } from "@/lib/credential-providers/registry"

/** Brands an agent thinks and pays with — offered as an AI provider account. */
const AI_PROVIDER_KEYS = new Set(["ANTHROPIC", "OPENAI", "GOOGLE"])

export interface PasteDetection {
  provider: string
  label: string
  /** The secret shape to store it as — a single value. */
  itemType: "TOKEN"
  envVar: string
  suggestedName: string
  /** True when the key belongs in AI providers. */
  aiProvider: boolean
}

export function detectPastedValue(raw: string): PasteDetection | null {
  const value = raw.trim()
  if (value.length < 8) return null
  const brand = detectBrandFromValue(value)
  if (!brand || brand.key.startsWith("VAULT_")) return null
  const isSecretKey = /^(?:sk|rk)_(?:live|test)_/.test(value)
  return {
    provider: brand.key,
    label: brand.label,
    itemType: "TOKEN",
    envVar: defaultEnvVarName(brand),
    suggestedName: `${brand.label} ${isSecretKey ? "secret key" : "key"}`,
    aiProvider: AI_PROVIDER_KEYS.has(brand.key),
  }
}
