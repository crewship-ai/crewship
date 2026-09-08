"use client"

import * as React from "react"
import { LockKeyhole, ListTree } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { apiFetch } from "@/lib/api-fetch"
import { CREDENTIAL_ITEM_TYPES } from "@/lib/credentials/item-types"

export interface EditableCredentialField { key: string; value: string | null; is_secret: boolean }
const specs = CREDENTIAL_ITEM_TYPES.flatMap((t) => t.extra)

/** Each part is an independent API operation; never imply a multi-field transaction. */
export function CredentialExtraFieldsEditor({ fields, workspaceId, credentialId, onDirtyChange, onSaved }: {
  fields: EditableCredentialField[]; workspaceId: string; credentialId: string
  onDirtyChange: (dirty: boolean) => void; onSaved: () => void
}) {
  const [drafts, setDrafts] = React.useState<Record<string, string>>({})
  const [busy, setBusy] = React.useState<string | null>(null)
  const [message, setMessage] = React.useState<string | null>(null)
  const [shown, setShown] = React.useState<Record<string, boolean>>({})
  React.useEffect(() => onDirtyChange(Object.keys(drafts).length > 0), [drafts, onDirtyChange])
  function discard(key: string) { setDrafts((prev) => { const next = { ...prev }; delete next[key]; return next }); setShown((prev) => ({ ...prev, [key]: false })) }
  async function save(field: EditableCredentialField) {
    const value = drafts[field.key]
    if (!value?.trim()) { setMessage("Enter a replacement, or cancel to keep the stored field."); return }
    setBusy(field.key); setMessage(null)
    try {
      const r = await apiFetch(`/api/v1/credentials/${encodeURIComponent(credentialId)}/fields/${encodeURIComponent(field.key)}?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ value, is_secret: field.is_secret }),
      })
      if (!r.ok) throw new Error(`Field could not be saved (HTTP ${r.status}). Your other changes are still here.`)
      discard(field.key); setMessage(`${field.key} saved.`); onSaved()
    } catch (e) { setMessage(e instanceof Error ? e.message : "Field could not be saved.") }
    finally { setBusy(null) }
  }
  if (!fields.length) return null
  return <section className="space-y-3 rounded-xl border border-border p-4" aria-label="Additional fields">
    <h3 className="flex items-center gap-2 text-sm font-medium"><ListTree className="h-4 w-4" /> Additional fields</h3>
    <p className="text-xs text-muted-foreground">Each field is saved separately. Secret values are never read back.</p>
    {fields.map((field) => {
      const spec = specs.find((s) => s.key === field.key)
      const label = spec?.label ?? field.key
      const editing = field.key in drafts
      return <div key={field.key} className="space-y-2 border-t border-border pt-3">
        <div className="flex items-center justify-between gap-2 text-xs">
          <span className="flex items-center gap-1.5">{field.is_secret && <LockKeyhole className="h-3.5 w-3.5" />}{label}</span>
          {!editing && <Button type="button" size="sm" variant="outline" onClick={() => setDrafts((prev) => ({ ...prev, [field.key]: field.is_secret ? "" : field.value ?? "" }))}>Edit {label}</Button>}
        </div>
        {editing ? <>
          {spec?.multiline ? <Textarea aria-label={label} value={drafts[field.key]} autoComplete="off" spellCheck={false} className={field.is_secret && !shown[field.key] ? "font-mono [-webkit-text-security:disc]" : "font-mono"} onChange={(e) => setDrafts((prev) => ({ ...prev, [field.key]: e.target.value }))} />
            : <Input aria-label={label} type={field.is_secret && !shown[field.key] ? "password" : "text"} autoComplete="off" value={drafts[field.key]} onChange={(e) => setDrafts((prev) => ({ ...prev, [field.key]: e.target.value }))} />}
          {field.is_secret && <Button type="button" variant="ghost" size="sm" onClick={() => setShown((prev) => ({ ...prev, [field.key]: !prev[field.key] }))}>{shown[field.key] ? "Hide" : "Show"} {label} draft</Button>}
          <div className="flex gap-2"><Button type="button" size="sm" disabled={busy !== null} onClick={() => save(field)}>{busy === field.key ? "Saving…" : `Save ${label}`}</Button><Button type="button" size="sm" variant="ghost" disabled={busy !== null} onClick={() => discard(field.key)}>Cancel {label}</Button></div>
        </> : <p className="break-all whitespace-pre-wrap text-xs text-muted-foreground">{field.is_secret ? "Stored securely" : field.value ?? "Not reported"}</p>}
      </div>
    })}
    {message && <p role="status" className="text-xs">{message}</p>}
  </section>
}
