import React, { lazy, Suspense } from 'react'
import type { ComponentType } from 'react'
// Test-only Next dynamic loader seam. Production components and editor remain
// unchanged; this isolated Vite fixture has no Next server or application route.
export default function dynamic<Props extends object>(loader: () => Promise<{ default: ComponentType<Props> }>) {
  const Component = lazy(loader)
  return function Loaded(props: Props) { return <Suspense fallback={<p>Loading fixture editor…</p>}><Component {...props} /></Suspense> }
}
