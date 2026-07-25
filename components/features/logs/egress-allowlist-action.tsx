"use client"

import { useCallback, useState } from "react"
import { ShieldCheck, ShieldX } from "lucide-react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import type { JournalEntry } from "@/lib/types/journal"

// #1377 gap 4 — a blocked egress already lands in Crow's Nest as a
// `network.egress` entry with `payload.denied = true`, but the fix ("add this
// host to the crew's allowed_domains") meant leaving the timeline for the crew
// settings tab and retyping the hostname. This turns the denial itself into the
// remediation: expand the row, one click, done.
//
// Deliberately self-contained (own GET + PATCH via apiFetch) rather than
// threading a handler down through LogsPanel → LogsList → Detail: the action
// needs the crew's CURRENT allowed_domains to union onto, which only the server
// knows, and no ancestor holds that state.

/**
 * Strip a :port suffix and lowercase, mirroring the server's `normalizeDomain`
 * (internal/api/crews.go:26). CONNECT denials carry "example.com:443"; storing
 * that verbatim would never match the port-stripped allowlist lookup.
 *
 * IPv6 literals ("[::1]:443" / "::1") are returned as null — they are never a
 * valid allowlist entry (normalizeDomain requires a dot and rejects them), so
 * offering the button would promise a fix that the save silently drops.
 */
export function normalizeEgressHost(raw: unknown): string | null {
  if (typeof raw !== "string") return null
  let s = raw.trim().toLowerCase()
  if (!s) return null
  if (s.includes("[") || s.includes("]")) return null
  // Only strip a trailing :port — a bare "::1" has several colons and is out.
  const colons = (s.match(/:/g) ?? []).length
  if (colons === 1) s = s.slice(0, s.indexOf(":"))
  else if (colons > 1) return null
  if (!s || !s.includes(".") || /[\s/]/.test(s)) return null
  return s
}

/**
 * True when a host could be visually mistaken for a different one — a non-ASCII
 * label (Cyrillic "а" in "аpi.anthropic.com") or an already-punycoded "xn--"
 * label.
 *
 * This matters specifically because of the one-click affordance below. The
 * denied host is chosen by the agent that was blocked, and the button renders
 * it as its own label — so a rogue agent in a restricted crew could request a
 * homoglyph of a trusted domain, get denied on purpose, and have the remediation
 * button read "Add api.anthropic.com to allowlist" while actually allowlisting
 * a host it controls. The server's normalizeDomain accepts these (it only
 * rejects whitespace and requires a dot), so the check belongs here, at the
 * point where a human is being asked to approve the string by sight.
 *
 * Typing the same host into crew settings by hand stays possible — the goal is
 * to keep a confusable host from being a *single click* that looks routine.
 */
export function isConfusableHost(host: string): boolean {
  // eslint-disable-next-line no-control-regex
  if (/[^\x00-\x7F]/.test(host)) return true
  return host.split(".").some((label) => label.startsWith("xn--"))
}

/**
 * The blocked host for a journal entry, or null when the entry isn't a
 * remediable egress denial. Exported so the row can decide whether to render
 * the action without duplicating the predicate.
 */
export function blockedEgressHost(entry: JournalEntry): string | null {
  if (entry.entry_type !== "network.egress") return null
  const payload = entry.payload as Record<string, unknown> | undefined
  if (!payload || payload.denied !== true) return null
  if (!entry.crew_id) return null
  return normalizeEgressHost(payload.host)
}

interface CrewNetworkShape {
  network_mode?: string
  allowed_domains?: string[] | string | null
}

function parseDomains(raw: CrewNetworkShape["allowed_domains"]): string[] {
  if (Array.isArray(raw)) return raw.filter((d): d is string => typeof d === "string")
  if (typeof raw === "string") return raw.split(/[,\n]+/).map((d) => d.trim()).filter(Boolean)
  return []
}

export function EgressAllowlistAction({ entry }: { entry: JournalEntry }) {
  const host = blockedEgressHost(entry)
  const { role } = useAbilities()
  // Editing a crew is a "manage" (ADMIN) route server-side — don't offer a
  // button that would 403.
  const isAdmin = role === "OWNER" || role === "ADMIN"
  const [state, setState] = useState<"idle" | "saving" | "done">("idle")

  const onAdd = useCallback(async () => {
    if (!host || !entry.crew_id) return
    setState("saving")
    try {
      const res = await apiFetch(`/api/v1/crews/${entry.crew_id}`)
      if (!res.ok) {
        toast.error(`Could not load the crew's network policy (HTTP ${res.status})`)
        setState("idle")
        return
      }
      const crew = (await res.json()) as CrewNetworkShape
      const current = parseDomains(crew.allowed_domains)
      if (current.includes(host)) {
        // Either a wildcard already covers it or someone else just added it.
        // Either way there is nothing to write — say so instead of a no-op PATCH.
        toast.info(`${host} is already in this crew's allowlist`)
        setState("done")
        return
      }

      const patch = await apiFetch(`/api/v1/crews/${entry.crew_id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ allowed_domains: [...current, host] }),
      })
      if (!patch.ok) {
        const body = (await patch.json().catch(() => ({}))) as { error?: string }
        toast.error(body.error || `Failed to update the allowlist (HTTP ${patch.status})`)
        setState("idle")
        return
      }
      setState("done")
      toast.success(`${host} added to the crew allowlist`, {
        description: "The crew container restarts with the new policy on its next run.",
      })
    } catch {
      toast.error("Network error while updating the allowlist")
      setState("idle")
    }
  }, [host, entry.crew_id])

  if (!host) return null

  // A confusable host never gets the one-click path — the whole affordance
  // rests on an admin trusting the string they see in the button label.
  if (isConfusableHost(host)) {
    return (
      <p className="text-[10px] text-amber-400 flex items-start gap-1">
        <ShieldX className="h-3 w-3 shrink-0 mt-px" />
        <span>
          Blocked host <code className="font-mono text-foreground/80">{host}</code> contains
          non-ASCII or punycode labels and may impersonate a familiar domain. No one-click
          add — verify it, then add it deliberately in the crew&apos;s Network settings.
        </span>
      </p>
    )
  }

  if (!isAdmin) {
    return (
      <p className="text-[10px] text-muted-foreground flex items-center gap-1">
        <ShieldX className="h-3 w-3 shrink-0" />
        Blocked by the crew network policy — an admin can add{" "}
        <code className="font-mono text-foreground/80">{host}</code> to the allowlist.
      </p>
    )
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      <button
        type="button"
        onClick={onAdd}
        disabled={state !== "idle"}
        className="inline-flex items-center gap-1 h-6 px-2 rounded border border-emerald-500/40 bg-emerald-500/10 text-[10px] text-emerald-300 hover:bg-emerald-500/20 disabled:opacity-60 disabled:hover:bg-emerald-500/10"
      >
        <ShieldCheck className="h-3 w-3" />
        {state === "saving"
          ? "Adding…"
          : state === "done"
            ? "Added to allowlist"
            : `Add ${host} to allowlist`}
      </button>
      {state === "idle" && (
        <span className="text-[10px] text-muted-foreground">
          Adds the exact host. Use{" "}
          <code className="font-mono">*.{host.split(".").slice(-2).join(".")}</code> in crew
          settings to cover every subdomain.
        </span>
      )}
    </div>
  )
}
