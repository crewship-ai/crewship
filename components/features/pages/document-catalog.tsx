"use client"

import type { PageView } from "@/hooks/use-pages"
import { Button } from "@/components/ui/button"

/** Page metadata only: this catalog never guesses a Document revision or author. */
export function DocumentCatalog({pages,onSelect}:{pages:PageView[];onSelect:(slug:string)=>void}) {
  const documents = pages.filter(page=>page.hasDocument === true)
  if (!documents.length) return null
  return <section aria-label="Documents" className="space-y-3 border-b p-6">
    <h2 className="text-lg font-semibold">Documents</h2>
    <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">{documents.map(page=><Button key={page.id} variant="outline" className="h-auto min-h-24 items-start justify-start whitespace-normal p-4 text-left" onClick={()=>onSelect(page.slug)}><span><span className="block font-medium">{page.name}</span>{page.description && <span className="mt-1 block text-sm font-normal text-muted-foreground">{page.description}</span>}<span className="mt-2 block text-xs font-normal text-muted-foreground">Doc</span></span></Button>)}</div>
  </section>
}
