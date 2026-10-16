"use client"

import { useState } from 'react'
import type { PageView } from '@/hooks/use-pages'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { formatInstant } from './panels/freshness'
import { ArtifactGalleryPreview } from './artifact-gallery-preview'

export function ArtifactGallery({ workspaceId, pages, loading, error, onSelect, onCreate, onOpenChat }: {
  workspaceId?: string
  pages: PageView[]; loading: boolean; error: string | null; onSelect: (slug: string) => void
  onCreate?: () => void; onOpenChat: () => void
}) {
  const [preview, setPreview] = useState<{ workspace: string; id: string } | null>(null)
  if (error) return <p role="alert" className="p-6 text-destructive">{error}</p>
  if (loading) return <div className="grid gap-4 p-6 md:grid-cols-2">{[0,1,2].map(key => <Skeleton key={key} className="h-40" />)}</div>
  return <section className="space-y-5 p-4 md:p-6" aria-label="Artifact gallery">
    <div><h2 className="type-page-value">Artifacts</h2><p className="type-page-meta text-muted-foreground">Reports, prototypes and other published work. Preview a card or open it to discuss and revise.</p></div>
    {!pages.length ? <div className="space-y-3 rounded-lg border border-dashed p-6"><p className="type-page-value">No artifacts match this view</p><p className="type-page-meta text-muted-foreground">Ask an agent in a private chat, or create an artifact and import your HTML.</p><div className="flex flex-wrap gap-2"><Button variant="outline" onClick={onOpenChat} className="coarse:h-12">Open chats</Button>{onCreate && <Button onClick={onCreate} className="coarse:h-12">Import HTML artifact</Button>}</div></div> : <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">{pages.map(page => {
      const summary = page.artifactSummary
      const human = summary?.author_name || (summary?.author_user_id ? `User ${summary.author_user_id}` : 'Former user')
      const author = (summary?.author_agent_id || summary?.author_run_id) ? `${summary.author_agent_name || (summary.author_agent_id ? `Agent ${summary.author_agent_id}` : 'Former agent')} on behalf of ${human}` : human
      return <article key={page.id} className="flex min-w-0 flex-col gap-3 rounded-lg border bg-card p-4">
        <div className="flex items-start justify-between gap-2"><h3 className="type-page-value break-words">{page.name}</h3><span className="type-page-meta shrink-0 text-muted-foreground">{summary ? `Revision ${summary.head_rev}` : 'Published'}</span></div>
        {page.description && <p className="type-page-meta line-clamp-3 break-words text-muted-foreground">{page.description}</p>}
        <dl className="type-page-meta space-y-1 text-muted-foreground"><div><dt className="inline">Author: </dt><dd className="inline break-words">{summary ? author : 'Unknown'}</dd></div><div><dt className="inline">Last activity: </dt><dd className="inline">{summary?.last_activity_at ? formatInstant(new Date(summary.last_activity_at)) : 'Unknown'}</dd></div></dl>
        {workspaceId && <Button variant="ghost" size="sm" className="coarse:h-12" aria-expanded={preview?.workspace === workspaceId && preview.id === page.id} onClick={() => setPreview(current => current?.workspace === workspaceId && current.id === page.id ? null : { workspace: workspaceId, id: page.id })}>{preview?.workspace === workspaceId && preview.id === page.id ? 'Close preview' : 'Preview'}<span className="sr-only"> of {page.name}</span></Button>}
        {workspaceId && preview?.workspace === workspaceId && preview.id === page.id && <ArtifactGalleryPreview key={`${workspaceId}:${page.id}`} workspaceId={workspaceId} slug={page.slug} name={page.name} />}
        <Button variant="outline" onClick={() => onSelect(page.slug)} className="mt-auto coarse:h-12">Open static preview<span className="sr-only"> of {page.name}</span></Button>
      </article>
    })}</div>}
  </section>
}
