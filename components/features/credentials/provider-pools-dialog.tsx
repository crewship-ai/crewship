"use client"

import * as React from "react"
import { ArrowLeft, Layers, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useProviderPools, type PoolDraft, type ProviderPool } from "@/hooks/use-provider-pools"
import { LoginBrandMark } from "./provider-login-bits"

const blank = (): PoolDraft => ({ name: "", provider: "", mode: "api_key", allow_cross_owner: false, members: [] })

// Mounted only for OWNER/ADMIN and keyed by workspace by the parent. Drafts
// cannot follow a workspace switch, and closed dialogs make no data requests.
export function ProviderPoolsDialog({ workspaceId, onClose }: { workspaceId: string; onClose: () => void }) {
  const [after, setAfter] = React.useState("")
  const api = useProviderPools(workspaceId, true, after)
  const [editor, setEditor] = React.useState<{ current?: ProviderPool; draft: PoolDraft } | null>(null)
  const [removing, setRemoving] = React.useState<ProviderPool | null>(null)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState("")
  const accounts = (api.accounts ?? []).filter(a => a.login && (a.type === "API_KEY" || a.type === "PROVIDER_LOGIN"))
  const choices = [...new Set(accounts.map(a => `${a.login!.provider}:${a.login!.mode}`))]
  const eligible = editor ? accounts.filter(a => a.login!.provider === editor.draft.provider && a.login!.mode === editor.draft.mode) : []
  const missing = editor?.draft.members.filter(m => !eligible.some(a => a.id === m.credential_id)) ?? []
  const patch = (value: Partial<PoolDraft>) => setEditor(old => old ? { ...old, draft: { ...old.draft, ...value } } : old)
  const run = async (fn: () => Promise<void>) => {
    setBusy(true); setError("")
    try { await fn() } catch (e) { setError(e instanceof Error ? e.message : "Request failed.") } finally { setBusy(false) }
  }
  const edit = (pool: ProviderPool) => void run(async () => {
    const current = await api.detail(pool.id)
    setEditor({ current, draft: { name: current.name, provider: current.provider, mode: current.mode, allow_cross_owner: current.allow_cross_owner, members: current.members ?? [] } })
  })
  const toggle = (id: string, checked: boolean) => {
    if (!editor) return
    patch({ members: checked ? [...editor.draft.members, { credential_id: id, priority: 0 }] : editor.draft.members.filter(m => m.credential_id !== id) })
  }
  const valid = editor && editor.draft.name.trim() && editor.draft.provider && editor.draft.members.length > 0 && editor.draft.members.length <= 100 && missing.length === 0 && editor.draft.members.every(m => Number.isSafeInteger(m.priority))
  return <Dialog open onOpenChange={open => { if (!open && !busy) onClose() }}>
    <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl" onInteractOutside={e => { if (busy || editor || removing) e.preventDefault() }}>
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2"><Layers className="size-4" />{editor ? editor.current ? "Edit account group" : "Create account group" : removing ? "Remove account group?" : "Account groups"}</DialogTitle>
        <DialogDescription>Prepare provider accounts together. Groups do not assign accounts or enable automatic switching yet.</DialogDescription>
      </DialogHeader>
      {(error || api.error) && <div role="alert" className="rounded-lg border border-destructive/40 p-3 text-sm text-destructive">{error || api.error?.message}</div>}
      {removing ? <div className="space-y-4">
        <p className="text-sm">Remove <strong>{removing.name}</strong>? Its provider accounts will remain. This group will no longer be available; its name stays reserved.</p>
        <div className="flex justify-end gap-2"><Button variant="outline" disabled={busy} onClick={() => { setRemoving(null); setError("") }}>Cancel</Button><Button variant="destructive" disabled={busy} onClick={() => void run(async () => { await api.remove(removing); setRemoving(null); setAfter("") })}><Trash2 className="mr-2 size-4" />{busy ? "Removing…" : "Remove group"}</Button></div>
      </div> : editor ? <form className="space-y-5" onSubmit={e => { e.preventDefault(); if (valid && !busy) void run(async () => { await api.save(editor.draft, editor.current); setEditor(null); setAfter("") }) }}>
        <fieldset disabled={busy} className="space-y-5">
          <label className="grid gap-2 text-sm">Group name<Input required maxLength={200} value={editor.draft.name} onChange={e => patch({ name: e.target.value })} placeholder="e.g. Development team" /></label>
          <label className="grid gap-2 text-sm">Provider and connection
            <select className="h-10 w-full rounded-md border bg-background px-3" disabled={!!editor.current} value={editor.draft.provider ? `${editor.draft.provider}:${editor.draft.mode}` : ""} onChange={e => { const [provider, mode] = e.target.value.split(":"); patch({ provider, mode: mode as PoolDraft["mode"], members: [], allow_cross_owner: false }) }}>
              <option value="">Choose from your connected accounts</option>
              {[...new Set([...choices, ...(editor.current ? [`${editor.current.provider}:${editor.current.mode}`] : [])])].map(value => <option key={value} value={value}>{value.replace(":api_key", " · API key").replace(":subscription", " · Subscription")}</option>)}
            </select>
          </label>
          <section className="space-y-2" aria-label="Group accounts">
            <h3 className="text-sm font-medium">Accounts · {editor.draft.members.length} selected</h3>
            <p className="text-xs text-muted-foreground">Lower priority numbers are preferred. Equal priorities rotate when runtime assignment becomes available.</p>
            {eligible.map(a => { const selected = editor.draft.members.find(m => m.credential_id === a.id); return <div key={a.id} className="flex flex-col items-stretch gap-3 rounded-lg border p-3 sm:flex-row sm:items-center">
              <label className="flex min-w-0 flex-1 items-center gap-3 text-sm"><input type="checkbox" checked={!!selected} onChange={e => toggle(a.id, e.target.checked)} /><LoginBrandMark provider={a.provider} size="sm" /><span className="min-w-0 break-words">{a.name}<span className="block text-xs text-muted-foreground">{a.login?.owner_email ?? "Owner unknown"} · {a.status.toLowerCase()}</span></span></label>
              {selected && <label className="flex items-center gap-2 text-xs">Priority<Input aria-label={`Priority for ${a.name}`} type="number" step="1" className="w-24" value={Number.isNaN(selected.priority) ? "" : selected.priority} onChange={e => patch({ members: editor.draft.members.map(m => m.credential_id === a.id ? { ...m, priority: e.target.value === "" ? NaN : Number(e.target.value) } : m) })} /></label>}
            </div> })}
            {missing.map(m => <div key={m.credential_id} className="rounded-lg border p-3 text-sm">Unavailable account · {m.credential_id}<Button type="button" variant="ghost" onClick={() => toggle(m.credential_id, false)}>Remove from group</Button></div>)}
            {eligible.length === 0 && <p className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">Choose a connection above, or add a provider account first. Legacy CLI token accounts must be re-imported as provider logins before grouping.</p>}
          </section>
          <label className="flex items-start gap-3 rounded-lg border p-3 text-sm"><input type="checkbox" className="mt-1" checked={editor.draft.allow_cross_owner} onChange={e => patch({ allow_cross_owner: e.target.checked })} /><span>Allow accounts owned by different people<span className="mt-1 block text-xs text-muted-foreground">Only enable with the account owners’ authorization. Provider terms and shared quota limits still apply.</span></span></label>
        </fieldset>
        <div className="flex justify-between gap-2"><Button type="button" variant="outline" disabled={busy} onClick={() => { setEditor(null); setError("") }}><ArrowLeft className="mr-2 size-4" />Back</Button><Button type="submit" disabled={!valid || busy || !!api.error}>{busy ? "Saving…" : editor.current ? "Save changes" : "Create group"}</Button></div>
      </form> : <div className="space-y-4">
        <div className="flex flex-wrap justify-between gap-2"><Button variant="outline" disabled={busy} onClick={() => { setError(""); api.reload() }}><RefreshCw className="mr-2 size-4" />Refresh</Button><Button disabled={busy || api.loading || !!api.error} onClick={() => { setError(""); setEditor({ draft: blank() }) }}><Plus className="mr-2 size-4" />Create group</Button></div>
        {api.loading ? <p role="status">Loading account groups…</p> : !api.error && api.pools?.items.length === 0 ? <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">No account groups on this page.</p> : null}
        {!api.error && api.pools?.items.map(pool => <div key={pool.id} className="flex flex-wrap items-center gap-3 rounded-xl border p-4"><LoginBrandMark provider={pool.provider} /><div className="min-w-0 flex-1"><h3 className="break-words text-sm font-medium">{pool.name}</h3><p className="text-xs text-muted-foreground">{pool.member_count} accounts · {pool.mode === "api_key" ? "API key" : "Subscription"} · Not assigned</p></div><Button size="icon" variant="ghost" aria-label={`Edit ${pool.name}`} disabled={busy} onClick={() => edit(pool)}><Pencil className="size-4" /></Button><Button size="icon" variant="ghost" aria-label={`Remove ${pool.name}`} disabled={busy} onClick={() => { setError(""); setRemoving(pool) }}><Trash2 className="size-4" /></Button></div>)}
        <div className="flex justify-end gap-2">{after && <Button variant="outline" disabled={busy} onClick={() => setAfter("")}>First page</Button>}{api.pools?.next_cursor && <Button variant="outline" disabled={busy} onClick={() => setAfter(api.pools!.next_cursor!)}>Next page</Button>}</div>
      </div>}
    </DialogContent>
  </Dialog>
}
