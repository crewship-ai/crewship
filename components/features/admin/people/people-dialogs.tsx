"use client"

import * as React from "react"
import { Building2, UserPlus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { displayName, personStatus, type Person, type Role, type Workspace } from "./people-model"
import { RoleSelect, SetupLinkBox } from "./people-parts"
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
      <DialogContent className="sm:max-w-[520px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2"><UserPlus className="h-4 w-4" />Add person</DialogTitle>
          <DialogDescription>Creates the account and gives you a setup link. No email is sent.</DialogDescription>
        </DialogHeader>
        {result ? (
          <div className="grid gap-3">
            <p className="text-[13px]"><b className="font-medium">{result.link.email}</b> has an account now. Send them this link to choose a password.</p>
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
              <legend className="mb-1.5 text-sm font-medium">Workspaces</legend>
              <div className="max-h-56 overflow-y-auto rounded-lg border border-border">
                {workspaces.map((w) => (
                  <div key={w.id} className="flex items-center gap-2.5 border-b border-border px-3 py-2 last:border-b-0">
                    <WorkspaceTile id={w.id} name={w.name} />
                    <span className="min-w-0 flex-1 truncate text-[13px]">{w.name}</span>
                    <RoleSelect label={`Role in ${w.name}`} allowNone value={roles[w.id] ?? null} onChange={(r) => setRoles((prev) => ({ ...prev, [w.id]: r }))} />
                  </div>
                ))}
              </div>
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
  React.useEffect(() => {
    if (open) { setName(""); setSlug(""); setSlugTouched(false); setOwner("") }
  }, [open])
  const effectiveSlug = slugTouched ? slug : slugify(name)
  const valid = name.trim().length >= 2 && effectiveSlug.length >= 2 && !!owner
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!valid) return
    const id = await actions.createWorkspace(name.trim(), effectiveSlug, owner)
    if (id) { onCreated(id); onOpenChange(false) }
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[460px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2"><Building2 className="h-4 w-4" />New workspace</DialogTitle>
          <DialogDescription>It starts empty. Its owner adds crews from there; you are not added.</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="new-ws-name">Name</Label>
            <Input id="new-ws-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="Marketing" />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="new-ws-slug">Slug</Label>
            <Input id="new-ws-slug" className="font-mono" value={effectiveSlug} onChange={(e) => { setSlugTouched(true); setSlug(slugify(e.target.value)) }} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="new-ws-owner">Owner</Label>
            <select id="new-ws-owner" value={owner} onChange={(e) => setOwner(e.target.value)}
              className="h-9 rounded-md border border-control-border bg-surface-subtle px-2 text-control">
              <option value="">Choose a person…</option>
              {owners.map((p) => <option key={p.id} value={p.id}>{displayName(p)} · {p.email}</option>)}
            </select>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button type="submit" disabled={!valid || busy === "create-ws"}>Create workspace</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
