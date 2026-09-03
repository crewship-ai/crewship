import catalog from "@/config/models.json"

/**
 * The web side of config/models.json — the one list of models Crewship
 * offers, shared with the Go binary (config.ModelsJSON → internal/llm).
 *
 * Nothing in the web bundle should carry a model id or display name of its
 * own any more: every picker (onboarding, the agent form, the composer's
 * model menu, the model library) reads through here, so adding a model is one
 * edit to one file and it shows up everywhere, on both sides, at once.
 */

export type ModelCategory = "frontier" | "reasoning" | "fast" | "cheap" | "legacy" | "local"
export type ModelRole = "cheap" | "default" | "top"

export interface CatalogModel {
  id: string
  label: string
  category?: ModelCategory
  role?: ModelRole
}

interface CatalogProvider {
  label: string
  default?: string
  live_only?: boolean
  models: CatalogModel[]
  aliases?: Record<string, string>
}

interface CatalogAdapter {
  provider: string
  default: string
  /** A string refers to a provider model by id; an object is the adapter's own row. */
  models: Array<string | CatalogModel>
}

interface ModelCatalog {
  version: number
  providers: Record<string, CatalogProvider>
  adapters: Record<string, CatalogAdapter>
}

// The JSON import is structurally typed by TypeScript; the cast pins the
// contract this module promises to callers and lets the file carry `$comment`
// keys without them leaking into the type.
export const MODEL_CATALOG = catalog as unknown as ModelCatalog

const providerKey = (provider: string) => provider.trim().toLowerCase()

/** The curated models of a provider ("ANTHROPIC" or "anthropic"), most capable first. */
export function providerModels(provider: string): CatalogModel[] {
  return MODEL_CATALOG.providers[providerKey(provider)]?.models ?? []
}

/** The provider's default model id, or "" when it has none. */
export function providerDefaultModel(provider: string): string {
  return MODEL_CATALOG.providers[providerKey(provider)]?.default ?? ""
}

/** The models a CLI adapter accepts, with references to provider rows resolved. */
export function adapterModels(adapterKey: string): CatalogModel[] {
  const adapter = MODEL_CATALOG.adapters[adapterKey]
  if (!adapter) return []
  const byId = new Map(providerModels(adapter.provider).map((m) => [m.id, m]))
  const out: CatalogModel[] = []
  for (const entry of adapter.models) {
    if (typeof entry === "string") {
      const m = byId.get(entry)
      // A dangling reference is a catalog defect; model-catalog.test.ts pins
      // that none exist, so at runtime it is simply skipped.
      if (m) out.push(m)
      continue
    }
    out.push(entry)
  }
  return out
}

/** The adapter's default model id, or "" for an unknown adapter. */
export function adapterDefaultModel(adapterKey: string): string {
  return MODEL_CATALOG.adapters[adapterKey]?.default ?? ""
}

/** The catalog's provider key ("anthropic") for an adapter, or "". */
export function adapterProvider(adapterKey: string): string {
  return MODEL_CATALOG.adapters[adapterKey]?.provider ?? ""
}

/**
 * Display name for any model id the catalog knows — provider rows first, then
 * the aliases a workspace may still store (date-suffixed and superseded
 * Anthropic ids), then adapter-specific rows. Returns undefined for an id the
 * catalog has never heard of; callers decide whether to show the raw id.
 */
export function catalogModelLabel(id: string): string | undefined {
  for (const p of Object.values(MODEL_CATALOG.providers)) {
    const hit = p.models.find((m) => m.id === id)
    if (hit) return hit.label
  }
  for (const p of Object.values(MODEL_CATALOG.providers)) {
    const alias = p.aliases?.[id]
    if (alias) return alias
  }
  for (const key of Object.keys(MODEL_CATALOG.adapters)) {
    const hit = adapterModels(key).find((m) => m.id === id)
    if (hit) return hit.label
  }
  return undefined
}
