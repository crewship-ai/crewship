"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import { WebLinksAddon } from "@xterm/addon-web-links"

export type TerminalStatus = "connecting" | "connected" | "disconnected" | "error"

interface UseTerminalOptions {
  containerRef: React.RefObject<HTMLDivElement | null>
  crewId: string
  crewSlug: string
  mode?: "shell" | "attach"
  agentSlug?: string
  enabled?: boolean
}

interface UseTerminalResult {
  status: TerminalStatus
  disconnect: () => void
}

export function useTerminal(options: UseTerminalOptions): UseTerminalResult {
  const { containerRef, crewId, crewSlug, mode = "shell", agentSlug, enabled = true } = options
  const [status, setStatus] = useState<TerminalStatus>("disconnected")
  const terminalRef = useRef<Terminal | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)

  const disconnect = useCallback(() => {
    if (wsRef.current) {
      wsRef.current.close()
      wsRef.current = null
    }
    if (terminalRef.current) {
      terminalRef.current.dispose()
      terminalRef.current = null
    }
    fitAddonRef.current = null
    setStatus("disconnected")
  }, [])

  useEffect(() => {
    if (!enabled || !containerRef.current) return

    let cancelled = false
    const el = containerRef.current

    async function connect() {
      try {
        setStatus("connecting")

        // Fetch WS token.
        const tokenRes = await fetch("/api/v1/ws-token", { credentials: "include" })
        if (!tokenRes.ok) {
          setStatus("error")
          return
        }
        const { token } = await tokenRes.json()
        if (!token || cancelled) return

        // Build WebSocket URL.
        const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
        let host = window.location.host
        // Dev mode: Next.js on :3001/:3011, backend on :8080/:8081
        const devPort = parseInt(window.location.port, 10)
        if (devPort >= 3001 && devPort <= 3019) {
          const backendPort = 8080 + (devPort - 3001)
          host = window.location.hostname + ":" + backendPort
        }
        const wsUrl = `${proto}//${host}/ws/terminal?token=${encodeURIComponent(token)}`

        // Create terminal.
        const terminal = new Terminal({
          cursorBlink: true,
          fontSize: 13,
          fontFamily: "'JetBrains Mono', 'Fira Code', 'Cascadia Code', Menlo, monospace",
          theme: {
            background: "#0a0a0a",
            foreground: "#e5e5e5",
            cursor: "#e5e5e5",
            selectionBackground: "#3b3b3b",
          },
          allowProposedApi: true,
        })

        const fitAddon = new FitAddon()
        terminal.loadAddon(fitAddon)
        terminal.loadAddon(new WebLinksAddon())
        terminal.open(el)
        fitAddon.fit()

        terminalRef.current = terminal
        fitAddonRef.current = fitAddon

        // Open WebSocket.
        const ws = new WebSocket(wsUrl)
        ws.binaryType = "arraybuffer"
        wsRef.current = ws

        ws.onopen = () => {
          if (cancelled) { ws.close(); return }
          // Send init message.
          const initMsg = JSON.stringify({
            mode,
            crew_id: crewId,
            crew_slug: crewSlug,
            agent_slug: agentSlug || "",
            rows: terminal.rows,
            cols: terminal.cols,
          })
          ws.send(initMsg)
          setStatus("connected")
        }

        ws.onmessage = (event) => {
          if (event.data instanceof ArrayBuffer) {
            // Binary: raw terminal output.
            terminal.write(new Uint8Array(event.data))
          } else if (typeof event.data === "string") {
            // Text: JSON control message (e.g., error).
            try {
              const msg = JSON.parse(event.data)
              if (msg.type === "error") {
                terminal.writeln(`\r\n\x1b[31mError: ${msg.message}\x1b[0m`)
                setStatus("error")
              } else if (msg.type === "info") {
                terminal.writeln(`\r\n\x1b[33m${msg.message}\x1b[0m`)
              }
            } catch {
              // Not JSON, write as text.
              terminal.write(event.data)
            }
          }
        }

        ws.onclose = () => {
          if (!cancelled) {
            terminal.writeln("\r\n\x1b[90m[Connection closed]\x1b[0m")
            setStatus("disconnected")
          }
        }

        ws.onerror = () => {
          setStatus("error")
        }

        // Terminal input → WebSocket.
        terminal.onData((data) => {
          if (ws.readyState === WebSocket.OPEN) {
            const encoder = new TextEncoder()
            ws.send(encoder.encode(data))
          }
        })

        // Terminal resize → WebSocket.
        terminal.onResize(({ rows, cols }) => {
          if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: "resize", rows, cols }))
          }
        })

        // Auto-fit on container resize.
        const observer = new ResizeObserver(() => {
          if (fitAddonRef.current) {
            fitAddonRef.current.fit()
          }
        })
        observer.observe(el)

        // Cleanup on unmount.
        return () => {
          observer.disconnect()
        }
      } catch {
        if (!cancelled) setStatus("error")
      }
    }

    const cleanup = connect()

    return () => {
      cancelled = true
      cleanup?.then((fn) => fn?.())
      disconnect()
    }
  }, [enabled, crewId, crewSlug, mode, agentSlug, containerRef, disconnect])

  return { status, disconnect }
}
