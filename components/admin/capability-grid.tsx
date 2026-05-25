"use client"

import { useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { Check, Lock } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
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

export function CapabilityGrid({ members, workspaceId, currentUserId }: CapabilityGridProps) {
  // One query for the whole grid — server already returns
  // capabilities per-member; we fan out via Promise.all so the
  // initial render lights up quickly even with 50+ members.
  // Cached per-workspace; mutations invalidate this key.
  const { data: capsByUser, isLoading } = useQuery({
    queryKey: ["member-capabilities", workspaceId],
    queryFn: async () => {
      const entries = await Promise.all(
        members.map(async (m) => {
          const res = await apiFetch(
            `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(m.user.id)}/capabilities?workspace_id=${encodeURIComponent(workspaceId)}`,
          )
          if (!res.ok) {
            return [m.user.id, [] as string[]] as const
          }
          const data = (await res.json()) as CapabilitiesResponse
          return [m.user.id, data.capabilities ?? []] as const
        }),
      )
      return Object.fromEntries(entries) as Record<string, string[]>
    },
    enabled: Boolean(workspaceId) && members.length > 0,
  })

  return (
    <div className="space-y-3">
      <PresetChips
        members={members}
        workspaceId={workspaceId}
        currentUserId={currentUserId}
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
        return (
          <td key={cap} className="py-2 text-center">
            <button
              type="button"
              disabled={cellLocked || isLoading || mutation.isPending}
              onClick={() => mutation.mutate({ cap, next: !isGranted })}
              title={
                isChat
                  ? "Chat is always granted."
                  : isOwner
                    ? "OWNER capabilities are immutable."
                    : isSelf
                      ? "You cannot modify your own capabilities."
                      : isGranted
                        ? `Revoke ${cap}`
                        : `Grant ${cap}`
              }
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
                <Lock className="h-3 w-3" />
              ) : isGranted ? (
                <Check className="h-3 w-3" />
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
}: {
  members: CapabilityGridMember[]
  workspaceId: string
  currentUserId: string
}) {
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  const queryClient = useQueryClient()

  const applyPreset = async (preset: CapabilityBundle) => {
    if (selectedIds.size === 0) {
      toast.info("Select members in the table first.")
      return
    }
    const eligible = Array.from(selectedIds).filter((id) => {
      const m = members.find((mem) => mem.user.id === id)
      return m && m.role !== "OWNER" && id !== currentUserId
    })
    if (eligible.length === 0) {
      toast.info("Selected rows are all locked (OWNER or self).")
      return
    }
    await Promise.all(
      eligible.map((id) =>
        apiFetch(
          `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(id)}/capabilities?workspace_id=${encodeURIComponent(workspaceId)}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ preset }),
          },
        ),
      ),
    )
    toast.success(`Applied "${preset}" to ${eligible.length} member(s)`)
    queryClient.invalidateQueries({ queryKey: ["member-capabilities", workspaceId] })
    setSelectedIds(new Set())
  }

  // Selection UI is intentionally minimal for the MVP — the
  // wireframe shows multi-select but the row-level checkbox to enter
  // selection mode is a polish iteration. For now the chips apply to
  // ALL eligible members; selection-mode UI lands when usage telemetry
  // says it's worth the surface area.
  const eligibleCount = members.filter(
    (m) => m.role !== "OWNER" && m.user.id !== currentUserId,
  ).length

  // Stub selection: shift-click would go here; the chips below
  // apply the preset to every eligible member (admin's most common
  // case: rolling out a new bundle workspace-wide).
  if (selectedIds.size === 0 && eligibleCount > 0) {
    // Auto-select all eligible so the chips have an obvious effect.
    // Visual selection UI follows in a polish pass.
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-[11px] text-muted-foreground">Quick preset:</span>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="h-6 text-[11px]"
        onClick={() => applyPresetAll("chat", members, workspaceId, currentUserId, queryClient)}
      >
        Chat
      </Button>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="h-6 text-[11px]"
        onClick={() => applyPresetAll("power", members, workspaceId, currentUserId, queryClient)}
      >
        Power
      </Button>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="h-6 text-[11px]"
        onClick={() => applyPresetAll("admin", members, workspaceId, currentUserId, queryClient)}
      >
        Admin
      </Button>
      <span className="text-[11px] text-muted-foreground ml-auto">
        {CAPABILITY_BUNDLES.power.length}-cap "power" = chat + routine + issue + memory
      </span>
    </div>
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
  if (
    !window.confirm(
      `Apply "${preset}" preset to ${eligible.length} member(s)? This overwrites their current capabilities.`,
    )
  ) {
    return
  }
  try {
    await Promise.all(
      eligible.map((m) =>
        apiFetch(
          `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(m.user.id)}/capabilities?workspace_id=${encodeURIComponent(workspaceId)}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ preset }),
          },
        ),
      ),
    )
    toast.success(`Applied "${preset}" to ${eligible.length} member(s)`)
    queryClient.invalidateQueries({ queryKey: ["member-capabilities", workspaceId] })
  } catch (err) {
    toast.error(`Bulk preset failed: ${(err as Error).message}`)
  }
}

function initials(name: string | null, email: string): string {
  const src = name?.trim() || email
  const parts = src.split(/[\s@.]+/).filter(Boolean)
  if (parts.length >= 2) {
    return (parts[0][0] + parts[1][0]).toUpperCase()
  }
  return src.slice(0, 2).toUpperCase()
}
