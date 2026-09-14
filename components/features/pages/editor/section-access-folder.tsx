"use client"

/**
 * From folder <name> — what this Page inherits from the folder it is in
 * (#2533, folder permissions F3′ §7 "Access sekce stránky").
 *
 * The cards above it are the Page's OWN access: its grants, its tokens, its
 * links. This one is what the folder adds — every subject on the folder's
 * sharing list reaches every page in it, this one included, for as long as
 * the page is there. Read-only, on purpose: the folder's permissions are
 * changed on the folder, in Sharing, so a manager gets a way there and
 * nobody gets a control here that would be a second place to edit one list.
 *
 * Which of two things it says is the server's decision, not this card's:
 *
 *  · A reader the ACL route answers is a manager of the owning crew or an
 *    admin, and sees the list with names and the way to Sharing.
 *  · A reader it refuses sees the marker the folder row carries — "Shared
 *    with a crew", "Shared with everyone in this workspace", "Only the
 *    owning crew" — the server's own sentence, and their own paths to this
 *    page, which are theirs to know.
 *
 * Nothing when the Page is not in a folder, or the server predates folders:
 * a card that said "not in a folder" would be a card about an absence.
 */

import * as React from "react"
import { Folder } from "lucide-react"

import { Button } from "@/components/ui/button"
import { DetailCard, EntityChip, Pill } from "@/components/ui/detail"
import { Spinner } from "@/components/ui/spinner"
import { ControlRefusal, Refusal, pageSubjectIcon } from "@/components/features/pages/page-settings"
import { FolderSharingDialog } from "@/components/features/pages/folder-sharing-dialog"
import { toPageFolderRef } from "@/hooks/use-pages"
import type { WirePageDetail } from "@/hooks/use-page-grants"
import { useFolderAcl, usePageAccessMe, usePageFolders } from "@/hooks/use-page-folders"
import { folderSharingSentence, moveImpactFromAcl, ownPathsSentence } from "@/lib/pages/folder-sharing"

export function FolderAccessCard({
  workspaceId,
  slug,
  page,
}: {
  workspaceId: string
  slug: string
  page: WirePageDetail | null
}) {
  const ref = page && page.folder !== undefined ? toPageFolderRef(page.folder) : null
  const folderSlug = ref?.slug ?? null
  const acl = useFolderAcl(workspaceId, folderSlug, folderSlug !== null)
  const folders = usePageFolders(workspaceId, folderSlug !== null)
  const folder = folderSlug ? (folders.folders.find((f) => f.slug === folderSlug) ?? null) : null
  // The marker as this card may state it. A list that failed to REFRESH
  // still hands back its previous rows (React Query keeps them), and a
  // marker from before the failure is not one this card can vouch for —
  // so an error on the list, first read or refresh, makes it "unknown"
  // (follow-up review 2026-09-14).
  const marker = folders.error !== null ? "unknown" : (folder?.shared ?? "unknown")
  const me = usePageAccessMe(workspaceId, slug, folderSlug !== null && acl.refusal !== null)
  const [sharing, setSharing] = React.useState(false)

  if (!ref) return null

  const answer = acl.loading
    ? "loading"
    : acl.manages
      ? `${acl.entries.length} ${acl.entries.length === 1 ? "entry" : "entries"}`
      : acl.refusal !== null
        ? // The marker comes from the folder LIST, a separate read: while it is
          // still loading, or when it failed or lacks this folder, the honest
          // word is "unknown", never "only the owning crew" (counter-review
          // 2026-09-13, R2).
          folders.loading
          ? "loading"
          : folderSharingSentence(marker).toLowerCase()
        : "could not read"

  // `DetailCard` has no `data-slot`; the tests and the section address this
  // card by one, title band included, so a `contents` wrapper carries it
  // without becoming a box of its own. The folder's name rides in the
  // subtitle — the band's title is UPPERCASE and a folder name is not.
  return (
    <div data-slot="page-folder-access" className="contents">
      <DetailCard
        title="From folder"
        icon={Folder}
        subtitle={`${ref.name} · ${answer}`}
        action={
          acl.manages ? (
            <Button type="button" size="xs" variant="outline" onClick={() => setSharing(true)}>
              Open folder sharing…
            </Button>
          ) : undefined
        }
        footer="Folder permissions apply to every page in the folder and are changed on the folder, not here."
      >
      <div className="flex flex-col gap-3">
        {acl.error && <Refusal>{acl.error}</Refusal>}

        {acl.loading && (
          <div className="type-meta flex items-center gap-2 py-2 text-muted-foreground">
            <Spinner className="h-3.5 w-3.5" />
            Reading the folder&rsquo;s permissions…
          </div>
        )}

        {acl.manages && (
          <>
            <p className="type-page-value text-foreground/85">{moveImpactFromAcl(acl.entries)}</p>
            {acl.entries.length > 0 && (
              <div data-slot="folder-acl-readonly" className="flex flex-col divide-y divide-border/40">
                {acl.entries.map((e) => (
                  <div key={`${e.subjectType}:${e.subjectId}`} className="flex items-center gap-2 py-1.5">
                    <EntityChip icon={pageSubjectIcon(e.subjectType)} label={e.label} note={e.subjectType} />
                    <div className="flex-1" />
                    <Pill tone={e.canWrite ? "blue" : "default"}>{e.canWrite ? "Can edit" : "Can view"}</Pill>
                  </div>
                ))}
              </div>
            )}
            {sharing && (
              <FolderSharingDialog
                workspaceId={workspaceId}
                open
                onOpenChange={(open) => !open && setSharing(false)}
                folder={folder}
                pageSlug={slug}
              />
            )}
          </>
        )}

        {acl.refusal !== null && (
          <>
            <p data-slot="folder-sharing-marker" className="type-page-value text-foreground/85">
              {folders.loading ? "Reading the folder's sharing…" : folderSharingSentence(marker)}
            </p>
            <ControlRefusal>{acl.refusal}</ControlRefusal>
            <p data-slot="own-paths" className="type-meta text-muted-foreground">
              {me.loading ? (
                <span className="inline-flex items-center gap-2">
                  <Spinner className="h-3 w-3" />
                  Reading your own access…
                </span>
              ) : me.error ? (
                `Your own access could not be read: ${me.error}`
              ) : (
                ownPathsSentence(me.paths)
              )}
            </p>
          </>
        )}
      </div>
      </DetailCard>
    </div>
  )
}
