"use client"

import * as React from "react"

import { NO_PAGE_CAPABILITIES, type PageCapabilities } from "@/lib/pages/editor-contract"
import { sealedPanelCount } from "@/components/features/pages/page-editor"
import type { WirePageDetail } from "@/hooks/use-page-grants"

/**
 * What this viewer may do, section by section.
 *
 * The shape this replaces was one boolean for the whole surface:
 *
 *     canEdit = Boolean(selectedSlug) && detail.page != null && sealed === 0
 *
 * and it gated Edit, App preview, Source history and Publications together.
 * A single panel the viewer may not see therefore removed four doors at once,
 * three of which the server would have answered — the review recorded that as
 * V01. Worse, the reason it is right for one of them does not transfer: a
 * sealed panel makes the *document* unsafe to replace, because saving a
 * document that cannot describe a panel deletes it. It says nothing about
 * reading an ACL, minting a token, or listing publications.
 *
 * So the gate is split, and it is deliberately **optimistic** everywhere the
 * client cannot know the answer. The server decides all of these and re-checks
 * on every request; the job here is only to avoid offering a control that is
 * certain to fail, and to keep one section's refusal from blanking the other
 * three. A refusal that does arrive is rendered at the control, never used to
 * hide the section — otherwise the person cannot tell "you may not" from
 * "this product does not have that".
 */
export interface PageCapabilityOverrides {
  /** Lowered once a grants read comes back refused. */
  mayManageAccess?: boolean
  /** Lowered from the review snapshot's `capabilities.may_publish`. */
  mayPublishApplication?: boolean
  /** Lowered once a source-history read comes back refused. */
  mayViewSourceHistory?: boolean
}

export function derivePageCapabilities(
  page: WirePageDetail | null,
  overrides: PageCapabilityOverrides = {},
): PageCapabilities {
  if (page == null) return NO_PAGE_CAPABILITIES
  const sealed = sealedPanelCount(page)
  // `has_application` has no `omitempty` on the wire (internal/api/pages_handler.go
  // `pageWire`), so the server always states it, and the editor takes it at
  // its word. The looser `!== false` the live application view uses is right
  // there — it only decides whether to *try* loading an application and falls
  // back to panels — but here an absent field would route an ordinary panel
  // Page into the application surface, which is a much worse guess to make.
  const hasApplication = page.has_application === true
  // A draft with no publication yet is the first-publication case, and it is
  // the one the review exists for: nothing is live, so the whole candidate is
  // what has to be read. `has_application` cannot see it.
  const hasApplicationDraft = hasApplication || page.has_project === true
  return {
    loaded: true,
    // Name and description go through PATCH /pages/{slug}, which does not
    // carry the panel list, so a sealed panel cannot be lost by writing them.
    mayEditMetadata: true,
    mayEditDocument: sealed === 0,
    documentRefusal:
      sealed === 0
        ? null
        : sealed === 1
          ? "This Page carries a panel you may not see. Editing it as a document would delete that panel, so the document editor is closed here. Its name, description and access can still be changed."
          : `This Page carries ${sealed} panels you may not see. Editing it as a document would delete them, so the document editor is closed here. Its name, description and access can still be changed.`,
    mayManageAccess: overrides.mayManageAccess ?? true,
    hasApplication,
    hasApplicationDraft,
    mayPublishApplication: hasApplicationDraft && (overrides.mayPublishApplication ?? true),
    mayViewSourceHistory: overrides.mayViewSourceHistory ?? true,
  }
}

export function usePageCapabilities(
  page: WirePageDetail | null,
  overrides: PageCapabilityOverrides = {},
): PageCapabilities {
  const { mayManageAccess, mayPublishApplication, mayViewSourceHistory } = overrides
  return React.useMemo(
    () => derivePageCapabilities(page, { mayManageAccess, mayPublishApplication, mayViewSourceHistory }),
    [page, mayManageAccess, mayPublishApplication, mayViewSourceHistory],
  )
}
