"use client"

import { RAIL_ROW } from "@/components/layout/rail"
import { useEffect, useState } from "react"
import Link from "next/link"
import { Check, ChevronDown, Plus, Settings, UserPlus } from "lucide-react"
import { toast } from "sonner"
import { useWorkspace, type WorkspaceData } from "@/hooks/use-workspace"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem } from "@/components/ui/sidebar"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { isAdminTier } from "@/lib/permissions/tiers"

const ROLE_LABEL: Record<string, string> = {
  OWNER: "Owner",
  ADMIN: "Admin",
  MANAGER: "Manager",
  MEMBER: "Member",
  VIEWER: "Viewer",
}

function avatarLetter(name: string): string {
  const trimmed = name.trim()
  return trimmed ? trimmed[0]!.toUpperCase() : "?"
}

/**
 * A workspace drawn the way its people know it: its logo when one was
 * uploaded (#3005), its initial otherwise. Size and colours come from
 * `className`; a logo that fails to load falls back to the initial.
 */
export function WorkspaceMark({ name, logoUrl, className }: { name: string; logoUrl?: string | null; className?: string }) {
  const [broken, setBroken] = useState(false)
  useEffect(() => setBroken(false), [logoUrl])
  if (logoUrl && !broken) {
    return <img src={logoUrl} alt="" aria-hidden onError={() => setBroken(true)} className={cn("shrink-0 object-cover", className)} />
  }
  return <span className={className} aria-hidden>{avatarLetter(name)}</span>
}

function slugify(name: string): string {
  return name
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 50)
}

export function WorkspaceSwitcher() {
  const { workspace, role, loading, setWorkspaceId, refresh } = useWorkspace()
  const [createOpen, setCreateOpen] = useState(false)

  const triggerLabel = workspace?.name ?? (loading ? "Loading…" : "No workspace")
  const triggerSub = workspace ? ROLE_LABEL[role ?? ""] ?? role ?? "" : ""

  return (
    <>
      <SidebarMenu>
        <SidebarMenuItem>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <SidebarMenuButton
                size="lg"
                tooltip={triggerSub ? `${triggerLabel} · ${triggerSub}` : triggerLabel}
                aria-label={`Current workspace: ${triggerLabel}`}
                className={RAIL_ROW}
              >
                {workspace ? (
                  <WorkspaceMark name={workspace.name} logoUrl={workspace.logo_url}
                    className="rail-tile rail-tile-static bg-primary! text-[13px] font-bold text-primary-foreground! shadow-[inset_0_1px_0_rgb(255_255_255/.18)]" />
                ) : (
                  <span className="rail-tile rail-tile-static bg-primary! text-[13px] font-bold text-primary-foreground! shadow-[inset_0_1px_0_rgb(255_255_255/.18)]" aria-hidden>·</span>
                )}
                {/* The name gets the whole row, up to two lines. The rail row is
                    36px and the name had ~120px beside the tile and chevron,
                    so with the role stacked under it any real name truncated
                    ("Demo User's …"). The role is in the tooltip, in the menu
                    rows below, and in the user menu. */}
                <span className="rail-label min-w-0 flex-1 text-left">
                  <span className="line-clamp-2 break-words text-[12.5px] font-semibold leading-[1.15]">{triggerLabel}</span>
                </span>
                <ChevronDown className="rail-label mr-2 h-3 w-3 shrink-0 text-muted-foreground" />
              </SidebarMenuButton>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" side="bottom" className="w-72">
              <WorkspaceMenuItems onCreate={() => setCreateOpen(true)} />
            </DropdownMenuContent>
          </DropdownMenu>
        </SidebarMenuItem>
      </SidebarMenu>

      <CreateWorkspaceDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={async (id) => {
          await refresh()
          setWorkspaceId(id)
        }}
      />
    </>
  )
}

/**
 * The switcher's menu body, shared by the rail (desktop) and the phone
 * navigation sheet, which has no rail and used to print a hardcoded
 * placeholder workspace in that slot instead.
 *
 * Beyond the list it carries the two places people go "for the workspace":
 * its settings and its people. Both are rows in Settings already, but from
 * here they are one click from the name they belong to rather than a trip
 * through the profile menu, which is about the person, not the workspace.
 */
function WorkspaceMenuItems({ onCreate, onNavigate }: { onCreate: () => void; onNavigate?: () => void }) {
  const { workspace, workspaces, role, loading, setWorkspaceId } = useWorkspace()
  return (
    <>
      <DropdownMenuLabel className="text-micro uppercase tracking-wider text-muted-foreground font-medium">
        Workspaces
      </DropdownMenuLabel>
      {workspaces.length === 0 && (
        <div className="px-2 py-2 text-xs text-muted-foreground">
          {loading ? "Loading workspaces…" : "No workspaces yet"}
        </div>
      )}
      {workspaces.map((ws) => (
        <WorkspaceRow
          key={ws.id}
          ws={ws}
          active={ws.id === workspace?.id}
          onSelect={() => setWorkspaceId(ws.id)}
        />
      ))}
      <DropdownMenuSeparator />
      {workspace && (
        <>
          <DropdownMenuItem asChild className="text-xs gap-2">
            <Link href="/settings?tab=general" onClick={onNavigate}>
              <Settings className="h-3.5 w-3.5" />
              Workspace settings
            </Link>
          </DropdownMenuItem>
          {/* The roster is readable at every role; inviting is ADMIN+ there
              (the Members page's isAdmin), so the label only promises it to
              those who can. */}
          <DropdownMenuItem asChild className="text-xs gap-2">
            <Link href="/settings/members" onClick={onNavigate}>
              <UserPlus className="h-3.5 w-3.5" />
              {isAdminTier(role) ? "Invite members" : "Members"}
            </Link>
          </DropdownMenuItem>
        </>
      )}
      <DropdownMenuItem
        className="text-xs gap-2"
        onSelect={(e) => {
          e.preventDefault()
          onCreate()
        }}
      >
        <Plus className="h-3.5 w-3.5" />
        Create workspace
      </DropdownMenuItem>
    </>
  )
}

/**
 * The phone navigation sheet's header: the same switcher, drawn without the
 * rail's sidebar primitives (the sheet is not inside a sidebar).
 */
export function MobileWorkspaceSwitcher({ onNavigate }: { onNavigate?: () => void }) {
  const { workspace, role, loading, refresh, setWorkspaceId } = useWorkspace()
  const [createOpen, setCreateOpen] = useState(false)
  const label = workspace?.name ?? (loading ? "Loading…" : "No workspace")
  const sub = workspace ? ROLE_LABEL[role ?? ""] ?? role ?? "" : ""
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label={`Current workspace: ${label}`}
            className="flex min-h-12 min-w-0 flex-1 items-center gap-2 rounded-md px-1 text-left hover:bg-accent"
          >
            {workspace ? (
              <WorkspaceMark name={workspace.name} logoUrl={workspace.logo_url}
                className="flex h-7 w-7 items-center justify-center rounded-md bg-primary text-xs font-bold text-primary-foreground" />
            ) : (
              <span className="flex h-7 w-7 items-center justify-center rounded-md bg-primary text-xs font-bold text-primary-foreground" aria-hidden>·</span>
            )}
            <span className="grid min-w-0 flex-1 leading-tight">
              <span className="truncate text-sm font-semibold">{label}</span>
              {sub && <span className="truncate text-micro text-muted-foreground">{sub}</span>}
            </span>
            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" side="bottom" className="w-72">
          <WorkspaceMenuItems onCreate={() => setCreateOpen(true)} onNavigate={onNavigate} />
        </DropdownMenuContent>
      </DropdownMenu>
      <CreateWorkspaceDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={async (id) => {
          await refresh()
          setWorkspaceId(id)
        }}
      />
    </>
  )
}

function WorkspaceRow({
  ws,
  active,
  onSelect,
}: {
  ws: WorkspaceData
  active: boolean
  onSelect: () => void
}) {
  return (
    <DropdownMenuItem
      onSelect={(e) => {
        e.preventDefault()
        onSelect()
      }}
      className={cn("flex items-center gap-3 py-2", active && "bg-primary/5")}
    >
      <WorkspaceMark name={ws.name} logoUrl={ws.logo_url}
        className="flex h-7 w-7 items-center justify-center rounded-lg bg-primary text-micro font-bold text-primary-foreground shrink-0" />
      <div className="min-w-0 flex-1">
        <div className="text-xs font-medium truncate">{ws.name}</div>
        <div className="text-micro text-muted-foreground truncate">
          {ROLE_LABEL[ws.currentUserRole ?? ""] ?? ws.currentUserRole ?? ws.slug}
        </div>
      </div>
      {active && <Check className="h-3.5 w-3.5 text-primary shrink-0" />}
    </DropdownMenuItem>
  )
}

export function CreateWorkspaceDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: (workspaceId: string) => void | Promise<void>
}) {
  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const [slugTouched, setSlugTouched] = useState(false)
  const [submitting, setSubmitting] = useState(false)

  function reset() {
    setName("")
    setSlug("")
    setSlugTouched(false)
    setSubmitting(false)
  }

  // Clear form when the dialog closes from any source — Cancel button,
  // Escape, or overlay click. The Dialog's own onOpenChange handles
  // Esc/overlay, but Cancel calls the parent setter directly, which
  // would otherwise leak stale name/slug into the next open.
  useEffect(() => {
    if (!open && !submitting) reset()
  }, [open, submitting])

  function handleNameChange(v: string) {
    setName(v)
    if (!slugTouched) setSlug(slugify(v))
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (name.trim().length < 2) {
      toast.error("Name must be at least 2 characters")
      return
    }
    if (slug.length < 2) {
      toast.error("Slug must be at least 2 characters")
      return
    }
    setSubmitting(true)
    try {
      const res = await apiFetch("/api/v1/workspaces", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name.trim(), slug }),
      })
      if (!res.ok) {
        const text = await res.text().catch(() => "")
        let message = "Failed to create workspace"
        try {
          const json = JSON.parse(text)
          message = json.error ?? json.message ?? message
        } catch {
          if (text) message = text
        }
        toast.error(message)
        return
      }
      const data = (await res.json()) as { id: string; name: string }
      toast.success(`Workspace "${data.name}" created`)
      await onCreated(data.id)
      reset()
      onOpenChange(false)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to create workspace")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!submitting) onOpenChange(v)
      }}
    >
      <DialogContent className="max-w-md">
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>Create workspace</DialogTitle>
            <DialogDescription>
              Workspaces are isolated tenants — crews, credentials, and runs live inside one workspace.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-1.5">
              <Label htmlFor="ws-name">Name</Label>
              <Input
                id="ws-name"
                autoFocus
                value={name}
                onChange={(e) => handleNameChange(e.target.value)}
                placeholder="Acme Engineering"
                maxLength={100}
                disabled={submitting}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="ws-slug">Slug</Label>
              <Input
                id="ws-slug"
                value={slug}
                onChange={(e) => {
                  setSlugTouched(true)
                  setSlug(slugify(e.target.value))
                }}
                placeholder="acme"
                maxLength={50}
                disabled={submitting}
              />
              <p className="text-micro text-muted-foreground">
                Lowercase, used in URLs and API calls. Auto-generated from the name.
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
              disabled={submitting}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={submitting || name.trim().length < 2 || slug.length < 2}>
              {submitting ? "Creating…" : "Create workspace"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
