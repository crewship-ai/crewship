"use client"

import * as React from "react"
import { ShieldAlert } from "lucide-react"

import { Button } from "@/components/ui/button"
import { useApiMutation } from "@/hooks/use-api-mutation"
import { useWorkspace } from "@/hooks/use-workspace"
import { refusalSentence, usePanelPage } from "./panel-actions"
import type { PanelSpec } from "./types"

/** The endpoint the owner's button posts to (internal/api/pages_agent_producers.go). */
export function producerAuthorizeUrl(slug: string, panelId: string, workspaceId?: string | null): string {
  const path = `/api/v1/pages/${encodeURIComponent(slug)}/panels/${encodeURIComponent(panelId)}/producer/authorize`
  return workspaceId ? `${path}?workspace_id=${encodeURIComponent(workspaceId)}` : path
}

/**
 * #5260: a panel whose agent producer the page's owner has not authorized.
 * It says what the state is, who can fix it, and gives the owner the one
 * button that fixes it. The button never appears for anyone else, and it
 * authorizes the producer the spec declares; it bypasses nothing.
 */
export function ProducerAuthorizationNotice({ panel, publicView = false }: { panel: PanelSpec; publicView?: boolean }) {
  const auth = panel.producer_authorization
  const page = usePanelPage()
  if (!auth || publicView || panel.sealed === true) return null
  return (
    <div
      data-slot="panel-producer-authorization"
      role="status"
      className="flex flex-col gap-2 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2"
    >
      <p className="type-page-value inline-flex items-center gap-1.5 font-medium text-foreground">
        <ShieldAlert className="h-3.5 w-3.5 text-destructive" aria-hidden="true" />
        Producer not authorized by the owner
      </p>
      <p className="type-page-meta text-muted-foreground">
        <span className="font-mono">{auth.producer}</span> may not push this panel&apos;s data until the
        page&apos;s owner authorizes it.{" "}
        {auth.can_authorize
          ? "You own this page, so you can authorize it."
          : "Only the page's owner can authorize it; ask them to."}
      </p>
      {auth.can_authorize && page ? <AuthorizeButton slug={page.slug} workspaceId={page.workspaceId} panel={panel} /> : null}
    </div>
  )
}

function AuthorizeButton({ slug, workspaceId: given, panel }: { slug: string; workspaceId?: string | null; panel: PanelSpec }) {
  const { workspaceId: storeWorkspaceId } = useWorkspace()
  const workspaceId = given ?? storeWorkspaceId
  const authorize = useApiMutation<void, unknown>({
    request: () => ({
      input: producerAuthorizeUrl(slug, panel.id, workspaceId),
      init: { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" },
    }),
    // The page document carries the panel's state; refetch it.
    invalidateKeys: workspaceId ? [["pages", workspaceId]] : [],
  })
  return (
    <div className="flex flex-col gap-1">
      <div>
        <Button size="sm" variant="outline" disabled={authorize.isPending} onClick={() => authorize.mutate()}>
          {authorize.isPending ? "Authorizing…" : "Authorize producer"}
        </Button>
      </div>
      {authorize.error ? (
        <p role="alert" className="type-page-meta text-destructive">
          {refusalSentence(authorize.error)}
        </p>
      ) : null}
    </div>
  )
}
