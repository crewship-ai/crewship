import type { ReactNode, RefObject } from "react"

/** The authorized resolver verifies this saved Document; this reference is not a grant. */
export interface ViewerTarget {
  workspaceId: string
  pageId: string
  type: "document"
  mode: "latest"
}
export interface AuthorizedDocumentView<Content> {
  status: "ready"
  scopeKey: string
  target: ViewerTarget
  title: string
  content: Content
}
export type DocumentViewerRead<Content> =
  | { status: "loading" | "denied" | "unavailable" | "error" }
  | AuthorizedDocumentView<Content>

export interface DocumentViewerProps<Content = unknown> {
  target: ViewerTarget | null
  scopeKey: string
  read: DocumentViewerRead<Content>
  renderBody: (document: AuthorizedDocumentView<Content>) => ReactNode
  layout: "side" | "mobile"
  expanded: boolean
  onClose: () => void
  onExpandedChange: (expanded: boolean) => void
  openerRef?: RefObject<HTMLElement | null>
}
