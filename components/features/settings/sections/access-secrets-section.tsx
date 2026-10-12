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
import { AlertTriangle, Eye, FolderLock, ShieldAlert, ShieldCheck, UserCheck } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { Spinner } from "@/components/ui/spinner"
import { SettingsCard, SettingsEmpty, SettingsRow, nativeSelect } from "@/components/features/settings/shared"
import { isAdminTier } from "@/lib/permissions/tiers"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { toastSwitchError } from "@/components/ui/page-save-bar"
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

/** The slice of GET /api/v1/credentials this page summarises. */
interface CredentialSummary {
  id: string
  name: string
  scope: string
  crew_ids?: string[] | null
  sensitivity?: string
}

const CLASSES: { id: string; label: string; description: string; rule: React.ReactNode }[] = [
  { id: "STANDARD", label: "Standard", description: "Dev tokens, read-only keys.", rule: "Revealable with the full ceremony" },
  { id: "RESTRICTED", label: "Restricted", description: "Production API keys, deploy keys.", rule: "Revealable today; earmarked for a second approver" },
  {
    id: "SEALED",
    label: "Sealed",
    description: "Production databases, root credentials, anything an agent created.",
    rule: <span className="inline-flex items-center gap-1.5 text-destructive"><ShieldAlert className="h-3 w-3 shrink-0" />Never revealable — rotate instead</span>,
  },
]

/** Up to `max` credential links, then "+N more" — each opens the
 *  credential's detail sheet, where its class and crews are changed. */
function CredentialLinks({ items, max = 4 }: { items: CredentialSummary[]; max?: number }) {
  if (items.length === 0) return <span className="text-label text-muted-foreground-soft">None</span>
  return (
    <span className="flex flex-wrap justify-end gap-1.5">
      {items.slice(0, max).map((c) => (
        <Link key={c.id} href={`/credentials?id=${encodeURIComponent(c.id)}`} className="max-w-[12rem] truncate rounded-md border border-border px-1.5 py-0.5 font-mono text-micro text-foreground/85 hover:border-line-strong hover:text-foreground">
          {c.name}
        </Link>
      ))}
      {items.length > max && <span className="px-1 text-micro text-muted-foreground">+{items.length - max} more</span>}
    </span>
  )
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
  // The bulk capabilities read is ADMIN+; a MANAGER cannot see the grants.
  const [capsHidden, setCapsHidden] = React.useState(false)
  const [capsFailed, setCapsFailed] = React.useState(false)
  // Who may grant or revoke: the capabilities PATCH is ADMIN+ server-side.
  const canEditGrants = isAdminTier(role)
  const [busyUser, setBusyUser] = React.useState<string | null>(null)
  const [grantTo, setGrantTo] = React.useState("")
  // The credentials this caller can see, and the crews they belong to — for
  // the class counts and the "where they live" overview. Both are reads the
  // Credentials page already makes; a failure just hides the summaries.
  const [creds, setCreds] = React.useState<CredentialSummary[] | null>(null)
  const [crewNames, setCrewNames] = React.useState<Record<string, { name: string; slug: string }>>({})

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
    apiFetch(`/api/v1/crews?workspace_id=${ws}`)
      .then(async (res) => (res.ok ? ((await res.json()) as unknown) : null))
      .then((body) => {
        if (cancelled) return
        const list = (Array.isArray(body) ? body : Array.isArray((body as { data?: unknown } | null)?.data) ? (body as { data: unknown[] }).data : []) as { id: string; name: string; slug: string }[]
        setCrewNames(Object.fromEntries(list.map((c) => [c.id, { name: c.name, slug: c.slug }])))
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [workspaceId])

  /** Grant or revoke credentials:reveal for one person — the same PATCH the
   *  Members page uses, so the two pages can never disagree. */
  async function setReveal(userId: string, grant: boolean) {
    setBusyUser(userId)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(userId)}/capabilities?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(grant ? { grant: [Capability.CredentialReveal] } : { revoke: [Capability.CredentialReveal] }),
        },
      )
      if (!res.ok) {
        toast.error(res.status === 403 ? "Your role cannot change who may reveal." : `Could not ${grant ? "grant" : "revoke"} reveal (${res.status}).`)
        return
      }
      const body = (await res.json().catch(() => null)) as { capabilities?: string[] } | null
      setCapsByUser((prev) => {
        const current = prev?.[userId] ?? []
        const next = body?.capabilities
          ?? (grant ? [...new Set([...current, Capability.CredentialReveal])] : current.filter((c) => c !== Capability.CredentialReveal))
        return { ...(prev ?? {}), [userId]: next }
      })
      setGrantTo("")
      toast.success(grant ? "Reveal granted" : "Reveal revoked")
    } catch {
      toast.error("Network error — nothing was changed.")
    } finally {
      setBusyUser(null)
    }
  }

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
  const nonHolders = React.useMemo(
    () => (capsByUser ? members.filter((m) => !(capsByUser[m.user.id] ?? []).includes(Capability.CredentialReveal)) : []),
    [capsByUser, members],
  )

  // Where credentials live: workspace-wide, or with a crew. A crew is the
  // folder — its members see its credentials (credentialVisibilityFilter).
  const placement = React.useMemo(() => {
    if (!creds) return null
    const workspaceWide = creds.filter((c) => c.scope !== "CREW")
    const byCrew = new Map<string, CredentialSummary[]>()
    const unlinked: CredentialSummary[] = []
    for (const c of creds) {
      if (c.scope !== "CREW") continue
      const ids = c.crew_ids ?? []
      if (ids.length === 0) unlinked.push(c)
      for (const id of ids) byCrew.set(id, [...(byCrew.get(id) ?? []), c])
    }
    return { workspaceWide, byCrew: [...byCrew.entries()].sort((a, b) => b[1].length - a[1].length), unlinked }
  }, [creds])

  return (
    <div className="space-y-6">
      <SettingsCard icon={Eye}
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
            <span className="text-label text-muted-foreground text-right">
              Only a workspace OWNER can move this switch — the API refuses everyone else, including
              ADMIN.
            </span>
          </SettingsRow>
        )}
        {policyError && (
          <SettingsRow label="Problem" border={false}>
            <span role="alert" className="text-label text-destructive text-right">{policyError}</span>
          </SettingsRow>
        )}
      </SettingsCard>

      <SettingsCard icon={UserCheck}
        title="Who may reveal"
        description="OWNER and ADMIN receive reveal by default unless their membership has an explicit capability set. Individual grants and revocations take precedence; workspace policy still applies."
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
          holders.map((m) => (
            <SettingsRow key={m.id} label={m.user.full_name || m.user.email} description={m.user.email}>
              <Badge variant="outline" className="text-micro px-1.5">{m.role}</Badge>
              <Badge variant="outline" className="text-micro px-1.5 border-warn/40 text-warn">
                can reveal
              </Badge>
              {canEditGrants && (
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 px-2 text-xs text-muted-foreground hover:text-destructive"
                  disabled={busyUser !== null}
                  aria-label={`Revoke reveal from ${m.user.full_name || m.user.email}`}
                  onClick={() => void setReveal(m.user.id, false)}
                >
                  {busyUser === m.user.id ? <Spinner className="h-3 w-3" /> : "Revoke"}
                </Button>
              )}
            </SettingsRow>
          ))
        )}
        {canEditGrants && !capsHidden && !capsFailed && holders !== null && nonHolders.length > 0 && (
          <SettingsRow label="Grant reveal" description="Pick a person; they can reveal while the switch above is on.">
            <span className="flex w-full items-center justify-end gap-2 sm:w-auto">
              <select
                aria-label="Person to grant reveal to"
                className={cn(nativeSelect, "min-w-0 flex-1 sm:w-56 sm:flex-none")}
                value={grantTo}
                onChange={(e) => setGrantTo(e.target.value)}
              >
                <option value="">Choose a person…</option>
                {nonHolders.map((m) => (
                  <option key={m.id} value={m.user.id}>{(m.user.full_name || m.user.email) + " · " + m.role}</option>
                ))}
              </select>
              <Button size="sm" variant="outline" className="h-8 text-xs" disabled={!grantTo || busyUser !== null} onClick={() => void setReveal(grantTo, true)}>
                Grant
              </Button>
            </span>
          </SettingsRow>
        )}
        {holders && holders.length > 2 && (
          <SettingsRow label="" border={false}>
            <span className="inline-flex items-start gap-1.5 text-label text-warn text-right">
              <AlertTriangle className="mt-[1px] h-3 w-3 shrink-0" />
              {holders.length} people can read secrets in plaintext. The recommendation for a
              corporate workspace is two.
            </span>
          </SettingsRow>
        )}
      </SettingsCard>

      <SettingsCard icon={ShieldCheck}
        title="Classification"
        description="What each class means, and who can move a credential between them."
      >
        {CLASSES.map((cls) => {
          const items = (creds ?? []).filter((c) => (c.sensitivity || "STANDARD").toUpperCase() === cls.id)
          return (
            <SettingsRow
              key={cls.id}
              label={<span className="inline-flex items-center gap-2">{cls.label}<span className="rounded-full bg-muted px-1.5 font-mono text-micro text-muted-foreground tabular-nums">{creds ? items.length : "…"}</span></span>}
              description={<>{cls.description} <span className="text-muted-foreground-soft">· {cls.rule}</span></>}
            >
              {creds && <CredentialLinks items={items} />}
            </SettingsRow>
          )
        })}
        <SettingsRow label="Changing a class" description="Raise it at any time; lowering is audited." border={false}>
          <span className="text-label text-muted-foreground text-right">
            Manager and up can raise · Owner or Admin can lower
          </span>
        </SettingsRow>
      </SettingsCard>

      <SettingsCard icon={FolderLock}
        title="Where credentials live"
        description="Workspace-wide credentials are visible to every member. A crew-scoped one is visible to that crew's members (and Managers and up) — the crew is the folder."
      >
        {placement === null ? (
          <SettingsEmpty>Loading credentials…</SettingsEmpty>
        ) : creds && creds.length === 0 ? (
          <SettingsEmpty>No credentials you can see in this workspace yet.</SettingsEmpty>
        ) : (
          <>
            <SettingsRow label={<span className="inline-flex items-center gap-2">Workspace-wide<span className="rounded-full bg-muted px-1.5 font-mono text-micro text-muted-foreground tabular-nums">{placement.workspaceWide.length}</span></span>} description="Every member sees these.">
              <CredentialLinks items={placement.workspaceWide} />
            </SettingsRow>
            {placement.byCrew.map(([crewId, items]) => (
              <SettingsRow
                key={crewId}
                label={<span className="inline-flex items-center gap-2">{crewNames[crewId]?.name ?? "Crew"}<span className="rounded-full bg-muted px-1.5 font-mono text-micro text-muted-foreground tabular-nums">{items.length}</span></span>}
                description={crewNames[crewId]
                  ? <Link href={`/crews?crew=${encodeURIComponent(crewNames[crewId].slug)}`} className="hover:text-foreground hover:underline">Members of this crew see these</Link>
                  : "Members of this crew see these"}
              >
                <CredentialLinks items={items} />
              </SettingsRow>
            ))}
            {placement.unlinked.length > 0 && (
              <SettingsRow label="Crew-scoped, no crew" description="Visible to Managers and up only, until linked to a crew.">
                <CredentialLinks items={placement.unlinked} />
              </SettingsRow>
            )}
          </>
        )}
      </SettingsCard>

      <p className="text-label text-muted-foreground">
        Open a credential to change its class or the crews it belongs to.
      </p>
    </div>
  )
}
