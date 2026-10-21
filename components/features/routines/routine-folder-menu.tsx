"use client"

import * as React from "react"
import { Check, FolderInput, FolderPlus, MoreHorizontal, Palette, Pencil, Trash2 } from "lucide-react"

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { IconPickerDialog } from "@/components/ui/icon-picker-dialog"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"
import { FolderGlyph } from "@/components/features/pages/folder-glyph"
import { toast } from "@/lib/toast"
import { cn } from "@/lib/utils"
import { ROUTINE_FOLDER_MAX_DEPTH, folderDepth, folderMoveTargets, type PanelFolder } from "@/lib/routine-panel-index"
import type { RoutineFolderPatch } from "@/hooks/use-routine-folders"
import { RoutineFolderDialog } from "./routine-folder-dialog"

// A routine folder's own menu (#3768, wireframe v67 `foldermenu`): the
// folder's name as a caption, Rename…, Icon and colour…, New subfolder,
// Move to…, and Delete folder… under a separator. Share is not here: a
// routine folder grants no access (no folder ACL on the server), so there is
// nothing for it to share — a Share that changed nothing would be a lie.
//
// Every refusal is shown in its dialog in the server's words and keeps what
// was chosen; a menu without the right to change folders is not drawn.

const TRIGGER_CLASS =
  "kit-tap inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-muted-foreground-soft transition-opacity " +
  "opacity-0 group-hover/section:opacity-100 group-focus-within/section:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100 " +
  "hover:bg-foreground/[0.06] hover:text-foreground coarse:opacity-100"

export interface RoutineFolderMenuProps {
  folder: PanelFolder
  folders: ReadonlyMap<string, PanelFolder>
  onCreate: (name: string, parentId?: string | null) => Promise<string | null>
  onUpdate: (id: string, patch: RoutineFolderPatch) => Promise<string | null>
  onDelete: (id: string) => Promise<string | null>
}

type Open = null | "rename" | "icon" | "subfolder" | "move" | "delete"

export function RoutineFolderMenu({ folder, folders, onCreate, onUpdate, onDelete }: RoutineFolderMenuProps) {
  const [open, setOpen] = React.useState<Open>(null)
  const canNest = folderDepth(folders, folder.id) < ROUTINE_FOLDER_MAX_DEPTH
  const close = (next: boolean) => {
    if (!next) setOpen(null)
  }
  const saveIcon = async ({ icon, color }: { icon: string; color: string }) => {
    const message = await onUpdate(folder.id, { icon, color })
    if (message) toast.error("Could not change the icon", { description: message })
  }

  return (
    <>
      {/* modal={false}: an item opens a dialog, and a modal menu closing in the
          same tick as a modal dialog opening leaves the body unclickable. */}
      <DropdownMenu modal={false}>
        <DropdownMenuTrigger asChild>
          <button type="button" aria-label={`Options for folder ${folder.name}`} title="Folder options" data-routine-folder-menu className={TRIGGER_CLASS}>
            <MoreHorizontal className="h-3.5 w-3.5" aria-hidden />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" aria-label={`Options for folder ${folder.name}`} className="min-w-[180px] [&_[role=menuitem]]:text-xs">
          <DropdownMenuLabel className="truncate text-label text-muted-foreground">{folder.name}</DropdownMenuLabel>
          <DropdownMenuItem onSelect={() => setOpen("rename")}>
            <Pencil aria-hidden />
            Rename…
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setOpen("icon")}>
            <Palette aria-hidden />
            Icon and colour…
          </DropdownMenuItem>
          {canNest && (
            <DropdownMenuItem onSelect={() => setOpen("subfolder")}>
              <FolderPlus aria-hidden />
              New subfolder
            </DropdownMenuItem>
          )}
          <DropdownMenuItem onSelect={() => setOpen("move")}>
            <FolderInput aria-hidden />
            Move to…
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onSelect={() => setOpen("delete")}>
            <Trash2 aria-hidden />
            Delete folder…
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <RoutineFolderDialog
        open={open === "rename"}
        onOpenChange={close}
        title="Rename folder"
        description="The routines in it stay where they are."
        context={folder.name}
        initialName={folder.name}
        primaryLabel="Save"
        onCreate={(name) => onUpdate(folder.id, { name })}
      />
      <RoutineFolderDialog
        open={open === "subfolder"}
        onOpenChange={close}
        title="New subfolder"
        description={`Goes inside ${folder.name}. Folders nest at most ${ROUTINE_FOLDER_MAX_DEPTH} levels deep.`}
        context={folder.name}
        onCreate={(name) => onCreate(name, folder.id)}
      />
      <IconPickerDialog
        open={open === "icon"}
        onOpenChange={close}
        context={folder.name}
        concept="routines"
        description="Pick an icon and a colour for the folder. They show beside its name in the Routines panel."
        icon={folder.icon ?? null}
        color={folder.color ?? null}
        defaultIcon="folder"
        onSave={saveIcon}
      />
      <RoutineFolderMoveDialog open={open === "move"} onOpenChange={close} folder={folder} folders={folders} onUpdate={onUpdate} />
      <ConfirmDialog
        open={open === "delete"}
        onOpenChange={close}
        title={`Delete folder ${folder.name}?`}
        description="The folder goes away; nothing in it is deleted."
        consequences={[
          { tone: "kept", text: "Its routines are kept and become unfiled." },
          { tone: "kept", text: "Its subfolders move up a level, with what they hold." },
        ]}
        confirmLabel="Delete folder"
        destructive
        onConfirm={async () => {
          const message = await onDelete(folder.id)
          if (message) toast.error("Could not delete the folder", { description: message })
          else toast.success(`Deleted folder ${folder.name}`)
        }}
      />
    </>
  )
}

/** Move to…: the top level and every folder this one can go in. */
function RoutineFolderMoveDialog({
  open,
  onOpenChange,
  folder,
  folders,
  onUpdate,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  folder: PanelFolder
  folders: ReadonlyMap<string, PanelFolder>
  onUpdate: RoutineFolderMenuProps["onUpdate"]
}) {
  const current = folder.parent_id && folders.has(folder.parent_id) ? folder.parent_id : null
  const [target, setTarget] = React.useState<string | null>(current)
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const [busy, setBusy] = React.useState(false)
  React.useEffect(() => {
    if (open) {
      setTarget(current)
      setRefusal(null)
    }
  }, [open, current])
  const options = React.useMemo(() => folderMoveTargets(folders, folder.id), [folders, folder.id])
  const listId = React.useId()
  const changed = target !== current
  const submit = async () => {
    if (!changed || busy) return
    setBusy(true)
    setRefusal(null)
    try {
      const message = await onUpdate(folder.id, { parent_id: target })
      if (message) setRefusal(message)
      else onOpenChange(false)
    } finally {
      setBusy(false)
    }
  }
  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm" dirty={changed} onSubmit={() => void submit()}>
      <CreateSurfaceHeader
        concept="routines"
        context={folder.name}
        title="Move folder"
        description={`Its routines and subfolders go with it. Folders nest at most ${ROUTINE_FOLDER_MAX_DEPTH} levels deep.`}
        onClose={() => onOpenChange(false)}
      />
      <CreateSurfaceBody>
        <div role="radiogroup" aria-labelledby={listId} className="flex flex-col gap-1">
          <span id={listId} className="type-page-label text-muted-foreground">
            Move to
          </span>
          {options.map((o) => {
            const active = target === o.id
            return (
              <button
                key={o.id ?? "__top"}
                type="button"
                role="radio"
                aria-checked={active}
                data-target={o.id ?? "top"}
                onClick={() => {
                  setTarget(o.id)
                  setRefusal(null)
                }}
                className={cn(
                  "flex min-h-8 items-center gap-2 rounded-md border px-2.5 py-1.5 text-left text-xs transition-colors coarse:min-h-[44px]",
                  active
                    ? "border-primary/40 bg-primary/15 text-primary-hover"
                    : "border-hairline bg-foreground/[0.03] text-muted-foreground hover:bg-foreground/[0.07] hover:text-foreground",
                )}
              >
                <FolderGlyph icon={o.folder?.icon} color={o.folder?.color} className={o.folder ? undefined : "opacity-60"} />
                <span className="min-w-0 flex-1 truncate" title={o.label}>
                  {o.label}
                </span>
                {o.id === current && <span className="type-page-label shrink-0 text-muted-foreground-soft">current</span>}
                {active && <Check className="h-3.5 w-3.5 shrink-0" aria-hidden />}
              </button>
            )
          })}
        </div>
      </CreateSurfaceBody>
      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />
      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel="Move"
        onPrimary={() => void submit()}
        primaryDisabled={!changed || busy}
        busy={busy}
      />
    </CreateSurface>
  )
}
