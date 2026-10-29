"use client"

import * as React from "react"
import { useSessionSafe } from "@/hooks/use-auth"
import { useLeaveGuard } from "@/lib/editor-lifecycle/use-leave-guard"
import { createCapabilityChannel, decodeMessage, transferEditorPort, verifyEditorHello, type CapabilityPort } from "@/lib/mooring/channel"
import { connectWordCraftHost, loadWordCraftReceiptSDK, openWordCraftSession, WORDCRAFT_BOOTSTRAP_BYTES, WORDCRAFT_FRAME_BYTES, type WordCraftBootstrap, type WordCraftHostState } from "@/lib/mooring/wordcraft-host-session"
import { editorFrameSrc } from "@/lib/workbooks/editor-client"
import { createEditorFrame, editorFrameSupported } from "./workbook-editor-host"

const labels: Record<WordCraftHostState, string> = {
  connecting: "Connecting…", ready: "No unsaved changes", draft: "Not saved · draft", pending: "Saving…",
  saved: "Saved", unknown: "Not confirmed", "unsaved-draft": "Not saved · local draft retained", stopped: "Editor closed",
}
const exact = (message: Record<string, unknown>, keys: string[]) => Object.keys(message).length === keys.length && keys.every(key => key in message)

/** Actual WordCraft protocol host; the legacy JSON pilot uses a separate host. */
export function WordCraftEditorHost({ workspaceId, pageId, name, onDirtyChange, onUnavailable, unavailable = false }: { unavailable?: boolean; workspaceId: string; pageId: string; name?: string; onDirtyChange?: (dirty: boolean) => void; onUnavailable?: () => void }) {
  const session = useSessionSafe()
  const humanId = session.status === "authenticated" ? session.data?.user?.id ?? null : null
  const displayName = React.useRef("WordCraft")
  displayName.current = Array.from((name ?? "WordCraft").replace(/\p{Cc}/gu, " ").trim()).slice(0, 128).join("") || "WordCraft"
  const mount = React.useRef<HTMLDivElement>(null)
  const stopCurrent = React.useRef<((reason: string) => void) | null>(null)
  const pageUnavailable = React.useRef(unavailable)
  pageUnavailable.current = unavailable
  const [state, setState] = React.useState<WordCraftHostState>("connecting")
  const [error, setError] = React.useState<string>()
  const unavailableCallback = React.useRef(onUnavailable)
  React.useEffect(() => { unavailableCallback.current = onUnavailable }, [onUnavailable])
  const dirty = ["draft", "pending", "unknown", "unsaved-draft"].includes(state)
  useLeaveGuard(dirty)
  React.useEffect(() => { onDirtyChange?.(dirty) }, [dirty, onDirtyChange])
  React.useEffect(() => {
    const abort = new AbortController()
    let dead = false, loads = 0, connected = false, bundle = ""
    let frame: HTMLIFrameElement | null = null, port: CapabilityPort | undefined
    let host: ReturnType<typeof connectWordCraftHost> | undefined, bootstrap: WordCraftBootstrap | undefined
    const send = (value: object, limit = WORDCRAFT_FRAME_BYTES) => {
      const raw = JSON.stringify({ protocol: 1, ...value })
      if (!port || new TextEncoder().encode(raw).length > limit) throw new Error("invalid_port_frame")
      port.postMessage(raw)
    }
    const close = (reason: string) => {
      if (dead) return
      dead = true
      unavailableCallback.current?.()
      setState(previous => ["draft", "pending", "unknown", "unsaved-draft"].includes(previous) ? "unsaved-draft" : "stopped")
      host?.stop(reason)
      // Preserve the original producer's memory on a live failure, but end
      // interaction and all future content delivery immediately.
      if (port) { try { send({ type: "wordcraft:stop", reason }) } catch { /* already closed */ }; port.onmessage = null; port.close(); port = undefined }
      if (frame) frame.hidden = true
      setError("The editor is unavailable. Unconfirmed changes are not saved.")
    }
    const deliverBootstrap = () => {
      if (bootstrap && port) { send({ type: "wordcraft:bootstrap", ...bootstrap, display_name: displayName.current }, WORDCRAFT_BOOTSTRAP_BYTES); bootstrap = undefined }
    }
    const receive = (raw: unknown) => {
      if (dead) return
      // IPC already delivered the string, but do not allocate a second UTF-8
      // copy of an unbounded child value before checking the cheap lower bound.
      if (typeof raw !== "string" || raw.length > WORDCRAFT_FRAME_BYTES) { close("invalid_editor_frame"); return }
      const message = decodeMessage(raw, WORDCRAFT_FRAME_BYTES)
      // The private flat machine envelope is compact round-trip JSON. This
      // rejects duplicate keys before accepting a child command; the opaque
      // inner original packet string is never normalized or reconstructed.
      if (!message || JSON.stringify(message) !== raw) { close("invalid_editor_frame"); return }
      if (message.type === "wordcraft:ready" && exact(message, ["protocol", "type"])) { host?.ready(); return }
      if (message.type === "wordcraft:submit" && exact(message, ["protocol", "type", "raw"]) && typeof message.raw === "string") { host?.submit(message.raw); return }
      if (message.type === "wordcraft:status" && exact(message, ["protocol", "type", "state", "reason"]) && typeof message.reason === "string" && message.reason.length <= 64) {
        if (message.state === "draft") { setError(undefined); host?.draft(); return }
        // A closed local refusal is explanatory only: it cannot clear dirty
        // work, advance Saved, close the producer or authorize a mutation.
        if (message.state === "history-refused" && message.reason === "unsupported_delete_undo") {
          setError("Undo or redo is unavailable for deletions or changes that overlap another person's deletion. Your document has not been changed.")
          return
        }
        // Child diagnostics cannot clear dirty work or declare Saved.
        if (message.state === "pending") return
        if (["unknown", "unsaved-draft", "stopped"].includes(String(message.state))) { close("editor_stopped"); return }
      }
      close("invalid_editor_sequence")
    }
    const onWindowMessage = (event: MessageEvent) => {
      if (dead) return
      const hello = verifyEditorHello(event, frame?.contentWindow, { namespace: "wordcraft", bundle, loads, waiting: !connected })
      if (hello === null) return
      if (hello !== "accept" || !frame?.contentWindow) { close("invalid_editor_handshake"); return }
      connected = true
      port = transferEditorPort(frame.contentWindow, createCapabilityChannel(), "wordcraft", { bundle }, receive)
      try { deliverBootstrap() } catch { close("invalid_bootstrap") }
    }
    stopCurrent.current = close
    window.addEventListener("message", onWindowMessage)
    setError(undefined); setState("connecting")
    void (async () => {
      try {
        if (!humanId || pageUnavailable.current) { close("page_unavailable"); return }
        if (!editorFrameSupported()) throw new Error("isolation_unavailable")
        let write = true
        // A refused writer admission grants nothing. Reader admission is a
        // distinct original request and is never used to send a mutation.
        let opening
        try { opening = await openWordCraftSession(workspaceId, pageId, true, abort.signal) }
        catch (cause) {
          if (!(cause instanceof Error) || cause.message !== "access_unavailable") throw cause
          write = false; opening = await openWordCraftSession(workspaceId, pageId, false, abort.signal)
        }
        if (dead || abort.signal.aborted) return
        const src = editorFrameSrc(opening.editor.url, opening.editor.bundle_digest, window.location.origin)
        if (!src) throw new Error("invalid_editor_package")
        // Fixed application mode waits for bootstrap; it never opens a blank
        // native document and replaces it later with collaborative content.
        const editorURL = new URL(src)
        editorURL.searchParams.set("mooring", "1")
        // Explicit E3 browser profile; default software WebGPU remains unqualified.
        editorURL.searchParams.set("webgl", "1")
        const sdk = await loadWordCraftReceiptSDK(opening.sdk_prefix)
        if (dead || abort.signal.aborted) return
        bundle = opening.editor.bundle_digest
        frame = createEditorFrame(document, editorURL.href, () => { loads++; if (loads !== 1 || connected) close("editor_navigated") })
        if (!frame) throw new Error("isolation_unavailable")
        frame.title = "WordCraft editor"
        host = connectWordCraftHost(workspaceId, pageId, opening, write, sdk, {
          bootstrap(value) { if (dead) return; bootstrap = value; try { deliverBootstrap() } catch { close("invalid_bootstrap") } },
          hostFrame(raw) { if (!dead) send({ type: "wordcraft:host-frame", raw }) },
          state(value) { if (!dead) setState(value) },
          stopped(reason) { close(reason) },
        })
        mount.current?.append(frame)
      } catch {
        if (!dead && !abort.signal.aborted) { close("startup_unavailable"); setState("stopped") }
      }
    })()
    return () => {
      stopCurrent.current = null
      dead = true; abort.abort(); window.removeEventListener("message", onWindowMessage)
      host?.close(); if (port) { port.onmessage = null; port.close() }; frame?.remove()
    }
  }, [workspaceId, pageId, humanId])
  // Metadata denial is stop-only: retaining the original producer never grants
  // fresh admission, and a later metadata recovery cannot restart this source.
  React.useEffect(() => { if (unavailable) stopCurrent.current?.("page_unavailable") }, [unavailable])
  return <section className="flex h-full min-h-0 flex-col" aria-label={`${name ?? "WordCraft"} editor`}>
    <div className="px-4 py-2 text-sm" role="status">{labels[state]}</div>
    {error && <p className="px-4 py-2 text-sm" role="alert">{error}</p>}
    <div className="relative min-h-0 flex-1" ref={mount} />
  </section>
}
