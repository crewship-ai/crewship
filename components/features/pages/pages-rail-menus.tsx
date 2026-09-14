"use client"

/**
 * The two "⋯" menus on the Pages rail (#2527, collections analysis §3 S-1).
 *
 *  · `PageRowMenu` sits at the end of a page row: *Move to folder…* and, when
 *    the page is in one, *Remove from folder*.
 *  · `FolderHeaderMenu` sits beside a folder's section header: *Rename or
 *    change icon…*, *Sharing…* (#2533) and *Delete folder…*.
 *
 * Both are Radix dropdowns from the shared kit, and both are reachable
 * without a pointer. The trigger is a real button in the row's tab order,
 * revealed on hover or focus rather than absent; the rail's list handler
 * also opens the row menu on Shift+F10 and the ContextMenu key, which is
 * the convention every desktop tree uses. Opened from the keyboard, Radix
 * lands focus on the first item, so Enter is the next key.
 *
 * The trigger and the menu stop their keyboard and pointer events at
 * themselves. Both live INSIDE `ListRow` in the React tree — the menu is
 * portaled in the DOM, but a synthetic event still bubbles up the component
 * tree — and the row answers Enter, Space and click by selecting the page;
 * without the stop, choosing an item would also open the page.
 */

import * as React from "react"
import { FolderInput, FolderMinus, MoreHorizontal, Pencil, Trash2, Users } from "lucide-react"

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { cn } from "@/lib/utils"

const TRIGGER_CLASS =
  "kit-tap inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-muted-foreground-soft transition-opacity " +
  "opacity-0 group-hover/row:opacity-100 group-focus-within/row:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100 " +
  "hover:bg-white/[0.06] hover:text-foreground coarse:opacity-100"

function stop(e: React.SyntheticEvent) {
  e.stopPropagation()
}

export interface PageRowMenuProps {
  pageName: string
  inFolder: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
  onMove: () => void
  onRemoveFromFolder: () => void
}

export function PageRowMenu({
  pageName,
  inFolder,
  open,
  onOpenChange,
  onMove,
  onRemoveFromFolder,
}: PageRowMenuProps) {
  return (
    // `modal={false}`: an item here opens a dialog, and a modal menu closing
    // in the same tick as a modal dialog opening leaves `pointer-events:none`
    // on the body — the dialog then cannot be clicked.
    <DropdownMenu open={open} onOpenChange={onOpenChange} modal={false}>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={`Actions for ${pageName}`}
          data-rail-row-menu
          className={TRIGGER_CLASS}
          onClick={stop}
          onKeyDown={stop}
          onPointerDown={stop}
        >
          <MoreHorizontal className="h-3.5 w-3.5" aria-hidden />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        aria-label={`Actions for ${pageName}`}
        // The kit's item is `text-sm`; the rail is the navigation register.
        className="min-w-[180px] [&_[role=menuitem]]:text-xs"
        onKeyDown={stop}
        onClick={stop}
        onPointerDown={stop}
      >
        <DropdownMenuItem onSelect={onMove}>
          <FolderInput aria-hidden />
          Move to folder…
        </DropdownMenuItem>
        {inFolder && (
          <DropdownMenuItem onSelect={onRemoveFromFolder}>
            <FolderMinus aria-hidden />
            Remove from folder
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export interface FolderHeaderMenuProps {
  folderName: string
  onRename: () => void
  /** Absent, the item is not drawn — a server without folder permissions. */
  onShare?: () => void
  onDelete: () => void
}

export function FolderHeaderMenu({ folderName, onRename, onShare, onDelete }: FolderHeaderMenuProps) {
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={`Actions for folder ${folderName}`}
          data-rail-folder-menu
          className={cn(TRIGGER_CLASS, "group-hover/section:opacity-100 group-focus-within/section:opacity-100")}
        >
          <MoreHorizontal className="h-3.5 w-3.5" aria-hidden />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        aria-label={`Actions for folder ${folderName}`}
        className="min-w-[180px] [&_[role=menuitem]]:text-xs"
        onKeyDown={stop}
        onClick={stop}
        onPointerDown={stop}
      >
        <DropdownMenuItem onSelect={onRename}>
          <Pencil aria-hidden />
          Rename or change icon…
        </DropdownMenuItem>
        {onShare && (
          <DropdownMenuItem onSelect={onShare}>
            <Users aria-hidden />
            Sharing…
          </DropdownMenuItem>
        )}
        <DropdownMenuItem variant="destructive" onSelect={onDelete}>
          <Trash2 aria-hidden />
          Delete folder…
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
