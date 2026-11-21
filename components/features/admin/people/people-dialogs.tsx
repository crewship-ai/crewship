"use client"

import * as React from "react"
import { Building2, Check, UserPlus, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Combobox, type ComboboxOption } from "@/components/ui/combobox"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { toast } from "@/lib/toast"
import { LANGUAGES } from "@/lib/languages"
import { displayName, isGuest, isPersonal, personStatus, roleIn, type Person, type Role, type Workspace } from "./people-model"
import { PersonAvatar, RoleSegmented, SetupLinkBox, roleLabel } from "./people-parts"
import { personOptions, workspaceOptions } from "./people-pickers"
import type { OwnershipConflict, SetupLink, usePeople } from "./use-people"

type Actions = ReturnType<typeof usePeople>["actions"]

/**
 * Add person: an account, access to any of the workspaces, and a setup link to
 * pass on. Workspaces are found by search (there may be dozens) and each one
 * picked carries its own role. Owner is never offered: a workspace gets its
 * owner when it is created or handed over.
 */
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
  const [picked, setPicked] = React.useState<{ id: string; role: Role }[]>([])
  const [result, setResult] = React.useState<{ userId: string; link: SetupLink } | null>(null)
  React.useEffect(() => {
    if (open) { setEmail(""); setName(""); setPicked([]); setResult(null) }
  }, [open])
  const teams = workspaces.filter((w) => !isPersonal(w))
  const addable = teams.filter((w) => !picked.some((x) => x.id === w.id))
  const valid = email.includes("@")
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!valid) return
    const out = await actions.createPerson(email.trim(), name.trim(), picked.map((x) => ({ workspace_id: x.id, role: x.role })))
    if (out) setResult(out)
  }
  const formRef = React.useRef<HTMLFormElement>(null)
  const creating = busy === "create-person"
  return (
    <CreateSurface
      open={open}
      onOpenChange={onOpenChange}
      size="md"
      onSubmit={() => { if (!result && valid && !creating) formRef.current?.requestSubmit() }}
    >
        <CreateSurfaceHeader
          icon={result ? Check : UserPlus}
          accent={result ? "green" : undefined}
          title={result ? "Send them this link" : "Add person"}
          description={result ? "Shown once. It is stored hashed and cannot be displayed again." : "Creates the account and gives you a setup link. No email is sent."}
          onClose={() => onOpenChange(false)}
        />
        {result ? (
          <>
          <CreateSurfaceBody className="grid gap-3">
            <p className="text-control"><b className="font-medium">{result.link.email}</b> has an account now. Send them this link to choose a password.</p>
            <div className="-mx-4"><SetupLinkBox url={result.link.url} expiresAt={result.link.expires_at} /></div>
            <p className="text-label text-muted-foreground">A new link makes the old one stop working.</p>
          </CreateSurfaceBody>
          <CreateSurfaceFooter
            hint={null}
            onCancel={() => onOpenChange(false)}
            cancelLabel="Close"
            primaryLabel="Open their profile"
            onPrimary={() => { onCreated(result.userId); onOpenChange(false) }}
          />
          </>
        ) : (
          <>
          <CreateSurfaceBody>
          <form ref={formRef} onSubmit={submit} className="grid gap-4">
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
              <Combobox modal ariaLabel="Add a workspace" placeholder="Add a workspace" searchPlaceholder={`Search ${teams.length} workspaces`}
                value={null} options={workspaceOptions(addable)} onChange={(id) => setPicked((prev) => [...prev, { id, role: "MEMBER" }])}
                disabled={addable.length === 0} />
              {picked.length > 0 && (
                <div className="rounded-lg border border-border">
                  {picked.map((x) => {
                    const w = teams.find((t) => t.id === x.id)
                    if (!w) return null
                    return (
                      <div key={x.id} className="flex flex-wrap items-center gap-2.5 border-b border-border px-3 py-2 last:border-b-0">
                        <WorkspaceTile id={w.id} name={w.name} logoUrl={w.logo_url} />
                        <span className="min-w-0 flex-1 truncate text-control">{w.name}</span>
                        <RoleSegmented label={`Role in ${w.name}`} value={x.role} onChange={(r) => r && setPicked((prev) => prev.map((y) => (y.id === x.id ? { ...y, role: r } : y)))} />
                        <Button type="button" size="icon-sm" variant="ghost" className="h-7 w-7" aria-label={`Do not add to ${w.name}`}
                          onClick={() => setPicked((prev) => prev.filter((y) => y.id !== x.id))}><X className="h-3.5 w-3.5" /></Button>
                      </div>
                    )
                  })}
                </div>
              )}
              <p className="text-label text-muted-foreground">Search finds any of the {teams.length} workspaces. With none picked, they sign in to nothing until added. Personal accounts are not offered.</p>
            </fieldset>
          </form>
          </CreateSurfaceBody>
          <CreateSurfaceFooter
            onCancel={() => onOpenChange(false)}
            primaryLabel="Create and get link"
            onPrimary={() => formRef.current?.requestSubmit()}
            primaryDisabled={!valid || creating}
          />
          </>
        )}
    </CreateSurface>
  )
}

const slugify = (s: string) => s.toLowerCase().normalize("NFD").replace(/[̀-ͯ]/g, "").replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 50)

const NO_LANGUAGE = "none"
const languageOptions: ComboboxOption[] = [
  { value: NO_LANGUAGE, label: "Not set", description: "The workspace owner can set it later" },
  ...LANGUAGES.map((l) => ({ value: l.name, label: `${l.flag} ${l.name}`, description: l.native, keywords: [l.code, l.native] })),
]

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
  const [language, setLanguage] = React.useState(NO_LANGUAGE)
  const [adds, setAdds] = React.useState<{ id: string; role: Role }[]>([])
  React.useEffect(() => {
    if (open) { setName(""); setSlug(""); setSlugTouched(false); setOwner(""); setLanguage(NO_LANGUAGE); setAdds([]) }
  }, [open])
  const addable = owners.filter((p) => p.id !== owner && !adds.some((a) => a.id === p.id))
  const effectiveSlug = slugTouched ? slug : slugify(name)
  const valid = name.trim().length >= 2 && effectiveSlug.length >= 2 && !!owner
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!valid) return
    const id = await actions.createWorkspace(name.trim(), effectiveSlug, owner, language === NO_LANGUAGE ? null : language)
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
  const formRef = React.useRef<HTMLFormElement>(null)
  const creating = busy === "create-ws"
  return (
    <CreateSurface
      open={open}
      onOpenChange={onOpenChange}
      size="md"
      onSubmit={() => { if (valid && !creating) formRef.current?.requestSubmit() }}
    >
        <CreateSurfaceHeader
          icon={Building2}
          title="New workspace"
          description="A place for a team. It starts empty; its owner adds crews. You are not added."
          onClose={() => onOpenChange(false)}
        />
        <CreateSurfaceBody>
        <form ref={formRef} onSubmit={submit} className="grid gap-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="new-ws-name">Name</Label>
              <Input id="new-ws-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="Marketing" />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="new-ws-slug">Address</Label>
              <Input id="new-ws-slug" className="font-mono" value={effectiveSlug} onChange={(e) => { setSlugTouched(true); setSlug(slugify(e.target.value)) }} />
            </div>
          </div>
          <p className="-mt-2 text-label text-muted-foreground">The address is made from the name. It cannot change later; the name can.</p>
          <div className="grid gap-1.5">
            <Label htmlFor="new-ws-owner">Owner</Label>
            <Combobox modal id="new-ws-owner" ariaLabel="Owner" placeholder="Choose a person…" searchPlaceholder={`Search ${owners.length} people`}
              value={owner || null} options={personOptions(owners)} onChange={setOwner} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="new-ws-language">Agent language</Label>
            <Combobox modal id="new-ws-language" ariaLabel="Agent language" searchPlaceholder="Search language…"
              value={language} options={languageOptions} onChange={setLanguage} />
            <p className="text-label text-muted-foreground">Agents answer in this language.</p>
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
                  <RoleSegmented label={`Role of ${displayName(p)}`} value={a.role} guest={isGuest(p)} onChange={(r) => r && setAdds((prev) => prev.map((x) => (x.id === a.id ? { ...x, role: r } : x)))} />
                  <Button type="button" size="icon-sm" variant="ghost" className="h-7 w-7" aria-label={`Do not add ${displayName(p)}`}
                    onClick={() => setAdds((prev) => prev.filter((x) => x.id !== a.id))}><X className="h-3.5 w-3.5" /></Button>
                </div>
              )
            })}
            <Combobox modal ariaLabel="Add someone" placeholder={addable.length ? "Add someone" : "Everyone is already picked"} searchPlaceholder={`Search ${addable.length} people`}
              value={null} options={personOptions(addable)} disabled={addable.length === 0}
              onChange={(id) => setAdds((prev) => [...prev, { id, role: "MEMBER" }])} />
          </fieldset>
        </form>
        </CreateSurfaceBody>
        <CreateSurfaceFooter
          onCancel={() => onOpenChange(false)}
          primaryLabel="Create workspace"
          onPrimary={() => formRef.current?.requestSubmit()}
          primaryDisabled={!valid || creating}
        />
    </CreateSurface>
  )
}

/**
 * Add one or several people to a workspace: found by search, with a role that
 * is never Owner (the workspace is handed over for that). Each person is one
 * call; the toast counts what went through.
 */
export function AddToWorkspaceDialog({ open, onOpenChange, people, workspaces, actions, busy, onDone }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  people: Person[]
  /** Team workspaces only: a personal account is one person's own. */
  workspaces: Workspace[]
  actions: Actions
  busy: string | null
  onDone?: () => void
}) {
  const [wsId, setWsId] = React.useState("")
  const [role, setRole] = React.useState<Role | null>("MEMBER")
  React.useEffect(() => { if (open) { setWsId(""); setRole("MEMBER") } }, [open])
  const n = people.length
  const anyGuest = people.some((p) => isGuest(p))
  // For one person, the workspaces they are already in are not offered.
  const offered = n === 1 ? workspaces.filter((w) => !roleIn(people[0], w.id)) : workspaces
  const target = offered.find((w) => w.id === wsId)
  const go = async () => {
    if (!target || !role) return
    let ok = 0
    for (const p of people) {
      const done = n === 1 ? `${displayName(p)} joined ${target.name} as ${roleLabel(role)}` : ""
      if (await actions.setRole(p.id, target.id, role, done)) ok++
    }
    if (n > 1) toast.success(`${ok} of ${n} added to ${target.name}`)
    if (ok > 0) { onOpenChange(false); onDone?.() }
  }
  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm" onSubmit={() => { if (target && role) void go() }}>
      <CreateSurfaceHeader icon={Building2} title={n > 1 ? `Add ${n} people to a workspace` : "Add to a workspace"} onClose={() => onOpenChange(false)} />
      <CreateSurfaceBody className="grid gap-4">
        <div className="grid gap-1.5">
          <Label htmlFor="add-to-ws">Workspace</Label>
          <Combobox modal id="add-to-ws" ariaLabel="Workspace" placeholder={`Search ${offered.length} workspaces`} searchPlaceholder={`Search ${offered.length} workspaces`}
            value={wsId || null} options={workspaceOptions(offered)} onChange={setWsId} />
        </div>
        <div className="grid gap-1.5">
          <span className="text-sm font-medium">Role</span>
          <RoleSegmented label="Role" value={role} onChange={setRole} guest={anyGuest} />
          {anyGuest && <p className="text-label text-muted-foreground">{n > 1 ? "Some of them are Guests" : "This person is a Guest"}, so Admin is not offered.</p>}
          <p className="text-label text-muted-foreground">Owner is given by transferring the workspace, never here.</p>
        </div>
      </CreateSurfaceBody>
      <CreateSurfaceFooter hint={null} onCancel={() => onOpenChange(false)} primaryLabel={n > 1 ? `Add ${n}` : "Add"}
        primaryDisabled={!target || !role || busy !== null} onPrimary={() => void go()} />
    </CreateSurface>
  )
}

/**
 * Suspend one person or several, through the shared confirmation. It says what
 * ends and what stays — including that sessions and CLI tokens stay ended
 * after a reactivation — and takes an optional reason for the audit log.
 */
export function SuspendConfirm({ open, onOpenChange, people, onSuspend }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  people: Person[]
  /** Throws when nothing went through, which keeps the dialog open. */
  onSuspend: (reason: string) => Promise<void>
}) {
  const [reason, setReason] = React.useState("")
  React.useEffect(() => { if (!open) setReason("") }, [open])
  const n = people.length
  const one = n === 1 ? displayName(people[0]) : null
  return (
    <ConfirmDialog open={open} onOpenChange={onOpenChange} destructive
      title={one ? `Suspend ${one}?` : `Suspend ${n} people?`}
      description={one ? `${one} cannot sign in until reactivated.` : `${people.map(displayName).join(", ")} can no longer sign in.`}
      consequences={[
        { tone: "lost", text: "Their sessions and CLI tokens end first, and stay ended after a reactivation" },
        { tone: "lost", text: "They cannot sign in anywhere on this server" },
        { tone: "kept", text: "Memberships, crews and data are kept; Reactivate brings them back" },
      ]}
      confirmLabel={one ? `Suspend ${one.split(/\s+/)[0]}` : `Suspend ${n}`}
      onConfirm={() => onSuspend(reason.trim())}>
      <Label htmlFor="suspend-reason" className="text-xs">Reason (kept in the audit log)</Label>
      <Input id="suspend-reason" value={reason} onChange={(e) => setReason(e.target.value)} />
    </ConfirmDialog>
  )
}

/**
 * The server refused to remove someone because pages they own have no one to
 * take over. Its own explanation first, then what to do instead.
 */
export function OwnershipConflictDialog({ conflict, people, workspaces, onClose }: {
  conflict: OwnershipConflict | null
  people: Person[]
  workspaces: Workspace[]
  onClose: () => void
}) {
  const who = conflict ? people.find((p) => p.id === conflict.userId) : undefined
  const where = conflict ? workspaces.find((w) => w.id === conflict.workspaceId) : undefined
  const name = who ? displayName(who) : "This person"
  return (
    <ConfirmDialog open={conflict !== null} onOpenChange={(o) => { if (!o) onClose() }} closeOnly cancelLabel="Got it"
      title="Pages need a new owner first"
      description={conflict?.message}
      summary={conflict && conflict.pages > 0 ? `${name} owns ${conflict.pages} ${conflict.pages === 1 ? "page" : "pages"}${where ? ` in ${where.name}` : ""} nobody else can take over` : undefined}
      consequences={[
        { tone: "warn", text: `Ask an administrator of ${where?.name ?? "the workspace"} to hand those pages to someone who stays` },
        { tone: "warn", text: "They can list them with “crewship workspace page-ownership list” and hand them over with “crewship workspace page-ownership transfer”" },
        { tone: "kept", text: `Then remove ${name} again; nothing was changed now` },
      ]} />
  )
}

/** A freshly issued setup link: shown once, never stored. */
export function SetupLinkDialog({ link, onClose, onOpenProfile }: {
  link: (SetupLink & { userId?: string }) | null
  onClose: () => void
  onOpenProfile?: (userId: string) => void
}) {
  // Keep the last link on screen while the dialog fades out.
  const last = React.useRef<SetupLink | null>(null)
  if (link) last.current = link
  const shown = link ?? last.current
  return (
    <CreateSurface open={link !== null} onOpenChange={(o) => { if (!o) onClose() }} size="sm">
      <CreateSurfaceHeader icon={Check} accent="green" title="Send them this link"
        description="Shown once. It is stored hashed and cannot be displayed again." onClose={onClose} />
      <CreateSurfaceBody className="grid gap-3">
        {shown && (
          <>
            <p className="text-control"><b className="font-medium">{shown.email}</b> chooses their password with this link.</p>
            <div className="-mx-4"><SetupLinkBox url={shown.url} expiresAt={shown.expires_at} /></div>
            <p className="text-label text-muted-foreground">A new link makes the old one stop working.</p>
          </>
        )}
      </CreateSurfaceBody>
      <CreateSurfaceFooter hint={null} onCancel={onClose} cancelLabel="Done"
        secondary={link?.userId && onOpenProfile ? <Button type="button" variant="ghost" onClick={() => { onOpenProfile(link.userId!); onClose() }}>Open their profile</Button> : undefined} />
    </CreateSurface>
  )
}
