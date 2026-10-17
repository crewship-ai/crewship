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
import Link from "next/link"
import { AlertTriangle, Building, ChevronRight, Eye, FolderLock, Layers, Lock, Shield, UserCheck, Users, type LucideIcon } from "lucide-react"

import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import { Switch } from "@/components/ui/switch"
import { Spinner } from "@/components/ui/spinner"
import { SettingsCard, SettingsCounts, SettingsEmpty, SettingsRow } from "@/components/features/settings/shared"
import { apiFetch } from "@/lib/api-fetch"
import { toastSwitchError } from "@/components/ui/page-save-bar"
import { Capability } from "@/lib/capabilities"

export interface AccessSecretsMember {
  id: string
  role: string
  user: { id: string; email: string; full_name: string | null; avatar_url?: string | null }
}

export interface AccessSecretsSectionProps {
  workspaceId: string
  /** The caller's workspace role. */
  role: string | null | undefined
  members: AccessSecretsMember[]
}

/** The slice of GET /api/v1/credentials this page summarises. */
interface CredentialSummary {
  id: string
  name: string
  scope: string
  crew_ids?: string[] | null
  sensitivity?: string
}

const CLASSES: { id: string; label: string; icon: LucideIcon }[] = [
  { id: "STANDARD", label: "Standard", icon: Shield },
  { id: "RESTRICTED", label: "Restricted", icon: Lock },
  { id: "SEALED", label: "Sealed", icon: AlertTriangle },
]

const OWNER_ONLY = "Only a workspace OWNER can move this switch — the API refuses everyone else, including ADMIN."

interface BulkCapabilities {
  members?: { user_id: string; role: string; capabilities: string[] }[]
}

export function AccessSecretsSection({ workspaceId, role, members }: AccessSecretsSectionProps) {
  const isOwner = role === "OWNER"

  const [enabled, setEnabled] = React.useState<boolean | null>(null)
  const [policyError, setPolicyError] = React.useState<string | null>(null)
  const [saving, setSaving] = React.useState(false)
  const [capsByUser, setCapsByUser] = React.useState<Record<string, string[]> | null>(null)
  // The bulk capabilities read is ADMIN+; a MANAGER cannot see the grants.
  const [capsHidden, setCapsHidden] = React.useState(false)
  const [capsFailed, setCapsFailed] = React.useState(false)
  // The credentials this caller can see — for the class counts and the
  // "where they live" counts. The same read the Credentials page makes; a
  // failure just shows the summaries as empty.
  const [creds, setCreds] = React.useState<CredentialSummary[] | null>(null)

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
      .then(async (res) => {
        if (res.status === 401 || res.status === 403) return "forbidden" as const
        return res.ok ? ((await res.json()) as BulkCapabilities) : null
      })
      .then((body) => {
        if (cancelled) return
        if (body === "forbidden") { setCapsHidden(true); setCapsByUser({}); return }
        if (!body) { setCapsFailed(true); setCapsByUser({}); return }
        const map: Record<string, string[]> = {}
        for (const m of body.members ?? []) map[m.user_id] = m.capabilities ?? []
        setCapsByUser(map)
      })
      .catch(() => { if (!cancelled) { setCapsFailed(true); setCapsByUser({}) } })
    return () => { cancelled = true }
  }, [workspaceId])

  React.useEffect(() => {
    let cancelled = false
    const ws = encodeURIComponent(workspaceId)
    apiFetch(`/api/v1/credentials?workspace_id=${ws}`)
      .then(async (res) => (res.ok ? ((await res.json()) as unknown) : null))
      .then((body) => {
        if (cancelled) return
        const list = Array.isArray(body) ? body : Array.isArray((body as { data?: unknown } | null)?.data) ? (body as { data: unknown[] }).data : []
        setCreds(list as CredentialSummary[])
      })
      .catch(() => { if (!cancelled) setCreds([]) })
    return () => { cancelled = true }
  }, [workspaceId])

  async function toggle(next: boolean) {
    setSaving(true)
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
        toastSwitchError("Value reveal", typeof data.error === "string" ? data.error : `Request failed (${res.status}).`)
        return
      }
      setEnabled(Boolean(data.enabled))
    } catch {
      toastSwitchError("Value reveal", "Network error — the policy was not changed.")
    } finally {
      setSaving(false)
    }
  }

  const holders = React.useMemo(() => {
    if (!capsByUser) return null
    return members.filter((m) => (capsByUser[m.user.id] ?? []).includes(Capability.CredentialReveal))
  }, [capsByUser, members])

  // Where credentials live: workspace-wide, or with a crew. A crew is the
  // folder — its members see its credentials (credentialVisibilityFilter).
  const placement = React.useMemo(() => {
    if (!creds) return null
    const crewScoped = creds.filter((c) => c.scope === "CREW")
    const unlinked = crewScoped.filter((c) => (c.crew_ids ?? []).length === 0).length
    return { workspaceWide: creds.length - crewScoped.length, perCrew: crewScoped.length - unlinked, unlinked }
  }, [creds])

  return (
    <div className="space-y-4">
      <SettingsCard icon={Eye}
        title="Value reveal"
        description="Whether anyone in this workspace can show a stored value in clear"
      >
        <SettingsRow
          label="Reveal is enabled"
          lock="Owner"
          lockTitle={OWNER_ONLY}
          border={!policyError}
          description={
            enabled === null && !policyError
              ? "Reading the current setting…"
              : !isOwner
                ? OWNER_ONLY
                : enabled
                  ? "Holders of the capability can reveal a value, with a fresh sign-in and a written reason, recorded in the audit log first."
                  : "Off for new workspaces. Each reveal still needs a fresh sign-in and a reason, and is written to the audit log first."
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
        {policyError && (
          <SettingsRow label="Problem" border={false} description={<span role="alert" className="text-destructive">{policyError}</span>}>
            <span />
          </SettingsRow>
        )}
      </SettingsCard>

      {/* Who holds the grant is a question about people, so it is answered —
          and changed — in Members; this card only says how many and links
          there, instead of keeping a second editable list of the same rows. */}
      <SettingsCard icon={UserCheck}
        title="Who may reveal"
        description="People who hold credentials:reveal"
        footer={holders && holders.length > 2 ? (
          <span className="flex items-start gap-1.5 text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" aria-hidden />
            {holders.length} people can read secrets in plaintext. The recommendation for a corporate workspace is two.
          </span>
        ) : undefined}
      >
        {capsHidden ? (
          <SettingsEmpty>
            Your role cannot see who holds <span className="font-mono">credentials:reveal</span>. An
            Admin or the Owner can check it in Members.
          </SettingsEmpty>
        ) : capsFailed ? (
          <SettingsEmpty>The capability grants could not be loaded. Reload the page to try again.</SettingsEmpty>
        ) : holders === null ? (
          <SettingsEmpty>Loading the capability grants…</SettingsEmpty>
        ) : holders.length === 0 ? (
          <SettingsEmpty>
            Nobody holds <span className="font-mono">credentials:reveal</span>. Even with the switch
            on, no value can be revealed until someone is granted it in Members.
          </SettingsEmpty>
        ) : (
          <SettingsRow
            label={<><span className="font-mono tabular-nums">{holders.length}</span> {holders.length === 1 ? "person can" : "people can"} reveal secrets</>}
            description={<>
              {holders.slice(0, 4).map((m) => personLabel(m.user.full_name, m.user.email)).join(", ")}{holders.length > 4 ? ` and ${holders.length - 4} more` : ""}
              <span className="block">Owners and admins by default. More than 2 shows a warning.</span>
            </>}
            border={false}
          >
            <span className="flex shrink-0 -space-x-1.5" aria-hidden data-slot="reveal-holders">
              {holders.slice(0, 5).map((m) => (
                <UserAvatar key={m.id} name={m.user.full_name} email={m.user.email} src={m.user.avatar_url ?? null} className="h-6 w-6 ring-2 ring-card" textClassName="text-micro" />
              ))}
            </span>
            <Link href="/settings/members?flag=reveal" className="inline-flex items-center gap-1 text-control font-medium text-primary-hover underline-offset-2 hover:underline">
              Manage in Members<ChevronRight className="h-3 w-3" aria-hidden />
            </Link>
          </SettingsRow>
        )}
      </SettingsCard>

      {/* Counts only, each a way into the Vault filtered to that class (the
          Vault reads ?class=, lib/credentials/vault-url-state.ts): the class
          itself is set on each secret there, so this card has nothing to edit. */}
      <SettingsCard icon={Layers}
        title="Classification"
        description="Read-only here; change it on each secret in Vault"
        footer="Sealed values are never revealed or exported."
      >
        {creds === null ? (
          <SettingsEmpty>Loading credentials…</SettingsEmpty>
        ) : (
          <SettingsCounts
            items={CLASSES.map((cls) => ({
              icon: cls.icon,
              label: cls.label,
              value: creds.filter((c) => (c.sensitivity || "STANDARD").toUpperCase() === cls.id).length,
              href: `/credentials?class=${cls.id}`,
            }))}
          />
        )}
      </SettingsCard>

      <SettingsCard icon={FolderLock} title="Where credentials live" description="Read-only">
        {placement === null ? (
          <SettingsEmpty>Loading credentials…</SettingsEmpty>
        ) : creds && creds.length === 0 ? (
          <SettingsEmpty>No credentials you can see in this workspace yet.</SettingsEmpty>
        ) : (
          <SettingsCounts
            items={[
              { icon: Building, label: "Workspace-wide", value: placement.workspaceWide, href: "/credentials" },
              { icon: Users, label: "Per crew", value: placement.perCrew, href: "/credentials" },
              { icon: AlertTriangle, label: "Crew-scoped, no crew", value: placement.unlinked, href: "/credentials" },
            ]}
          />
        )}
      </SettingsCard>
    </div>
  )
}
