"use client"

/**
 * Settings → Access & Secrets — PRD-CREDENTIALS-V2-2026 §2.6, wireframe
 * screen 8.
 *
 * This is workspace-wide security policy, so it lives here and not on the
 * page for one secret (§7 decision #5). The role split mirrors the server
 * exactly, and the two halves are NOT the same:
 *
 *   · reading the policy  → GET /credentials/reveal-policy, MANAGER+
 *   · changing the switch → PUT /credentials/reveal-policy, OWNER only
 *     (`role != "OWNER"` — a literal string comparison in SetPolicy, not the
 *     usual canRole ladder, so an ADMIN really is refused)
 *
 * A MANAGER has to know the rules they work under; letting them relax those
 * rules would defeat the point. So MANAGER and ADMIN both see the switch and
 * neither can move it — ADMIN is told why, because "disabled with no
 * explanation" is how a control gets reported as broken.
 */

import * as React from "react"
import { AlertTriangle, ShieldAlert } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Switch } from "@/components/ui/switch"
import { Spinner } from "@/components/ui/spinner"
import { SettingsCard, SettingsEmpty, SettingsRow } from "@/components/features/settings/shared"
import { apiFetch } from "@/lib/api-fetch"
import { Capability } from "@/lib/capabilities"

export interface AccessSecretsMember {
  id: string
  role: string
  user: { id: string; email: string; full_name: string | null }
}

export interface AccessSecretsSectionProps {
  workspaceId: string
  /** The caller's workspace role. */
  role: string | null | undefined
  members: AccessSecretsMember[]
}

interface BulkCapabilities {
  members?: { user_id: string; role: string; capabilities: string[] }[]
}

export function AccessSecretsSection({ workspaceId, role, members }: AccessSecretsSectionProps) {
  const isOwner = role === "OWNER"

  const [enabled, setEnabled] = React.useState<boolean | null>(null)
  const [policyError, setPolicyError] = React.useState<string | null>(null)
  const [saving, setSaving] = React.useState(false)
  const [capsByUser, setCapsByUser] = React.useState<Record<string, string[]> | null>(null)

  React.useEffect(() => {
    let cancelled = false
    apiFetch(`/api/v1/credentials/reveal-policy?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then(async (res) => {
        if (!res.ok) throw new Error(String(res.status))
        return (await res.json()) as { enabled?: boolean }
      })
      .then((body) => { if (!cancelled) setEnabled(Boolean(body?.enabled)) })
      .catch(() => {
        // Never guess. "We could not read the policy" is a different thing
        // from "reveal is off", and rendering the second would tell an
        // operator their workspace is safe when we have no idea.
        if (!cancelled) setPolicyError("Couldn't read the reveal policy for this workspace.")
      })
    return () => { cancelled = true }
  }, [workspaceId])

  React.useEffect(() => {
    let cancelled = false
    apiFetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/capabilities` +
        `?workspace_id=${encodeURIComponent(workspaceId)}`,
    )
      .then(async (res) => (res.ok ? ((await res.json()) as BulkCapabilities) : null))
      .then((body) => {
        if (cancelled) return
        if (!body) { setCapsByUser({}); return }
        const map: Record<string, string[]> = {}
        for (const m of body.members ?? []) map[m.user_id] = m.capabilities ?? []
        setCapsByUser(map)
      })
      .catch(() => { if (!cancelled) setCapsByUser({}) })
    return () => { cancelled = true }
  }, [workspaceId])

  async function toggle(next: boolean) {
    setSaving(true)
    setPolicyError(null)
    try {
      const res = await apiFetch(
        `/api/v1/credentials/reveal-policy?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ enabled: next }),
        },
      )
      const data = (await res.json().catch(() => ({}))) as { enabled?: boolean; error?: string }
      if (!res.ok) {
        setPolicyError(typeof data.error === "string" ? data.error : `Request failed (${res.status})`)
        return
      }
      setEnabled(Boolean(data.enabled))
    } catch {
      setPolicyError("Network error — the policy was not changed.")
    } finally {
      setSaving(false)
    }
  }

  const holders = React.useMemo(() => {
    if (!capsByUser) return null
    return members.filter((m) => (capsByUser[m.user.id] ?? []).includes(Capability.CredentialReveal))
  }, [capsByUser, members])

  return (
    <div className="space-y-6">
      <SettingsCard
        title="Value reveal"
        description="Whether anyone in this workspace may read a stored secret back in plaintext."
      >
        <SettingsRow
          label="Reveal is enabled"
          description={
            enabled === null
              ? "Reading the current setting…"
              : enabled
                ? "Holders of the capability can reveal a value, with a written reason, recorded in the journal."
                : "A new workspace starts with reveal off. Turning it on is itself a journaled event."
          }
        >
          {enabled === null && !policyError ? (
            <Spinner className="h-3.5 w-3.5 text-muted-foreground" />
          ) : (
            <Switch
              size="sm"
              checked={Boolean(enabled)}
              disabled={!isOwner || saving || enabled === null}
              onCheckedChange={toggle}
              aria-label="Enable credential reveal for this workspace"
            />
          )}
        </SettingsRow>
        {!isOwner && (
          <SettingsRow label="Read-only for your role" border={false}>
            <span className="text-[11px] text-muted-foreground text-right">
              Only a workspace OWNER can move this switch — the API refuses everyone else, including
              ADMIN.
            </span>
          </SettingsRow>
        )}
        {policyError && (
          <SettingsRow label="Problem" border={false}>
            <span role="alert" className="text-[11px] text-destructive text-right">{policyError}</span>
          </SettingsRow>
        )}
      </SettingsCard>

      <SettingsCard
        title="Who may reveal"
        description="Reveal is granted per person, never by role. Being an OWNER is not sufficient."
      >
        {holders === null ? (
          <SettingsEmpty>Loading the capability grants…</SettingsEmpty>
        ) : holders.length === 0 ? (
          <SettingsEmpty>
            Nobody holds <span className="font-mono">credentials:reveal</span>. Even with the switch
            on, no value can be revealed until someone is granted it in Members.
          </SettingsEmpty>
        ) : (
          holders.map((m) => (
            <SettingsRow key={m.id} label={m.user.full_name || m.user.email} description={m.user.email}>
              <Badge variant="outline" className="text-[10px] px-1.5">{m.role}</Badge>
              <Badge variant="outline" className="text-[10px] px-1.5 border-warn/40 text-warn">
                can reveal
              </Badge>
            </SettingsRow>
          ))
        )}
        {holders && holders.length > 2 && (
          <SettingsRow label="" border={false}>
            <span className="inline-flex items-start gap-1.5 text-[11px] text-warn text-right">
              <AlertTriangle className="mt-[1px] h-3 w-3 shrink-0" />
              {holders.length} people can read secrets in plaintext. The recommendation for a
              corporate workspace is two.
            </span>
          </SettingsRow>
        )}
      </SettingsCard>

      <SettingsCard
        title="Classification"
        description="What each class means, and who can move a credential between them."
      >
        <SettingsRow label="STANDARD" description="Dev tokens, read-only keys.">
          <span className="text-[11px] text-muted-foreground text-right">
            Revealable with the full ceremony
          </span>
        </SettingsRow>
        <SettingsRow label="RESTRICTED" description="Production API keys, deploy keys.">
          <span className="text-[11px] text-muted-foreground text-right">
            Revealable today; earmarked for a second approver
          </span>
        </SettingsRow>
        <SettingsRow
          label="SEALED"
          description="Production databases, root credentials, anything an agent created."
        >
          <span className="inline-flex items-center gap-1.5 text-[11px] text-destructive text-right">
            <ShieldAlert className="h-3 w-3 shrink-0" />
            Never revealable — rotate instead
          </span>
        </SettingsRow>
        <SettingsRow label="Changing a class" description="Raise it at any time; lowering is audited." border={false}>
          <span className="text-[11px] text-muted-foreground text-right">
            MANAGER+ to raise · OWNER/ADMIN to lower
          </span>
        </SettingsRow>
      </SettingsCard>

      <p className="text-[11px] text-muted-foreground">
        Per-category default classifications are not configurable yet — the API has no endpoint for
        them, so a control here would be a setting that saves nowhere. Set the class on each
        credential from its detail sheet until it lands.
      </p>
    </div>
  )
}
