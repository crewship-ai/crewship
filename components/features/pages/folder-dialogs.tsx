"use client"

/**
 * The three folder dialogs (#2527, collections analysis §3 S-2, S-3, S-4).
 *
 *  · `FolderEditDialog` — New folder, and Rename / icon for an existing one.
 *    Name, owning crew (fixed after creation), icon + colour through the
 *    shared `IconPickerDialog`.
 *  · `FolderDeleteDialog` — the shared `AlertDialog`. Only an empty folder
 *    can go; a folder with pages the caller can see has its confirm disabled
 *    with the count, and a folder the server says is not empty — pages the
 *    caller cannot see count too — shows the server's own sentence.
 *  · `MoveToFolderDialog` — the target list, Unfiled included, and under it
 *    "After the move": who will reach the page once it is there (#2533). A
 *    folder's permissions are inherited by every page in it, so a move is a
 *    change of audience and the person is shown it before they confirm. A
 *    manager of the target's owning crew is shown names, from the target's
 *    ACL; anyone else the marker the folder row carries. The request carries
 *    the page's `pages_version` and the target's `acl_version`; a 409
 *    re-reads both lists AND the target's ACL — the block is regenerated —
 *    and the person confirms again against what is on screen now. There is
 *    no "restore sharing": moving back is another move, with its own preview.
 *    With several subjects the same dialog files them through the batch
 *    route, all or nothing, and a refusal names the page it stopped at.
 *
 * Every refusal renders IN the dialog, in the server's words, and changes
 * nothing typed (#1563 rules 2 and 3). The owner picker offers every crew
 * rather than guessing which ones the caller is MANAGER+ in — the crews list
 * does not say — so a refused owner is a sentence at the control, never a
 * missing option.
 */

import * as React from "react"
import { AlertTriangle, Check } from "lucide-react"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { IconPickerDialog } from "@/components/ui/icon-picker-dialog"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"
import { cn } from "@/lib/utils"
import type { PageFolderRef } from "@/hooks/use-pages"
import {
  FOLDER_CONFLICT_SENTENCE,
  folderConflictOf,
  refusedPageOf,
  useFolderAcl,
  useFolderOwnerChoices,
  usePageFolderMutations,
  usePageFolders,
  type PageFolderView,
} from "@/hooks/use-page-folders"
import { MOVE_IMPACT_UNFILED, moveImpactFromAcl, moveImpactFromShared } from "@/lib/pages/folder-sharing"
import { FolderGlyph } from "@/components/features/pages/folder-glyph"

const SELECT_CLASS = cn(
  CREATE_SURFACE_INPUT,
  "w-full rounded-md border border-hairline bg-foreground/[0.03] px-2 text-foreground outline-none focus:border-primary/40",
)

function messageOf(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback
}

// ── New folder / Rename ────────────────────────────────────────────────────

export interface FolderEditDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Null creates; a folder renames it or changes its icon and colour. */
  folder: PageFolderView | null
  /** `crew/<slug>` preselected for a new folder — the crew most of this
   *  workspace's pages already belong to. */
  defaultOwner?: string | null
  onSaved?: (folder: PageFolderView | null) => void
}

export function FolderEditDialog({
  workspaceId,
  open,
  onOpenChange,
  folder,
  defaultOwner,
  onSaved,
}: FolderEditDialogProps) {
  const creating = folder === null
  const [name, setName] = React.useState(folder?.name ?? "")
  const [owner, setOwner] = React.useState<string>(folder?.ownerRef ?? defaultOwner ?? "")
  const [icon, setIcon] = React.useState<string | null>(folder?.icon ?? null)
  const [color, setColor] = React.useState<string | null>(folder?.color ?? null)
  const [pickingIcon, setPickingIcon] = React.useState(false)
  const [refusal, setRefusal] = React.useState<string | null>(null)

  // Each opening starts from the folder as it is now, or from nothing.
  React.useEffect(() => {
    if (!open) return
    setName(folder?.name ?? "")
    setOwner(folder?.ownerRef ?? defaultOwner ?? "")
    setIcon(folder?.icon ?? null)
    setColor(folder?.color ?? null)
    setRefusal(null)
  }, [open, folder, defaultOwner])

  const owners = useFolderOwnerChoices(workspaceId, open && creating)
  // A default the list does not contain is still a valid owner; keep it
  // selectable rather than silently dropping to the first crew.
  const ownerOptions = React.useMemo(() => {
    const list = (owners.data ?? []).map((c) => ({ ref: `crew/${c.slug}`, label: c.name }))
    if (owner && !list.some((o) => o.ref === owner)) list.unshift({ ref: owner, label: owner.replace(/^crew\//, "") })
    return list
  }, [owners.data, owner])
  React.useEffect(() => {
    if (creating && !owner && ownerOptions.length > 0) setOwner(ownerOptions[0].ref)
  }, [creating, owner, ownerOptions])

  const { create, update } = usePageFolderMutations(workspaceId)
  const busy = create.isPending || update.isPending
  const trimmedName = name.trim()
  const dirty = creating
    ? trimmedName !== "" || icon !== null || color !== null
    : trimmedName !== folder.name || icon !== folder.icon || color !== folder.color
  const submittable = trimmedName !== "" && (creating ? owner !== "" : dirty) && !busy

  const submit = async () => {
    if (!submittable) return
    setRefusal(null)
    try {
      const saved = creating
        ? await create.mutateAsync({ name: trimmedName, owner, icon, color })
        : await update.mutateAsync({
            slug: folder.slug,
            ...(trimmedName !== folder.name ? { name: trimmedName } : {}),
            ...(icon !== folder.icon ? { icon } : {}),
            ...(color !== folder.color ? { color } : {}),
          })
      onSaved?.(saved)
      onOpenChange(false)
    } catch (error) {
      setRefusal(messageOf(error, creating ? "The folder could not be created." : "The folder could not be saved."))
    }
  }

  const nameId = React.useId()
  const ownerId = React.useId()

  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm" dirty={dirty} onSubmit={() => void submit()}>
      <CreateSurfaceHeader
        concept="pages"
        context={creating ? undefined : folder.name}
        title={creating ? "New folder" : "Rename or change icon"}
        description={
          creating
            ? "A folder groups pages under one crew. A page is in at most one folder."
            : "The folder's pages stay where they are."
        }
        onClose={() => onOpenChange(false)}
      />

      <CreateSurfaceBody>
        <div className="flex flex-col gap-4">
          <CreateSurfaceField label="Name" htmlFor={nameId} required>
            <Input
              id={nameId}
              className={CREATE_SURFACE_INPUT}
              value={name}
              autoFocus
              onChange={(e) => {
                setName(e.target.value)
                if (refusal) setRefusal(null)
              }}
            />
          </CreateSurfaceField>

          <CreateSurfaceField
            label="Owning crew"
            htmlFor={ownerId}
            required={creating}
            hint={
              creating
                ? "The crew that manages the folder. You need to be a manager in it, or a workspace admin."
                : undefined
            }
          >
            {creating ? (
              <select
                id={ownerId}
                className={SELECT_CLASS}
                value={owner}
                disabled={owners.isPending && ownerOptions.length === 0}
                onChange={(e) => {
                  setOwner(e.target.value)
                  if (refusal) setRefusal(null)
                }}
              >
                {ownerOptions.length === 0 && <option value="">{owners.isPending ? "Loading crews…" : "No crews"}</option>}
                {ownerOptions.map((o) => (
                  <option key={o.ref} value={o.ref}>
                    {o.label}
                  </option>
                ))}
              </select>
            ) : (
              <p id={ownerId} className="text-xs text-muted-foreground">
                {folder.ownerLabel ?? folder.ownerRef ?? "—"}
              </p>
            )}
          </CreateSurfaceField>

          <CreateSurfaceField label="Icon and colour">
            <div className="flex items-center gap-3">
              {icon ? (
                <CrewIcon icon={icon} color={color} size="sm" />
              ) : (
                <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-foreground/[0.05]">
                  <FolderGlyph icon={null} className="h-4 w-4 text-muted-foreground" />
                </span>
              )}
              <span className="flex min-w-0 flex-1 items-center gap-1.5 text-xs text-muted-foreground">
                <FolderGlyph icon={icon} color={color} className={icon ? undefined : "opacity-60"} />
                {icon ?? "No icon"}
                {color ? ` · ${color}` : ""}
              </span>
              <Button type="button" variant="outline" size="sm" onClick={() => setPickingIcon(true)}>
                Choose…
              </Button>
            </div>
          </CreateSurfaceField>
        </div>
      </CreateSurfaceBody>

      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />

      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel={creating ? "Create folder" : "Save"}
        onPrimary={() => void submit()}
        primaryDisabled={!submittable}
        busy={busy}
      />

      <IconPickerDialog
        open={pickingIcon}
        onOpenChange={setPickingIcon}
        context={trimmedName || "Folder"}
        concept="pages"
        description="Pick an icon and a colour for the folder. The colour shows as a dot beside its name in the list."
        icon={icon}
        color={color}
        defaultIcon="folder"
        onSave={({ icon: nextIcon, color: nextColor }) => {
          setIcon(nextIcon)
          setColor(nextColor)
        }}
      />
    </CreateSurface>
  )
}

// ── Delete ─────────────────────────────────────────────────────────────────

export interface FolderDeleteDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  folder: PageFolderView | null
  onDeleted?: (slug: string) => void
}

export function FolderDeleteDialog({ workspaceId, open, onOpenChange, folder, onDeleted }: FolderDeleteDialogProps) {
  const { remove } = usePageFolderMutations(workspaceId)
  const [refusal, setRefusal] = React.useState<string | null>(null)
  React.useEffect(() => {
    if (open) setRefusal(null)
  }, [open, folder])

  const visible = folder?.pageCount ?? 0
  const blocked = visible > 0

  const confirm = async () => {
    if (!folder || blocked) return
    setRefusal(null)
    try {
      await remove.mutateAsync(folder.slug)
      onDeleted?.(folder.slug)
      onOpenChange(false)
    } catch (error) {
      setRefusal(messageOf(error, "The folder could not be deleted."))
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={(next) => !remove.isPending && onOpenChange(next)}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle className="flex items-center gap-2 text-sm">
            <AlertTriangle className="h-4 w-4 text-destructive" aria-hidden />
            Delete folder {folder?.name ?? ""}
          </AlertDialogTitle>
          <AlertDialogDescription className="text-xs">
            {blocked ? (
              <>
                Move its {visible} {visible === 1 ? "page" : "pages"} out first. A folder is deleted only when
                nothing is in it — pages are never deleted with it.
              </>
            ) : (
              <>The folder is removed from the list. It holds no page you can see; pages are never deleted with a folder.</>
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {refusal && (
          <p role="alert" className="type-page-value text-destructive">
            {refusal}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={remove.isPending}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={blocked || remove.isPending || !folder}
            onClick={(e) => {
              // Keep the dialog open until the server has answered.
              e.preventDefault()
              void confirm()
            }}
          >
            {remove.isPending && <Spinner className="h-3.5 w-3.5" />}
            Delete folder
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

// ── Move ───────────────────────────────────────────────────────────────────

/** The page being moved, as the dialog needs it. Passed FRESH by the caller
 *  on every render, so after a 409 re-read the version on screen is the one
 *  the next confirm sends. */
export interface MoveSubject {
  slug: string
  name: string
  folder: PageFolderRef | null
  pagesVersion: number | null
}

export interface MoveToFolderDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  subject: MoveSubject | null
  /**
   * Several pages at once. Given, the dialog files them all through the
   * batch route — one transaction, all or nothing — and `subject` is
   * ignored. Passed FRESH on every render, like `subject`.
   */
  subjects?: MoveSubject[] | null
  onMoved?: (target: PageFolderRef | null) => void
}

const MIXED = Symbol("mixed")

export function MoveToFolderDialog({ workspaceId, open, onOpenChange, subject, subjects, onMoved }: MoveToFolderDialogProps) {
  const folders = usePageFolders(workspaceId, open)
  const { addPage, addPages, removePage } = usePageFolderMutations(workspaceId)
  const busy = addPage.isPending || addPages.isPending || removePage.isPending

  const bulk = subjects != null
  const list = React.useMemo(() => (bulk ? subjects : subject ? [subject] : []), [bulk, subjects, subject])
  const first = list[0] ?? null
  // Where the subjects are now: one folder (or Unfiled) when they agree,
  // MIXED when they do not — then nothing is marked current and any target
  // is a change.
  const currentSlug: string | null | typeof MIXED = list.every(
    (s) => (s.folder?.slug ?? null) === (first?.folder?.slug ?? null),
  )
    ? (first?.folder?.slug ?? null)
    : MIXED
  // The chosen target — a folder slug, or null for Unfiled. `undefined`
  // means nothing chosen yet, which starts as "where it is now".
  const [target, setTarget] = React.useState<string | null | undefined>(undefined)
  const [notice, setNotice] = React.useState<{ tone: "error" | "warn"; text: string; detail?: string } | null>(null)
  const identity = list.map((s) => s.slug).join("\u0000")
  React.useEffect(() => {
    if (!open) return
    setTarget(undefined)
    setNotice(null)
  }, [open, identity])

  const chosen: string | null | undefined = target === undefined ? (currentSlug === MIXED ? undefined : currentSlug) : target
  const changed = chosen !== undefined && chosen !== currentSlug
  const submittable = list.length > 0 && changed && !busy && !folders.loading
  const targetFolder = typeof chosen === "string" ? (folders.folders.find((f) => f.slug === chosen) ?? null) : null

  // "After the move" — read for the chosen target only, and only while a
  // folder is chosen. The server's 200 or 403 decides whether the block
  // carries names or the marker; nothing here guesses who manages what.
  const targetAcl = useFolderAcl(workspaceId, typeof chosen === "string" ? chosen : null, open && typeof chosen === "string")
  const impact: string | null =
    chosen === undefined
      ? null
      : chosen === null
        ? MOVE_IMPACT_UNFILED
        : targetAcl.loading
          ? null
          : targetAcl.manages
            ? moveImpactFromAcl(targetAcl.entries)
            : moveImpactFromShared(targetFolder?.shared ?? "unknown")

  const nameOf = (slug: string) => list.find((s) => s.slug === slug)?.name ?? slug

  const submit = async () => {
    if (!submittable || chosen === undefined) return
    setNotice(null)
    try {
      if (chosen === null) {
        // Out of a folder. There is no batch removal on the wire, so each
        // page leaves on its own request, in order, and the first refusal
        // stops the rest and names the page.
        for (const s of list) {
          if (!s.folder) continue
          try {
            await removePage.mutateAsync({ folder: s.folder.slug, page: s.slug, pagesVersion: s.pagesVersion })
          } catch (error) {
            throw new Error(`${s.name}: ${messageOf(error, "could not be removed from its folder.")}`)
          }
        }
        onMoved?.(null)
      } else if (bulk) {
        await addPages.mutateAsync({
          folder: chosen,
          pages: list.map((s) => ({ page: s.slug, pagesVersion: s.pagesVersion })),
          aclVersion: targetFolder?.aclVersion ?? null,
        })
        onMoved?.(targetFolder ? { slug: targetFolder.slug, name: targetFolder.name, icon: targetFolder.icon, color: targetFolder.color } : null)
      } else {
        await addPage.mutateAsync({
          folder: chosen,
          page: list[0].slug,
          pagesVersion: list[0].pagesVersion,
          aclVersion: targetFolder?.aclVersion ?? null,
        })
        onMoved?.(targetFolder ? { slug: targetFolder.slug, name: targetFolder.name, icon: targetFolder.icon, color: targetFolder.color } : null)
      }
      onOpenChange(false)
    } catch (error) {
      const conflict = folderConflictOf(error)
      const page = refusedPageOf(error)
      if (conflict) {
        // The person consented to a move as things were — where the page
        // was, AND who the target would show it to. Both lists and the
        // target's permissions are read again so what is on screen — the
        // page's folder, the target's count, the versions the next confirm
        // will send, the "After the move" block — is current, and then they
        // are asked again. Never retried on their behalf.
        await Promise.all([folders.reread(), targetAcl.reread()])
        setNotice({
          tone: "warn",
          text: page ? `${nameOf(page)}: ${FOLDER_CONFLICT_SENTENCE}` : FOLDER_CONFLICT_SENTENCE,
          detail: conflict.error !== FOLDER_CONFLICT_SENTENCE ? conflict.error : undefined,
        })
        return
      }
      const text = messageOf(error, list.length > 1 ? "The pages could not be moved." : "The page could not be moved.")
      setNotice({ tone: "error", text: page ? `${nameOf(page)}: ${text}` : text })
    }
  }

  const options: Array<{ slug: string | null; name: string; folder: PageFolderView | null }> = [
    ...folders.folders.map((f) => ({ slug: f.slug, name: f.name, folder: f })),
    { slug: null, name: "Unfiled", folder: null },
  ]
  const listId = React.useId()

  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm" onSubmit={() => void submit()}>
      <CreateSurfaceHeader
        concept="pages"
        context={list.length > 1 ? `${list.length} pages` : first?.name}
        title="Move to folder"
        description={
          list.length > 1
            ? currentSlug === MIXED
              ? "The pages are in different folders. Choose where they all go, or Unfiled to take them out."
              : first?.folder
                ? `All in ${first.folder.name}. Choose where they go, or Unfiled to take them out.`
                : "None in a folder yet. Choose where they go."
            : first?.folder
              ? `Now in ${first.folder.name}. Choose where it goes, or Unfiled to take it out.`
              : "Not in a folder yet. Choose where it goes."
        }
        onClose={() => onOpenChange(false)}
      />

      <CreateSurfaceBody>
        {folders.error ? (
          <p role="alert" className="type-page-value text-destructive">
            The folders could not be read: {folders.error}
          </p>
        ) : folders.loading ? (
          <p role="status" className="flex items-center gap-2 text-xs text-muted-foreground">
            <Spinner className="h-3.5 w-3.5" />
            Reading folders…
          </p>
        ) : (
          <div role="radiogroup" aria-labelledby={listId} className="flex flex-col gap-1">
            <span id={listId} className="type-page-label text-muted-foreground">
              Folder
            </span>
            {options.map((o) => {
              const active = chosen === o.slug
              const current = currentSlug === o.slug
              return (
                <button
                  key={o.slug ?? "__unfiled"}
                  type="button"
                  role="radio"
                  aria-checked={active}
                  data-folder-option={o.slug ?? "unfiled"}
                  onClick={() => {
                    setTarget(o.slug)
                    setNotice(null)
                  }}
                  className={cn(
                    "flex min-h-8 items-center gap-2 rounded-md border px-2.5 py-1.5 text-left text-xs transition-colors coarse:min-h-11",
                    active
                      ? "border-primary/40 bg-primary/15 text-primary-hover"
                      : "border-hairline bg-foreground/[0.03] text-muted-foreground hover:bg-foreground/[0.07] hover:text-foreground",
                  )}
                >
                  <FolderGlyph icon={o.folder?.icon} color={o.folder?.color} className={o.folder ? undefined : "opacity-60"} />
                  <span className="min-w-0 flex-1 truncate" title={o.name}>
                    {o.name}
                  </span>
                  {o.folder && (
                    <span className="shrink-0 tabular-nums text-muted-foreground-soft">{o.folder.pageCount}</span>
                  )}
                  {current && <span className="type-page-label shrink-0 text-muted-foreground-soft">current</span>}
                  {active && <Check className="h-3.5 w-3.5 shrink-0" aria-hidden />}
                </button>
              )
            })}
          </div>
        )}

        {/* Who will reach the page once it is there. Drawn for whatever is
            chosen, the current folder included — the person may be looking
            for exactly that — and redrawn after a 409 from the fresh ACL. */}
        {!folders.error && !folders.loading && chosen !== undefined && (
          <div data-slot="move-impact" className="mt-3 flex flex-col gap-1 rounded-md border border-border/50 bg-muted/20 px-3 py-2">
            <span className="type-page-label text-muted-foreground-soft">After the move</span>
            {impact === null ? (
              <p role="status" className="flex items-center gap-2 text-xs text-muted-foreground">
                <Spinner className="h-3 w-3" />
                Reading who will see it…
              </p>
            ) : (
              <p className="text-xs leading-relaxed text-foreground/85">
                {list.length > 1 ? `For all ${list.length} pages: ${impact}` : impact}
              </p>
            )}
          </div>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceRefusal
        tone={notice?.tone ?? "error"}
        message={
          notice ? (
            <>
              {notice.text}
              {notice.detail && <span className="block text-muted-foreground">{notice.detail}</span>}
            </>
          ) : null
        }
        onDismiss={() => setNotice(null)}
      />

      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel={chosen === null && currentSlug !== null ? "Remove from folder" : "Move"}
        onPrimary={() => void submit()}
        primaryDisabled={!submittable}
        busy={busy}
        guardCancel={false}
      />
    </CreateSurface>
  )
}
