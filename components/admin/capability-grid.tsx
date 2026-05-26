"use client"

import { useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { Check, Lock, ArrowRight } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { apiFetch } from "@/lib/api-fetch"
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
 * Per-member capability checkbox grid for the workspace Members
 * settings tab. Admin-only — the caller is responsible for hiding
 * this when the current user is not ADMIN+.
 *
 * Rows are workspace members (passed in via props so the parent
 * controls the membership query); columns are capabilities ordered
 * low-stakes → high-stakes (the order matches lib/capabilities.ts).
 *
 * Inline toggle, no save button — each click is its own PATCH so a
 * single failure doesn't roll back the whole row. Optimistic UI
 * with rollback on 4xx.
 *
 * OWNER capabilities are visually locked (server PATCH would 403);
 * caller's own row is locked (defence against downgrade-then-restore).
 * Chat column always shows checked + disabled.
 */

interface CapabilityGridMember {
  id: string
  user: {
    id: string
    email: string
    full_name: string | null
    avatar_url: string | null
  }
  role: string
}

interface CapabilityGridProps {
  members: CapabilityGridMember[]
  workspaceId: string
  currentUserId: string
}

interface CapabilitiesResponse {
  user_id: string
  role: string
  capabilities: string[]
}

interface CapabilitiesBulkResponse {
  members: CapabilitiesResponse[]
}

export function CapabilityGrid({ members, workspaceId, currentUserId }: CapabilityGridProps) {
  // ONE round-trip for the whole grid via the bulk endpoint. The
  // previous N+1 fan-out (one GET per member) was tolerable for 5
  // members but quadratic for the 500-user Microsoft tenant the
  // PRD targets. The bulk endpoint runs one SELECT server-side and
  // a single admin-role check, so a 500-member workspace render
  // costs 1 HTTP call instead of 500.
  const { data: capsByUser, isLoading } = useQuery({
    queryKey: ["member-capabilities", workspaceId],
    queryFn: async () => {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/capabilities?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (!res.ok) {
        // 403 here means the user is no longer admin; the parent
        // surface hides this component for non-admins so the
        // outcome is a brief flash. Return empty so the table
        // renders without throwing.
        return {} as Record<string, string[]>
      }
      const data = (await res.json()) as CapabilitiesBulkResponse
      const map: Record<string, string[]> = {}
      for (const m of data.members ?? []) {
        map[m.user_id] = m.capabilities ?? []
      }
      return map
    },
    enabled: Boolean(workspaceId) && members.length > 0,
  })

  return (
    <div className="space-y-3">
      <PresetChips
        members={members}
        workspaceId={workspaceId}
        currentUserId={currentUserId}
        capsByUser={capsByUser}
      />
      <div className="overflow-x-auto">
        <table className="w-full text-xs border-collapse">
          <thead>
            <tr className="border-b border-border/60">
              <th className="text-left pl-3 py-2 font-medium text-muted-foreground sticky left-0 bg-background z-10 min-w-[180px]">
                Member
              </th>
              <th className="text-left py-2 font-medium text-muted-foreground min-w-[80px]">
                Role
              </th>
              {ALL_CAPABILITIES.map((cap) => (
                <th
                  key={cap}
                  className="py-2 px-2 font-medium text-muted-foreground text-center min-w-[100px]"
                  title={CAPABILITY_LABELS[cap].description}
                >
                  <div className="text-[10px] font-mono">{cap}</div>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {members.map((m) => (
              <CapabilityRow
                key={m.id}
                member={m}
                workspaceId={workspaceId}
                currentUserId={currentUserId}
                granted={capsByUser?.[m.user.id] ?? []}
                isLoading={isLoading}
              />
            ))}
          </tbody>
        </table>
      </div>
      <p className="text-[10px] text-muted-foreground">
        Click any checkbox to toggle. Changes apply immediately; the affected member sees updates within 30 s.
      </p>
    </div>
  )
}

function CapabilityRow({
  member,
  workspaceId,
  currentUserId,
  granted,
  isLoading,
}: {
  member: CapabilityGridMember
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

  const mutation = useMutation({
    mutationFn: async ({
      cap,
      next,
    }: {
      cap: CapabilityValue
      next: boolean
    }) => {
      const body = next ? { grant: [cap] } : { revoke: [cap] }
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(member.user.id)}/capabilities?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
      )
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `PATCH failed: ${res.status}`)
      }
      return (await res.json()) as CapabilitiesResponse
    },
    onMutate: async ({ cap, next }) => {
      // Optimistic update — mutate the cached entry so the checkbox
      // flips immediately. Rollback on error via onError.
      await queryClient.cancelQueries({ queryKey: ["member-capabilities", workspaceId] })
      const prev = queryClient.getQueryData<Record<string, string[]>>(
        ["member-capabilities", workspaceId],
      )
      queryClient.setQueryData<Record<string, string[]>>(
        ["member-capabilities", workspaceId],
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
        queryClient.setQueryData(["member-capabilities", workspaceId], ctx.prev)
      }
      toast.error((err as Error).message)
    },
    onSuccess: (data) => {
      // Sync with the server's canonical set in case our optimistic
      // diff missed a derived field (e.g. server stripping a chat
      // entry to keep the stored form canonical).
      queryClient.setQueryData<Record<string, string[]>>(
        ["member-capabilities", workspaceId],
        (old) => {
          if (!old) return old
          return { ...old, [member.user.id]: data.capabilities }
        },
      )
    },
  })

  return (
    <tr className="border-b border-border/40 hover:bg-muted/30">
      <td className="pl-3 py-2 sticky left-0 bg-background z-10">
        <div className="flex items-center gap-2">
          <div className="h-6 w-6 shrink-0 rounded-full bg-primary/80 flex items-center justify-center">
            <span className="text-[10px] font-semibold text-primary-foreground">
              {initials(member.user.full_name, member.user.email)}
            </span>
          </div>
          <div className="min-w-0">
            <div className="text-xs truncate">
              {member.user.full_name ?? member.user.email}
            </div>
            {member.user.full_name && (
              <div className="text-[10px] text-muted-foreground/80 font-mono truncate">
                {member.user.email}
              </div>
            )}
          </div>
        </div>
      </td>
      <td className="py-2">
        <Badge variant="outline" className="text-[10px]">
          {member.role}
        </Badge>
      </td>
      {ALL_CAPABILITIES.map((cap) => {
        const isChat = cap === Capability.Chat
        const isGranted = isChat || grantedSet.has(cap)
        const cellLocked = locked || isChat
        const memberLabel = member.user.full_name ?? member.user.email
        // title attribute is unreliable for screen
        // readers / keyboard users. aria-label provides the
        // accessible name; aria-pressed exposes the toggle state so
        // assistive tech announces "Routine create, pressed" rather
        // than just "button". role="switch" is the WAI-ARIA pattern
        // for a binary on/off control.
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
          <td key={cap} className="py-2 text-center">
            <button
              type="button"
              role="switch"
              aria-checked={isGranted}
              aria-label={ariaLabel}
              aria-disabled={cellLocked}
              disabled={cellLocked || isLoading || mutation.isPending}
              onClick={() => mutation.mutate({ cap, next: !isGranted })}
              title={ariaLabel}
              className={cn(
                "inline-flex h-5 w-5 items-center justify-center rounded border transition-colors",
                isGranted
                  ? "bg-primary border-primary text-primary-foreground"
                  : "bg-background border-border",
                cellLocked && "opacity-60 cursor-not-allowed",
                !cellLocked && "cursor-pointer hover:border-primary/60",
              )}
            >
              {isChat ? (
                <Lock className="h-3 w-3" aria-hidden="true" />
              ) : isGranted ? (
                <Check className="h-3 w-3" aria-hidden="true" />
              ) : null}
            </button>
          </td>
        )
      })}
    </tr>
  )
}

function PresetChips({
  members,
  workspaceId,
  currentUserId,
  capsByUser,
}: {
  members: CapabilityGridMember[]
  workspaceId: string
  currentUserId: string
  capsByUser: Record<string, string[]> | undefined
}) {
  // Quick presets apply workspace-wide to every eligible member
  // (excludes OWNER + self). Per-row selection mode is a future
  // iteration.
  const queryClient = useQueryClient()
  const [pending, setPending] = useState<CapabilityBundle | null>(null)

  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[11px] text-muted-foreground">Quick preset:</span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-6 text-[11px]"
          onClick={() => setPending("chat")}
        >
          Chat
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-6 text-[11px]"
          onClick={() => setPending("power")}
        >
          Power
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-6 text-[11px]"
          onClick={() => setPending("admin")}
        >
          Admin
        </Button>
        <span className="text-[11px] text-muted-foreground ml-auto">
          {CAPABILITY_BUNDLES.power.length}-cap &quot;power&quot; = chat + routine + issue + memory
        </span>
      </div>
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

// PresetDiffDialog renders the before/after capability diff for every
// member the preset would touch, so the admin can see exactly which
// rows lose / gain which capabilities before committing. Replaces the
// previous window.confirm — bulk preset is irreversible (overwrites
// per-row tuning) and the operator needs to see the blast radius.
//
// "Eligible" = not OWNER + not caller's own row. OWNERs are immutable
// server-side; caller's own row is immutable client-side (defence
// against downgrade-then-restore).
function PresetDiffDialog({
  preset,
  members,
  currentUserId,
  capsByUser,
  onCancel,
  onConfirm,
}: {
  preset: CapabilityBundle | null
  members: CapabilityGridMember[]
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

  // Per-row diff: gains (in target, not currently) + losses (in
  // current, not in target). Skip rows where the diff is empty —
  // applying the preset to them is a server-side no-op.
  type rowDiff = {
    member: CapabilityGridMember
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
                      {d.member.user.full_name ?? d.member.user.email}
                    </span>
                    <Badge variant="outline" className="text-[10px]">
                      {d.member.role}
                    </Badge>
                  </div>
                  {d.gains.length > 0 && (
                    <div className="flex items-center gap-1.5 text-[11px]">
                      <span className="text-emerald-600 dark:text-emerald-400 font-mono">
                        +{d.gains.length}
                      </span>
                      <ArrowRight className="h-3 w-3 text-muted-foreground" />
                      <code className="bg-emerald-50 dark:bg-emerald-950/40 text-emerald-700 dark:text-emerald-300 px-1 rounded text-[10px]">
                        {d.gains.join(", ")}
                      </code>
                    </div>
                  )}
                  {d.losses.length > 0 && (
                    <div className="flex items-center gap-1.5 text-[11px]">
                      <span className="text-rose-600 dark:text-rose-400 font-mono">
                        -{d.losses.length}
                      </span>
                      <ArrowRight className="h-3 w-3 text-muted-foreground" />
                      <code className="bg-rose-50 dark:bg-rose-950/40 text-rose-700 dark:text-rose-300 px-1 rounded text-[10px]">
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
  members: CapabilityGridMember[],
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
  // Confirmation lives in the PresetDiffDialog above (visible
  // before-and-after diff). By the time we reach here the admin
  // has clicked "Apply" on that dialog.
  //
  // Partition responses by resp.ok — apiFetch resolves on 4xx/5xx
  // too (only network errors reject), so a server-side rejection
  // (typo, OWNER target slipping through, ...) would otherwise be
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
        return {
          id: m.user.id,
          ok: false,
          status: 0,
          body: (err as Error).message,
        }
      }
    }),
  )
  const ok = results.filter((r) => r.ok).length
  const failed = results.filter((r) => !r.ok)
  if (ok > 0) {
    queryClient.invalidateQueries({ queryKey: ["member-capabilities", workspaceId] })
  }
  if (failed.length === 0) {
    toast.success(`Applied "${preset}" to ${ok} member(s)`)
    return
  }
  if (ok === 0) {
    // Whole batch failed — show the first body so the admin sees the
    // actual server message (typo, role too low, etc.) rather than
    // a generic "bulk failed".
    toast.error(
      `Bulk preset failed (${failed.length}/${eligible.length}): ${failed[0].body || `HTTP ${failed[0].status}`}`,
    )
    return
  }
  // Partial success — surface both counts so the admin knows the
  // cache invalidate ran but some rows still need their attention.
  toast.warning(
    `Applied "${preset}" to ${ok}/${eligible.length} member(s); ${failed.length} failed.`,
  )
}

function initials(name: string | null, email: string): string {
  const src = name?.trim() || email
  const parts = src.split(/[\s@.]+/).filter(Boolean)
  if (parts.length >= 2) {
    return (parts[0][0] + parts[1][0]).toUpperCase()
  }
  return src.slice(0, 2).toUpperCase()
}
