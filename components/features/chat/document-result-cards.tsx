"use client"
import { useState } from "react"
import { ExternalLink, FileText } from "lucide-react"
import { useDocumentChatResults, DocumentChatResultsError } from "@/hooks/use-document-chat-results"
import { Button } from "@/components/ui/button"

export function DocumentResultCards({ workspaceId, chatId, isStreaming, currentUserId, documentsEnabled = false }: { workspaceId: string; chatId: string; isStreaming: boolean; currentUserId?: string | null; documentsEnabled?: boolean }) {
  const query = useDocumentChatResults(workspaceId, chatId, isStreaming, documentsEnabled)
  const [expanded, setExpanded] = useState(false)
  if (!documentsEnabled || !workspaceId || !chatId) return null
  if (query.error instanceof DocumentChatResultsError && [401, 403, 404].includes(query.error.status)) return null
  const results = query.data?.results ?? []
  if (!results.length) return null
  return <section aria-label="Saved documents from this conversation" className="mx-auto my-4 w-full max-w-3xl space-y-3">
    <h3 className="text-sm font-medium">Saved documents</h3>
    {query.error && <p role="status" className="text-xs text-muted-foreground">Saved documents could not refresh. Showing the last authorized results.</p>}
    <div className="grid gap-3 sm:grid-cols-2">{(expanded ? results : results.slice(0, 3)).map(result => <a key={result.page_id} href={`/pages?${new URLSearchParams({ ref: result.page_id, workspace_id: workspaceId })}`} target="_blank" rel="noopener noreferrer" className="group flex min-w-0 gap-3 rounded-xl border bg-card p-4 transition-colors hover:bg-accent coarse:min-h-12" aria-label={`Open document ${result.title}`}>
      <FileText className="mt-0.5 h-5 w-5 shrink-0 text-primary" aria-hidden />
      <div className="min-w-0 flex-1"><p className="truncate text-sm font-medium">{result.title}</p><p className="mt-1 text-xs text-muted-foreground">Document · {result.agent_name || "Agent"}</p><p className="mt-1 text-xs text-muted-foreground">On behalf of {currentUserId && result.on_behalf_of_user_id === currentUserId ? "you" : "a human collaborator"}</p></div><ExternalLink className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
    </a>)}</div>
    {results.length > 3 && <Button size="sm" variant="ghost" className="coarse:min-h-12" onClick={() => setExpanded(value => !value)}>{expanded ? "Show fewer documents" : `Show all ${results.length} documents`}</Button>}
  </section>
}
