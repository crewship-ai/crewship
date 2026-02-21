"use client"

import { Copy, ThumbsUp, ThumbsDown, AlertCircle, AlertTriangle, Crown, CheckCircle2, Clock, FileText } from "lucide-react"
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

function InlineToolCall({ part }: { part: TurnPart }) {
  const toolName = (part.metadata?.tool_name as string) ?? part.content ?? "Tool"
  const isCompleted = !!part.metadata?.completed
  return (
    <Tool defaultOpen={false}>
      <ToolHeader
        title={toolName}
        type="tool-invocation"
        state={isCompleted ? "output-available" : "input-available"}
      />
      <ToolContent>
        {part.metadata?.input != null && (
          <CodeBlock code={JSON.stringify(part.metadata.input, null, 2)} language="json" />
        )}
      </ToolContent>
    </Tool>
  )
}

function InlineToolResult({ part }: { part: TurnPart }) {
  return (
    <Tool defaultOpen={false}>
      <ToolHeader title="Tool Result" type="tool-invocation" state="output-available" />
      <ToolContent>
        <pre className="text-xs whitespace-pre-wrap break-all">{part.content}</pre>
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
  const fileMatch = fileCreationPart?.content.match(/[`"]?([a-zA-Z0-9_\-/.]+\.[a-zA-Z0-9]+)[`"]?/)

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
