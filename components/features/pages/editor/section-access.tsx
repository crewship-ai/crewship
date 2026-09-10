"use client"

/**
 * Access — who reads this Page, who may send data to it, and public links.
 *
 * Three sub-sections in a fixed order, and the order is the question a reader
 * arrives with: people first, then the machines those people vouch for, then
 * the world. Every one of them is an existing card from
 * `components/features/pages/page-settings.tsx`; this section re-homes them,
 * it does not re-implement them.
 *
 * Producer tokens are here, and NOT in Data & actions, because minting a
 * webhook token is issuing a credential and a credential is a permission
 * question (independent review §7). Data & actions links to this section
 * rather than growing a second mint form, so there is exactly one place a
 * token comes into existence.
 *
 * Two rules this section is written around:
 *
 *  1. **There is no Save button here, and there never can be.** Every write
 *     on this screen is `SaveEffect: "immediate-grant"` — one request, on its
 *     own, the moment you confirm it. A grant, a token and a public link are
 *     rows in tables no Page document can express, so they cannot be staged
 *     into a draft and cannot be carried by a publication. A Save button
 *     would imply the opposite of all three.
 *
 *  2. **A missing right hides controls, never the section.** With
 *     `mayManageAccess` false the lists still render — who reaches this Page
 *     is exactly what such a reader legitimately needs — and each card says,
 *     in a sentence, which right it is missing. An empty screen sends people
 *     to the CLI to find out why (V01, the reason capabilities went
 *     per-section in the first place).
 *
 * Export and Delete live at the end. They have nowhere else to go now the
 * settings modal is deleted, and both are access-shaped: an export is the
 * whole Page leaving the workspace as a file, and a delete is the most
 * complete revocation there is.
 */

import * as React from "react"
import { useRouter } from "next/navigation"

import { SAVE_EFFECT_NOTE } from "@/lib/pages/editor-contract"
import {
  AccessCard,
  DangerCard,
  ExportCard,
  SharingCard,
  WebhooksCard,
  pagePanelIDs,
} from "@/components/features/pages/page-settings"

import type { EditorSectionProps } from "./section-props"

/**
 * Why each card's controls are gone, in that card's own terms.
 *
 * One shared "you may not do this" would be cheaper and would also be the
 * least useful sentence on the screen: the three refusals are three different
 * rights being missing, and a reader has to know which one to ask for.
 */
const REFUSAL = {
  grants:
    "You may not change this Page's grants. Only its owner or a workspace admin issues or revokes them (§7.1 rule 3), and no grant of any level lets its holder widen who reaches the Page.",
  tokens:
    "You may not mint or revoke producer tokens on this Page. Minting one issues a credential, so it takes the same right as changing a grant.",
  links:
    "You may not publish or withdraw a public link for this Page. The links already in force are listed above.",
} as const

export function EditorAccessSection({
  workspaceId,
  slug,
  page,
  capabilities,
  onDirtyChange,
}: EditorSectionProps) {
  const router = useRouter()
  const panelIDs = React.useMemo(() => pagePanelIDs(page), [page])

  // Nothing on this section is ever unwritten work, so it never raises the
  // shell's dirty guard. Announced rather than assumed: the shell keeps the
  // flag per-section, and a section that stayed silent would inherit whatever
  // the last one left behind.
  React.useEffect(() => {
    onDirtyChange(false)
  }, [onDirtyChange])

  const canManage = capabilities.mayManageAccess

  return (
    <div data-slot="editor-section-access" className="flex w-full max-w-3xl flex-col gap-4">
      <p className="type-page-meta text-muted-foreground">
        {SAVE_EFFECT_NOTE["immediate-grant"]} Access changes take effect on their own — they are
        not part of a draft and not part of a publication.
      </p>

      {/* Holding this right does not open the other sections, and saying so
          here is cheaper than a reader discovering it at a refused Save. */}
      {canManage && (
        <p className="type-page-meta text-muted-foreground-soft">
          Administering access does not by itself let you edit this Page&rsquo;s panels or its
          application source. Those are separate rights, checked in their own sections.
        </p>
      )}

      <AccessCard
        title="People and crews"
        workspaceId={workspaceId}
        slug={slug}
        panelIDs={panelIDs}
        canManage={canManage}
        manageRefusal={REFUSAL.grants}
      />

      <WebhooksCard
        title="Producer tokens"
        workspaceId={workspaceId}
        slug={slug}
        panelIDs={panelIDs}
        canManage={canManage}
        manageRefusal={REFUSAL.tokens}
      />

      <SharingCard
        workspaceId={workspaceId}
        slug={slug}
        canManage={canManage}
        manageRefusal={REFUSAL.links}
      />

      <ExportCard workspaceId={workspaceId} slug={slug} />

      {/* Deleting is NOT gated on mayManageAccess. Ending a Page is the
          owner's right (or a workspace admin's), which is a different right
          from administering its ACL and one the capabilities do not model —
          deriving it here would be a second, wrong copy of the server's rule.
          The card already shows the server's own refusal in place when it
          says no. */}
      <DangerCard
        workspaceId={workspaceId}
        slug={slug}
        // `usePageDelete` invalidates the index and nothing else, on purpose:
        // refetching a deleted Page's grants only produces a 404 to swallow,
        // and its own comment says the caller navigates away. The overview is
        // the only address that still exists.
        onDeleted={() => router.push("/pages")}
      />
    </div>
  )
}
