"use client"

import * as React from "react"
import { Building2, Check, House, UserPlus, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { displayName, isPersonal, personStatus, type Person, type Role, type Workspace } from "./people-model"
import { PersonAvatar, RoleSegmented, SetupLinkBox } from "./people-parts"
import { MemberDialogIcon } from "@/components/features/settings/members/member-bits"
import type { SetupLink, usePeople } from "./use-people"

type Actions = ReturnType<typeof usePeople>["actions"]

/** Add person: an account, access to any workspaces, and a setup link to pass on. */
export function AddPersonDialog({ open, onOpenChange, workspaces, actions, busy, onCreated }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaces: Workspace[]
  actions: Actions
  busy: string | null
  onCreated: (userId: string) => void
}) {
  const [email, setEmail] = React.useState("")
  const [name, setName] = React.useState("")
  const [roles, setRoles] = React.useState<Record<string, Role | null>>({})
  const [result, setResult] = React.useState<{ userId: string; link: SetupLink } | null>(null)
  React.useEffect(() => {
    if (open) { setEmail(""); setName(""); setRoles({}); setResult(null) }
  }, [open])
  const valid = email.includes("@")
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!valid) return
    const memberships = Object.entries(roles).flatMap(([workspace_id, role]) => (role ? [{ workspace_id, role }] : []))
    const out = await actions.createPerson(email.trim(), name.trim(), memberships)
    if (out) setResult(out)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px]">
        <DialogHeader className="flex-row items-start gap-3 space-y-0 text-left">
          <MemberDialogIcon icon={result ? Check : UserPlus} tint={result ? "var(--success)" : "var(--primary)"} />
          <div className="min-w-0 space-y-1">
            <DialogTitle>{result ? "Send them this link" : "Add person"}</DialogTitle>
            <DialogDescription>Creates the account and gives you a setup link. No email is sent.</DialogDescription>
          </div>
        </DialogHeader>
        {result ? (
          <div className="grid gap-3">
            <p className="text-control"><b className="font-medium">{result.link.email}</b> has an account now. Send them this link to choose a password.</p>
            <div className="-mx-4"><SetupLinkBox url={result.link.url} expiresAt={result.link.expires_at} /></div>
            <DialogFooter>
              <Button variant="outline" onClick={() => onOpenChange(false)}>Close</Button>
              <Button onClick={() => { onCreated(result.userId); onOpenChange(false) }}>Open their profile</Button>
            </DialogFooter>
          </div>
        ) : (
          <form onSubmit={submit} className="grid gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="add-person-email">Email</Label>
              <Input id="add-person-email" type="email" autoFocus value={email} onChange={(e) => setEmail(e.target.value)} placeholder="lucie@acme.example" />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="add-person-name">Full name <span className="font-normal text-muted-foreground">optional</span></Label>
              <Input id="add-person-name" value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <fieldset className="grid gap-1.5">
              <legend className="mb-1.5 text-sm font-medium">Workspaces <span className="font-normal text-muted-foreground">· and the role in each</span></legend>
              <div className="max-h-64 overflow-y-auto rounded-lg border border-border">
                {workspaces.filter((w) => !isPersonal(w)).map((w) => (
                  <div key={w.id} className="flex flex-wrap items-center gap-2.5 border-b border-border px-3 py-2 last:border-b-0">
                    <WorkspaceTile id={w.id} name={w.name} logoUrl={w.logo_url} />
                    <span className="min-w-0 flex-1 truncate text-control">{w.name}</span>
                    <RoleSegmented label={`Role in ${w.name}`} allowNone value={roles[w.id] ?? null} onChange={(r) => setRoles((prev) => ({ ...prev, [w.id]: r }))} />
                  </div>
                ))}
              </div>
              <p className="flex items-center gap-1.5 text-label text-muted-foreground"><House className="h-3 w-3" aria-hidden />Personal workspaces are not offered. With none picked, they sign in to nothing until added.</p>
            </fieldset>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
              <Button type="submit" disabled={!valid || busy === "create-person"}>Create and get link</Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

const slugify = (s: string) => s.toLowerCase().normalize("NFD").replace(/[̀-ͯ]/g, "").replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 50)

/** New workspace: owned by someone you pick, and you are not added to it. */
export function NewWorkspaceDialog({ open, onOpenChange, people, actions, busy, onCreated }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  people: Person[]
  actions: Actions
  busy: string | null
  onCreated: (workspaceId: string) => void
}) {
  const owners = people.filter((p) => personStatus(p) !== "suspended").sort((a, b) => displayName(a).localeCompare(displayName(b)))
  const [name, setName] = React.useState("")
  const [slug, setSlug] = React.useState("")
  const [slugTouched, setSlugTouched] = React.useState(false)
  const [owner, setOwner] = React.useState("")
  const [adds, setAdds] = React.useState<{ id: string; role: Role }[]>([])
  React.useEffect(() => {
    if (open) { setName(""); setSlug(""); setSlugTouched(false); setOwner(""); setAdds([]) }
  }, [open])
  const addable = owners.filter((p) => p.id !== owner && !adds.some((a) => a.id === p.id))
  const effectiveSlug = slugTouched ? slug : slugify(name)
  const valid = name.trim().length >= 2 && effectiveSlug.length >= 2 && !!owner
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!valid) return
    const id = await actions.createWorkspace(name.trim(), effectiveSlug, owner)
    if (!id) return
    // The people picked here join right after; a refusal is toasted per
    // person and the workspace stays.
    for (const a of adds) {
      const p = people.find((x) => x.id === a.id)
      await actions.setRole(a.id, id, a.role, p ? `${displayName(p)} joined ${name.trim()}` : "")
    }
    onCreated(id)
    onOpenChange(false)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px]">
        <DialogHeader className="flex-row items-start gap-3 space-y-0 text-left">
          <MemberDialogIcon icon={Building2} />
          <div className="min-w-0 space-y-1">
            <DialogTitle>New workspace</DialogTitle>
            <DialogDescription>A place for a team. It starts empty; its owner adds crews from there. You are not added.</DialogDescription>
          </div>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="new-ws-name">Name</Label>
            <Input id="new-ws-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="Marketing" />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="new-ws-slug">Address</Label>
            <Input id="new-ws-slug" className="font-mono" value={effectiveSlug} onChange={(e) => { setSlugTouched(true); setSlug(slugify(e.target.value)) }} />
            <p className="text-label text-muted-foreground">Made from the name. It cannot change later; the name can.</p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="new-ws-owner">Owner</Label>
            <select id="new-ws-owner" value={owner} onChange={(e) => setOwner(e.target.value)}
              className="h-9 rounded-md border border-control-border bg-surface-subtle px-2 text-control">
              <option value="">Choose a person…</option>
              {owners.map((p) => <option key={p.id} value={p.id}>{displayName(p)} · {p.email}</option>)}
            </select>
          </div>
          <fieldset className="grid gap-1.5">
            <legend className="mb-1.5 text-sm font-medium">People to add <span className="font-normal text-muted-foreground">· optional</span></legend>
            {adds.map((a) => {
              const p = people.find((x) => x.id === a.id)
              if (!p) return null
              return (
                <div key={a.id} className="flex flex-wrap items-center gap-2 rounded-lg bg-surface-subtle px-2 py-1.5">
                  <PersonAvatar person={p} className="h-6 w-6" />
                  <span className="min-w-0 flex-1 truncate text-control">{displayName(p)}</span>
                  <RoleSegmented label={`Role of ${displayName(p)}`} value={a.role} onChange={(r) => r && setAdds((prev) => prev.map((x) => (x.id === a.id ? { ...x, role: r } : x)))} />
                  <Button type="button" size="icon-sm" variant="ghost" className="h-7 w-7" aria-label={`Do not add ${displayName(p)}`}
                    onClick={() => setAdds((prev) => prev.filter((x) => x.id !== a.id))}><X className="h-3.5 w-3.5" /></Button>
                </div>
              )
            })}
            <select aria-label="Add someone" value="" onChange={(e) => { if (e.target.value) setAdds((prev) => [...prev, { id: e.target.value, role: "MEMBER" }]) }}
              disabled={addable.length === 0}
              className="h-9 rounded-md border border-control-border bg-surface-subtle px-2 text-control">
              <option value="">{addable.length ? "Add someone…" : "Everyone is already picked"}</option>
              {addable.map((p) => <option key={p.id} value={p.id}>{displayName(p)} · {p.email}</option>)}
            </select>
          </fieldset>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button type="submit" disabled={!valid || busy === "create-ws"}>Create workspace</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
