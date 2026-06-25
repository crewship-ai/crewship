"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { Check, ChevronDown, Plus } from "lucide-react"
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
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem, useSidebar } from "@/components/ui/sidebar"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { cn } from "@/lib/utils"

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

function slugify(name: string): string {
  return name
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 50)
}

export function WorkspaceSwitcher() {
  const { workspace, workspaces, role, loading, setWorkspaceId, refresh } = useWorkspace()
  const [createOpen, setCreateOpen] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  const { setPopoverOpen } = useSidebar()

  // Bridge the dropdown's open state to the sidebar so a hover-expanded
  // sidebar stays pinned open while this menu is up — the menu portals
  // outside the sidebar, so otherwise moving onto it collapses the panel
  // mid-click. A ref mirrors the state so the unmount cleanup only
  // releases the lock when the menu was actually open.
  const menuOpenRef = useRef(false)
  const handleMenuOpenChange = useCallback(
    (open: boolean) => {
      menuOpenRef.current = open
      setMenuOpen(open)
      setPopoverOpen(open)
    },
    [setPopoverOpen],
  )
  useEffect(() => () => {
    if (menuOpenRef.current) setPopoverOpen(false)
  }, [setPopoverOpen])

  const triggerLabel = workspace?.name ?? (loading ? "Loading…" : "No workspace")
  const triggerSub = workspace ? ROLE_LABEL[role ?? ""] ?? role ?? "" : ""

  return (
    <>
      <SidebarMenu>
        <SidebarMenuItem>
          <DropdownMenu open={menuOpen} onOpenChange={handleMenuOpenChange}>
            <DropdownMenuTrigger asChild>
              <SidebarMenuButton
                size="lg"
                tooltip={triggerLabel}
                aria-label={`Current workspace: ${triggerLabel}`}
              >
                <div className="flex h-6 w-6 items-center justify-center rounded-md bg-primary text-[9px] font-bold text-primary-foreground shrink-0">
                  {workspace ? avatarLetter(workspace.name) : "·"}
                </div>
                <div className="grid flex-1 text-left text-sm leading-tight group-data-[collapsible=icon]:hidden">
                  <span className="truncate font-semibold text-[13px]">{triggerLabel}</span>
                  {triggerSub && (
                    <span className="truncate text-[10px] text-muted-foreground">{triggerSub}</span>
                  )}
                </div>
                <ChevronDown className="h-3 w-3 text-muted-foreground shrink-0 group-data-[collapsible=icon]:hidden" />
              </SidebarMenuButton>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" side="bottom" className="w-72">
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
              <DropdownMenuItem
                className="text-xs gap-2"
                onSelect={(e) => {
                  e.preventDefault()
                  setCreateOpen(true)
                }}
              >
                <Plus className="h-3.5 w-3.5" />
                Create workspace
              </DropdownMenuItem>
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
      <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-primary text-micro font-bold text-primary-foreground shrink-0">
        {avatarLetter(ws.name)}
      </div>
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
      const res = await fetch("/api/v1/workspaces", {
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
