"use client"

import { useEffect, useRef, useState } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { FileSpreadsheet } from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { apiFetch } from "@/lib/api-fetch"
import { pagesKeys, usePage } from "@/hooks/use-pages"
import { clearUnfinishedImport, unfinishedImports } from "@/components/features/chat/artifact-context/sheet/unfinished-imports"

/** One noted Page, shown only while the server still returns it and it is still empty. */
function UnfinishedImport({ workspaceId, userId, pageId, onGone }: { workspaceId: string; userId: string; pageId: string; onGone: () => void }) {
  const client = useQueryClient()
  const { page, loading, notFound } = usePage(workspaceId, `@${pageId}`)
  const [asking, setAsking] = useState(false)
  // The keyboard goes to the safe choice when asked, and back to Delete when the person keeps it.
  const keepRef = useRef<HTMLButtonElement>(null), deleteRef = useRef<HTMLButtonElement>(null), answered = useRef(false)
  useEffect(() => { if (asking) keepRef.current?.focus(); else if (answered.current) deleteRef.current?.focus() }, [asking])
  const [state, setState] = useState<"idle" | "deleting" | "failed">("idle")
  const filled = Boolean(page && (page.hasWorkbook || page.nativeCraftApplication || page.artifactKind || page.hasDocument))
  useEffect(() => { if (!loading && (notFound || filled)) { clearUnfinishedImport({ userId, workspaceId }, pageId); onGone() } }, [loading, notFound, filled]) // eslint-disable-line react-hooks/exhaustive-deps -- once the server answered
  if (!page || filled) return null
  const remove = async () => {
    setState("deleting")
    try {
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent("@" + pageId)}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "DELETE" })
      if (!response.ok) throw Error("delete_refused")
      clearUnfinishedImport({ userId, workspaceId }, pageId)
      await client.invalidateQueries({ queryKey: pagesKeys.all(workspaceId) })
      onGone()
    } catch { setState("failed") }
  }
  const name = page.name || "Sheet"
  return <li role="group" aria-label={name} className="flex min-w-0 flex-wrap items-center gap-2 px-3 py-2">
    <FileSpreadsheet className="size-4 shrink-0 text-sheet" aria-hidden />
    <span className="min-w-0 flex-1 truncate">{name}</span>
    <StatusPill tone="muted" label="Import not finished" />
    {asking
      ? <>
        <span className="text-xs text-muted-foreground">It is empty. Delete it?</span>
        <Button type="button" size="sm" variant="destructive" className="coarse:min-h-[44px]" disabled={state === "deleting"} onClick={() => void remove()}>Delete the empty Sheet</Button>
        <Button type="button" size="sm" variant="outline" className="coarse:min-h-[44px]" disabled={state === "deleting"} ref={keepRef} onClick={() => { answered.current = true; setAsking(false); setState("idle") }}>Keep</Button>
      </>
      : <Button type="button" size="sm" variant="outline" className="coarse:min-h-[44px]" ref={deleteRef} aria-label={`Delete ${name}`} onClick={() => setAsking(true)}>Delete</Button>}
    {state === "failed" && <p role="alert" className="basis-full text-xs text-destructive">The empty Sheet could not be deleted. It stays in the library.</p>}
  </li>
}

/**
 * Sheets left empty because an import from chat was not finished, as noted in
 * this browser. Shown with a label and, on confirmation, deleted; the server
 * decides whether the Page is there and who may delete it.
 */
export function UnfinishedImportsNotice({ workspaceId, userId }: { workspaceId: string; userId: string }) {
  const [ids, setIds] = useState<string[]>(() => userId ? unfinishedImports({ userId, workspaceId }) : [])
  useEffect(() => { setIds(userId ? unfinishedImports({ userId, workspaceId }) : []) }, [userId, workspaceId])
  if (!userId || ids.length === 0) return null
  return <section aria-label="Unfinished imports" className="overflow-hidden rounded-card border bg-card text-sm">
    <ul className="divide-y">{ids.map(id => <UnfinishedImport key={id} workspaceId={workspaceId} userId={userId} pageId={id} onGone={() => setIds(previous => previous.filter(entry => entry !== id))} />)}</ul>
  </section>
}
