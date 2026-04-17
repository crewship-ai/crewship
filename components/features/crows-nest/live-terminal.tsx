"use client"

import { useEffect, useRef } from "react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import "@xterm/xterm/css/xterm.css"
import { TerminalSquare } from "lucide-react"
import type { JournalEntry } from "@/lib/types/journal"

/** Live-streamed terminal block — one xterm instance that receives append-only
 *  chunks from `exec.command` / `exec.output_chunk` journal entries. */
interface LiveTerminalProps {
  /**
   * Ordered feed of observability journal entries (oldest first). The
   * component is idempotent against the same entry appearing twice — it
   * tracks the last-written ID and skips anything older.
   */
  entries: JournalEntry[]
  connected: boolean
}

/**
 * Renders a live xterm.js console that replays exec commands and their output
 * chunks from the journal stream. Each new `exec.command` entry opens a fresh
 * "block" (header line), subsequent `exec.output_chunk` entries with the same
 * `command_id` stream into it, and the block is closed when an exit code
 * lands in the command entry's payload.
 */
export function LiveTerminal({ entries, connected }: LiveTerminalProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const writtenIdsRef = useRef<Set<string>>(new Set())

  // Boot the xterm instance once; teardown on unmount.
  useEffect(() => {
    if (!containerRef.current || termRef.current) return
    const term = new Terminal({
      cursorBlink: false,
      convertEol: true,
      fontSize: 12,
      fontFamily: "'JetBrains Mono', 'Fira Code', 'Cascadia Code', Menlo, monospace",
      theme: {
        background: "#0a0a0a",
        foreground: "#e5e5e5",
        cursor: "#0a0a0a", // hide cursor (read-only)
      },
      scrollback: 5000,
      disableStdin: true,
      allowProposedApi: true,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(containerRef.current)
    fit.fit()
    term.writeln("\x1b[90m─── Crow's Nest live terminal ───\x1b[0m")
    term.writeln("\x1b[90mWaiting for exec events…\x1b[0m")

    termRef.current = term
    fitRef.current = fit

    const resize = () => fit.fit()
    const observer = new ResizeObserver(resize)
    observer.observe(containerRef.current)

    return () => {
      observer.disconnect()
      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
  }, [])

  // Write newly-seen entries into the terminal. Entries is append-only from
  // the parent so we can dedupe by id.
  useEffect(() => {
    const term = termRef.current
    if (!term) return
    for (const entry of entries) {
      if (writtenIdsRef.current.has(entry.id)) continue
      writtenIdsRef.current.add(entry.id)
      if (entry.entry_type === "exec.command") {
        const cmd = typeof entry.payload?.command === "string" ? entry.payload.command : entry.summary
        const exit = entry.payload?.exit_code
        const agent = entry.actor_id ? `@${entry.actor_id.slice(0, 8)} ` : ""
        term.writeln("")
        term.writeln(`\x1b[36m╭─ ${agent}\x1b[0m\x1b[1m$ ${cmd}\x1b[0m`)
        if (typeof exit === "number") {
          const color = exit === 0 ? "32" : "31"
          term.writeln(`\x1b[${color}m╰─ exit ${exit}\x1b[0m`)
        }
      } else if (entry.entry_type === "exec.output_chunk") {
        const chunk = typeof entry.payload?.data === "string"
          ? entry.payload.data
          : typeof entry.payload?.chunk === "string"
            ? entry.payload.chunk
            : entry.summary
        if (chunk) {
          // xterm handles ANSI colors natively — just write the raw string.
          term.write(chunk)
          if (!chunk.endsWith("\n") && !chunk.endsWith("\r")) {
            term.write("\r\n")
          }
        }
      }
    }
  }, [entries])

  return (
    <div className="flex flex-col h-full bg-[#0a0a0a] border border-border/50 rounded-lg overflow-hidden">
      <div className="flex items-center justify-between px-3 py-1.5 bg-neutral-900 border-b border-border/50 shrink-0">
        <div className="flex items-center gap-2">
          <TerminalSquare className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-[11px] text-muted-foreground">Live Terminal</span>
        </div>
        <div className="flex items-center gap-1.5">
          <span
            className={
              "h-1.5 w-1.5 rounded-full " +
              (connected ? "bg-emerald-400" : "bg-neutral-500")
            }
            aria-hidden
          />
          <span className="text-[10px] text-muted-foreground uppercase tracking-wider">
            {connected ? "Live" : "Idle"}
          </span>
        </div>
      </div>
      <div ref={containerRef} className="flex-1 min-h-0" />
    </div>
  )
}
