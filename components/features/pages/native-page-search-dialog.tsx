"use client"

import { useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useNativePageSearch } from "@/hooks/use-native-page-search"

export function NativePageSearchDialog({ workspaceId, viewerId, onClose, onOpenPage }: {
  workspaceId: string
  viewerId: string | null
  onClose: () => void
  onOpenPage: (pageId: string) => void
}) {
  const [query, setQuery] = useState("")
  const search = useNativePageSearch(workspaceId, viewerId, true)
  return <Dialog open onOpenChange={open => { if (!open) onClose() }}>
    <DialogContent className="max-h-[85dvh] overflow-y-auto">
      <DialogHeader>
        <DialogTitle>Search Docs and Slides text</DialogTitle>
        <DialogDescription>Search supported native text in this workspace. All words must match. Images, comments and other Page types are outside this search.</DialogDescription>
      </DialogHeader>
      <form className="flex gap-2" onSubmit={event => { event.preventDefault(); void search.search(query) }}>
        <label className="sr-only" htmlFor="native-page-search">Words to find</label>
        <Input id="native-page-search" autoFocus value={query} maxLength={256} placeholder="Words to find" onChange={event => { setQuery(event.target.value); search.clear() }} />
        <Button type="submit" disabled={!viewerId || !query.trim() || search.busy} className="coarse:min-h-12">Search</Button>
      </form>
      {search.busy && <p role="status" className="text-sm text-muted-foreground">Checking current native content…</p>}
      {search.error && <p role="alert" className="text-sm text-destructive">{search.error}</p>}
      {search.complete && search.matches.length === 0 && <p role="status" className="text-sm">No matching supported native Pages.</p>}
      {search.matches.length > 0 && <ul className="space-y-2" aria-label="Native content matches">
        {search.matches.map(result => <li key={result.page_id}>
          <button type="button" className="w-full rounded-md border p-3 text-left hover:bg-accent coarse:min-h-12" onClick={() => onOpenPage(result.page_id)}>
            <span className="block font-medium">{result.name}</span>
            <span className="block text-xs text-muted-foreground">{result.content_type === "document" ? "Doc" : "Slides"} · revision {result.source_revision}</span>
            <span className="block break-words text-sm">{result.snippet}</span>
          </button>
        </li>)}
      </ul>}
      {search.cursor && <Button type="button" variant="outline" className="coarse:min-h-12" disabled={search.busy} onClick={() => { void search.search(query, true) }}>Next results</Button>}
      <p className="text-xs text-muted-foreground">Each result page rechecks access. Results expire after 15 seconds or a content/access notification; search again explicitly. If search is unavailable, an error appears instead of empty results.</p>
    </DialogContent>
  </Dialog>
}
