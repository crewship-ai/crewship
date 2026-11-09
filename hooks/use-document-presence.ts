"use client"

import { useEffect, useRef, useState } from "react"
import type { Editor } from "@tiptap/core"
import { TextSelection } from "@tiptap/pm/state"
import { absolutePositionToRelativePosition, ySyncPluginKey, type ProsemirrorBinding } from "@tiptap/y-tiptap"
import { apiFetch } from "@/lib/api-fetch"
import { encodePosition, parsePresenceReply, type PresencePeer } from "@/lib/documents/presence"
import { setRemoteCarets } from "@/components/features/pages/document-remote-carets"

/** A selection change is shared this soon at most; the heartbeat repeats it anyway. */
const SELECTION_THROTTLE_MS = 150

function tabId(): string {
  const random = typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`
  return `tab-${random}`.replace(/[^A-Za-z0-9_-]/g, "").slice(0, 64)
}

/** This editor's caret and range as encoded Yjs relative positions, or nothing when it has none to share. */
function caretOf(editor: Editor): { anchor: string; head: string } | Record<string, never> {
  const binding = ySyncPluginKey.getState(editor.state)?.binding as ProsemirrorBinding | undefined
  const selection = editor.state.selection
  if (!binding || binding.projectionError || !(selection instanceof TextSelection)) return {}
  try {
    return { anchor: encodePosition(absolutePositionToRelativePosition(selection.anchor, binding.type, binding.mapping)), head: encodePosition(absolutePositionToRelativePosition(selection.head, binding.type, binding.mapping)) }
  } catch { return {} }
}

/**
 * Docs presence (#5166): a heartbeat per open tab that shares this person's
 * caret and returns who else is on the document, with their carets drawn in
 * the editor. The server authorizes every heartbeat on the live document
 * read; a refusal (access gone, Docs off) clears everybody and stops, and
 * never touches the document session, which reports access on its own.
 * Leaving the document (unmount) tells the server at once; a closed tab
 * expires on the server without heartbeats.
 */
export function useDocumentPresence({ workspaceId, slug, editor, enabled }: { workspaceId: string; slug: string; editor: Editor | null; enabled: boolean }) {
  const [peers, setPeers] = useState<PresencePeer[]>([])
  const client = useRef<string>("")
  if (!client.current) client.current = tabId()
  useEffect(() => {
    if (!enabled || !editor || !workspaceId || !slug) { setPeers([]); return }
    const url = `/api/v1/pages/${encodeURIComponent(slug)}/document/presence?workspace_id=${encodeURIComponent(workspaceId)}`
    const post = (body: object, keepalive = false) => apiFetch(url, { method: "POST", keepalive, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
    let stopped = false, inflight = false, again = false, interval = 1000, lastSent = 0
    let timer: ReturnType<typeof setTimeout> | undefined, soon: ReturnType<typeof setTimeout> | undefined
    const show = (next: PresencePeer[]) => { if (stopped) return; setPeers(next); setRemoteCarets(editor, next) }
    const beat = async () => {
      if (stopped) return
      if (inflight) { again = true; return }
      inflight = true; again = false; lastSent = Date.now()
      clearTimeout(timer)
      try {
        const response = await post({ client: client.current, ...caretOf(editor) })
        if (stopped) return
        if ([401, 403, 404, 503].includes(response.status)) { show([]); stopped = true; return }
        if (response.ok) {
          const reply = parsePresenceReply(await response.json().catch(() => null))
          interval = reply.intervalMs
          show(reply.peers)
        }
      } catch { /* offline: keep the last view until the next heartbeat answers */ }
      finally {
        inflight = false
        if (!stopped) { if (again) void beat(); else timer = setTimeout(() => void beat(), interval) }
      }
    }
    const moved = () => {
      if (stopped || soon) return
      soon = setTimeout(() => { soon = undefined; void beat() }, Math.max(0, SELECTION_THROTTLE_MS - (Date.now() - lastSent)))
    }
    editor.on("selectionUpdate", moved)
    void beat()
    return () => {
      stopped = true
      clearTimeout(timer); clearTimeout(soon)
      editor.off("selectionUpdate", moved)
      if (!editor.isDestroyed) setRemoteCarets(editor, [])
      setPeers([])
      void post({ client: client.current, leave: true }, true).catch(() => {})
    }
  }, [enabled, editor, workspaceId, slug])
  return peers
}
