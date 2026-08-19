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
  /** The widget to draw: text, textarea, cron, timezone, secret, slug,
   *  priority, number, boolean, … It says what the user sees, not what
   *  the server receives. */
  type: string
  required?: boolean
  /** Always a string — a form field's value is a string. A non-string
   *  default is formatted into one server-side and parsed back out via
   *  `value_type`. */
  default?: string
  /**
   * The JSON type the server expects back for this field: string |
   * integer | number | boolean | array | object. Absent means string,
   * which is every field in the static catalog.
   *
   * `type` cannot answer this. A routine's `array` input and an issue's
   * `description` both draw a textarea, and one of them has to reach the
   * server as a parsed JSON array while the other must not — so the wire
   * carries both. See lib/routine-inputs.ts for the conversion.
   */
  value_type?: string
  /** Rendered under the field. Server-supplied for a routine input that
   *  declares a description; the static catalog carries none. */
  help?: string
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
