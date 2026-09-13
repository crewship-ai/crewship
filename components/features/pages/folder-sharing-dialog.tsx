"use client"

/**
 * Folder → Sharing (#2533, folder permissions F3′ §7).
 *
 * Who reaches every page in a folder, and whether they may edit. Opened from
 * the folder's header menu and from the Access section's "From folder" card.
 *
 * The server decides which of two surfaces this is, by answering the ACL
 * read with 200 or 403 — nothing here guesses who manages a folder:
 *
 *  · **Managers** (the owning crew's MANAGER+, workspace admins) get the
 *    table: the owning crew first, fixed at Can edit; then every entry with
 *    a two-state control — *Can view* / *Can edit*. Edit implies view, so
 *    the control has exactly those two states and never offers "edit
 *    without view", which does not exist in the data either. Then a row for
 *    everyone in this workspace, off until somebody turns it on. Then a form
 *    to add a person or a crew, by the same reference the page grant form
 *    takes — an email, a crew slug — because that is the one subject
 *    vocabulary this product has.
 *
 *  · **Everyone else** gets the marker the folder row already carries —
 *    "Shared with a crew", "Shared with everyone in this workspace", "Only
 *    the owning crew" — the server's own refusal sentence at the control,
 *    and, when a page in the folder is open, their own paths to it. Names
 *    are the manager's to see; the fact that the folder is shared is not.
 *
 * Every write is immediate: one request, on its own, the moment it is
 * confirmed. A refusal renders IN the dialog, in the server's words, and
 * changes nothing typed (#1563 rules 2 and 3).
 */

import * as React from "react"
import { Plus, Trash2 } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"
import { cn } from "@/lib/utils"
import {
  EVERYONE_LABEL,
  FOLDER_ACL_SENTENCE,
  folderSharingSentence,
  ownPathsSentence,
  type FolderAclEntry,
  type FolderAclSubjectType,
} from "@/lib/pages/folder-sharing"
import {
  useFolderAcl,
  useFolderAclMutations,
  usePageAccessMe,
  type PageFolderView,
} from "@/hooks/use-page-folders"
import { FolderGlyph } from "@/components/features/pages/folder-glyph"

const SELECT_CLASS = cn(
  CREATE_SURFACE_INPUT,
  "rounded-md border border-hairline bg-foreground/[0.03] px-2 text-foreground outline-none focus:border-primary/40",
)

function messageOf(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback
}

type Level = "view" | "edit"

const LEVEL_OPTIONS: { value: Level; label: string; hint: string }[] = [
  { value: "view", label: "Can view", hint: "See the folder and open the pages in it." },
  {
    value: "edit",
    label: "Can edit",
    hint: "Can view, and rename the folder, edit the pages in it and remove pages from it.",
  },
]

/**
 * The two-state control. Exactly two states, because `w` implies `r`: a
 * subject either views, or views and edits. There is no third.
 */
export function AccessLevelChoice({
  subject,
  value,
  onChange,
}: {
  subject: string
  value: Level
  onChange: (next: Level) => void
}) {
  return <CreateSurfaceChoice ariaLabel={`Permission for ${subject}`} value={value} options={LEVEL_OPTIONS} onChange={onChange} />
}

function SubjectKind({ type }: { type: FolderAclSubjectType | "owner" }) {
  return (
    <Badge variant="outline" className="h-4 shrink-0 px-1.5 font-mono leading-none">
      {type === "owner" ? "crew" : type}
    </Badge>
  )
}

export interface FolderSharingDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  folder: PageFolderView | null
  /**
   * A page in this folder that is open right now, if any. A reader who may
   * not see the table is shown their own paths to it — that is the half of
   * "who reaches this" that is theirs to know.
   */
  pageSlug?: string | null
}

export function FolderSharingDialog({ workspaceId, open, onOpenChange, folder, pageSlug }: FolderSharingDialogProps) {
  const slug = folder?.slug ?? null
  const acl = useFolderAcl(workspaceId, slug, open)
  const { set, unset } = useFolderAclMutations(workspaceId, slug)
  const busy = set.isPending || unset.isPending
  const [refusal, setRefusal] = React.useState<string | null>(null)
  React.useEffect(() => {
    if (open) setRefusal(null)
  }, [open, slug])

  const everyone = acl.entries.find((e) => e.subjectType === "workspace") ?? null
  const named = acl.entries.filter((e) => e.subjectType !== "workspace")

  const write = async (run: () => Promise<unknown>, fallback: string) => {
    setRefusal(null)
    try {
      await run()
    } catch (error) {
      setRefusal(messageOf(error, fallback))
    }
  }

  const setLevel = (entry: FolderAclEntry, level: Level) => {
    if ((level === "edit") === entry.canWrite) return
    void write(
      () => set.mutateAsync({ subjectType: entry.subjectType, subjectId: entry.subjectId, canWrite: level === "edit" }),
      "The permission could not be changed.",
    )
  }

  const removeEntry = (entry: FolderAclEntry) =>
    void write(() => unset.mutateAsync({ subjectType: entry.subjectType, subjectId: entry.subjectId }), "The permission could not be removed.")

  const toggleEveryone = (on: boolean) =>
    void write(
      () =>
        on
          ? set.mutateAsync({ subjectType: "workspace", canWrite: false })
          : unset.mutateAsync({ subjectType: "workspace", subjectId: "" }),
      on ? "The folder could not be shared with the workspace." : "The workspace's permission could not be removed.",
    )

  // ── Add ───────────────────────────────────────────────────────────────────
  const [kind, setKind] = React.useState<"user" | "crew">("user")
  const [reference, setReference] = React.useState("")
  const [level, setNewLevel] = React.useState<Level>("view")
  const kindId = React.useId()
  const refId = React.useId()
  const trimmedRef = reference.trim()
  const addable = trimmedRef !== "" && !busy

  const add = async () => {
    if (!addable) return
    await write(async () => {
      await set.mutateAsync({ subjectType: kind, subjectId: trimmedRef, canWrite: level === "edit" })
      // Only the reference clears, and only on a success (#1563 rule 3).
      setReference("")
    }, "The permission could not be added.")
  }

  // ── Not a manager ─────────────────────────────────────────────────────────
  const me = usePageAccessMe(workspaceId, pageSlug ?? null, open && acl.refusal !== null && Boolean(pageSlug))

  const title = folder ? (
    <span className="inline-flex items-center gap-1.5">
      <FolderGlyph icon={folder.icon} color={folder.color} className={folder.color ? undefined : "text-muted-foreground"} />
      <span className="min-w-0 truncate">{folder.name}</span>
    </span>
  ) : (
    "Folder"
  )

  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="md" ariaLabel={`Sharing for folder ${folder?.name ?? ""}`}>
      <CreateSurfaceHeader
        concept="pages"
        context={title}
        title="Sharing"
        description={
          folder?.ownerLabel
            ? `Owned by ${folder.ownerLabel}. What is set here applies to every page in the folder.`
            : "What is set here applies to every page in the folder."
        }
        onClose={() => onOpenChange(false)}
      />

      <CreateSurfaceBody>
        {acl.error ? (
          <p role="alert" className="type-page-value text-destructive">
            The folder&rsquo;s permissions could not be read: {acl.error}
          </p>
        ) : acl.loading ? (
          <p role="status" className="flex items-center gap-2 text-xs text-muted-foreground">
            <Spinner className="h-3.5 w-3.5" />
            Reading who this folder is shared with…
          </p>
        ) : acl.refusal !== null ? (
          <div data-slot="folder-sharing-marker" className="flex flex-col gap-3">
            <p className="text-sm font-medium text-foreground">{folderSharingSentence(folder?.shared ?? "none")}</p>
            <p data-slot="control-refusal" className="type-page-meta rounded-md border border-border/50 bg-muted/30 px-3 py-2 text-muted-foreground">
              {acl.refusal}
            </p>
            {pageSlug && (
              <p data-slot="own-paths" className="text-xs text-muted-foreground">
                {me.loading ? (
                  <span className="inline-flex items-center gap-2">
                    <Spinner className="h-3 w-3" />
                    Reading your own access…
                  </span>
                ) : me.error ? (
                  `Your own access could not be read: ${me.error}`
                ) : (
                  <>
                    <span className="font-medium text-foreground/80">{pageSlug}</span> — {ownPathsSentence(me.paths)}
                  </>
                )}
              </p>
            )}
          </div>
        ) : (
          <div data-slot="folder-sharing-table" className="flex flex-col gap-4">
            <p className="text-xs leading-relaxed text-muted-foreground">{FOLDER_ACL_SENTENCE}</p>

            <div role="table" aria-label="Who reaches this folder" className="flex flex-col divide-y divide-border/40 rounded-md border border-border/50">
              {/* The owning crew: always edits, never removed. A fixed word, not a
                  disabled control — a disabled control says "later, maybe". */}
              <div role="row" data-slot="acl-owner" className="flex min-h-10 flex-wrap items-center gap-2 px-2.5 py-2">
                <SubjectKind type="owner" />
                <span role="cell" className="min-w-0 flex-1 truncate text-xs font-medium text-foreground" title={folder?.ownerLabel ?? undefined}>
                  {folder?.ownerLabel ?? folder?.ownerRef ?? "Owning crew"}
                </span>
                <span role="cell" className="type-page-meta shrink-0 text-muted-foreground" title="The owning crew always edits its folder.">
                  Can edit · owner
                </span>
              </div>

              {named.map((entry) => (
                <div
                  key={`${entry.subjectType}:${entry.subjectId}`}
                  role="row"
                  data-slot="acl-entry"
                  data-subject={`${entry.subjectType}:${entry.subjectId}`}
                  className="flex min-h-10 flex-wrap items-center gap-2 px-2.5 py-2"
                >
                  <SubjectKind type={entry.subjectType} />
                  {/* One line whatever the label's length; the whole of it is in `title`. */}
                  <span role="cell" className="min-w-0 flex-1 truncate text-xs font-medium text-foreground" title={entry.label}>
                    {entry.label}
                  </span>
                  <div role="cell" className="flex shrink-0 items-center gap-1.5">
                    <AccessLevelChoice subject={entry.label} value={entry.canWrite ? "edit" : "view"} onChange={(next) => setLevel(entry, next)} />
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      disabled={busy}
                      onClick={() => removeEntry(entry)}
                      aria-label={`Remove ${entry.label}`}
                      className="h-8 w-8 shrink-0 px-0 text-muted-foreground hover:text-destructive"
                    >
                      <Trash2 className="h-3.5 w-3.5" aria-hidden />
                    </Button>
                  </div>
                </div>
              ))}

              <div role="row" data-slot="acl-everyone" data-on={everyone ? "true" : "false"} className="flex min-h-10 flex-wrap items-center gap-2 px-2.5 py-2">
                <SubjectKind type="workspace" />
                <span role="cell" className="min-w-0 flex-1 truncate text-xs font-medium text-foreground">
                  {EVERYONE_LABEL}
                </span>
                <div role="cell" className="flex shrink-0 items-center gap-2">
                  {everyone && (
                    <AccessLevelChoice subject={EVERYONE_LABEL} value={everyone.canWrite ? "edit" : "view"} onChange={(next) => setLevel(everyone, next)} />
                  )}
                  <Switch aria-label={EVERYONE_LABEL} checked={everyone !== null} disabled={busy} onCheckedChange={toggleEveryone} />
                </div>
              </div>
            </div>

            <form
              onSubmit={(e) => {
                e.preventDefault()
                void add()
              }}
              className="flex flex-col gap-2 rounded-md border border-border/50 p-2.5"
              aria-label="Add a person or a crew"
            >
              <div className="flex flex-wrap items-center gap-2">
                <label className="sr-only" htmlFor={kindId}>
                  Subject kind
                </label>
                <select
                  id={kindId}
                  className={SELECT_CLASS}
                  value={kind}
                  onChange={(e) => {
                    setKind(e.target.value as "user" | "crew")
                    if (refusal) setRefusal(null)
                  }}
                >
                  <option value="user">user</option>
                  <option value="crew">crew</option>
                </select>
                <label className="sr-only" htmlFor={refId}>
                  {kind === "user" ? "Email" : "Crew slug"}
                </label>
                <Input
                  id={refId}
                  value={reference}
                  onChange={(e) => {
                    setReference(e.target.value)
                    if (refusal) setRefusal(null)
                  }}
                  placeholder={kind === "user" ? "ada@example.com" : "support"}
                  className={cn(CREATE_SURFACE_INPUT, "min-w-[10rem] flex-1")}
                />
                <AccessLevelChoice subject="the new entry" value={level} onChange={setNewLevel} />
                <Button type="submit" size="sm" variant="soft" disabled={!addable} className="h-8 gap-1.5 text-xs">
                  {set.isPending ? <Spinner className="h-3 w-3" /> : <Plus className="h-3 w-3" aria-hidden />}
                  Add
                </Button>
              </div>
            </form>
          </div>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />

      {/* No primary: every change above was written the moment it was made. */}
      <CreateSurfaceFooter hint={null} onCancel={() => onOpenChange(false)} cancelLabel="Close" guardCancel={false} />
    </CreateSurface>
  )
}
