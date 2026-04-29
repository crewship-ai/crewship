"use client"

import { Copy, ThumbsUp, ThumbsDown, AlertCircle, AlertTriangle, Crown, CheckCircle2, Clock, FileText, DollarSign, Zap, CircleDot, HelpCircle, FileCode } from "lucide-react"
import { useArtifactStore } from "@/stores/artifact-store"
import { useReactionsStore } from "@/stores/reactions-store"
import { ReactionPicker } from "./reactions/reaction-picker"
import { ReactionsRow } from "./reactions/reactions-row"
import {
  Message,
  MessageContent,
  MessageResponse,
  MessageActions,
  MessageAction,
} from "@/components/ai-elements/message"
import {
  Reasoning,
  ReasoningContent,
  ReasoningTrigger,
} from "@/components/ai-elements/reasoning"
import { Tool, ToolContent, ToolHeader } from "@/components/ai-elements/tool"
import { CodeBlock } from "@/components/ai-elements/code-block"
import { StatusIndicator } from "@/components/features/chat/status-indicator"
import type { ChatTurn, TurnPart } from "@/hooks/use-chat"
import { formatCost } from "@/lib/utils/format"

interface AssistantTurnProps {
  turn: ChatTurn
  onCopy: (content: string) => void
  onFileClick: (fileName: string) => void
  /** Active agent — required for "Open in Artifact" so tabs are scoped
   *  to the agent that produced them. When omitted (older callers,
   *  tests), the affordance is hidden rather than risk cross-agent
   *  reads/writes against the global artifact store. */
  agentId?: string
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`
  const totalSecs = Math.round(ms / 1000)
  if (totalSecs < 60) return `${totalSecs}s`
  const mins = Math.floor(totalSecs / 60)
  const remSecs = totalSecs % 60
  return remSecs > 0 ? `${mins}m ${remSecs}s` : `${mins}m`
}



function formatTokens(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`
  return String(n)
}

function ResultCard({ part }: { part: TurnPart }) {
  const meta = part.metadata ?? {}
  const cost = meta.total_cost_usd as number | undefined
  const durationMs = meta.duration_ms as number | undefined
  const numTurns = meta.num_turns as number | undefined
  const isError = meta.is_error as boolean | undefined
  const usage = meta.usage as Record<string, number> | undefined
  const modelUsage = meta.model_usage as Record<string, Record<string, number>> | undefined
  const errors = meta.errors as string[] | undefined

  const inputTokens = usage?.input_tokens ?? 0
  const outputTokens = usage?.output_tokens ?? 0
  const cacheRead = usage?.cache_read_input_tokens ?? 0

  const modelName = modelUsage ? Object.keys(modelUsage)[0] : undefined

  if (isError && errors?.length) {
    return (
      <div className="flex items-center gap-2 px-3 py-2 bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-800 rounded-lg text-label text-red-700 dark:text-red-400 max-w-lg">
        <AlertCircle className="h-4 w-4 shrink-0" />
        <span>{errors.join("; ")}</span>
      </div>
    )
  }

  // Compact one-line summary for the trigger
  const summaryParts: string[] = []
  if (cost != null && cost > 0) summaryParts.push(formatCost(cost, true))
  if (durationMs != null && durationMs > 0) summaryParts.push(formatDuration(durationMs))
  if (numTurns != null && numTurns > 0) summaryParts.push(`${numTurns} turn${numTurns !== 1 ? "s" : ""}`)
  if (modelName) summaryParts.push(modelName)

  return (
    <details className="max-w-lg group">
      <summary className="flex items-center gap-2 text-micro text-muted-foreground cursor-pointer hover:text-foreground select-none list-none">
        <DollarSign className="h-3 w-3 shrink-0" />
        <span>{summaryParts.join(" · ") || "Run complete"}</span>
        <span className="text-micro opacity-60 group-open:hidden">&#9654;</span>
        <span className="text-micro opacity-60 hidden group-open:inline">&#9660;</span>
      </summary>
      <div className="mt-1.5 bg-muted/30 border rounded-lg px-4 py-3 space-y-2">
        <div className="flex items-center gap-4 flex-wrap text-label">
          {cost != null && cost > 0 && (
            <span className="flex items-center gap-1 text-emerald-600 dark:text-emerald-400 font-medium">
              <DollarSign className="h-3 w-3" />
              {formatCost(cost, true)}
            </span>
          )}
          {durationMs != null && durationMs > 0 && (
            <span className="flex items-center gap-1 text-muted-foreground">
              <Clock className="h-3 w-3" />
              {formatDuration(durationMs)}
            </span>
          )}
          {numTurns != null && numTurns > 0 && (
            <span className="flex items-center gap-1 text-muted-foreground">
              <Zap className="h-3 w-3" />
              {numTurns} turn{numTurns !== 1 ? "s" : ""}
            </span>
          )}
          {modelName && (
            <span className="text-muted-foreground font-mono text-micro">
              {modelName}
            </span>
          )}
        </div>
        {(inputTokens > 0 || outputTokens > 0) && (
          <div className="flex items-center gap-3 text-micro text-muted-foreground">
            <span>In: <strong className="text-foreground">{formatTokens(inputTokens)}</strong></span>
            <span>Out: <strong className="text-foreground">{formatTokens(outputTokens)}</strong></span>
            {cacheRead > 0 && <span>Cache: <strong className="text-foreground">{formatTokens(cacheRead)}</strong></span>}
          </div>
        )}
      </div>
    </details>
  )
}

interface AskQuestion {
  question: string
  header: string
  options: { label: string; description: string }[]
  multiSelect?: boolean
}

function AskUserCard({ part, agentId }: { part: TurnPart; agentId?: string }) {
  const input = part.metadata?.input as { questions?: AskQuestion[] } | undefined
  const questions = input?.questions
  if (!questions?.length) {
    return <DefaultToolCall part={part} agentId={agentId} />
  }

  return (
    <div className="max-w-lg space-y-3">
      {questions.map((q, qi) => (
        <div key={qi} className="bg-primary/5 border border-primary/20 rounded-lg overflow-hidden">
          <div className="px-4 py-3 border-b border-primary/10 flex items-center gap-2">
            <HelpCircle className="h-3.5 w-3.5 text-primary" />
            <span className="text-label font-medium">{q.header}</span>
          </div>
          <div className="px-4 py-3">
            <p className="text-body mb-2">{q.question}</p>
            <div className="flex flex-wrap gap-1.5">
              {q.options.map((opt, oi) => (
                <span
                  key={oi}
                  className="inline-flex items-center gap-1 px-2.5 py-1 rounded-full text-label bg-muted border cursor-default"
                  title={opt.description}
                >
                  <CircleDot className="h-3 w-3 text-muted-foreground" />
                  {opt.label}
                </span>
              ))}
            </div>
          </div>
        </div>
      ))}
    </div>
  )
}

interface TodoItem {
  content: string
  status: "pending" | "in_progress" | "completed"
  activeForm?: string
}

function TodoWriteCard({ part, agentId }: { part: TurnPart; agentId?: string }) {
  const input = part.metadata?.input as { todos?: TodoItem[] } | undefined
  const todos = input?.todos
  if (!todos?.length) return <DefaultToolCall part={part} agentId={agentId} />

  const completed = todos.filter((t) => t.status === "completed").length
  const inProgress = todos.filter((t) => t.status === "in_progress").length
  const pct = Math.round((completed / todos.length) * 100)

  return (
    <div className="max-w-md">
      <div className="bg-muted/30 border rounded-lg overflow-hidden">
        <div className="px-4 py-2.5 border-b flex items-center justify-between">
          <span className="text-label font-medium flex items-center gap-1.5">
            <CheckCircle2 className="h-3.5 w-3.5 text-muted-foreground" />
            Agent Progress
          </span>
          <span className="text-micro text-muted-foreground">{completed}/{todos.length}</span>
        </div>
        <div className="px-4 py-2 space-y-1">
          {todos.map((todo, i) => (
            <div key={i} className="flex items-start gap-2 text-label py-0.5">
              {todo.status === "completed" ? (
                <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500 shrink-0 mt-0.5" />
              ) : todo.status === "in_progress" ? (
                <Clock className="h-3.5 w-3.5 text-amber-500 shrink-0 mt-0.5 animate-spin" />
              ) : (
                <CircleDot className="h-3.5 w-3.5 text-muted-foreground/40 shrink-0 mt-0.5" />
              )}
              <span className={todo.status === "completed" ? "text-muted-foreground line-through" : ""}>{todo.content}</span>
            </div>
          ))}
        </div>
        <div className="px-4 py-2 border-t">
          <progress
            value={pct}
            max={100}
            aria-label={`Progress ${pct}%`}
            className="h-1.5 w-full overflow-hidden rounded-full bg-muted [&::-webkit-progress-bar]:bg-muted [&::-webkit-progress-bar]:rounded-full [&::-webkit-progress-value]:bg-emerald-500 [&::-webkit-progress-value]:rounded-full [&::-webkit-progress-value]:transition-all [&::-moz-progress-bar]:bg-emerald-500 [&::-moz-progress-bar]:rounded-full"
          />
          {inProgress > 0 && (
            <p className="text-micro text-amber-500 mt-1">{inProgress} in progress</p>
          )}
        </div>
      </div>
    </div>
  )
}

function TaskCard({ part, agentId }: { part: TurnPart; agentId?: string }) {
  const input = part.metadata?.input as { description?: string; prompt?: string; subagent_type?: string } | undefined
  if (!input?.description) return <DefaultToolCall part={part} agentId={agentId} />

  const isCompleted = !!part.metadata?.completed
  const promptPreview = input.prompt ? (input.prompt.length > 120 ? input.prompt.slice(0, 120) + "..." : input.prompt) : null

  return (
    <div className="max-w-lg">
      <div className="bg-primary/5 border border-primary/20 border-l-4 border-l-amber-400 rounded-lg overflow-hidden">
        <div className="px-4 py-3">
          <div className="flex items-center gap-2 text-label">
            <Crown className="h-3.5 w-3.5 text-amber-500" />
            <span className="font-medium">Subagent: {input.subagent_type ?? "worker"}</span>
            {isCompleted ? (
              <span className="ml-auto inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-micro bg-emerald-50 dark:bg-emerald-950/30 text-emerald-700 dark:text-emerald-400">
                <CheckCircle2 className="h-3 w-3" /> Done
              </span>
            ) : (
              <span className="ml-auto inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-micro bg-amber-50 dark:bg-amber-950/30 text-amber-700 dark:text-amber-400">
                <Clock className="h-3 w-3 animate-spin" /> Working
              </span>
            )}
          </div>
          <p className="text-body font-medium mt-1.5">{input.description}</p>
          {promptPreview && (
            <p className="text-label text-muted-foreground mt-1 bg-background rounded px-2.5 py-1.5 border">{promptPreview}</p>
          )}
        </div>
      </div>
    </div>
  )
}

const SENSITIVE_KEY_RE = /(?:api[_-]?key|token|secret|password|authorization|auth|cookie|private[_-]?key|credential)/i

function redactSensitiveKeys(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(redactSensitiveKeys)
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>).map(([k, v]) => [
        k, SENSITIVE_KEY_RE.test(k) ? "[REDACTED]" : redactSensitiveKeys(v),
      ])
    )
  }
  return value
}

const FILE_TOOLS = new Set(["Edit", "Write", "MultiEdit", "Read", "NotebookEdit"])

function DefaultToolCall({ part, agentId }: { part: TurnPart; agentId?: string }) {
  const toolName = (part.metadata?.tool_name as string) ?? part.content ?? "Tool"
  const isCompleted = !!part.metadata?.completed
  const rawInput = part.metadata?.input as Record<string, unknown> | undefined
  const input = rawInput ? redactSensitiveKeys(rawInput) as Record<string, unknown> : undefined
  const openArtifact = useArtifactStore((s) => s.openFile)

  let subtitle = ""
  let filePath: string | null = null
  if (input) {
    if (input.file_path) {
      subtitle = String(input.file_path)
      filePath = String(input.file_path)
    }
    else if (input.command) subtitle = `$ ${String(input.command).slice(0, 60)}${String(input.command).length > 60 ? "..." : ""}`
    else if (input.url) subtitle = String(input.url)
    else if (input.query) subtitle = String(input.query)
    else if (input.pattern) subtitle = String(input.pattern)
  }

  // Reject absolute paths and traversal segments before exposing the
  // "Open in Artifact" affordance — a crafted tool payload could
  // otherwise surface arbitrary host paths.
  const isSafeArtifactPath = (p: string | null): p is string => {
    if (!p) return false
    if (p.startsWith("/") || p.startsWith("\\")) return false
    if (/(^|\/)\.\.(\/|$)/.test(p)) return false
    return p.length > 0
  }
  const safeFilePath = isSafeArtifactPath(filePath) ? filePath : null
  // agentId is required for safe scoping — without it we can't bind the
  // tab to the agent that produced it, so don't expose the affordance.
  const canOpenArtifact = safeFilePath && FILE_TOOLS.has(toolName) && !!agentId
  const fileName = safeFilePath ? safeFilePath.split("/").pop() ?? safeFilePath : ""

  return (
    <div className="flex flex-col gap-1.5">
      <Tool defaultOpen={false}>
        <ToolHeader
          title={subtitle ? `${toolName}  ${subtitle}` : toolName}
          type="tool-invocation"
          state={isCompleted ? "output-available" : "input-available"}
        />
        <ToolContent>
          {input != null && (
            <CodeBlock code={JSON.stringify(input, null, 2)} language="json" />
          )}
        </ToolContent>
      </Tool>
      {canOpenArtifact && safeFilePath && agentId && (
        <button
          type="button"
          onClick={() =>
            openArtifact({
              id: `${agentId}::${safeFilePath}`,
              agentId,
              path: safeFilePath,
              title: fileName,
            })
          }
          className="flex items-center gap-2 px-3 py-1.5 bg-primary/5 border border-primary/20 rounded-lg text-label text-primary hover:bg-primary/10 max-w-md transition-colors w-fit"
        >
          <FileCode className="h-3.5 w-3.5" />
          <span>Open in Artifact</span>
          <span className="font-mono text-micro text-muted-foreground truncate max-w-[200px]">{fileName}</span>
        </button>
      )}
    </div>
  )
}

function InlineToolCall({ part, agentId }: { part: TurnPart; agentId?: string }) {
  const toolName = (part.metadata?.tool_name as string) ?? part.content ?? "Tool"

  switch (toolName) {
    case "AskUserQuestion": return <AskUserCard part={part} agentId={agentId} />
    case "TodoWrite": return <TodoWriteCard part={part} agentId={agentId} />
    case "Task": return <TaskCard part={part} agentId={agentId} />
    default: return <DefaultToolCall part={part} agentId={agentId} />
  }
}

function InlineToolResult({ part }: { part: TurnPart }) {
  const content = part.content ?? ""
  const isLong = content.length > 200

  return (
    <Tool defaultOpen={false}>
      <ToolHeader
        title={`Result${isLong ? ` (${content.length} chars)` : ""}`}
        type="tool-invocation"
        state="output-available"
      />
      <ToolContent>
        <pre className="text-xs whitespace-pre-wrap break-all max-h-64 overflow-y-auto">{content || "(empty)"}</pre>
      </ToolContent>
    </Tool>
  )
}

function DelegationContent({ content }: { content: string }) {
  const targetMatch = content.match(/to\s+([^]]+?)(?:\s*\(|$)/)
  const taskMatch = content.match(/"([^"]+)"/)
  const completedMatch = content.match(/Completed in (.+)/)
  const isCompleted = !!completedMatch

  return (
    <div className="max-w-xl">
      <div className="bg-primary/5 border border-primary/20 border-l-4 border-l-[#4ECDC4] rounded-lg overflow-hidden">
        <div className="px-4 py-3">
          <div className="flex items-center gap-2 text-label">
            <Crown className="h-3.5 w-3.5 text-amber-500" />
            <span className="font-medium text-muted-foreground">Delegated to</span>
            <span className="font-semibold">{targetMatch?.[1] ?? "Agent"}</span>
          </div>
          {taskMatch && (
            <div className="mt-1.5 text-label text-muted-foreground bg-background rounded px-2.5 py-1.5 border">
              {taskMatch[1]}
            </div>
          )}
          <div className="mt-2 flex items-center gap-2">
            {isCompleted ? (
              <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-micro font-medium bg-emerald-50 dark:bg-emerald-950/30 text-emerald-700 dark:text-emerald-400">
                <CheckCircle2 className="h-3 w-3" />
                Completed in {completedMatch[1]}
              </span>
            ) : (
              <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-micro font-medium bg-amber-50 dark:bg-amber-950/30 text-amber-700 dark:text-amber-400">
                <Clock className="h-3 w-3 animate-spin" />
                In progress...
              </span>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

export function AssistantTurn({ turn, onCopy, onFileClick, agentId }: AssistantTurnProps) {
  // Collect all text content for copy action
  const fullText = turn.parts
    .filter((p) => p.type === "text")
    .map((p) => p.content)
    .join("")

  // Check if any text part has delegation content
  const hasDelegation = turn.parts.some(
    (p) => p.type === "text" && p.content.startsWith("[DELEGATED")
  )

  // Check for file creation notification
  const fileCreationPart = turn.parts.find(
    (p) => p.type === "text" && /file (created|written|saved)/i.test(p.content)
  )
  const fileMatchRaw = fileCreationPart?.content.match(/[`"]?([a-zA-Z0-9_\-/.]+\.[a-zA-Z0-9]+)[`"]?/)
  // Sanitize: reject path traversal and absolute paths
  const fileMatch = fileMatchRaw && fileMatchRaw[1] &&
    !fileMatchRaw[1].includes("..") &&
    !fileMatchRaw[1].startsWith("/") ? fileMatchRaw : null

  return (
    <Message from="assistant">
      {turn.parts.map((part) => {
        switch (part.type) {
          case "status":
            return <StatusIndicator key={part.id} content={part.content} />

          case "thinking":
            return (
              <Reasoning key={part.id} isStreaming={part.isStreaming} defaultOpen={part.isStreaming}>
                <ReasoningTrigger />
                <ReasoningContent>{part.content}</ReasoningContent>
              </Reasoning>
            )

          case "tool_call":
            return <InlineToolCall key={part.id} part={part} agentId={agentId} />

          case "tool_result":
            return <InlineToolResult key={part.id} part={part} />

          case "error":
            if (part.content.toLowerCase().includes("rate limit") || part.content.toLowerCase().includes("429")) {
              return (
                <div key={part.id} className="flex items-center gap-2 px-3 py-2 bg-amber-50 dark:bg-amber-950/30 border border-amber-200 dark:border-amber-800 rounded-lg text-label text-amber-700 dark:text-amber-400 max-w-md">
                  <AlertTriangle className="h-4 w-4 shrink-0" />
                  <span>{part.content}</span>
                </div>
              )
            }
            return (
              <MessageContent key={part.id} className="border-destructive/50 bg-destructive/5 rounded-lg px-4 py-3">
                <div className="flex items-center gap-2 text-destructive text-body">
                  <AlertCircle className="h-4 w-4 shrink-0" />
                  {part.content}
                </div>
              </MessageContent>
            )

          case "image": {
            const allowedMedia = new Set(["image/png", "image/jpeg", "image/webp", "image/gif"])
            const rawMedia = typeof part.metadata?.media_type === "string" ? part.metadata.media_type : "image/png"
            const mediaType = allowedMedia.has(rawMedia) ? rawMedia : "image/png"
            return (
              <div key={part.id} className="max-w-md rounded-lg overflow-hidden border">
                <img
                  src={`data:${mediaType};base64,${part.content}`}
                  alt="Agent screenshot"
                  className="w-full h-auto"
                  loading="lazy"
                  decoding="async"
                />
              </div>
            )
          }

          case "result":
            return <ResultCard key={part.id} part={part} />

          case "text":
            if (part.content.startsWith("[DELEGATED")) {
              return <DelegationContent key={part.id} content={part.content} />
            }
            return (
              <MessageContent key={part.id}>
                <MessageResponse>
                  {part.isStreaming ? part.content + " " : part.content}
                </MessageResponse>
              </MessageContent>
            )

          default:
            return null
        }
      })}

      {/* File creation notification */}
      {fileMatch && !turn.isStreaming && (
        <button
          onClick={() => onFileClick(fileMatch[1])}
          className="flex items-center gap-2 px-3 py-2 bg-primary/5 border border-primary/20 rounded-lg text-label text-primary hover:bg-primary/10 max-w-md transition-colors"
        >
          <FileText className="h-4 w-4" />
          <span>File created: <span className="font-mono font-medium">{fileMatch[1]}</span></span>
          <span className="ml-auto font-medium">Preview &rarr;</span>
        </button>
      )}

      {/* Reactions row */}
      <TurnReactions turnId={turn.id} streaming={turn.isStreaming} />

      {/* Actions (only when done streaming and has text content) */}
      {!turn.isStreaming && fullText && !hasDelegation && (
        <MessageActions>
          <MessageAction tooltip="Copy" onClick={() => onCopy(fullText)}>
            <Copy className="h-3.5 w-3.5" />
          </MessageAction>
          <MessageAction tooltip="Good response">
            <ThumbsUp className="h-3.5 w-3.5" />
          </MessageAction>
          <MessageAction tooltip="Bad response">
            <ThumbsDown className="h-3.5 w-3.5" />
          </MessageAction>
          <ReactionPicker onPick={(emoji) => useReactionsStore.getState().add(turn.id, emoji)} />
        </MessageActions>
      )}
    </Message>
  )
}

function TurnReactions({ turnId, streaming }: { turnId: string; streaming: boolean }) {
  const reactions = useReactionsStore((s) => s.byTurn[turnId])
  const toggle = useReactionsStore((s) => s.toggle)
  if (streaming || !reactions || Object.keys(reactions).length === 0) return null
  return (
    <ReactionsRow
      reactions={reactions}
      onToggle={(emoji) => toggle(turnId, emoji)}
      className="mt-1"
    />
  )
}
