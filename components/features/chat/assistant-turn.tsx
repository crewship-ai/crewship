"use client"

import { Copy, ThumbsUp, ThumbsDown, AlertCircle, AlertTriangle, Crown, CheckCircle2, Clock, FileText, DollarSign, Zap, CircleDot, HelpCircle } from "lucide-react"
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

interface AssistantTurnProps {
  turn: ChatTurn
  onCopy: (content: string) => void
  onFileClick: (fileName: string) => void
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`
  const secs = ms / 1000
  if (secs < 60) return `${secs.toFixed(1)}s`
  const mins = Math.floor(secs / 60)
  const remSecs = Math.round(secs % 60)
  return remSecs > 0 ? `${mins}m ${remSecs}s` : `${mins}m`
}

function formatCost(usd: number): string {
  if (usd < 0.01) return `$${usd.toFixed(4)}`
  return `$${usd.toFixed(2)}`
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
      <div className="flex items-center gap-2 px-3 py-2 bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-800 rounded-lg text-xs text-red-700 dark:text-red-400 max-w-lg">
        <AlertCircle className="h-4 w-4 shrink-0" />
        <span>{errors.join("; ")}</span>
      </div>
    )
  }

  // Compact one-line summary for the trigger
  const summaryParts: string[] = []
  if (cost != null && cost > 0) summaryParts.push(formatCost(cost))
  if (durationMs != null && durationMs > 0) summaryParts.push(formatDuration(durationMs))
  if (numTurns != null && numTurns > 0) summaryParts.push(`${numTurns} turn${numTurns !== 1 ? "s" : ""}`)
  if (modelName) summaryParts.push(modelName)

  return (
    <details className="max-w-lg group">
      <summary className="flex items-center gap-2 text-[11px] text-muted-foreground cursor-pointer hover:text-foreground select-none list-none">
        <DollarSign className="h-3 w-3 shrink-0" />
        <span>{summaryParts.join(" · ") || "Run complete"}</span>
        <span className="text-[10px] opacity-60 group-open:hidden">&#9654;</span>
        <span className="text-[10px] opacity-60 hidden group-open:inline">&#9660;</span>
      </summary>
      <div className="mt-1.5 bg-muted/30 border rounded-lg px-4 py-3 space-y-2">
        <div className="flex items-center gap-4 flex-wrap text-xs">
          {cost != null && cost > 0 && (
            <span className="flex items-center gap-1 text-emerald-600 dark:text-emerald-400 font-medium">
              <DollarSign className="h-3 w-3" />
              {formatCost(cost)}
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
            <span className="text-muted-foreground font-mono text-[10px]">
              {modelName}
            </span>
          )}
        </div>
        {(inputTokens > 0 || outputTokens > 0) && (
          <div className="flex items-center gap-3 text-[11px] text-muted-foreground">
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

function AskUserCard({ part }: { part: TurnPart }) {
  const input = part.metadata?.input as { questions?: AskQuestion[] } | undefined
  const questions = input?.questions
  if (!questions?.length) {
    return <DefaultToolCall part={part} />
  }

  return (
    <div className="max-w-lg space-y-3">
      {questions.map((q, qi) => (
        <div key={qi} className="bg-primary/5 border border-primary/20 rounded-lg overflow-hidden">
          <div className="px-4 py-3 border-b border-primary/10 flex items-center gap-2">
            <HelpCircle className="h-3.5 w-3.5 text-primary" />
            <span className="text-xs font-medium">{q.header}</span>
          </div>
          <div className="px-4 py-3">
            <p className="text-sm mb-2">{q.question}</p>
            <div className="flex flex-wrap gap-1.5">
              {q.options.map((opt, oi) => (
                <span
                  key={oi}
                  className="inline-flex items-center gap-1 px-2.5 py-1 rounded-full text-xs bg-muted border cursor-default"
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

function TodoWriteCard({ part }: { part: TurnPart }) {
  const input = part.metadata?.input as { todos?: TodoItem[] } | undefined
  const todos = input?.todos
  if (!todos?.length) return <DefaultToolCall part={part} />

  const completed = todos.filter((t) => t.status === "completed").length
  const inProgress = todos.filter((t) => t.status === "in_progress").length
  const pct = Math.round((completed / todos.length) * 100)

  return (
    <div className="max-w-md">
      <div className="bg-muted/30 border rounded-lg overflow-hidden">
        <div className="px-4 py-2.5 border-b flex items-center justify-between">
          <span className="text-xs font-medium flex items-center gap-1.5">
            <CheckCircle2 className="h-3.5 w-3.5 text-muted-foreground" />
            Agent Progress
          </span>
          <span className="text-[11px] text-muted-foreground">{completed}/{todos.length}</span>
        </div>
        <div className="px-4 py-2 space-y-1">
          {todos.map((todo, i) => (
            <div key={i} className="flex items-start gap-2 text-xs py-0.5">
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
          <div className="h-1.5 bg-muted rounded-full overflow-hidden">
            <div
              className="h-full bg-emerald-500 rounded-full transition-all duration-300 w-[var(--pct)]"
              style={{ "--pct": `${pct}%` } as React.CSSProperties}
            />
          </div>
          {inProgress > 0 && (
            <p className="text-[10px] text-amber-500 mt-1">{inProgress} in progress</p>
          )}
        </div>
      </div>
    </div>
  )
}

function TaskCard({ part }: { part: TurnPart }) {
  const input = part.metadata?.input as { description?: string; prompt?: string; subagent_type?: string } | undefined
  if (!input?.description) return <DefaultToolCall part={part} />

  const isCompleted = !!part.metadata?.completed
  const promptPreview = input.prompt ? (input.prompt.length > 120 ? input.prompt.slice(0, 120) + "..." : input.prompt) : null

  return (
    <div className="max-w-lg">
      <div className="bg-primary/5 border border-primary/20 border-l-4 border-l-amber-400 rounded-lg overflow-hidden">
        <div className="px-4 py-3">
          <div className="flex items-center gap-2 text-xs">
            <Crown className="h-3.5 w-3.5 text-amber-500" />
            <span className="font-medium">Subagent: {input.subagent_type ?? "worker"}</span>
            {isCompleted ? (
              <span className="ml-auto inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] bg-emerald-50 dark:bg-emerald-950/30 text-emerald-700 dark:text-emerald-400">
                <CheckCircle2 className="h-3 w-3" /> Done
              </span>
            ) : (
              <span className="ml-auto inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] bg-amber-50 dark:bg-amber-950/30 text-amber-700 dark:text-amber-400">
                <Clock className="h-3 w-3 animate-spin" /> Working
              </span>
            )}
          </div>
          <p className="text-sm font-medium mt-1.5">{input.description}</p>
          {promptPreview && (
            <p className="text-xs text-muted-foreground mt-1 bg-background rounded px-2.5 py-1.5 border">{promptPreview}</p>
          )}
        </div>
      </div>
    </div>
  )
}

function DefaultToolCall({ part }: { part: TurnPart }) {
  const toolName = (part.metadata?.tool_name as string) ?? part.content ?? "Tool"
  const isCompleted = !!part.metadata?.completed
  const input = part.metadata?.input as Record<string, unknown> | undefined

  let subtitle = ""
  if (input) {
    if (input.file_path) subtitle = String(input.file_path)
    else if (input.command) subtitle = `$ ${String(input.command).slice(0, 60)}${String(input.command).length > 60 ? "..." : ""}`
    else if (input.url) subtitle = String(input.url)
    else if (input.query) subtitle = String(input.query)
    else if (input.pattern) subtitle = String(input.pattern)
  }

  return (
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
  )
}

function InlineToolCall({ part }: { part: TurnPart }) {
  const toolName = (part.metadata?.tool_name as string) ?? part.content ?? "Tool"

  switch (toolName) {
    case "AskUserQuestion": return <AskUserCard part={part} />
    case "TodoWrite": return <TodoWriteCard part={part} />
    case "Task": return <TaskCard part={part} />
    default: return <DefaultToolCall part={part} />
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
          <div className="flex items-center gap-2 text-xs">
            <Crown className="h-3.5 w-3.5 text-amber-500" />
            <span className="font-medium text-muted-foreground">Delegated to</span>
            <span className="font-semibold">{targetMatch?.[1] ?? "Agent"}</span>
          </div>
          {taskMatch && (
            <div className="mt-1.5 text-xs text-muted-foreground bg-background rounded px-2.5 py-1.5 border">
              {taskMatch[1]}
            </div>
          )}
          <div className="mt-2 flex items-center gap-2">
            {isCompleted ? (
              <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium bg-emerald-50 dark:bg-emerald-950/30 text-emerald-700 dark:text-emerald-400">
                <CheckCircle2 className="h-3 w-3" />
                Completed in {completedMatch[1]}
              </span>
            ) : (
              <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium bg-amber-50 dark:bg-amber-950/30 text-amber-700 dark:text-amber-400">
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

export function AssistantTurn({ turn, onCopy, onFileClick }: AssistantTurnProps) {
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
            return <InlineToolCall key={part.id} part={part} />

          case "tool_result":
            return <InlineToolResult key={part.id} part={part} />

          case "error":
            if (part.content.toLowerCase().includes("rate limit") || part.content.toLowerCase().includes("429")) {
              return (
                <div key={part.id} className="flex items-center gap-2 px-3 py-2 bg-amber-50 dark:bg-amber-950/30 border border-amber-200 dark:border-amber-800 rounded-lg text-xs text-amber-700 dark:text-amber-400 max-w-md">
                  <AlertTriangle className="h-4 w-4 shrink-0" />
                  <span>{part.content}</span>
                </div>
              )
            }
            return (
              <MessageContent key={part.id} className="border-destructive/50 bg-destructive/5 rounded-lg px-4 py-3">
                <div className="flex items-center gap-2 text-destructive text-sm">
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
          className="flex items-center gap-2 px-3 py-2 bg-primary/5 border border-primary/20 rounded-lg text-xs text-primary hover:bg-primary/10 max-w-md transition-colors"
        >
          <FileText className="h-4 w-4" />
          <span>File created: <span className="font-mono font-medium">{fileMatch[1]}</span></span>
          <span className="ml-auto font-medium">Preview &rarr;</span>
        </button>
      )}

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
        </MessageActions>
      )}
    </Message>
  )
}
