import { entityHref } from "@/lib/entity-links"

/**
 * Turn a Crewship reference string — the `kind/slug` form pages and routines
 * store for owners and producers ("crew/ops", "agent/riley",
 * "routine/uptime-sweep", "page/watch") — into the route where that object
 * lives, via the one link map. Null for a ref of a kind that has no page.
 */
export function refHref(ref: string | null | undefined): string | null {
  if (!ref) return null
  const idx = ref.indexOf("/")
  if (idx <= 0) return null
  const kind = ref.slice(0, idx).toLowerCase()
  const slug = ref.slice(idx + 1)
  if (!slug) return null
  switch (kind) {
    case "crew":
      return entityHref({ kind: "crew", slug })
    case "agent":
      return entityHref({ kind: "agent", slug })
    case "routine":
    case "pipeline":
      return entityHref({ kind: "routine", slug })
    case "page":
      return entityHref({ kind: "page", slug })
    case "issue":
      return entityHref({ kind: "issue", identifier: slug })
    default:
      return null
  }
}

/** The human half of a ref: "routine/uptime-sweep" → "uptime-sweep". */
export function refLabel(ref: string | null | undefined): string {
  if (!ref) return ""
  const idx = ref.indexOf("/")
  return idx >= 0 ? ref.slice(idx + 1) : ref
}
