"use client"

import * as React from "react"
import { useSessionSafe } from "@/hooks/use-auth"
import { useLeaveGuard } from "@/lib/editor-lifecycle/use-leave-guard"
import { createCapabilityChannel, decodeMessage, transferEditorPort, verifyEditorHello, type CapabilityPort } from "@/lib/mooring/channel"
import { COMMAND_INPUT_BYTES, COMMAND_MESSAGE_BYTES, exactCommandKeys } from "@/lib/mooring/wordcraft-command-contract"
import { commandRequest, createCommandSession, type CommandState } from "@/lib/mooring/wordcraft-command-session"
import { editorFrameSrc } from "@/lib/workbooks/editor-client"
import { createEditorFrame, editorFrameSupported } from "./workbook-editor-host"

const labels: Record<CommandState, string> = { connecting: "Connecting…", ready: "No unsaved changes", reader: "Read only", pending: "Saving…", saved: "Saved", unknown: "Not confirmed · input paused", stopped: "Editor unavailable" }

/** Actual Page entrypoint for the separate native command product. One command
 * at a time; no draft/IME buffer, automatic reconnect or mutation replay. */
export function WordCraftCommandEditorHost({ workspaceId, pageId, name, unavailable = false, onDirtyChange, onUnavailable }: { workspaceId: string; pageId: string; name?: string; unavailable?: boolean; onDirtyChange?: (dirty: boolean) => void; onUnavailable?: () => void }) {
  const session = useSessionSafe()
  const human = session.status === "authenticated" ? session.data?.user?.id : undefined
  const mount = React.useRef<HTMLDivElement>(null)
  const control = React.useRef<ReturnType<typeof createCommandSession> | undefined>(undefined)
  const closeCurrent = React.useRef<(() => void) | undefined>(undefined)
  const unavailableRef = React.useRef(unavailable); unavailableRef.current = unavailable
  const callback = React.useRef(onUnavailable); callback.current = onUnavailable
  const [state, setState] = React.useState<CommandState>("connecting")
  const [error, setError] = React.useState<string>()
  const dirty = state === "pending" || state === "unknown"
  useLeaveGuard(dirty)
  React.useEffect(() => { onDirtyChange?.(dirty) }, [dirty, onDirtyChange])
  React.useEffect(() => {
    const abort = new AbortController()
    let dead = false, loads = 0, connected = false, ready = false, bundle = ""
    let frame: HTMLIFrameElement | undefined, port: CapabilityPort | undefined
    let host: ReturnType<typeof createCommandSession> | undefined
    let bootstrap: object | undefined
    let startupTimer: ReturnType<typeof setTimeout> | undefined
    const send = (value: object) => {
      const raw = JSON.stringify({ protocol: 1, ...value })
      if (!port || raw.length > COMMAND_MESSAGE_BYTES || new TextEncoder().encode(raw).length > COMMAND_MESSAGE_BYTES) throw new Error("invalid_command_frame")
      port.postMessage(raw)
    }
    const stop = () => {
      if (dead) return
      dead = true; clearTimeout(startupTimer); abort.abort(); host?.stop(); callback.current?.()
      try { send({ type: "wordcraft-command:stop" }) } catch { /* retain frame and original */ }
      if (frame) frame.hidden = true
      if (port) { port.onmessage = null; port.close() }
      setState(prior => prior === "pending" || prior === "unknown" ? "unknown" : "stopped")
      setError("The editor is unavailable. Unconfirmed changes have not been marked saved.")
    }
    const receive = (raw: unknown) => {
      if (dead) return
      if (typeof raw !== "string" || raw.length > 2 * COMMAND_INPUT_BYTES + 4096) { stop(); return }
      const value = decodeMessage(raw, 2 * COMMAND_INPUT_BYTES + 4096)
      if (!value || JSON.stringify(value) !== raw) { stop(); return }
      if (value.type === "wordcraft-command:ready" && exactCommandKeys(value, ["protocol", "type"]) && !ready) { ready = true; clearTimeout(startupTimer); host?.start(); return }
      if (value.type === "wordcraft-command:command" && ready && exactCommandKeys(value, ["protocol", "type", "raw", "revision"]) && typeof value.raw === "string" && typeof value.revision === "number") {
        if (!host?.submit(value.raw, value.revision)) stop()
        return
      }
      if (value.type === "wordcraft-command:applied" && ready && exactCommandKeys(value, ["protocol", "type", "own", "revision"]) && typeof value.own === "boolean" && typeof value.revision === "number" && Number.isSafeInteger(value.revision)) { host?.applied(value.own, value.revision); return }
      stop()
    }
    const onWindowMessage = (event: MessageEvent) => {
      if (dead) return
      const result = verifyEditorHello(event, frame?.contentWindow, { namespace: "wordcraft-command", bundle, loads, waiting: !connected })
      if (result === null) return
      if (result !== "accept" || !frame?.contentWindow) { stop(); return }
      connected = true
      port = transferEditorPort(frame.contentWindow, createCapabilityChannel(), "wordcraft-command", { bundle }, receive)
      try { if (!bootstrap) throw new Error("missing_bootstrap"); send(bootstrap); bootstrap = undefined } catch { stop() }
    }
    closeCurrent.current = stop
    window.addEventListener("message", onWindowMessage)
    setState("connecting"); setError(undefined)
    void (async () => {
      try {
        if (!human || unavailableRef.current || !editorFrameSupported()) { stop(); return }
        let write = true, opened
        try { opened = await commandRequest(workspaceId, pageId, { action: "open", write: true }, abort.signal) }
        catch (cause) {
          if (!(cause instanceof Error) || cause.message !== "access_unavailable") throw cause
          write = false; opened = await commandRequest(workspaceId, pageId, { action: "open", write: false }, abort.signal)
        }
        if (dead) return
        if (opened.actor !== `human:${human}` || opened.outcome !== null) throw new Error("invalid_actor")
        const src = editorFrameSrc(opened.editor.url, opened.editor.bundle_digest, window.location.origin)
        if (!src) throw new Error("invalid_bundle")
        const url = new URL(src); url.searchParams.set("mooring", "1"); url.searchParams.set("commands", "1"); url.searchParams.set("webgl", "1")
        bundle = opened.editor.bundle_digest
        frame = createEditorFrame(document, url.href, () => { loads++; if (loads !== 1 || connected) stop() }) ?? undefined
        if (!frame) throw new Error("isolation_unavailable")
        frame.title = "WordCraft command editor"
        bootstrap = { type: "wordcraft-command:bootstrap", projection: opened.projection, write }
        host = createCommandSession(workspaceId, pageId, opened, write, {
          project(projection, selection) { if (!dead) send({ type: "wordcraft-command:projection", projection, selection }) },
          state(value) {
            if (dead) return
            setState(value)
            if (value === "unknown") { try { send({ type: "wordcraft-command:pause" }) } catch { stop() } }
          },
          stopped: stop,
        })
        control.current = host
        startupTimer = setTimeout(stop, 30_000)
        mount.current?.append(frame)
      } catch { if (!dead) stop() }
    })()
    return () => {
      dead = true; clearTimeout(startupTimer); abort.abort(); closeCurrent.current = undefined; control.current = undefined
      host?.close(); if (port) { port.onmessage = null; port.close() }; frame?.remove(); window.removeEventListener("message", onWindowMessage)
    }
  }, [workspaceId, pageId, human])
  React.useEffect(() => { if (unavailable) closeCurrent.current?.() }, [unavailable])
  return <section className="flex h-full min-h-0 flex-col" aria-label={`${name ?? "WordCraft"} command editor`}>
    <div className="flex items-center gap-3 px-4 py-2 text-sm"><span role="status">{labels[state]}</span>{state === "unknown" && !error && <button type="button" className="underline" onClick={() => { void control.current?.reconcile() }}>Check original save</button>}</div>
    <p className="px-4 pb-2 text-xs text-muted-foreground">Test editor: one command at a time. Undo and rapid typing are not available.</p>
    {error && <p role="alert" className="px-4 py-2 text-sm">{error}</p>}
    <div className="relative min-h-0 flex-1" ref={mount} />
  </section>
}
