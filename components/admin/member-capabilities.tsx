"use client"

import { useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "@/lib/toast"
import { Lock, ArrowRight, SlidersHorizontal } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { personLabel } from "@/components/ui/user-avatar"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { apiFetch } from "@/lib/api-fetch"
import { devWarn } from "@/lib/client-log"
import { cn } from "@/lib/utils"
import {
  CAPABILITY_BUNDLES,
  CAPABILITY_LABELS,
  Capability,
  type CapabilityBundle,
  type CapabilityValue,
} from "@/lib/capabilities"

/**
 * Per-member capabilities for Settings › Members (/settings/members).
 *
 * This used to be `capability-grid.tsx`: a second list of the same people
 * the roster above already listed. Two lists keyed by the same identity meant
 * answering "what can this person do?" required reading both and reconciling
 * them (#1517). What is left are the pieces the Members page composes:
 *
 *   - `useMemberCapabilities` — the bulk query, still one round-trip for the
 *     whole roster (the N+1 fan-out this replaced was quadratic for the
 *     500-user tenant the PRD targets).
 *   - `useToggleCapability` — the single-grant PATCH, optimistic with
 *     rollback on 4xx. The person's detail panel and the matrix both use it,
 *     so a cell and a switch can never disagree about what a click sends.
 *   - `MemberCapabilityToggles` — the detail panel's switches, grouped
 *     Routines / Content / Credentials. No save button: each click is its own
 *     PATCH so one failure doesn't roll back the rest.
 *   - `BulkPresetAction` — workspace-wide presets. They act on the whole
 *     roster, so they belong to the page's sub-bar rather than to any person.
 *
 * Role is not a capability. `requireCapabilityOrForbid` gates on the stored
 * set alone, with no OWNER bypass; the only grant that "comes with" every
 * role is `chat`, which is implied and can never be toggled. So the stored
 * set IS the effective set, and "beyond the role" means "stored, minus chat".
 *
 * OWNER capabilities are locked (server PATCH would 403); the caller's own
 * row is locked (defence against downgrade-then-restore).
 *
 * Admin-only — every caller is responsible for hiding this when the current
 * user is not ADMIN+.
 */

export interface CapabilityMember {
  id: string
  role: string
  user: {
    id: string
    email: string
    full_name: string | null
    avatar_url: string | null
  }
}

interface CapabilitiesResponse {
  user_id: string
  role: string
  capabilities: string[]
}

interface CapabilitiesBulkResponse {
  members: CapabilitiesResponse[]
}

/** Cache key shared by the query and every mutation that patches it. */
export function memberCapabilitiesKey(workspaceId: string) {
  return ["member-capabilities", workspaceId] as const
}

/**
 * One round-trip for the whole roster. Returns `user_id -> capabilities`.
 *
 * A failed read rejects: the editor must distinguish unknown grants from an
 * empty set before it can safely replace them. A 403 can mean the caller is
 * no longer admin; the surface reports the failure instead of enabling edits.
 */
export function useMemberCapabilities(workspaceId: string, enabled: boolean) {
  return useQuery({
    queryKey: memberCapabilitiesKey(workspaceId),
    queryFn: async () => {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/capabilities?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      // A failed read is unknown, not "holds nothing": an empty map would let
      // an edit replace what the person really holds (writes send the whole set).
      if (!res.ok) throw new Error(`Could not read what members may do (${res.status})`)
      const data = (await res.json()) as CapabilitiesBulkResponse
      const map: Record<string, string[]> = {}
      for (const m of data.members ?? []) {
        map[m.user_id] = m.capabilities ?? []
      }
      return map
    },
    enabled: Boolean(workspaceId) && enabled,
  })
}

// ── Grouping and stakes ──────────────────────────────────────────────

/**
 * The switches in a person's detail panel, grouped by what they touch. The
 * groups follow ALL_CAPABILITIES' low → high stakes order, so the
 * Credentials group — the one that can hand someone a plaintext secret —
 * is last and drawn in the destructive tone.
 */
export const CAPABILITY_GROUPS: { key: string; label: string; caps: CapabilityValue[]; highStakes?: boolean }[] = [
  { key: "routines", label: "Routines", caps: [Capability.RoutineCreate, Capability.RoutineRun] },
  {
    key: "content",
    label: "Content",
    caps: [Capability.Chat, Capability.IssueCreate, Capability.PageCreate, Capability.MemoryWrite, Capability.SkillCreate],
  },
  {
    key: "credentials",
    label: "Credentials",
    caps: [Capability.CredentialCreate, Capability.CredentialRotate, Capability.CredentialReveal],
    highStakes: true,
  },
]

/** Grants an admin should think twice about: the ones whose own descriptions
 *  call them high-stakes (skills run inside agent prompts; credentials reach
 *  the vault). Drawn in the destructive tone wherever they are listed. */
export const HIGH_STAKES_CAPABILITIES: ReadonlySet<CapabilityValue> = new Set<CapabilityValue>([
  Capability.SkillCreate,
  Capability.CredentialCreate,
  Capability.CredentialRotate,
  Capability.CredentialReveal,
])

/**
 * Why a capability cannot be toggled for this person, or null when it can.
 * "role" is the one grant every role carries (chat); "owner" and "self" are
 * whole-row locks; "readonly" is a caller who may look but not change.
 */
export type CapabilityLock = "role" | "owner" | "self" | "readonly" | null

export function capabilityLock(
  member: CapabilityMember,
  cap: CapabilityValue,
  currentUserId: string | undefined,
  canEdit = true,
): CapabilityLock {
  if (cap === Capability.Chat) return "role"
  if (member.role === "OWNER") return "owner"
  if (member.user.id === currentUserId) return "self"
  if (!canEdit) return "readonly"
  return null
}

/**
 * The accessible name of one capability control. `title` is unreliable for
 * screen-reader and keyboard users; this names the action a click would take,
 * or why there is none. Shared by the switches and the matrix cells.
 */
export function capabilityAriaLabel(cap: CapabilityValue, memberLabel: string, granted: boolean, lock: CapabilityLock): string {
  switch (lock) {
    case "role":
      return `Chat is always granted for ${memberLabel}`
    case "owner":
      return `OWNER capabilities are immutable: ${cap} for ${memberLabel}`
    case "self":
      return `You cannot modify your own capabilities: ${cap}`
    case "readonly":
      return `${memberLabel} ${granted ? "has" : "does not have"} ${cap}`
    default:
      return granted ? `Revoke ${cap} from ${memberLabel}` : `Grant ${cap} to ${memberLabel}`
  }
}

// ── Saving one grant ─────────────────────────────────────────────────

export interface ToggleCapabilityVars {
  userId: string
  cap: CapabilityValue
  next: boolean
}

/**
 * Grant or revoke one capability for one person. Optimistic: the cached set
 * flips now and rolls back on a refusal, with a sanitized toast.
 */
export function useToggleCapability(workspaceId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ userId, cap, next }: ToggleCapabilityVars) => {
      const body = next ? { grant: [cap] } : { revoke: [cap] }
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(userId)}/capabilities?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
      )
      // apiFetch resolves on 4xx/5xx — only a network failure rejects. A bare
      // `await` here would report every 403 as a successful grant.
      if (!res.ok) {
        // Sanitize the user-facing message — server bodies can carry SQL
        // fragments / stack traces that don't belong in a toast. Log the full
        // text dev-only for operator debugging.
        const text = await res.text().catch(() => "")
        if (text) devWarn("[capability PATCH] server error:", text)
        throw new Error(humanizePatchError(res.status))
      }
      return (await res.json()) as CapabilitiesResponse
    },
    onMutate: async ({ userId, cap, next }) => {
      // Optimistic: flip the control now, roll back in onError.
      await queryClient.cancelQueries({ queryKey: memberCapabilitiesKey(workspaceId) })
      const prev = queryClient.getQueryData<Record<string, string[]>>(
        memberCapabilitiesKey(workspaceId),
      )
      queryClient.setQueryData<Record<string, string[]>>(
        memberCapabilitiesKey(workspaceId),
        (old) => {
          if (!old) return old
          const current = new Set(old[userId] ?? [])
          if (next) current.add(cap)
          else current.delete(cap)
          return { ...old, [userId]: Array.from(current) }
        },
      )
      return { prev }
    },
    onError: (err, _vars, ctx) => {
      if (ctx?.prev) {
        queryClient.setQueryData(memberCapabilitiesKey(workspaceId), ctx.prev)
      }
      toast.error((err as Error).message)
    },
    onSuccess: (data, { userId }) => {
      // Sync with the server's canonical set in case the optimistic diff
      // missed a derived field (e.g. the server stripping a chat entry to
      // keep the stored form canonical).
      queryClient.setQueryData<Record<string, string[]>>(
        memberCapabilitiesKey(workspaceId),
        (old) => (old ? { ...old, [userId]: data.capabilities } : old),
      )
    },
  })
}

// ── The detail panel's switches ──────────────────────────────────────

/**
 * Every capability for one person as labelled switches, grouped Routines /
 * Content / Credentials, with each description inline. The old table put the
 * descriptions in a `title` attribute on a column header — invisible to
 * anyone deciding whether `credentials:reveal` is safe to hand out.
 */
export function MemberCapabilityToggles({
  member,
  workspaceId,
  currentUserId,
  granted,
  isLoading,
}: {
  member: CapabilityMember
  workspaceId: string
  currentUserId: string
  granted: string[]
  isLoading: boolean
}) {
  const grantedSet = useMemo(() => new Set(granted), [granted])
  const memberLabel = personLabel(member.user.full_name, member.user.email)
  const mutation = useToggleCapability(workspaceId)

  return (
    <div className="space-y-3">
      {CAPABILITY_GROUPS.map((group) => (
        <section key={group.key} aria-label={group.label}>
          <h4
            className={cn(
              "mb-1 font-mono text-[10.5px] font-medium uppercase tracking-[0.08em]",
              group.highStakes ? "text-destructive" : "text-muted-foreground-soft",
            )}
          >
            {group.label}
            {group.highStakes && <span className="ml-1.5 normal-case tracking-normal">· high-stakes</span>}
          </h4>
          <div className="flex flex-col">
            {group.caps.map((cap) => {
              // Same rule as everywhere: the stored set is the truth for every
              // row, OWNER included. The lock only decides whether it can change.
              const isGranted = cap === Capability.Chat || grantedSet.has(cap)
              const lock = capabilityLock(member, cap, currentUserId)
              const meta = CAPABILITY_LABELS[cap]
              return (
                <button
                  key={cap}
                  type="button"
                  role="switch"
                  aria-checked={isGranted}
                  aria-label={capabilityAriaLabel(cap, memberLabel, isGranted, lock)}
                  aria-disabled={lock !== null}
                  disabled={lock !== null || isLoading || mutation.isPending}
                  data-capability={cap}
                  onClick={() => mutation.mutate({ userId: member.user.id, cap, next: !isGranted })}
                  className={cn(
                    "flex items-start gap-2.5 rounded-md px-1.5 py-1.5 text-left transition-colors",
                    lock ? "cursor-not-allowed" : "cursor-pointer hover:bg-muted/40",
                  )}
                >
                  <span className="min-w-0 flex-1">
                    <span className={cn("block text-xs leading-tight", HIGH_STAKES_CAPABILITIES.has(cap) ? "text-destructive" : "text-foreground")}>
                      {meta.en}
                      <code className="ml-1.5 font-mono text-[10px] text-muted-foreground/80">{cap}</code>
                    </span>
                    <span className="mt-0.5 block text-[10px] leading-snug text-muted-foreground">
                      {lock === "role" ? "Comes with every role." : meta.description}
                    </span>
                  </span>
                  <SwitchTrack on={isGranted} locked={lock !== null} />
                </button>
              )
            })}
          </div>
        </section>
      ))}
    </div>
  )
}

/** The switch's look, drawn inside a real button so the whole row (label
 *  and description included) is the hit target. A lock replaces the thumb's
 *  emptiness when the grant cannot change, so "on and fixed" reads at once. */
function SwitchTrack({ on, locked }: { on: boolean; locked: boolean }) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "relative mt-0.5 inline-flex h-[1.15rem] w-8 shrink-0 items-center rounded-full transition-colors",
        on ? "bg-primary" : "bg-input",
        locked && "opacity-60",
      )}
    >
      <span
        className={cn(
          "grid size-4 place-items-center rounded-full bg-background shadow-xs transition-transform",
          on ? "translate-x-[calc(100%-2px)]" : "translate-x-px",
        )}
      >
        {locked && <Lock className="size-2.5 text-muted-foreground" />}
      </span>
    </span>
  )
}

// ── Workspace-wide presets ───────────────────────────────────────────

/**
 * Presets apply to every eligible member at once (eligible = not OWNER, not
 * the caller's own row), so they hang off the page's sub-bar rather than off a
 * person's row. Per-row preset selection is a future iteration.
 */
export function BulkPresetAction({
  members,
  workspaceId,
  currentUserId,
  capsByUser,
}: {
  members: CapabilityMember[]
  workspaceId: string
  currentUserId: string
  capsByUser: Record<string, string[]> | undefined
}) {
  const queryClient = useQueryClient()
  const [pending, setPending] = useState<CapabilityBundle | null>(null)

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          {/* Same weight as "Add member" beside it on the sub-bar: two equal
              page actions, neither the page's single main CTA. The label
              hides on a phone, so the accessible name stays on the button. */}
          <Button
            variant="outline"
            size="sm"
            className="h-7 gap-1.5 px-2.5 text-xs"
            aria-label="Bulk edit capabilities"
          >
            <SlidersHorizontal className="size-3.5" />
            <span className="hidden sm:inline">Bulk edit…</span>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-64">
          <DropdownMenuLabel className="text-[11px] font-normal text-muted-foreground">
            Replace capabilities for every member except owners and you
          </DropdownMenuLabel>
          <DropdownMenuSeparator />
          {(Object.keys(CAPABILITY_BUNDLES) as CapabilityBundle[]).map((bundle) => (
            <DropdownMenuItem
              key={bundle}
              className="text-xs capitalize"
              onSelect={() => setPending(bundle)}
            >
              {bundle}
              <span className="ml-auto font-mono text-[10px] text-muted-foreground">
                {CAPABILITY_BUNDLES[bundle].length} cap
                {CAPABILITY_BUNDLES[bundle].length === 1 ? "" : "s"}
              </span>
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>

      <PresetDiffDialog
        preset={pending}
        members={members}
        currentUserId={currentUserId}
        capsByUser={capsByUser}
        onCancel={() => setPending(null)}
        onConfirm={async () => {
          if (!pending) return
          await applyPresetAll(pending, members, workspaceId, currentUserId, queryClient)
          setPending(null)
        }}
      />
    </>
  )
}

// PresetDiffDialog renders the before/after capability diff for every member
// the preset would touch, so the admin sees the blast radius before
// committing. Bulk preset is irreversible (it overwrites per-row tuning).
//
// "Eligible" = not OWNER + not the caller's own row. OWNERs are immutable
// server-side; the caller's own row is immutable client-side (defence against
// downgrade-then-restore).
function PresetDiffDialog({
  preset,
  members,
  currentUserId,
  capsByUser,
  onCancel,
  onConfirm,
}: {
  preset: CapabilityBundle | null
  members: CapabilityMember[]
  currentUserId: string
  capsByUser: Record<string, string[]> | undefined
  onCancel: () => void
  onConfirm: () => void
}) {
  if (!preset) return null

  const target = new Set(CAPABILITY_BUNDLES[preset] as readonly string[])
  const eligible = members.filter(
    (m) => m.role !== "OWNER" && m.user.id !== currentUserId,
  )

  // Per-row diff: gains (in target, not current) + losses (current, not in
  // target). Rows with an empty diff are a server-side no-op — skip them.
  type rowDiff = {
    member: CapabilityMember
    current: string[]
    gains: string[]
    losses: string[]
  }
  const diffs: rowDiff[] = eligible
    .map((m) => {
      const current = (capsByUser?.[m.user.id] ?? []).slice().sort()
      const gains: string[] = []
      const losses: string[] = []
      for (const t of target) {
        if (!current.includes(t)) gains.push(t)
      }
      for (const c of current) {
        if (!target.has(c)) losses.push(c)
      }
      return { member: m, current, gains, losses }
    })
    .filter((d) => d.gains.length > 0 || d.losses.length > 0)

  const noChange = eligible.length > 0 && diffs.length === 0

  return (
    <Dialog open onOpenChange={(open) => !open && onCancel()}>
      <DialogContent className="sm:max-w-2xl max-h-[80dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            Apply preset &quot;{preset}&quot; ({CAPABILITY_BUNDLES[preset].length} cap
            {CAPABILITY_BUNDLES[preset].length === 1 ? "" : "s"})
          </DialogTitle>
          <DialogDescription>
            {eligible.length === 0
              ? "No eligible members (all OWNER or self)."
              : noChange
                ? `All ${eligible.length} eligible member(s) already match this preset — nothing to change.`
                : `${diffs.length} of ${eligible.length} eligible member(s) will be modified. Review below.`}
          </DialogDescription>
        </DialogHeader>

        {diffs.length > 0 && (
          <div className="space-y-3">
            <div className="text-[11px] text-muted-foreground">
              Target set:{" "}
              <code className="bg-muted px-1 py-0.5 rounded">
                {Array.from(target).sort().join(", ")}
              </code>
            </div>
            <div className="space-y-2 border border-border/60 rounded-md divide-y divide-border/40">
              {diffs.map((d) => (
                <div key={d.member.user.id} className="px-3 py-2 space-y-1.5">
                  <div className="flex items-center gap-2 text-xs">
                    <span className="font-medium">
                      {personLabel(d.member.user.full_name, d.member.user.email)}
                    </span>
                    <Badge variant="outline" className="text-[10px]">
                      {d.member.role}
                    </Badge>
                  </div>
                  {d.gains.length > 0 && (
                    <div className="flex items-center gap-1.5 text-[11px]">
                      <span className="text-success dark:text-success font-mono">
                        +{d.gains.length}
                      </span>
                      <ArrowRight className="h-3 w-3 text-muted-foreground" />
                      <code className="bg-success/[0.08] dark:bg-success/[0.08] text-success px-1 rounded text-[10px]">
                        {d.gains.join(", ")}
                      </code>
                    </div>
                  )}
                  {d.losses.length > 0 && (
                    <div className="flex items-center gap-1.5 text-[11px]">
                      <span className="text-destructive dark:text-destructive font-mono">
                        -{d.losses.length}
                      </span>
                      <ArrowRight className="h-3 w-3 text-muted-foreground" />
                      <code className="bg-destructive/[0.08] dark:bg-destructive/[0.08] text-destructive px-1 rounded text-[10px]">
                        {d.losses.join(", ")}
                      </code>
                    </div>
                  )}
                </div>
              ))}
            </div>
          </div>
        )}

        <DialogFooter>
          <Button type="button" variant="outline" onClick={onCancel}>
            Cancel
          </Button>
          <Button
            type="button"
            disabled={eligible.length === 0 || noChange}
            onClick={onConfirm}
          >
            Apply to {diffs.length} member{diffs.length === 1 ? "" : "s"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

async function applyPresetAll(
  preset: CapabilityBundle,
  members: CapabilityMember[],
  workspaceId: string,
  currentUserId: string,
  queryClient: ReturnType<typeof useQueryClient>,
) {
  const eligible = members.filter(
    (m) => m.role !== "OWNER" && m.user.id !== currentUserId,
  )
  if (eligible.length === 0) {
    toast.info("No eligible members (all OWNER or self).")
    return
  }
  // Confirmation lives in PresetDiffDialog above (visible before-and-after
  // diff). By the time we reach here the admin has clicked "Apply".
  //
  // Partition responses by resp.ok — apiFetch resolves on 4xx/5xx too (only
  // network errors reject), so a server-side rejection would otherwise be
  // toasted as success.
  type result = { id: string; ok: boolean; status: number; body: string }
  const results: result[] = await Promise.all(
    eligible.map(async (m): Promise<result> => {
      try {
        const resp = await apiFetch(
          `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(m.user.id)}/capabilities?workspace_id=${encodeURIComponent(workspaceId)}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ preset }),
          },
        )
        const body = resp.ok ? "" : await resp.text().catch(() => "")
        return { id: m.user.id, ok: resp.ok, status: resp.status, body }
      } catch (err) {
        return { id: m.user.id, ok: false, status: 0, body: (err as Error).message }
      }
    }),
  )
  const ok = results.filter((r) => r.ok).length
  const failed = results.filter((r) => !r.ok)
  if (ok > 0) {
    queryClient.invalidateQueries({ queryKey: memberCapabilitiesKey(workspaceId) })
  }
  if (failed.length === 0) {
    toast.success(`Applied "${preset}" to ${ok} member(s)`)
    return
  }
  if (ok === 0) {
    // Whole batch failed — log raw bodies dev-only, surface a sanitized
    // message + status to the toast. Server response text never goes into
    // the UI directly (see humanizePatchError).
    for (const f of failed) {
      devWarn(`[bulk preset] member ${f.id} failed: HTTP ${f.status}`, f.body)
    }
    toast.error(
      `Bulk preset failed for all ${failed.length} member(s): ${humanizePatchError(failed[0].status)}`,
    )
    return
  }
  // Partial success — surface both counts so the admin knows the cache
  // invalidate ran but some rows still need their attention.
  toast.warning(
    `Applied "${preset}" to ${ok}/${eligible.length} member(s); ${failed.length} failed.`,
  )
}

// humanizePatchError maps an HTTP status from the capability PATCH endpoint
// onto a UI-safe message. The raw server body can include SQL fragments /
// stack traces / internal field names that don't belong in an end-user toast
// — the operator finds the full detail in the console / server logs.
export function humanizePatchError(status: number): string {
  switch (status) {
    case 400:
      return "Invalid request. Check capability names and try again."
    case 401:
      return "Your session expired. Reload and sign in again."
    case 403:
      return "Permission denied. Your admin role may have been revoked."
    case 404:
      return "Member no longer exists in this workspace."
    case 413:
      return "Request too large. Reduce the change set."
    case 500:
      return "Server error. See the operator log for details."
    default:
      return `Request failed (HTTP ${status}).`
  }
}
