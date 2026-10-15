import React from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { PageBoardView } from '../../components/features/pages/page-board-view'
const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
const control = { refresh: () => client.invalidateQueries({ queryKey: ['page-board'] }) }
Object.assign(window, { boardProduct: control })
createRoot(document.getElementById('root')!).render(<QueryClientProvider client={client}><PageBoardView workspaceId="fixture" slug="design" /></QueryClientProvider>)
declare global { interface Window { boardProduct: typeof control } }
