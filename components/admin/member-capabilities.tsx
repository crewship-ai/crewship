"use client"

import { useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { Check, Lock, ArrowRight, SlidersHorizontal } from "lucide-react"

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
  ALL_CAPABILITIES,
  CAPABILITY_BUNDLES,
  CAPABILITY_LABELS,
  Capability,
  type CapabilityBundle,
  type CapabilityValue,
} from "@/lib/capabilities"

/**
 * Per-member capabilities for Settings → Members.
 *
 * This used to be `capability-grid.tsx`: a second list of the same people
 * the roster above already listed, as a horizontally-scrolling table with a
 * sticky first column. Two lists keyed by the same identity meant answering
 * "what can this person do?" required reading both and reconciling them
 * (#1517). The table is gone; what is left are the three pieces the Members
 * roster composes into ONE row per person:
 *
 *   - `useMemberCapabilities` — the bulk query, still one round-trip for the
 *     whole roster (the N+1 fan-out this replaced was quadratic for the
 *     500-user tenant the PRD targets).
 *   - `CapabilityPips` — the collapsed-row summary. Fixed columns in
 *     `ALL_CAPABILITIES` order, so a single capability is still scannable
 *     DOWN the roster: the one thing a table was good at, kept at ~60px
 *     instead of a scrolling grid.
 *   - `MemberCapabilityToggles` — the expanded-row control. Inline toggle,
 *     no save button: each click is its own PATCH so one failure doesn't
 *     roll back the row. Optimistic, with rollback on 4xx.
 *   - `BulkPresetAction` — workspace-wide presets. They act on the whole
 *     roster, so they belong to the card header rather than to any row.
 *
 * OWNER capabilities are locked (server PATCH would 403); the caller's own
 * row is locked (defence against downgrade-then-restore); the chat column is
 * always checked + disabled.
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
 * A 403 means the caller is no longer admin; the surfaces that render
 * capabilities are admin-gated, so the outcome is a brief flash rather than
 * a thrown error. Resolve empty instead of rejecting.
 */
export function useMemberCapabilities(workspaceId: string, enabled: boolean) {
  return useQuery({
    queryKey: memberCapabilitiesKey(workspaceId),
    queryFn: async () => {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/capabilities?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (!res.ok) return {} as Record<string, string[]>
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

// ── Collapsed-row summary ────────────────────────────────────────────

/**
 * Eight pips, one per capability, in `ALL_CAPABILITIES` order — the same
 * x-position in every row, so scanning a column down the roster still works
 * without a table. `chat` renders in the muted treatment because it is
 * implied and can never be toggled.
 */
export function CapabilityPips({
  granted,
  isOwner,
  label,
}: {
  granted: string[]
  isOwner: boolean
  label: string
}) {
  const grantedSet = useMemo(() => new Set(granted), [granted])
  const on = ALL_CAPABILITIES.filter(
    (c) => isOwner || c === Capability.Chat || grantedSet.has(c),
  )
  const summary = isOwner
    ? `${label}: all capabilities (OWNER)`
    : `${label}: ${on.join(", ")}`

  return (
    <span
      // role="img" so the summary is announced: aria-label on a bare span is
      // not reliably exposed, and the pips themselves carry no text.
      role="img"
      className="flex items-center gap-[3px]"
      title={summary}
      aria-label={summary}
    >
      {ALL_CAPABILITIES.map((cap) => {
        const isChat = cap === Capability.Chat
        const active = isOwner || isChat || grantedSet.has(cap)
        return (
          <span
            key={cap}
            aria-hidden="true"
            data-capability={cap}
            data-granted={active ? "true" : "false"}
            className={cn(
              "h-1.5 w-1.5 rounded-[2px]",
              !active && "bg-input/70",
              active && isChat && "bg-muted-foreground/60",
              active && !isChat && "bg-primary",
            )}
          />
        )
      })}
    </span>
  )
}

// ── Expanded-row control ─────────────────────────────────────────────

/**
 * The eight capability grants for one person, as a two-column checklist
 * with the description inline. The old table put the descriptions in a
 * `title` attribute on a column header — invisible to anyone deciding
 * whether `credentials:reveal` is safe to hand out.
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
  const isSelf = member.user.id === currentUserId
  const isOwner = member.role === "OWNER"
  const locked = isSelf || isOwner

  const queryClient = useQueryClient()
  const grantedSet = useMemo(() => new Set(granted), [granted])
  const memberLabel = personLabel(member.user.full_name, member.user.email)

  const mutation = useMutation({
    mutationFn: async ({ cap, next }: { cap: CapabilityValue; next: boolean }) => {
      const body = next ? { grant: [cap] } : { revoke: [cap] }
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(member.user.id)}/capabilities?workspace_id=${encodeURIComponent(workspaceId)}`,
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
    onMutate: async ({ cap, next }) => {
      // Optimistic: flip the checkbox now, roll back in onError.
      await queryClient.cancelQueries({ queryKey: memberCapabilitiesKey(workspaceId) })
      const prev = queryClient.getQueryData<Record<string, string[]>>(
        memberCapabilitiesKey(workspaceId),
      )
      queryClient.setQueryData<Record<string, string[]>>(
        memberCapabilitiesKey(workspaceId),
        (old) => {
          if (!old) return old
          const current = new Set(old[member.user.id] ?? [])
          if (next) current.add(cap)
          else current.delete(cap)
          return { ...old, [member.user.id]: Array.from(current) }
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
    onSuccess: (data) => {
      // Sync with the server's canonical set in case the optimistic diff
      // missed a derived field (e.g. the server stripping a chat entry to
      // keep the stored form canonical).
      queryClient.setQueryData<Record<string, string[]>>(
        memberCapabilitiesKey(workspaceId),
        (old) => (old ? { ...old, [member.user.id]: data.capabilities } : old),
      )
    },
  })

  return (
    <div className="grid gap-x-5 gap-y-0.5 sm:grid-cols-2">
      {ALL_CAPABILITIES.map((cap) => {
        const isChat = cap === Capability.Chat
        const isGranted = isOwner || isChat || grantedSet.has(cap)
        const cellLocked = locked || isChat
        const meta = CAPABILITY_LABELS[cap]
        // `title` is unreliable for screen-reader and keyboard users;
        // aria-label carries the accessible name and role="switch" +
        // aria-checked expose the toggle state.
        const ariaLabel = isChat
          ? `Chat is always granted for ${memberLabel}`
          : isOwner
            ? `OWNER capabilities are immutable: ${cap} for ${memberLabel}`
            : isSelf
              ? `You cannot modify your own capabilities: ${cap}`
              : isGranted
                ? `Revoke ${cap} from ${memberLabel}`
                : `Grant ${cap} to ${memberLabel}`
        return (
          <button
            key={cap}
            type="button"
            role="switch"
            aria-checked={isGranted}
            aria-label={ariaLabel}
            aria-disabled={cellLocked}
            disabled={cellLocked || isLoading || mutation.isPending}
            onClick={() => mutation.mutate({ cap, next: !isGranted })}
            className={cn(
              "flex items-start gap-2.5 rounded-md px-1.5 py-1.5 text-left transition-colors",
              cellLocked ? "cursor-not-allowed" : "cursor-pointer hover:bg-muted/40",
            )}
          >
            <span
              aria-hidden="true"
              className={cn(
                "mt-px inline-flex size-4 shrink-0 items-center justify-center rounded-[4px] border transition-colors",
                isGranted
                  ? "bg-primary border-primary text-primary-foreground"
                  // Translucent, matching components/ui/checkbox — bg-background
                  // is the page colour and renders as a dark square on a card.
                  : "bg-input/30 border-border",
                cellLocked && "opacity-60",
              )}
            >
              {isChat ? (
                <Lock className="size-2.5" />
              ) : isGranted ? (
                <Check className="size-3" />
              ) : null}
            </span>
            <span className="min-w-0">
              <span className="block text-xs leading-tight text-foreground">
                {meta.en}
                <code className="ml-1.5 font-mono text-[10px] text-muted-foreground/80">
                  {cap}
                </code>
              </span>
              <span className="mt-0.5 block text-[10px] leading-snug text-muted-foreground">
                {meta.description}
              </span>
            </span>
          </button>
        )
      })}
    </div>
  )
}

// ── Workspace-wide presets ───────────────────────────────────────────

/**
 * Presets apply to every eligible member at once (eligible = not OWNER, not
 * the caller's own row), so they hang off the card header rather than off a
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
          <Button
            variant="ghost"
            size="sm"
            className="h-7 gap-1.5 px-2 text-[11px] text-muted-foreground"
          >
            <SlidersHorizontal className="size-3.5" />
            <span className="hidden sm:inline">Bulk preset</span>
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
      <DialogContent className="sm:max-w-2xl max-h-[80vh] overflow-y-auto">
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
