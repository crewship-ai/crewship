"use client"

import { create } from "zustand"
import { persist, createJSONStorage } from "zustand/middleware"

export interface CaptainMessage {
  id: string
  role: "user" | "assistant"
  content: string
  timestamp: number
  toolCalls?: { id: string; name: string }[]
  toolResults?: { id: string; name: string; content: string }[]
}

interface CaptainStore {
  // Panel
  isOpen: boolean
  toggle: () => void
  setOpen: (open: boolean) => void

  // Messages (in-memory, loaded from API)
  messages: CaptainMessage[]
  addMessage: (msg: CaptainMessage) => void
  appendToLast: (text: string) => void
  addToolCallToLast: (tc: { id: string; name: string }) => void
  addToolResultToLast: (tr: { id: string; name: string; content: string }) => void
  clearMessages: () => void
  setMessages: (msgs: CaptainMessage[]) => void

  // Streaming
  isStreaming: boolean
  setStreaming: (s: boolean) => void
  activeToolCall: string | null
  setActiveToolCall: (name: string | null) => void

  // Badge
  badgeCount: number
  setBadgeCount: (n: number) => void

  // History loaded flag (prevents re-fetching)
  historyLoaded: boolean
  setHistoryLoaded: (v: boolean) => void
}

export const useCaptainStore = create<CaptainStore>()(
  persist(
    (set) => ({
      isOpen: false,
      toggle: () => set((s) => ({ isOpen: !s.isOpen })),
      setOpen: (isOpen) => set({ isOpen }),

      messages: [],
      addMessage: (msg) => set((s) => ({ messages: [...s.messages, msg] })),
      appendToLast: (text) =>
        set((s) => {
          const msgs = [...s.messages]
          const last = msgs[msgs.length - 1]
          if (last?.role === "assistant") {
            msgs[msgs.length - 1] = { ...last, content: last.content + text }
          }
          return { messages: msgs }
        }),
      addToolCallToLast: (tc) =>
        set((s) => {
          const msgs = [...s.messages]
          const last = msgs[msgs.length - 1]
          if (last?.role === "assistant") {
            msgs[msgs.length - 1] = {
              ...last,
              toolCalls: [...(last.toolCalls ?? []), tc],
            }
          }
          return { messages: msgs }
        }),
      addToolResultToLast: (tr) =>
        set((s) => {
          const msgs = [...s.messages]
          const last = msgs[msgs.length - 1]
          if (last?.role === "assistant") {
            msgs[msgs.length - 1] = {
              ...last,
              toolResults: [...(last.toolResults ?? []), tr],
            }
          }
          return { messages: msgs }
        }),
      clearMessages: () => set({ messages: [], historyLoaded: false }),
      setMessages: (messages) => set({ messages }),

      isStreaming: false,
      setStreaming: (isStreaming) => set({ isStreaming }),
      activeToolCall: null,
      setActiveToolCall: (activeToolCall) => set({ activeToolCall }),

      badgeCount: 0,
      setBadgeCount: (badgeCount) => set({ badgeCount }),

      historyLoaded: false,
      setHistoryLoaded: (historyLoaded) => set({ historyLoaded }),
    }),
    {
      name: "crewship-captain",
      storage: createJSONStorage(() => localStorage),
      partialize: (state) => ({ isOpen: state.isOpen }),
    }
  )
)
