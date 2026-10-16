"use client"
import { useState } from "react"
import { ExternalLink, FileCode2, FileText } from "lucide-react"
import { useArtifactChatResults, ArtifactChatResultsError } from "@/hooks/use-artifact-chat-results"
import { stablePageHref } from "@/lib/pages/stable-links"
import { Button } from "@/components/ui/button"
export function ArtifactResultCards({ workspaceId, chatId, isStreaming, currentUserId }: { workspaceId: string; chatId: string; isStreaming: boolean; currentUserId?: string | null }) {
  const query = useArtifactChatResults(workspaceId, chatId, isStreaming)
  const [expanded, setExpanded] = useState(false)
  if (query.error instanceof ArtifactChatResultsError && [401, 403, 404].includes(query.error.status)) return null
  const results = query.data?.results ?? []
  if (!results.length) return null
  const hasDocuments = results.some(result => result.type === "document")
  return <section aria-label={hasDocuments ? "Saved outputs from this conversation" : "Published artifacts from this conversation"} className="mx-auto my-4 w-full max-w-3xl space-y-3">
    <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="text-sm font-medium">{hasDocuments ? "Saved outputs" : "Published artifacts"}</h3><span className="text-xs text-muted-foreground">{hasDocuments ? "Documents and static artifacts" : "Saved agent revisions · Static preview"}</span></div>
    {query.error && <p role="status" className="text-xs text-muted-foreground">Published artifacts could not refresh. Showing the last authorized results.</p>}
    <div className="grid gap-3 sm:grid-cols-2">{(expanded ? results : results.slice(0, 3)).map(result => <a key={`${result.page_slug}:${result.revision}`} href={result.type === "document" && result.page_id ? stablePageHref(workspaceId, result.page_id) : `/pages/${encodeURIComponent(result.page_slug)}?revision=${result.revision}`} target="_blank" rel="noopener noreferrer" className="group flex min-w-0 gap-3 rounded-xl border bg-card p-4 transition-colors hover:bg-accent coarse:min-h-12" aria-label={result.type === "document" ? `Open document ${result.title}` : `Open ${result.title}, revision ${result.revision}`}>
      {result.type === "document" ? <FileText className="mt-0.5 h-5 w-5 shrink-0 text-primary" aria-hidden /> : <FileCode2 className="mt-0.5 h-5 w-5 shrink-0 text-primary" aria-hidden />}
      <div className="min-w-0 flex-1"><p className="truncate text-sm font-medium">{result.title}</p><p className="mt-1 text-xs text-muted-foreground">{result.type === "document" ? "Document" : `Revision ${result.revision}`} · {result.agent_name || "Agent"}</p><p className="mt-1 text-xs text-muted-foreground">On behalf of {result.on_behalf_of_user_id === currentUserId ? "you" : "a human collaborator"}</p></div><ExternalLink className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
    </a>)}</div>
    {results.length > 3 && <Button size="sm" variant="ghost" onClick={() => setExpanded(value => !value)}>{expanded ? "Show fewer artifacts" : `Show all ${results.length} artifacts`}</Button>}
  </section>
}
