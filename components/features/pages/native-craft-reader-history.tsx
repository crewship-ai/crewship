"use client"

import * as React from "react"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { registerDocumentDraftWriter } from "@/lib/document-queue"
import { createNativeDocumentClient } from "@/lib/mooring/native-document-client"
import { CRAFT_VERSION_HISTORY_FLAG, nativeFlagEnabled } from "@/lib/mooring/native-versions"
import { NativeCraftVersionHistory } from "./native-craft-version-history"

/**
 * The history for a person who reads the document without the editor (the
 * Artifacts preview or read-only detail). It checks the workspace flag first,
 * and only then opens its own native admission, read-only: it never acquires
 * the lease and offers no restore.
 *
 * Access follows the sibling shared preview: the privacy retirement registered
 * for this person (sign-out, another person signing in, read revoked), the
 * `page.access_recheck` push, a realtime reconnect and a 30 s poll all re-ask
 * the authorized listing. Any refusal or failure retires the history: the list,
 * times and names leave the DOM and the admission is dropped.
 */
export function NativeCraftReaderHistory({ humanId, workspaceId, pageId }: { humanId: string; workspaceId: string; pageId: string }) {
  return <ReaderHistorySession key={JSON.stringify([humanId, workspaceId, pageId])} humanId={humanId} workspaceId={workspaceId} pageId={pageId} />
}
function ReaderHistorySession({ humanId, workspaceId, pageId }: { humanId: string; workspaceId: string; pageId: string }) {
  const client = React.useMemo(() => createNativeDocumentClient({ workspaceId, pageId }), [workspaceId, pageId])
  const [retired, setRetired] = React.useState(false), [tick, setTick] = React.useState(0)
  const dead = React.useRef(false), opened = React.useRef<Promise<unknown>>(undefined)
  const retire = React.useCallback(() => { dead.current = true; opened.current = undefined; client.close(); setRetired(true) }, [client])
  React.useEffect(() => {
    const unregister = registerDocumentDraftWriter(humanId, async () => retire())
    const poll = setInterval(() => setTick(n => n + 1), 30000)
    return () => { clearInterval(poll); unregister(); dead.current = true; client.close() }
  }, [humanId, client, retire])
  useRealtimeEventSafe("page.access_recheck", event => { const id = event.payload.page_id; if (id === undefined || id === pageId) setTick(n => n + 1) })
  useRealtimeEventSafe("realtime.reconnected", () => setTick(n => n + 1))
  const load = React.useCallback(async (signal: AbortSignal) => {
    if (dead.current) return null
    const response = await apiFetch(`/api/v1/feature-flags?${new URLSearchParams({ workspace_id: workspaceId })}`, { cache: "no-store", signal })
    if (!response.ok || !nativeFlagEnabled(await response.json(), CRAFT_VERSION_HISTORY_FLAG)) { if (!signal.aborted) retire(); return null }
    try {
      opened.current ??= client.open(signal)
      await opened.current
      const list = await client.versions(signal)
      if (dead.current) return null
      if (!list && !signal.aborted) retire()
      return list
    } catch (cause) {
      // No Read right (comment-only, revoked, outside the workspace) or no
      // confirmation: nothing private stays on screen.
      if (!signal.aborted) retire()
      throw cause
    }
  }, [client, workspaceId, retire])
  if (retired) return null
  return <NativeCraftVersionHistory load={load} refreshKey={tick} canRestore={false} restoreHint="Obnovit verzi může jen ten, kdo dokument právě upravuje." />
}
