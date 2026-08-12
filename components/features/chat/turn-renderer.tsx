"use client"

import React from "react"
import { motion } from "motion/react"
import {
  AlertCircle,
  AlertTriangle,
  Settings2,
  Wrench,
  RefreshCw,
} from "lucide-react"
import {
  Message,
  MessageContent,
} from "@/components/ai-elements/message"
import {
  HoverCard,
  HoverCardContent,
  HoverCardTrigger,
} from "@/components/ui/hover-card"
import { arrival } from "@/lib/motion"
import type { ChatTurn } from "@/hooks/use-chat"
import { AssistantTurn } from "./assistant-turn"
import { EditableUserMessage } from "./messages/editable-user-message"
import { CrewProvisioningCard } from "./crew-provisioning-card"

function formatTimestamp(date: Date): string {
  return date.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
}

/** One entry of the init event's `mcp_servers` / `mcp_server_errors` arrays.
 *  Every field is optional: the shape is whatever the CLI that answered chose
 *  to emit, and other adapters emit `subtype: "init"` with different keys. */
interface McpServerInfo {
  name?: string
  status?: string
  type?: string
  message?: string
}

/** Init metadata is adapter-defined and passed through verbatim — `skills` in
 *  particular arrives as raw JSON. Render whatever is legible as one line and
 *  drop empties, so an unexpected shape costs a row instead of the pill. */
function describeMetaValue(value: unknown): string | undefined {
  if (typeof value === "string") return value || undefined
  if (Array.isArray(value)) {
    const items = value.map((v) => (typeof v === "string" ? v : JSON.stringify(v)))
    return items.length > 0 ? items.join(", ") : undefined
  }
  if (value == null) return undefined
  return JSON.stringify(value)
}

interface TurnRendererProps {
  turn: ChatTurn
  onCopy: (s: string) => void
  onFileClick: (s: string) => void
  isLastAssistant?: boolean
  onRegenerate?: () => void
  onEditUserMessage?: (turnId: string, newContent: string) => void
  /** Epoch ms cutoff. Turns whose timestamp is BEFORE this skip the
   *  arrival animation — they're either loaded from history or already
   *  rendered before the user switched session. */
  animateAfter?: number
  /** Active agent — forwarded to AssistantTurn so artifact tabs are
   *  scoped to the agent that produced the turn. Optional; tests and
   *  legacy callers can omit it (the artifact affordance hides itself). */
  agentId?: string
  /** Active chat id — forwarded to AssistantTurn so feedback rows
   *  land in the right workspace via the chats.workspace_id derivation
   *  on the server. Without this, the API falls back to the user's
   *  primary workspace, which on a multi-workspace user attaches the
   *  row to the wrong tenant. */
  chatId?: string
  /** Resolves a user id to a display name for group-chat author attribution.
   *  Returns null for the local user (no label) or when no participant info is
   *  available. Optional — callers without group context omit it. */
  resolveAuthorName?: (userId: string) => string | null
}

/** Render a single turn (user, assistant, or system). */
export const TurnRenderer = React.memo(function TurnRenderer({ turn, onCopy, onFileClick, isLastAssistant, onRegenerate, onEditUserMessage, animateAfter, agentId, chatId, resolveAuthorName }: TurnRendererProps) {
  const shouldAnimate = animateAfter == null || turn.timestamp.getTime() >= animateAfter
  const initialAnim = shouldAnimate ? arrival.initial : false
  const transition = shouldAnimate ? arrival.transition : { duration: 0 }
  if (turn.role === "user") {
    const textContent = turn.parts.find((p) => p.type === "text")?.content ?? ""
    // Group-chat attribution: a teammate's message shows their name; the local
    // user's own turns (resolver returns null) render as today.
    const authorName = turn.authorUserId ? resolveAuthorName?.(turn.authorUserId) ?? null : null
    return (
      <motion.div
        initial={initialAnim}
        animate={arrival.animate}
        exit={arrival.exit}
        transition={transition}
        data-turn-id={turn.id}
        className="group flex flex-col"
      >
        {authorName && (
          <div className="ml-auto mb-0.5 flex items-center gap-1.5 text-micro text-muted-foreground">
            <span aria-hidden="true" className="inline-flex h-4 w-4 items-center justify-center rounded-full bg-primary/15 text-[9px] font-semibold text-primary">
              {authorName.charAt(0).toUpperCase()}
            </span>
            <span>{authorName}</span>
          </div>
        )}
        {onEditUserMessage && !authorName ? (
          <EditableUserMessage
            text={textContent}
            timestamp={turn.timestamp}
            onSave={(next) => onEditUserMessage(turn.id, next)}
          />
        ) : (
          <Message from="user">
            <MessageContent>
              <div className="flex items-start gap-2">
                <span>{textContent}</span>
              </div>
            </MessageContent>
            <div className="text-micro text-muted-foreground ml-auto opacity-0 group-hover:opacity-100 transition-opacity">
              {formatTimestamp(turn.timestamp)}
            </div>
          </Message>
        )}
      </motion.div>
    )
  }

  if (turn.role === "system") {
    const part = turn.parts[0]
    const content = part?.content ?? ""
    const isError = part?.type === "error"
    const isInit = part?.type === "system_init"
    const isCrewProvisioning = part?.type === "crew_provisioning"

    if (isCrewProvisioning) {
      const meta = part?.metadata ?? {}
      const crewId = meta.crew_id as string | undefined
      const crewSlug = meta.crew_slug as string | undefined
      const enqueueStatus = meta.status as string | undefined
      const enqueueError = meta.error as string | undefined
      return (
        <motion.div
          initial={initialAnim}
          animate={arrival.animate}
          transition={transition}
          data-turn-id={turn.id}
          className="px-4 py-2"
        >
          <CrewProvisioningCard
            crewId={crewId}
            crewSlug={crewSlug}
            message={content}
            enqueueStatus={enqueueStatus}
            enqueueError={enqueueError}
          />
        </motion.div>
      )
    }

    if (isInit) {
      const meta = part?.metadata ?? {}
      const model = meta.model as string | undefined
      const tools = meta.tools as string[] | undefined
      const version = meta.claude_code_version as string | undefined
      const mcpServers = Array.isArray(meta.mcp_servers) ? (meta.mcp_servers as McpServerInfo[]) : []
      const mcpErrors = Array.isArray(meta.mcp_server_errors) ? (meta.mcp_server_errors as McpServerInfo[]) : []
      const skippedNames = mcpErrors.map((e) => e?.name || "unnamed").join(", ")
      const plural = mcpErrors.length === 1 ? "" : "s"

      const details: Array<[string, string]> = []
      const addDetail = (label: string, value: unknown) => {
        const text = describeMetaValue(value)
        if (text) details.push([label, text])
      }
      addDetail("Auth", meta.apiKeySource)
      addDetail("Permissions", meta.permissionMode)
      addDetail("Session", meta.session_id)
      addDetail("Working dir", meta.cwd)
      addDetail("Capabilities", meta.capabilities)
      addDetail("Skills", meta.skills)
      const hasDetails = details.length > 0 || mcpServers.length > 0 || mcpErrors.length > 0

      const pillClass = "flex items-center gap-3 px-4 py-2 bg-muted/40 border rounded-full text-label text-muted-foreground"
      const pillContent = (
        <>
          <Settings2 className="h-3 w-3" />
          <span>Session started</span>
          {model && (
            <span className="font-mono text-micro bg-background px-1.5 py-0.5 rounded border">{model}</span>
          )}
          {version && (
            <span className="font-mono text-micro bg-background px-1.5 py-0.5 rounded border">v{version}</span>
          )}
          {tools && tools.length > 0 && (
            <span className="flex items-center gap-1">
              <Wrench className="h-3 w-3" />
              {tools.length} tools
            </span>
          )}
          {mcpErrors.length > 0 && (
            // A skipped MCP server costs the agent a capability while the run
            // still exits 0, so this stays on the pill instead of behind the
            // hover. The names go on the label too: they must not require a
            // pointer to reach.
            <span
              className="flex items-center gap-1 rounded-full border border-warn/30 bg-warn/5 px-2 py-0.5 text-warn"
              aria-label={`${mcpErrors.length} MCP server${plural} skipped: ${skippedNames}`}
            >
              <AlertTriangle className="h-3 w-3" />
              {mcpErrors.length} MCP server{plural} skipped
            </span>
          )}
        </>
      )

      return (
        <motion.div
          initial={initialAnim}
          animate={arrival.animate}
          transition={transition}
          data-turn-id={turn.id}
          className="flex items-center justify-center py-2"
        >
          {hasDetails ? (
            // HoverCard over Tooltip: the rest of the provenance is a labelled
            // table plus a per-server status list, which a tooltip's single
            // balanced line cannot carry.
            <HoverCard openDelay={200} closeDelay={80}>
              <HoverCardTrigger asChild>
                {/* A button, not a div — the hover card is the only route to
                    these fields, so it has to open on keyboard focus too. */}
                <button type="button" className={`${pillClass} cursor-default outline-none focus-visible:ring-ring/50 focus-visible:ring-[3px]`}>
                  {pillContent}
                </button>
              </HoverCardTrigger>
              <HoverCardContent align="center" className="w-80 space-y-2 text-micro">
                {details.length > 0 && (
                  <dl className="space-y-1">
                    {details.map(([label, value]) => (
                      <div key={label} className="flex gap-2">
                        <dt className="w-24 shrink-0 text-muted-foreground">{label}</dt>
                        <dd className="min-w-0 flex-1 font-mono break-all">{value}</dd>
                      </div>
                    ))}
                  </dl>
                )}
                {mcpServers.length > 0 && (
                  <div className="space-y-1 border-t pt-2">
                    <div className="text-muted-foreground">MCP servers</div>
                    {mcpServers.map((server, i) => (
                      <div key={`${server?.name ?? "server"}-${i}`} className="flex items-center justify-between gap-2">
                        <span className="truncate font-mono">{server?.name || "unnamed"}</span>
                        <span className={server?.status === "connected" ? "text-success" : "text-muted-foreground"}>
                          {server?.status || "unknown"}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
                {mcpErrors.length > 0 && (
                  // The `message` below is CLI-supplied free text, and the
                  // credential scrubber only rewrites an event's Content — it
                  // never touches Metadata. It is shown here anyway: the text
                  // describes the workspace's own .mcp.json to the operator
                  // who owns it, it is the one field you can act on, and this
                  // card is ephemeral. The journal entry for the same event
                  // deliberately carries only name + error type, because that
                  // record is hash-chained and cannot be redacted later.
                  <div className="space-y-1 border-t border-warn/30 pt-2">
                    <div className="text-warn">Skipped: {skippedNames}</div>
                    {mcpErrors.map((err, i) => (
                      <div key={`${err?.name ?? "error"}-${i}`} className="text-muted-foreground">
                        <span className="font-mono">{err?.name || "unnamed"}</span>
                        {err?.type ? ` · ${err.type}` : ""}
                        {err?.message ? <div className="break-words">{err.message}</div> : null}
                      </div>
                    ))}
                  </div>
                )}
              </HoverCardContent>
            </HoverCard>
          ) : (
            <div className={pillClass}>{pillContent}</div>
          )}
        </motion.div>
      )
    }

    return (
      <motion.div
        initial={initialAnim}
        animate={arrival.animate}
        transition={transition}
        data-turn-id={turn.id}
      >
      <Message from="assistant">
        <MessageContent className={isError ? "border-destructive/50 bg-destructive/5 rounded-lg px-4 py-3" : ""}>
          <div className={`flex items-center gap-2 text-body ${isError ? "text-destructive" : "text-muted-foreground"}`}>
            <AlertCircle className="h-4 w-4 shrink-0" />
            {content}
          </div>
        </MessageContent>
      </Message>
      </motion.div>
    )
  }

  // Assistant turn - use the new grouped component
  return (
    <motion.div
      initial={initialAnim}
      animate={arrival.animate}
      transition={transition}
      data-turn-id={turn.id}
    >
      <AssistantTurn turn={turn} onCopy={onCopy} onFileClick={onFileClick} agentId={agentId} chatId={chatId} />
      {isLastAssistant && onRegenerate && !turn.isStreaming && (
        <div className="flex pl-4 -mt-1 mb-2">
          <button
            onClick={onRegenerate}
            className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
            title="Regenerate response"
          >
            <RefreshCw className="h-3 w-3" />
            <span>Regenerate</span>
          </button>
        </div>
      )}
    </motion.div>
  )
})
