import { ChatHome } from "@/components/features/chat/chat-home"

// `/chat` — the index over every conversation in the workspace, and the
// navigation's front door to the agents.
//
// This route exists because a bare /chat used to 404: the export only ever
// built `chat/_.html` for the [agentSlug] child, so /chat fell through the Go
// handler to the SPA index (documented at hooks/use-active-runs.ts, which
// routed agent runs to /crews to avoid it). Adding this page makes the export
// emit `out/chat.html`, which internal/api/static.go resolves via its
// `path + ".html"` lookup — before the one-level dynamic-placeholder rewrite,
// so /chat and /chat/<slug> stay distinct. See
// internal/api/static_chat_index_test.go.
//
// The client half lives in components/features/chat/chat-home.tsx rather than
// in app/, matching how the rest of the dashboard is laid out. It renders two
// panes: the shared ChatTreeSidebar on the left — the SAME column
// /chat/<agent> has, which is the whole point — and the recent-threads index
// on the right. No ChatPanel until a thread is chosen (PRD O7), so this route
// opens no WebSocket.
export default function ChatIndexPage() {
  return <ChatHome />
}
