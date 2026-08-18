"use client"

import React from "react"
import { motion } from "motion/react"
import {
  AlertCircle,
  AlertTriangle,
  ClipboardList,
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
import { askProvenanceForTurn } from "./asks/ask-provenance"
import { AssistantTurn } from "./assistant-turn"
import { EditableUserMessage } from "./messages/editable-user-message"
import { CrewProvisioningCard } from "./crew-provisioning-card"

function formatTimestamp(date: Date): string {
  return date.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
}

/** One entry of the init event's `mcp_servers` / `mcp_server_errors` report,
 *  after normalisation. Every field is optional: the shape is whatever the CLI
 *  that answered chose to emit, and other adapters emit `subtype: "init"` with
 *  different keys. Values only ever reach this struct through `fieldText`, so
 *  a field here is a string or it is absent — never the raw JSON. */
interface McpServerInfo {
  name?: string
  status?: string
  type?: string
  message?: string
}

/** The category the producer stores when the CLI reported something in a shape
 *  it could not read (orchestrator's `unrecognisedShape`). Mirrored here so the
 *  card names the failure the same way `crewship run get` does. */
const unrecognizedShape = "unrecognized_shape"

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

/** Read one CLI-supplied field as display text. Init metadata is forwarded
 *  unparsed, so a key that holds a string today can hold an object tomorrow,
 *  and TypeScript's `as` says nothing about it: calling `.trim()` on such a
 *  value threw during render, and nothing in the chat tree is an error
 *  boundary — the throw reaches the dashboard route's, which replaces the whole
 *  page of the very session that was degraded. A field we cannot read as text
 *  reads as absent instead, and the entry falls back to another one. */
function fieldText(value: unknown): string | undefined {
  if (typeof value === "string") return value.trim() || undefined
  if (typeof value === "number" || typeof value === "boolean") return String(value)
  return undefined
}

function mcpFields(value: unknown): McpServerInfo {
  if (value == null || typeof value !== "object" || Array.isArray(value)) return {}
  const o = value as Record<string, unknown>
  return {
    name: fieldText(o.name),
    status: fieldText(o.status),
    type: fieldText(o.type),
    message: fieldText(o.message),
  }
}

/** Normalise an MCP report into entries. Which shape arrives is the CLI's
 *  choice, not ours — the adapter forwards this value verbatim — so read the
 *  array of objects it emits today, an object keyed by server name, and bare
 *  name strings, and give anything else the unreadable category rather than
 *  dropping it. Dropping is the one outcome that must not happen for
 *  `mcp_server_errors`: an unread report would render as a healthy session,
 *  which is exactly what a shape change must not be able to cause.
 *
 *  An empty array, an empty object and an absent key all yield no entries —
 *  they mean nothing was skipped, and a gate keys off that. */
function mcpEntries(report: unknown): McpServerInfo[] {
  if (report == null) return []
  if (Array.isArray(report)) {
    return report.map((element) => {
      const fields = typeof element === "string" ? { name: fieldText(element) } : mcpFields(element)
      // An entry nothing identifies is still an entry: the count is the alarm.
      return fields.name || fields.type ? fields : { ...fields, type: unrecognizedShape }
    })
  }
  if (typeof report === "object") {
    return Object.entries(report as Record<string, unknown>).map(([key, value]) => ({
      // Keyed by name, so the value carries only the reason — a bare string
      // there is that reason, not another name.
      ...(typeof value === "string" ? { message: fieldText(value) } : mcpFields(value)),
      name: fieldText(key) ?? unrecognizedShape,
    }))
  }
  return fieldText(report) ? [{ type: unrecognizedShape }] : []
}

/** Label one MCP server entry the way `crewship run get` does: name (type) →
 *  name → type → unnamed. The backend stores a category-only sentinel when the
 *  CLI reports skips in a shape it cannot read, so keying on name alone renders
 *  an alarm with nothing to act on. */
function mcpEntryLabel(e: McpServerInfo | undefined, withType = true): string {
  // Through fieldText even though the entries arrive normalised: this label is
  // the one place a raw CLI value ever reached a string method, and a caller
  // that skips the normaliser should cost a vague label, not the page.
  const name = fieldText(e?.name)
  const type = fieldText(e?.type)
  if (name && type && withType) return `${name} (${type})`
  if (name) return name
  if (type) return type
  return "unnamed"
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
  /** Resolves a user turn's content to the ask form it was submitted from, or
   *  null for anything typed. Submitting a form sends an ORDINARY message —
   *  there is nothing on the wire that marks it — so without this line the
   *  transcript shows text the user never typed as if they had. Optional; a
   *  caller that has no ask forms omits it and nothing renders. */
  resolveAskProvenance?: (content: string) => string | null
}

/** Render a single turn (user, assistant, or system). */
export const TurnRenderer = React.memo(function TurnRenderer({ turn, onCopy, onFileClick, isLastAssistant, onRegenerate, onEditUserMessage, animateAfter, agentId, chatId, resolveAuthorName, resolveAskProvenance }: TurnRendererProps) {
  const shouldAnimate = animateAfter == null || turn.timestamp.getTime() >= animateAfter
  const initialAnim = shouldAnimate ? arrival.initial : false
  const transition = shouldAnimate ? arrival.transition : { duration: 0 }
  if (turn.role === "user") {
    const textContent = turn.parts.find((p) => p.type === "text")?.content ?? ""
    // Group-chat attribution: a teammate's message shows their name; the local
    // user's own turns (resolver returns null) render as today.
    const authorName = turn.authorUserId ? resolveAuthorName?.(turn.authorUserId) ?? null : null
    // Which form this turn came out of.
    //
    // The turn's own submission envelope first (asks/ask-envelope.ts): it is
    // carried WITH the message, so it survives a reload and it cannot confuse
    // two identical submissions the way a content key did (audit P0.6). The
    // injected content resolver is the fallback for a turn that has none —
    // today that is every optimistic turn, until the send path carries the
    // envelope end to end.
    //
    // Only the local user's own turns can have come from a form on this
    // client; a teammate's message is attributed to them instead.
    const askProvenance = turn.authorUserId
      ? null
      : askProvenanceForTurn(chatId ?? "", turn) ?? resolveAskProvenance?.(textContent) ?? null
    return (
      <motion.div
        initial={initialAnim}
        animate={arrival.animate}
        exit={arrival.exit}
        transition={transition}
        data-turn-id={turn.id}
        className="group flex flex-col"
      >
        {askProvenance && (
          <div
            data-testid="ask-provenance"
            className="ml-auto mb-0.5 flex items-center gap-1 text-micro text-muted-foreground"
          >
            <ClipboardList className="h-3 w-3" aria-hidden="true" />
            <span>via {askProvenance}</span>
          </div>
        )}
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
                {/* pre-wrap, because a user turn is not always one line. A
                    message sent with an attachment carries the file's
                    agent-visible path in its own text (the payload is an
                    ordinary user message — lib/attachment-message.ts), so the
                    transcript already shows exactly what the agent got; it
                    only reads as such if the block's line breaks survive.
                    Every other multi-line paste was collapsing here too. */}
                <span className="whitespace-pre-wrap">{textContent}</span>
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
      const model = fieldText(meta.model)
      const version = fieldText(meta.claude_code_version)
      // Only a list has a length worth reporting: a string inventory would
      // otherwise be counted by characters and claim "4 tools" for "Bash".
      const toolCount = Array.isArray(meta.tools) ? meta.tools.length : 0
      const mcpServers = mcpEntries(meta.mcp_servers)
      // Degraded is decided by the PRESENCE of a report, never by our ability
      // to read it — the same rule every backend path follows, because a CLI
      // release that changes the shape must not be able to turn a degraded
      // session into a silent one.
      const skipReport = meta.mcp_server_errors
      const mcpErrors = mcpEntries(skipReport)
      // A scalar report says a server went and nothing about how many, so the
      // pill counts only when the shape is one that can be counted.
      const skipCountKnown = skipReport != null && typeof skipReport === "object"
      // Names only in the pill's summary — the category is a fallback for an
      // entry that has no name, not an annotation on every one.
      const skippedNames = mcpErrors.map((e) => mcpEntryLabel(e, false)).join(", ")
      const plural = mcpErrors.length === 1 ? "" : "s"
      const skippedSummary = skipCountKnown
        ? `${mcpErrors.length} MCP server${plural} skipped`
        : "MCP servers skipped"

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
          {toolCount > 0 && (
            <span className="flex items-center gap-1">
              <Wrench className="h-3 w-3" />
              {toolCount} tools
            </span>
          )}
          {mcpErrors.length > 0 && (
            // A skipped MCP server costs the agent a capability while the run
            // still exits 0, so this stays on the pill instead of behind the
            // hover. The names go on the label too: they must not require a
            // pointer to reach.
            <span
              className="flex items-center gap-1 rounded-full border border-warn/30 bg-warn/5 px-2 py-0.5 text-warn"
              aria-label={`${skippedSummary}: ${skippedNames}`}
            >
              <AlertTriangle className="h-3 w-3" />
              {skippedSummary}
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
                        <span className="truncate font-mono">{mcpEntryLabel(server, false)}</span>
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
                    {!skipCountKnown && (
                      // The alarm is real and the detail is not ours to invent:
                      // say which of the two this is rather than let the
                      // category read as the server's name.
                      <div className="text-muted-foreground">
                        The CLI reported this in a shape this build does not recognise — which servers went, and how many, could not be read from it.
                      </div>
                    )}
                    {mcpErrors.map((err, i) => (
                      <div key={`${err?.name ?? "error"}-${i}`} className="text-muted-foreground">
                        <span className="font-mono">{mcpEntryLabel(err, false)}</span>
                        {err?.name && err?.type ? ` · ${err.type}` : ""}
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
