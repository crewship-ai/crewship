"use client"

import type { ReactNode } from "react"
import { ExternalLink } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useDocumentViewerRead, type DocumentViewerReadInput } from "@/hooks/use-document-viewer-read"
import { DocumentReadonly } from "@/components/features/pages/document-readonly/document-readonly"
import { DocumentViewer } from "../artifact-viewer/document-viewer"
import type { DocumentViewerProps } from "../artifact-viewer/types"

export type SavedDocumentPanelProps = DocumentViewerReadInput & Pick<DocumentViewerProps,
  "layout" | "expanded" | "onClose" | "onExpandedChange" | "openerRef"> & {
  /** Optional host controls under the title; a function receives whether the read is verified (linked-item switcher). Absent leaves the header unchanged. */
  headerExtra?: ReactNode | ((ready: boolean) => ReactNode)
}

/** Current saved content, resolved with the current human's authority on each activation. */
export function SavedDocumentPanel({ headerExtra, ...props }: SavedDocumentPanelProps) {
  const { scopeKey, read } = useDocumentViewerRead(props)
  return <DocumentViewer {...props} scopeKey={scopeKey} read={read}
    headerExtra={typeof headerExtra === "function" ? headerExtra(read.status === "ready") : headerExtra}
    renderBody={document => <div className="space-y-3">
      <Button asChild variant="outline" size="sm" className="coarse:min-h-12">
        <a href={`/pages?${new URLSearchParams({ ref: "@" + document.target.pageId, workspace_id: document.target.workspaceId })}`}
          target="_blank" rel="noopener noreferrer">
          Open full editor<ExternalLink className="size-4" aria-hidden />
        </a>
      </Button>
      <DocumentReadonly schema={document.content.schema} content={document.content.content} accessibleLabel="Saved document body" />
    </div>} />
}
