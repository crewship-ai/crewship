"use client"

/**
 * A secret's Settings tab (Harbor 2): its details edited in place with the
 * page's one save bar, the commands that do the same from a terminal, and the
 * danger zone. Replaces the separate Edit dialog.
 *
 * The value is not edited here: Replace value (with its grace period) is the
 * one way to change it, and a secret never travels through this form.
 */

import * as React from "react"
import { Plus, TerminalSquare, Trash2, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ConfirmDialog, type Consequence } from "@/components/ui/confirm-dialog"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { PageSaveBar, PageSaveProvider, usePageSave } from "@/components/ui/page-save-bar"
import { apiFetch } from "@/lib/api-fetch"
import { formatDate } from "@/lib/time"
import { toast } from "@/lib/toast"
import { cn } from "@/lib/utils"
import type { CredentialCrewRef } from "@/hooks/use-credential-readiness"
import { BrandPicker } from "./brand-picker"
import { CliLines } from "./vault-cli-dialog"
import { CredentialExtraFieldsEditor, type EditableCredentialField } from "./credential-extra-fields-editor"

export interface SettingsCredential {
  id: string
  name: string
  description: string | null
  type: string
  provider: string
  scope: string
  crew_ids?: string[]
  tags: string[]
  token_expires_at: string | null
  username: string | null
  created_at: string
  agent_names: string[]
}

interface Draft {
  name: string
  description: string
  provider: string
  tags: string[]
  expires: string
  username: string
  scope: "WORKSPACE" | "CREW"
  crewIds: string[]
}

function draftOf(c: SettingsCredential): Draft {
  return {
    name: c.name,
    description: c.description ?? "",
    provider: c.provider,
    tags: c.tags ?? [],
    expires: c.token_expires_at ? c.token_expires_at.slice(0, 10) : "",
    username: c.username ?? "",
    scope: c.scope === "CREW" ? "CREW" : "WORKSPACE",
    crewIds: c.crew_ids ?? [],
  }
}

const same = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i])

/** The PATCH body for what changed — and only that, so an untouched field is never rewritten. */
export function settingsPatch(c: SettingsCredential, d: Draft, opts: { isLogin: boolean }): Record<string, unknown> {
  const base = draftOf(c)
  const body: Record<string, unknown> = {}
  if (d.name.trim() !== base.name) body.name = d.name.trim()
  if (d.description !== base.description) body.description = d.description
  if (!opts.isLogin && d.provider !== base.provider) body.provider = d.provider
  if (!same(d.tags, base.tags)) body.tags = d.tags
  if (!opts.isLogin && d.expires !== base.expires) body.token_expires_at = d.expires ? new Date(d.expires).toISOString() : null
  if (c.type === "USERPASS" && d.username.trim() !== base.username) body.username = d.username.trim()
  if (d.scope !== base.scope || (d.scope === "CREW" && !same([...d.crewIds].sort(), [...base.crewIds].sort()))) {
    body.scope = d.scope
    body.crew_ids = d.scope === "CREW" ? d.crewIds : []
  }
  return body
}

function Row({ label, hint, htmlFor, children }: { label: string; hint?: string; htmlFor?: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-2 border-t border-border px-4 py-3 first:border-t-0 sm:grid-cols-[200px_minmax(0,1fr)] sm:items-start">
      <div className="min-w-0">
        <label htmlFor={htmlFor} className="text-control font-medium text-foreground">{label}</label>
        {hint && <p className="text-label text-muted-foreground">{hint}</p>}
      </div>
      <div className="min-w-0">{children}</div>
    </div>
  )
}

export function SettingsCard({ title, icon: Icon, tone, children }: { title: string; icon?: React.ComponentType<{ className?: string }>; tone?: "danger"; children: React.ReactNode }) {
  return (
    <section aria-label={title} className={cn("overflow-hidden rounded-card border bg-card", tone === "danger" ? "border-destructive/30" : "border-border")}>
      <header className="flex items-center gap-2 border-b border-border px-4 py-2.5">
        {Icon && <Icon className={cn("size-4", tone === "danger" ? "text-destructive" : "text-muted-foreground")} />}
        <b className="text-control font-semibold text-foreground">{title}</b>
      </header>
      {children}
    </section>
  )
}

function DetailsCard({
  workspaceId,
  credential,
  isLogin,
  canUpdate,
  crewsById,
  knownTags,
  onSaved,
}: {
  workspaceId: string
  credential: SettingsCredential
  isLogin: boolean
  canUpdate: boolean
  crewsById: Record<string, CredentialCrewRef>
  knownTags: string[]
  onSaved: () => void
}) {
  const [draft, setDraft] = React.useState<Draft>(() => draftOf(credential))
  const [tagDraft, setTagDraft] = React.useState("")
  const [saving, setSaving] = React.useState(false)
  // A refreshed credential is the new baseline.
  const key = JSON.stringify(draftOf(credential))
  React.useEffect(() => { setDraft(draftOf(credential)) }, [key]) // eslint-disable-line react-hooks/exhaustive-deps

  // A tag still in the field counts as typed: Save keeps it (#2428).
  const pending = tagDraft.trim().toLowerCase()
  const withPending = pending && !draft.tags.includes(pending) && draft.tags.length < 8 ? { ...draft, tags: [...draft.tags, pending] } : draft
  const patch = settingsPatch(credential, withPending, { isLogin })
  const count = Object.keys(patch).filter((k) => k !== "crew_ids").length
  const valid = draft.name.trim().length > 0 && (draft.scope === "WORKSPACE" || draft.crewIds.length > 0)
  const set = (p: Partial<Draft>) => setDraft((d) => ({ ...d, ...p }))

  const save = async () => {
    setSaving(true)
    try {
      const res = await apiFetch(`/api/v1/credentials/${encodeURIComponent(credential.id)}?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(patch),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        // The save bar turns a rejection into its sticky error toast; the draft stays.
        throw new Error(typeof data?.error === "string" ? data.error : `The server refused (HTTP ${res.status}).`)
      }
      setTagDraft("")
      onSaved()
    } finally {
      setSaving(false)
    }
  }

  usePageSave({ label: "Settings", count, canSave: valid, saving, save, discard: () => { setDraft(draftOf(credential)); setTagDraft("") } })

  const addTag = () => {
    const t = tagDraft.trim().toLowerCase()
    if (!t || draft.tags.includes(t) || draft.tags.length >= 8) { setTagDraft(""); return }
    set({ tags: [...draft.tags, t] })
    setTagDraft("")
  }
  const crews = Object.values(crewsById)
  const ro = !canUpdate

  return (
    <SettingsCard title="Details">
      <Row label="Name" htmlFor="secret-name">
        <Input id="secret-name" value={draft.name} disabled={ro} onChange={(e) => set({ name: e.target.value })} className="h-8 text-control" aria-invalid={!draft.name.trim() || undefined} />
      </Row>
      {!isLogin && (
        <Row label="Service" hint="The logo and the probe used by Test">
          {ro ? <span className="text-control">{credential.provider}</span> : <BrandPicker value={draft.provider} onChange={(provider) => set({ provider })} />}
        </Row>
      )}
      <Row label="Description" htmlFor="secret-description">
        <Textarea id="secret-description" rows={2} value={draft.description} disabled={ro} onChange={(e) => set({ description: e.target.value })} className="text-control" />
      </Row>
      {credential.type === "USERPASS" && (
        <Row label="Username" hint="Not secret · stored in clear" htmlFor="secret-username">
          <Input id="secret-username" value={draft.username} disabled={ro} onChange={(e) => set({ username: e.target.value })} className="h-8 font-mono text-control" autoComplete="off" />
        </Row>
      )}
      <Row label="Tags" hint={`${draft.tags.length} of 8`} htmlFor="secret-tag">
        <div className="flex flex-wrap items-center gap-1.5">
          {draft.tags.map((t) => (
            <span key={t} className="inline-flex items-center gap-1 rounded-md border border-border bg-muted px-1.5 py-0.5 text-label">
              {t}
              {!ro && (
                <button type="button" aria-label={`Remove tag ${t}`} className="kit-tap opacity-70 hover:opacity-100" onClick={() => set({ tags: draft.tags.filter((x) => x !== t) })}>
                  <X className="size-3" />
                </button>
              )}
            </span>
          ))}
          {!ro && draft.tags.length < 8 && (
            <span className="inline-flex items-center gap-1">
              <Input id="secret-tag" list="secret-known-tags" value={tagDraft} placeholder="Add a tag" className="h-7 w-32 text-label"
                onChange={(e) => setTagDraft(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter" || e.key === ",") { e.preventDefault(); addTag() } }} onBlur={() => { if (tagDraft.trim()) addTag() }} />
              <datalist id="secret-known-tags">{knownTags.map((t) => <option key={t} value={t} />)}</datalist>
              <Button type="button" size="icon-xs" variant="ghost" aria-label="Add tag" onClick={addTag}><Plus className="size-3.5" /></Button>
            </span>
          )}
        </div>
      </Row>
      {!isLogin && (
        <Row label="Expires" hint="Optional. The health strip warns 14 days before" htmlFor="secret-expires">
          <Input id="secret-expires" type="date" value={draft.expires} disabled={ro} onChange={(e) => set({ expires: e.target.value })} className="h-8 w-44 text-control" />
        </Row>
      )}
      <Row label="Scope" hint="Who can be given it: the whole workspace or selected crews">
        <div className="flex flex-col gap-2">
          <div role="radiogroup" aria-label="Scope" className="inline-flex w-fit rounded-md border border-border bg-muted/40 p-0.5">
            {(["WORKSPACE", "CREW"] as const).map((s) => (
              <button key={s} type="button" role="radio" aria-checked={draft.scope === s} disabled={ro} onClick={() => set({ scope: s })}
                className={cn("kit-tap h-7 rounded px-2.5 text-label", draft.scope === s ? "bg-card font-medium text-foreground shadow-xs" : "text-muted-foreground")}>
                {s === "WORKSPACE" ? "Workspace" : "Selected crews"}
              </button>
            ))}
          </div>
          {draft.scope === "CREW" && (
            <div className="flex flex-wrap gap-1.5" aria-label="Crews">
              {crews.length === 0 && <span className="text-label text-muted-foreground">No crews to pick from.</span>}
              {crews.map((c) => {
                const on = draft.crewIds.includes(c.id)
                return (
                  <button key={c.id} type="button" aria-pressed={on} disabled={ro}
                    onClick={() => set({ crewIds: on ? draft.crewIds.filter((x) => x !== c.id) : [...draft.crewIds, c.id] })}
                    className={cn("kit-tap rounded-md border px-2 py-0.5 text-label", on ? "border-primary/40 bg-primary/10 text-primary-hover" : "border-border text-muted-foreground")}>
                    {c.name}
                  </button>
                )
              })}
              {draft.crewIds.length === 0 && <span role="alert" className="text-label text-destructive">Pick at least one crew.</span>}
            </div>
          )}
        </div>
      </Row>
      <Row label="Created">
        <span className="text-control text-muted-foreground">{formatDate(credential.created_at)}</span>
      </Row>
    </SettingsCard>
  )
}

export function SecretSettings({
  workspaceId,
  credential,
  isLogin,
  canUpdate,
  canDelete,
  crewsById,
  knownTags,
  fields,
  onRefresh,
  onDeleted,
  dependents,
  onSignInAgain,
}: {
  workspaceId: string
  credential: SettingsCredential
  isLogin: boolean
  canUpdate: boolean
  canDelete: boolean
  crewsById: Record<string, CredentialCrewRef>
  knownTags: string[]
  fields: EditableCredentialField[]
  onRefresh: () => void
  onDeleted: () => void
  dependents: { routines: { slug: string; name: string }[] } | null | "error"
  /** AI provider accounts: replace the token without changing who it pays for. */
  onSignInAgain?: () => void
}) {
  const [confirm, setConfirm] = React.useState(false)
  const quoted = JSON.stringify(credential.name)
  const consequences: Consequence[] = [
    { tone: "lost", text: "The value is deleted for good. This cannot be undone." },
  ]
  if (credential.agent_names.length > 0) {
    consequences.push({ tone: "warn", text: `${credential.agent_names.length} agent${credential.agent_names.length === 1 ? "" : "s"} lose it at the next run: ${credential.agent_names.slice(0, 5).join(", ")}.` })
  }
  if (dependents === "error") consequences.push({ tone: "warn", text: "Routine use could not be checked." })
  else if (dependents && dependents.routines.length > 0) {
    consequences.push({ tone: "warn", text: `${dependents.routines.length} routine${dependents.routines.length === 1 ? "" : "s"} reference it: ${dependents.routines.slice(0, 5).map((r) => r.name || r.slug).join(", ")}.` })
  }

  const remove = async () => {
    const res = await apiFetch(`/api/v1/credentials/${encodeURIComponent(credential.id)}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "DELETE" }).catch(() => null)
    if (res && (res.ok || res.status === 404)) {
      toast.success(res.status === 404 ? `${credential.name} was already deleted` : `${credential.name} deleted`)
      onDeleted()
      return
    }
    toast.error(`Couldn't delete ${credential.name}.`)
    throw new Error("delete failed")
  }

  return (
    <PageSaveProvider>
      <div className="relative flex max-w-[860px] flex-col gap-4 pb-24">
        <DetailsCard workspaceId={workspaceId} credential={credential} isLogin={isLogin} canUpdate={canUpdate} crewsById={crewsById} knownTags={knownTags} onSaved={onRefresh} />
        {!isLogin && canUpdate && fields.length > 0 && (
          <SettingsCard title="Fields">
            <div className="p-4">
              <CredentialExtraFieldsEditor fields={fields} credentialId={credential.id} workspaceId={workspaceId} onDirtyChange={() => {}} onSaved={onRefresh} />
            </div>
          </SettingsCard>
        )}
        {isLogin && onSignInAgain && (
          <SettingsCard title="Sign-in">
            <div className="flex flex-wrap items-center gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <b className="text-control font-medium">Sign in again</b>
                <p className="text-label text-muted-foreground">Replace the token without changing who it pays for.</p>
              </div>
              <Button variant="outline" size="sm" onClick={onSignInAgain}>Sign in again</Button>
            </div>
          </SettingsCard>
        )}
        <SettingsCard title="Command line" icon={TerminalSquare}>
          <div className="p-4">
            <CliLines
              commands={[
                { cmd: `crewship credential get ${quoted}`, what: "Details, never the value" },
                { cmd: `crewship credential rotate ${quoted} --grace-seconds 86400`, what: "Replace the value; the old one works for 24 h" },
                { cmd: `crewship credential bind ${quoted} --slot ENV_VAR`, what: "Give it to agents under a variable name" },
                { cmd: `crewship credential audit ${quoted}`, what: "Who used, revealed or changed it" },
              ]}
            />
          </div>
        </SettingsCard>
        {canDelete && (
          <SettingsCard title="Danger zone" tone="danger">
            <div className="flex flex-wrap items-center gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <b className="text-control font-medium">{isLogin ? "Revoke" : "Delete"}</b>
                <p className="text-label text-muted-foreground">
                  {isLogin ? "Agents switch to the next account or stop." : `${credential.agent_names.length} agent${credential.agent_names.length === 1 ? "" : "s"} lose it at the next run.`}
                </p>
              </div>
              <Button variant="outline" size="sm" className="text-destructive" onClick={() => setConfirm(true)}>
                <Trash2 className="size-3.5" aria-hidden /> {isLogin ? "Revoke…" : "Delete…"}
              </Button>
            </div>
          </SettingsCard>
        )}
        <PageSaveBar className="fixed" />
      </div>
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={isLogin ? `Revoke ${credential.name}?` : `Delete ${credential.name}?`}
        consequences={consequences}
        confirmLabel={isLogin ? "Revoke" : "Delete"}
        destructive
        onConfirm={remove}
      />
    </PageSaveProvider>
  )
}
