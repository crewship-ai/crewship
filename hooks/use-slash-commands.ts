"use client"

import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"

/**
 * Server-driven slash command catalog. Mirror of the response shape
 * SlashCommandsHandler.List returns; see internal/api/slash_commands_handler.go.
 *
 * Capability-filtered server-side: the client only ever sees the
 * actions the caller's workspace_members.capabilities row grants.
 * The chat composer renders this list as the "Actions" group of the
 * slash palette (components/features/chat/composer/slash-palette.tsx).
 */

export interface SlashFormField {
  name: string
  type: string
  required?: boolean
  default?: string
}

/**
 * SlashActionSchema is the server-driven entry. Renamed from
 * `SlashCommand` in this file because slash-palette.tsx already
 * exports a different `SlashCommand` shape (client-side palette
 * row with `icon: ComponentType`, `group`, `run`); two unrelated
 * types under the same name made imports ambiguous. The wire shape
 * is intentionally narrower than a
 * full UI row — the icon string is resolved to a component at
 * render time by the consumer.
 */
export interface SlashActionSchema {
  id: string
  label: string
  /** Czech label; React picks based on user locale. Falls back to `label`. */
  label_cs?: string
  /** Lucide icon name. The chat composer resolves to a component. */
  icon?: string
  capability: string
  form_schema?: SlashFormField[]
}

/**
 * @deprecated Use SlashActionSchema. Kept as a type alias for one
 * release so any external import keeps compiling; remove in a
 * follow-up PR once dashboard consumers are migrated.
 */
export type SlashCommand = SlashActionSchema

/**
 * Fetch the slash command catalog for the active workspace.
 *
 * 5 min stale-time matches the server-side capability cache TTL
 * (capabilities_check.go uses 30s but the admin grant UI also calls
 * InvalidateCapabilityCache server-side, so stale UI data within a
 * 5 min window is acceptable — palette opens lag by < 5 s in
 * practice because we also refetch on window focus).
 */
export function useSlashCommands(workspaceId: string | null | undefined) {
  return useQuery({
    queryKey: ["slash-commands", workspaceId],
    enabled: Boolean(workspaceId),
    staleTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
    queryFn: async () => {
      const res = await apiFetch(
        `/api/v1/slash-commands?workspace_id=${encodeURIComponent(workspaceId!)}`,
      )
      if (!res.ok) {
        throw new Error(`slash-commands fetch failed: ${res.status}`)
      }
      return (await res.json()) as SlashActionSchema[]
    },
  })
}
