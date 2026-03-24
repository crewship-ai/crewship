"use client"

import { useRef, useState, type FormEvent, type KeyboardEvent } from "react"
import { Loader2, Send, Square, Trash2, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Conversation,
  ConversationContent,
  ConversationEmptyState,
  ConversationScrollButton,
} from "@/components/ai-elements/conversation"
import {
  Message,
  MessageContent,
  MessageResponse,
} from "@/components/ai-elements/message"
import { Suggestions, Suggestion } from "@/components/ai-elements/suggestion"
import { useCaptain } from "@/hooks/use-captain"
import { cn } from "@/lib/utils"

const TOOL_LABELS: Record<string, string> = {
  get_workspace_stats: "Checking workspace...",
  list_crews: "Looking up crews...",
  list_agents: "Finding agents...",
  list_credentials: "Checking credentials...",
  list_missions: "Reviewing missions...",
  list_escalations: "Checking escalations...",
  create_crew: "Creating crew...",
  create_agent: "Setting up agent...",
  create_mission: "Planning mission...",
  approve_proposal: "Approving proposal...",
  apply_crew_template: "Deploying template...",
}

const SUGGESTIONS = [
  "What's happening in my workspace?",
  "Show me pending escalations",
  "Help me create a new crew",
]

export function CaptainPanel() {
  const {
    messages,
    isStreaming,
    activeToolCall,
    isOpen,
    setOpen,
    sendMessage,
    stopGeneration,
    clearHistory,
  } = useCaptain()

  const [input, setInput] = useState("")
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  function handleSubmit(e?: FormEvent) {
    e?.preventDefault()
    const trimmed = input.trim()
    if (!trimmed || isStreaming) return
    sendMessage(trimmed)
    setInput("")
  }

  function handleKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault()
      handleSubmit()
    }
  }

  function handleSuggestion(text: string) {
    sendMessage(text)
  }

  if (!isOpen) return null

  return (
    <div
      className={cn(
        "fixed bottom-20 right-6 z-50",
        "flex w-[400px] max-h-[600px] flex-col overflow-hidden",
        "rounded-2xl border bg-background shadow-2xl",
        "animate-in fade-in-0 slide-in-from-bottom-4 duration-200"
      )}
    >
      {/* Header */}
      <div className="flex items-center justify-between border-b px-4 py-3">
        <h3 className="font-semibold text-sm">Captain</h3>
        <div className="flex items-center gap-1">
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={clearHistory}
            disabled={messages.length === 0 || isStreaming}
            aria-label="Clear history"
          >
            <Trash2 className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => setOpen(false)}
            aria-label="Close Captain"
          >
            <X className="h-4 w-4" />
          </Button>
        </div>
      </div>

      {/* Conversation */}
      <Conversation className="min-h-0 flex-1">
        <ConversationContent className="gap-4 p-3">
          {messages.length === 0 ? (
            <ConversationEmptyState
              title="Captain"
              description="Your AI workspace assistant. Ask anything about your crews, agents, and missions."
            >
              <div className="mt-4">
                <Suggestions className="justify-center">
                  {SUGGESTIONS.map((s) => (
                    <Suggestion
                      key={s}
                      suggestion={s}
                      onClick={handleSuggestion}
                      className="text-xs"
                    />
                  ))}
                </Suggestions>
              </div>
            </ConversationEmptyState>
          ) : (
            messages.map((msg) => (
              <Message key={msg.id} from={msg.role}>
                <MessageContent>
                  {msg.role === "assistant" ? (
                    <MessageResponse>{msg.content}</MessageResponse>
                  ) : (
                    <span>{msg.content}</span>
                  )}
                </MessageContent>
                {msg.toolCalls && msg.toolCalls.length > 0 && !isStreaming && (
                  <div className="text-muted-foreground text-xs">
                    Used {msg.toolCalls.length} tool{msg.toolCalls.length > 1 ? "s" : ""}
                  </div>
                )}
              </Message>
            ))
          )}
        </ConversationContent>
        <ConversationScrollButton />
      </Conversation>

      {/* Tool status bar */}
      {activeToolCall && (
        <div className="flex items-center gap-2 border-t px-4 py-2 text-xs text-muted-foreground">
          <Loader2 className="h-3 w-3 animate-spin" />
          <span>{TOOL_LABELS[activeToolCall] ?? `Running ${activeToolCall}...`}</span>
        </div>
      )}

      {/* Input */}
      <form onSubmit={handleSubmit} className="border-t p-3">
        <div className="flex items-end gap-2">
          <textarea
            ref={textareaRef}
            aria-label="Message Captain"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Ask Captain..."
            disabled={isStreaming}
            rows={1}
            className={cn(
              "flex-1 resize-none rounded-lg border bg-muted/50 px-3 py-2 text-sm",
              "placeholder:text-muted-foreground",
              "focus:outline-none focus:ring-1 focus:ring-ring",
              "disabled:opacity-50",
              "max-h-24 min-h-9"
            )}
          />
          {isStreaming ? (
            <Button
              type="button"
              size="icon-sm"
              variant="destructive"
              onClick={stopGeneration}
              aria-label="Stop generation"
            >
              <Square className="h-3 w-3" />
            </Button>
          ) : (
            <Button
              type="submit"
              size="icon-sm"
              disabled={!input.trim()}
              aria-label="Send message"
            >
              <Send className="h-3 w-3" />
            </Button>
          )}
        </div>
      </form>
    </div>
  )
}
