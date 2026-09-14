"use client"

import * as React from "react"
import { Box, Clock, Copy, FolderOpen, Link2, UserCircle2, type LucideIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { DetailCard } from "@/components/ui/detail"
import { cn } from "@/lib/utils"
import { formatRelativeTime } from "@/lib/time"
import type { PageCapabilities } from "@/lib/pages/editor-contract"
import { toPageFolderRef } from "@/hooks/use-pages"
import { toPageOwner, type WirePageDetail } from "@/hooks/use-page-grants"
import { PAGE_STATE_META } from "@/components/features/pages/page-state"
import { FolderGlyph } from "@/components/features/pages/folder-glyph"
import { MoveToFolderDialog } from "@/components/features/pages/folder-dialogs"
import { pageFreshness } from "@/components/features/pages/editor/editor-header"

/**
 * The Page's properties, in the sidebar, the way an issue keeps its status,
 * priority and assignee: an icon, a label, a value, and where the value can
 * change, the one control that changes it.
 *
 * Folder and address used to be two blocks inside the Content form, each with
 * its own full-size button. Neither is a field of that form — a move is its
 * own write with its own fence, through the same dialog the rail opens — so
 * they sit here with the rest of what the Page IS rather than what it says.
 */
export interface PropertiesCardProps {
  workspaceId: string
  slug: string
  page: WirePageDetail | null
  capabilities: PageCapabilities
}

export function PropertiesCard({ workspaceId, slug, page, capabilities }: PropertiesCardProps) {
  const owner = toPageOwner(page)
  const state = pageFreshness(page)
  const stateMeta = state ? PAGE_STATE_META[state] : null
  const publication =
    capabilities.hasApplication && typeof page?.publication_version === "number" && page.publication_version > 0
      ? page.publication_version
      : null

  return (
    <DetailCard title="Properties" data-testid="page-properties">
      <dl className="flex flex-col">
        {/* `folder === undefined` is a server that does not speak folders yet,
            not a Page in no folder; the row is drawn only when the answer
            exists (#2527). */}
        {page?.folder !== undefined && <FolderRow workspaceId={workspaceId} slug={slug} page={page} />}
        <AddressRow slug={slug} />
        <Row icon={UserCircle2} label="Owner">
          {owner ? (
            <>
              {/* The kind is printed, never trimmed off: a crew-owned Page
                  survives the person leaving (§7.1 rule 1). */}
              <span className="text-muted-foreground-soft">{owner.kind}</span>{" "}
              <span>{owner.label}</span>
            </>
          ) : (
            <Muted>Nobody recorded</Muted>
          )}
        </Row>
        <Row icon={Box} label="Application">
          {publication !== null ? (
            <>Publication {publication}</>
          ) : capabilities.hasApplicationDraft ? (
            <>Draft, not published</>
          ) : (
            <Muted>None</Muted>
          )}
        </Row>
        <Row icon={Clock} label="Freshness">
          {stateMeta ? (
            <span className={cn("inline-flex items-center gap-1.5", stateMeta.tone)}>
              {stateMeta.label}
              {page?.last_produced_at && (
                <span className="text-muted-foreground-soft">· {formatRelativeTime(page.last_produced_at)}</span>
              )}
            </span>
          ) : (
            <Muted>Unknown</Muted>
          )}
        </Row>
      </dl>
    </DetailCard>
  )
}

function FolderRow({ workspaceId, slug, page }: { workspaceId: string; slug: string; page: WirePageDetail }) {
  const [moving, setMoving] = React.useState(false)
  const folder = toPageFolderRef(page.folder)
  const pagesVersion =
    typeof page.pages_version === "number" && Number.isFinite(page.pages_version) ? page.pages_version : null

  return (
    <Row
      icon={FolderOpen}
      label="Folder"
      control={
        <Button type="button" variant="ghost" size="xs" className="coarse:min-h-11" onClick={() => setMoving(true)}>
          Change…
        </Button>
      }
      data-slot="page-folder"
    >
      {folder ? (
        <span className="inline-flex min-w-0 items-center gap-1.5">
          <FolderGlyph icon={folder.icon} color={folder.color} className={cn("h-3.5 w-3.5", !folder.color && "text-muted-foreground")} />
          <span className="min-w-0 truncate" title={folder.name}>
            {folder.name}
          </span>
        </span>
      ) : (
        <Muted>Unfiled</Muted>
      )}
      {moving && (
        <MoveToFolderDialog
          workspaceId={workspaceId}
          open
          onOpenChange={(open) => !open && setMoving(false)}
          subject={{ slug, name: page.name?.trim() || slug, folder, pagesVersion }}
        />
      )}
    </Row>
  )
}

function AddressRow({ slug }: { slug: string }) {
  const address = `/pages/${slug}`
  const [copied, setCopied] = React.useState<"idle" | "done" | "refused">("idle")

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(address)
      setCopied("done")
    } catch {
      setCopied("refused")
    }
  }

  return (
    <Row
      icon={Link2}
      label="Address"
      control={
        <span className="inline-flex items-center gap-2">
          <span aria-live="polite" className="type-meta text-muted-foreground">
            {copied === "done" ? "Copied" : copied === "refused" ? "This browser refused the clipboard" : ""}
          </span>
          <Button
            type="button"
            variant="ghost"
            size="xs"
            className="coarse:min-h-11"
            aria-label="Copy address"
            onClick={() => void copy()}
          >
            <Copy className="h-3 w-3" aria-hidden />
            Copy
          </Button>
        </span>
      }
    >
      <span className="type-page-stamp text-foreground/85">{address}</span>
    </Row>
  )
}

/** One property: icon, label, value — and, where the value can change, the control. */
function Row({
  icon: Icon,
  label,
  control,
  children,
  ...props
}: {
  icon: LucideIcon
  label: string
  control?: React.ReactNode
  children: React.ReactNode
  "data-slot"?: string
}) {
  return (
    <div className="flex items-center gap-2 py-1 type-page-meta" {...props}>
      <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
      <dt className="w-[70px] shrink-0 text-muted-foreground">{label}</dt>
      <dd className="flex min-w-0 flex-1 items-center gap-2 text-foreground/85">
        <span className="min-w-0 flex-1 truncate">{children}</span>
        {control && <span className="shrink-0">{control}</span>}
      </dd>
    </div>
  )
}

function Muted({ children }: { children: React.ReactNode }) {
  return <span className="text-muted-foreground-soft">{children}</span>
}
