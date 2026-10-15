import React from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ArtifactGallery } from '../../components/features/pages/artifact-gallery'
import { toPageView } from '../../hooks/use-pages'

const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
const control = { refresh: () => client.invalidateQueries({ queryKey: ['page-artifact'] }) }
Object.assign(window, { artifactGallery: control })
const pages = ['one', 'two'].map(id => toPageView({ id, slug: id, name: id, has_artifact: true, panels: [] }))
createRoot(document.getElementById('root')!).render(<QueryClientProvider client={client}><ArtifactGallery workspaceId="fixture" pages={pages} loading={false} error={null} onSelect={() => {}} onOpenChat={() => {}} /></QueryClientProvider>)
declare global { interface Window { artifactGallery: typeof control } }
